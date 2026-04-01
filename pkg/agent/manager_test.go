package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	gwNoise "github.com/waywardgeek/gateway/pkg/noise"
	"github.com/waywardgeek/gateway/pkg/config"
	"github.com/waywardgeek/gateway/pkg/router"
	"github.com/waywardgeek/gateway/pkg/scheduler"
	"github.com/waywardgeek/gateway/pkg/store"
	"github.com/waywardgeek/gateway/pkg/types"
)

func encodeKey(key []byte) string {
	return base64.StdEncoding.EncodeToString(key)
}

// TestNoiseHandshake tests the full Noise-KK handshake over WebSocket.
func TestNoiseHandshake(t *testing.T) {
	// Generate keypairs
	gatewayKey, _ := gwNoise.GenerateKeypair()
	agentKey, _ := gwNoise.GenerateKeypair()

	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Listen:  ":0",
			DataDir: t.TempDir(),
		},
		Agents: map[string]config.AgentConfig{
			"test-agent": {
				DisplayName: "Test Agent",
				PublicKey:   fmt.Sprintf("x25519:%s", encodeKey(agentKey.Public)),
			},
		},
		Channels: map[string]config.ChannelConfig{
			"test-ch": {
				Type:    "webhook",
				RouteTo: "test-agent",
				Trust:   "trusted",
			},
		},
		Scheduler: config.SchedulerConfig{Jobs: map[string]config.StaticJobConfig{}},
	}

	st, _ := store.New(cfg.Gateway.DataDir)
	r := router.New(cfg, st)
	sched := scheduler.New(cfg, r, st)
	mgr := NewManager(cfg, st, r, sched, gatewayKey)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ws", mgr.HandleWebSocket)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// --- Client side: connect as agent ---
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/ws"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// Create client Noise-KK session
	clientNoise, err := gwNoise.NewNoiseKK("initiator", agentKey, gatewayKey.Public, []byte("gateway-v1"))
	if err != nil {
		t.Fatalf("create client noise: %v", err)
	}

	// Write handshake msg1: [1-byte id-len][agent-id][noise-msg]
	msg1, err := clientNoise.WriteHandshakeMessage(nil)
	if err != nil {
		t.Fatalf("write msg1: %v", err)
	}
	agentIDBytes := []byte("test-agent")
	raw := make([]byte, 1+len(agentIDBytes)+len(msg1))
	raw[0] = byte(len(agentIDBytes))
	copy(raw[1:], agentIDBytes)
	copy(raw[1+len(agentIDBytes):], msg1)

	if err := ws.WriteMessage(websocket.BinaryMessage, raw); err != nil {
		t.Fatalf("send msg1: %v", err)
	}

	// Read handshake msg2
	_, msg2Raw, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read msg2: %v", err)
	}

	_, err = clientNoise.ReadHandshakeMessage(msg2Raw)
	if err != nil {
		t.Fatalf("process msg2: %v", err)
	}

	if !clientNoise.IsHandshakeComplete() {
		t.Fatal("handshake should be complete")
	}

	// Send encrypted hello frame
	helloPayload, _ := json.Marshal(types.HelloPayload{
		AgentID:      "test-agent",
		Version:      "test-0.1",
		LastAckedSeq: 0,
	})
	helloFrame, _ := json.Marshal(types.Frame{
		Type:    types.FrameHello,
		Payload: helloPayload,
	})
	encrypted, err := clientNoise.Encrypt(helloFrame, nil)
	if err != nil {
		t.Fatalf("encrypt hello: %v", err)
	}
	if err := ws.WriteMessage(websocket.BinaryMessage, encrypted); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	// Read welcome frame
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, welcomeEncrypted, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	welcomeJSON, err := clientNoise.Decrypt(welcomeEncrypted, nil)
	if err != nil {
		t.Fatalf("decrypt welcome: %v", err)
	}

	var welcomeFrame types.Frame
	json.Unmarshal(welcomeJSON, &welcomeFrame)
	if welcomeFrame.Type != types.FrameWelcome {
		t.Fatalf("expected welcome frame, got %s", welcomeFrame.Type)
	}

	var welcomePayload types.WelcomePayload
	json.Unmarshal(welcomeFrame.Payload, &welcomePayload)
	if welcomePayload.AgentID != "test-agent" {
		t.Errorf("expected agent ID 'test-agent', got %q", welcomePayload.AgentID)
	}
	if welcomePayload.ServerVersion != Version {
		t.Errorf("expected version %s, got %s", Version, welcomePayload.ServerVersion)
	}

	// Verify agent shows as connected
	time.Sleep(50 * time.Millisecond) // small race window for goroutine registration
	if !mgr.IsConnected("test-agent") {
		t.Error("agent should be connected")
	}
}

// TestMessageDelivery tests that messages queued before agent connects are delivered.
func TestMessageDelivery(t *testing.T) {
	gatewayKey, _ := gwNoise.GenerateKeypair()
	agentKey, _ := gwNoise.GenerateKeypair()

	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Listen:  ":0",
			DataDir: t.TempDir(),
		},
		Agents: map[string]config.AgentConfig{
			"test-agent": {
				DisplayName: "Test Agent",
				PublicKey:   fmt.Sprintf("x25519:%s", encodeKey(agentKey.Public)),
			},
		},
		Channels: map[string]config.ChannelConfig{
			"test-ch": {
				Type:    "webhook",
				RouteTo: "test-agent",
				Trust:   "trusted",
			},
		},
		Scheduler: config.SchedulerConfig{Jobs: map[string]config.StaticJobConfig{}},
	}

	st, _ := store.New(cfg.Gateway.DataDir)
	r := router.New(cfg, st)
	sched := scheduler.New(cfg, r, st)
	mgr := NewManager(cfg, st, r, sched, gatewayKey)

	// Queue a message BEFORE agent connects
	r.Deliver("test-ch", "user1", "User One", "Hello agent!", nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ws", mgr.HandleWebSocket)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Connect agent
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/ws"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	clientNoise, _ := gwNoise.NewNoiseKK("initiator", agentKey, gatewayKey.Public, []byte("gateway-v1"))

	// Handshake
	msg1, _ := clientNoise.WriteHandshakeMessage(nil)
	agentIDBytes := []byte("test-agent")
	raw := make([]byte, 1+len(agentIDBytes)+len(msg1))
	raw[0] = byte(len(agentIDBytes))
	copy(raw[1:], agentIDBytes)
	copy(raw[1+len(agentIDBytes):], msg1)
	ws.WriteMessage(websocket.BinaryMessage, raw)

	_, msg2Raw, _ := ws.ReadMessage()
	clientNoise.ReadHandshakeMessage(msg2Raw)

	// Send hello
	helloPayload, _ := json.Marshal(types.HelloPayload{AgentID: "test-agent", LastAckedSeq: 0})
	helloFrame, _ := json.Marshal(types.Frame{Type: types.FrameHello, Payload: helloPayload})
	encrypted, _ := clientNoise.Encrypt(helloFrame, nil)
	ws.WriteMessage(websocket.BinaryMessage, encrypted)

	// Read welcome — should say 1 queued
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, welcomeEnc, _ := ws.ReadMessage()
	welcomeJSON, _ := clientNoise.Decrypt(welcomeEnc, nil)
	var welcomeFrame types.Frame
	json.Unmarshal(welcomeJSON, &welcomeFrame)
	var welcome types.WelcomePayload
	json.Unmarshal(welcomeFrame.Payload, &welcome)

	if welcome.QueuedCount != 1 {
		t.Errorf("expected 1 queued message, got %d", welcome.QueuedCount)
	}

	// Read the delivered message
	_, deliverEnc, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read deliver: %v", err)
	}
	deliverJSON, _ := clientNoise.Decrypt(deliverEnc, nil)
	var deliverFrame types.Frame
	json.Unmarshal(deliverJSON, &deliverFrame)

	if deliverFrame.Type != types.FrameDeliver {
		t.Fatalf("expected deliver frame, got %s", deliverFrame.Type)
	}

	var deliver types.DeliverPayload
	json.Unmarshal(deliverFrame.Payload, &deliver)

	if deliver.Seq != 1 {
		t.Errorf("expected seq 1, got %d", deliver.Seq)
	}

	// Content should have provenance header
	if !strings.Contains(deliver.Envelope.Content, "[GATEWAY") {
		t.Error("delivered content should contain provenance header")
	}
	if !strings.Contains(deliver.Envelope.Content, "Hello agent!") {
		t.Error("delivered content should contain original message")
	}
}

// --- Test helpers ---

// testSetup creates a test gateway with a connected agent, ready for frame exchange.
type testSetup struct {
	mgr         *Manager
	sched       *scheduler.Scheduler
	ws          *websocket.Conn
	noise       *gwNoise.NoiseKK
	server      *httptest.Server
}

func (ts *testSetup) Close() {
	ts.ws.Close()
	ts.server.Close()
}

// sendFrame encrypts and sends a JSON frame to the gateway.
func (ts *testSetup) sendFrame(t *testing.T, frameType types.FrameType, payload any) {
	t.Helper()
	payloadJSON, _ := json.Marshal(payload)
	frame := types.Frame{Type: frameType, Payload: payloadJSON}
	frameJSON, _ := json.Marshal(frame)
	encrypted, err := ts.noise.Encrypt(frameJSON, nil)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if err := ts.ws.WriteMessage(websocket.BinaryMessage, encrypted); err != nil {
		t.Fatalf("send frame: %v", err)
	}
}

// readFrame reads and decrypts a JSON frame from the gateway.
func (ts *testSetup) readFrame(t *testing.T) types.Frame {
	t.Helper()
	ts.ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, ciphertext, err := ts.ws.ReadMessage()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	plaintext, err := ts.noise.Decrypt(ciphertext, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	var frame types.Frame
	json.Unmarshal(plaintext, &frame)
	return frame
}

// newTestSetup creates a full test gateway + connected agent.
func newTestSetup(t *testing.T) *testSetup {
	t.Helper()

	gatewayKey, _ := gwNoise.GenerateKeypair()
	agentKey, _ := gwNoise.GenerateKeypair()

	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Listen:  ":0",
			DataDir: t.TempDir(),
		},
		Agents: map[string]config.AgentConfig{
			"test-agent": {
				DisplayName: "Test Agent",
				PublicKey:   fmt.Sprintf("x25519:%s", encodeKey(agentKey.Public)),
			},
		},
		Channels:  map[string]config.ChannelConfig{},
		Scheduler: config.SchedulerConfig{Jobs: map[string]config.StaticJobConfig{}},
	}

	st, _ := store.New(cfg.Gateway.DataDir)
	r := router.New(cfg, st)
	sched := scheduler.New(cfg, r, st)
	mgr := NewManager(cfg, st, r, sched, gatewayKey)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ws", mgr.HandleWebSocket)
	srv := httptest.NewServer(mux)

	// Connect and handshake
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/ws"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		srv.Close()
		t.Fatalf("dial: %v", err)
	}

	clientNoise, _ := gwNoise.NewNoiseKK("initiator", agentKey, gatewayKey.Public, []byte("gateway-v1"))
	msg1, _ := clientNoise.WriteHandshakeMessage(nil)
	agentIDBytes := []byte("test-agent")
	raw := make([]byte, 1+len(agentIDBytes)+len(msg1))
	raw[0] = byte(len(agentIDBytes))
	copy(raw[1:], agentIDBytes)
	copy(raw[1+len(agentIDBytes):], msg1)
	ws.WriteMessage(websocket.BinaryMessage, raw)

	_, msg2Raw, _ := ws.ReadMessage()
	clientNoise.ReadHandshakeMessage(msg2Raw)

	ts := &testSetup{mgr: mgr, sched: sched, ws: ws, noise: clientNoise, server: srv}

	// Send hello, consume welcome
	ts.sendFrame(t, types.FrameHello, types.HelloPayload{AgentID: "test-agent"})
	welcome := ts.readFrame(t)
	if welcome.Type != types.FrameWelcome {
		t.Fatalf("expected welcome, got %s", welcome.Type)
	}

	return ts
}

// --- Scheduler Skill Tests ---

func TestScheduleCreateAndList(t *testing.T) {
	ts := newTestSetup(t)
	defer ts.Close()

	// Create a job
	ts.sendFrame(t, types.FrameScheduleCreate, types.ScheduleCreatePayload{
		RequestID: "req-1",
		Name:      "morning-briefing",
		Cron:      "0 7 * * *",
		Prompt:    "Good morning! What's on the schedule today?",
	})

	result := ts.readFrame(t)
	if result.Type != types.FrameScheduleResult {
		t.Fatalf("expected schedule_result, got %s", result.Type)
	}

	var createResult types.ScheduleResultPayload
	json.Unmarshal(result.Payload, &createResult)

	if !createResult.Success {
		t.Fatalf("create failed: %s", createResult.Error)
	}
	if createResult.RequestID != "req-1" {
		t.Errorf("request_id = %q, want %q", createResult.RequestID, "req-1")
	}
	if createResult.JobID == "" {
		t.Error("expected job_id in result")
	}
	jobID := createResult.JobID

	// List jobs
	ts.sendFrame(t, types.FrameScheduleList, types.ScheduleListPayload{
		RequestID: "req-2",
	})

	listResult := ts.readFrame(t)
	var listPayload types.ScheduleResultPayload
	json.Unmarshal(listResult.Payload, &listPayload)

	if !listPayload.Success {
		t.Fatalf("list failed: %s", listPayload.Error)
	}
	if len(listPayload.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(listPayload.Jobs))
	}
	if listPayload.Jobs[0].ID != jobID {
		t.Errorf("job ID = %q, want %q", listPayload.Jobs[0].ID, jobID)
	}
	if listPayload.Jobs[0].Name != "morning-briefing" {
		t.Errorf("job name = %q, want %q", listPayload.Jobs[0].Name, "morning-briefing")
	}
	if listPayload.Jobs[0].OwnerAgent != "test-agent" {
		t.Errorf("owner_agent = %q, want %q", listPayload.Jobs[0].OwnerAgent, "test-agent")
	}
}

func TestScheduleUpdate(t *testing.T) {
	ts := newTestSetup(t)
	defer ts.Close()

	// Create
	ts.sendFrame(t, types.FrameScheduleCreate, types.ScheduleCreatePayload{
		RequestID: "req-1",
		Name:      "reminder",
		Cron:      "0 9 * * *",
		Prompt:    "Original prompt",
	})
	createFrame := ts.readFrame(t)
	var createResult types.ScheduleResultPayload
	json.Unmarshal(createFrame.Payload, &createResult)
	jobID := createResult.JobID

	// Update the prompt and schedule
	newName := "updated-reminder"
	newPrompt := "Updated prompt text"
	newCron := "30 10 * * *"
	ts.sendFrame(t, types.FrameScheduleUpdate, types.ScheduleUpdatePayload{
		RequestID: "req-2",
		JobID:     jobID,
		Name:      &newName,
		Prompt:    &newPrompt,
		Cron:      &newCron,
	})

	updateFrame := ts.readFrame(t)
	var updateResult types.ScheduleResultPayload
	json.Unmarshal(updateFrame.Payload, &updateResult)

	if !updateResult.Success {
		t.Fatalf("update failed: %s", updateResult.Error)
	}

	// Verify via list
	ts.sendFrame(t, types.FrameScheduleList, types.ScheduleListPayload{RequestID: "req-3"})
	listFrame := ts.readFrame(t)
	var listResult types.ScheduleResultPayload
	json.Unmarshal(listFrame.Payload, &listResult)

	if len(listResult.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(listResult.Jobs))
	}
	job := listResult.Jobs[0]
	if job.Name != "updated-reminder" {
		t.Errorf("name = %q, want %q", job.Name, "updated-reminder")
	}
	if job.Prompt != "Updated prompt text" {
		t.Errorf("prompt = %q, want %q", job.Prompt, "Updated prompt text")
	}
	if job.Schedule.Cron != "30 10 * * *" {
		t.Errorf("cron = %q, want %q", job.Schedule.Cron, "30 10 * * *")
	}
}

func TestScheduleDelete(t *testing.T) {
	ts := newTestSetup(t)
	defer ts.Close()

	// Create
	ts.sendFrame(t, types.FrameScheduleCreate, types.ScheduleCreatePayload{
		RequestID: "req-1",
		Name:      "to-delete",
		Cron:      "0 12 * * *",
		Prompt:    "This will be deleted",
	})
	createFrame := ts.readFrame(t)
	var createResult types.ScheduleResultPayload
	json.Unmarshal(createFrame.Payload, &createResult)
	jobID := createResult.JobID

	// Delete
	ts.sendFrame(t, types.FrameScheduleDelete, types.ScheduleDeletePayload{
		RequestID: "req-2",
		JobID:     jobID,
	})

	deleteFrame := ts.readFrame(t)
	var deleteResult types.ScheduleResultPayload
	json.Unmarshal(deleteFrame.Payload, &deleteResult)

	if !deleteResult.Success {
		t.Fatalf("delete failed: %s", deleteResult.Error)
	}

	// Verify via list
	ts.sendFrame(t, types.FrameScheduleList, types.ScheduleListPayload{RequestID: "req-3"})
	listFrame := ts.readFrame(t)
	var listResult types.ScheduleResultPayload
	json.Unmarshal(listFrame.Payload, &listResult)

	if len(listResult.Jobs) != 0 {
		t.Errorf("expected 0 jobs after delete, got %d", len(listResult.Jobs))
	}
}

func TestScheduleAgentIsolation(t *testing.T) {
	ts := newTestSetup(t)
	defer ts.Close()

	// Create a job as test-agent
	ts.sendFrame(t, types.FrameScheduleCreate, types.ScheduleCreatePayload{
		RequestID: "req-1",
		Name:      "my-job",
		Cron:      "0 8 * * *",
		Prompt:    "Agent's own job",
	})
	createFrame := ts.readFrame(t)
	var createResult types.ScheduleResultPayload
	json.Unmarshal(createFrame.Payload, &createResult)

	// Verify the job's owner_agent was set from the Noise session, not from the frame
	ts.sendFrame(t, types.FrameScheduleList, types.ScheduleListPayload{RequestID: "req-2"})
	listFrame := ts.readFrame(t)
	var listResult types.ScheduleResultPayload
	json.Unmarshal(listFrame.Payload, &listResult)

	if len(listResult.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(listResult.Jobs))
	}
	if listResult.Jobs[0].OwnerAgent != "test-agent" {
		t.Errorf("owner_agent = %q, want %q (should be set from Noise session)",
			listResult.Jobs[0].OwnerAgent, "test-agent")
	}

	// Try to delete a job owned by another agent (should fail)
	ts.sendFrame(t, types.FrameScheduleDelete, types.ScheduleDeletePayload{
		RequestID: "req-3",
		JobID:     "nonexistent-job-id",
	})
	deleteFrame := ts.readFrame(t)
	var deleteResult types.ScheduleResultPayload
	json.Unmarshal(deleteFrame.Payload, &deleteResult)

	if deleteResult.Success {
		t.Error("delete of nonexistent/unowned job should fail")
	}
}

func TestScheduleOnceAtJob(t *testing.T) {
	ts := newTestSetup(t)
	defer ts.Close()

	// Create a one-shot job
	futureTime := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	ts.sendFrame(t, types.FrameScheduleCreate, types.ScheduleCreatePayload{
		RequestID:       "req-1",
		Name:            "dentist-reminder",
		OnceAt:          futureTime,
		Prompt:          "Don't forget the dentist at 3pm!",
		ResponseChannel: "discord-family",
	})

	createFrame := ts.readFrame(t)
	var createResult types.ScheduleResultPayload
	json.Unmarshal(createFrame.Payload, &createResult)

	if !createResult.Success {
		t.Fatalf("create one-shot failed: %s", createResult.Error)
	}

	// Verify it's listed with correct schedule type
	ts.sendFrame(t, types.FrameScheduleList, types.ScheduleListPayload{RequestID: "req-2"})
	listFrame := ts.readFrame(t)
	var listResult types.ScheduleResultPayload
	json.Unmarshal(listFrame.Payload, &listResult)

	if len(listResult.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(listResult.Jobs))
	}
	job := listResult.Jobs[0]
	if job.Schedule.Type != "once" {
		t.Errorf("schedule type = %q, want %q", job.Schedule.Type, "once")
	}
	if job.ResponseChannel != "discord-family" {
		t.Errorf("response_channel = %q, want %q", job.ResponseChannel, "discord-family")
	}
}

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
	mgr := NewManager(cfg, st, r, gatewayKey)

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
	mgr := NewManager(cfg, st, r, gatewayKey)

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

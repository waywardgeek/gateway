// Package agent manages WebSocket connections to AI agents.
// Each agent connects via Noise-KK encrypted WebSocket, authenticates,
// and receives prompt envelopes from the router.
package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	noiselib "github.com/flynn/noise"
	"github.com/waywardgeek/gateway/pkg/config"
	gwNoise "github.com/waywardgeek/gateway/pkg/noise"
	"github.com/waywardgeek/gateway/pkg/router"
	"github.com/waywardgeek/gateway/pkg/scheduler"
	"github.com/waywardgeek/gateway/pkg/store"
	"github.com/waywardgeek/gateway/pkg/types"
)

const (
	Version        = "0.1.0"
	writeTimeout   = 10 * time.Second
	readTimeout    = 60 * time.Second
	pingInterval   = 30 * time.Second
)

// ResponseHandler is called when an agent sends a response to a delivered prompt.
// messageID is the original prompt's message ID, content is the agent's response.
type ResponseHandler func(agentID, messageID, content string, metadata map[string]string)

// Manager manages all agent WebSocket connections.
type Manager struct {
	mu              sync.RWMutex
	cfg             *config.Config
	store           *store.Store
	router          *router.Router
	scheduler       *scheduler.Scheduler
	gatewayKey      noiselib.DHKey
	conns           map[string]*agentConn // agent ID → active connection
	upgrader        websocket.Upgrader
	responseHandler ResponseHandler
}

// agentConn represents a connected agent.
type agentConn struct {
	agentID string
	ws      *websocket.Conn
	noise   *gwNoise.NoiseKK
	cancel  context.CancelFunc
	sendMu  sync.Mutex
}

// NewManager creates a new agent connection manager.
func NewManager(cfg *config.Config, st *store.Store, r *router.Router, sched *scheduler.Scheduler, gatewayKey noiselib.DHKey) *Manager {
	m := &Manager{
		cfg:        cfg,
		store:      st,
		router:     r,
		scheduler:  sched,
		gatewayKey: gatewayKey,
		conns:      make(map[string]*agentConn),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	// Register as the router's notifier
	r.SetNotifier(m.notifyAgent)

	return m
}

// SetResponseHandler sets the function called when an agent responds to a prompt.
func (m *Manager) SetResponseHandler(fn ResponseHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responseHandler = fn
}

// HandleWebSocket handles the /v1/ws endpoint.
func (m *Manager) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	ws, err := m.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[agent-mgr] websocket upgrade failed: %v", err)
		return
	}
	defer ws.Close()

	log.Printf("[agent-mgr] new WebSocket connection from %s", r.RemoteAddr)

	// Step 1: Noise-KK handshake
	agentID, noiseSession, err := m.doHandshake(ws)
	if err != nil {
		log.Printf("[agent-mgr] handshake failed: %v", err)
		return
	}

	log.Printf("[agent-mgr] agent %s authenticated via Noise-KK", agentID)

	// Step 2: Register connection
	ctx, cancel := context.WithCancel(context.Background())
	conn := &agentConn{
		agentID: agentID,
		ws:      ws,
		noise:   noiseSession,
		cancel:  cancel,
	}

	m.mu.Lock()
	if old, ok := m.conns[agentID]; ok {
		old.cancel()
	}
	m.conns[agentID] = conn
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		if m.conns[agentID] == conn {
			delete(m.conns, agentID)
		}
		m.mu.Unlock()
		cancel()
		log.Printf("[agent-mgr] agent %s disconnected", agentID)
	}()

	// Step 3: Read hello frame (inside Noise)
	hello, err := m.readFrame(conn)
	if err != nil {
		log.Printf("[agent-mgr] agent %s: failed to read hello: %v", agentID, err)
		return
	}
	if hello.Type != types.FrameHello {
		log.Printf("[agent-mgr] agent %s: expected hello, got %s", agentID, hello.Type)
		return
	}

	var helloPayload types.HelloPayload
	if err := json.Unmarshal(hello.Payload, &helloPayload); err != nil {
		log.Printf("[agent-mgr] agent %s: bad hello payload: %v", agentID, err)
		return
	}

	// Step 4: Send welcome with queued message count
	pending := m.store.GetPendingPrompts(agentID, helloPayload.LastAckedSeq)
	welcome := types.WelcomePayload{
		AgentID:       agentID,
		QueuedCount:   len(pending),
		ServerVersion: Version,
	}
	if err := m.sendFrame(conn, types.FrameWelcome, welcome); err != nil {
		log.Printf("[agent-mgr] agent %s: failed to send welcome: %v", agentID, err)
		return
	}

	// Step 5: Deliver queued messages
	for _, env := range pending {
		if err := m.deliverPrompt(conn, env); err != nil {
			log.Printf("[agent-mgr] agent %s: delivery failed: %v", agentID, err)
			return
		}
	}

	// Step 6: Start ping ticker to keep connection alive
	go m.pingLoop(ctx, conn)

	// Step 7: Read loop
	m.readLoop(ctx, conn)
}

// doHandshake performs the Noise-KK handshake over raw WebSocket frames.
// The handshake uses binary messages (not JSON, not Noise-encrypted yet).
// Message format: [1-byte agent-id-length][agent-id-bytes][noise-message-bytes]
func (m *Manager) doHandshake(ws *websocket.Conn) (string, *gwNoise.NoiseKK, error) {
	// Read message 1 from initiator (agent)
	// Format: [1-byte len][agent-id][noise-kk-msg1]
	_, msg1Raw, err := ws.ReadMessage()
	if err != nil {
		return "", nil, fmt.Errorf("read handshake msg1: %w", err)
	}

	if len(msg1Raw) < 2 {
		return "", nil, fmt.Errorf("handshake msg1 too short")
	}

	idLen := int(msg1Raw[0])
	if len(msg1Raw) < 1+idLen {
		return "", nil, fmt.Errorf("handshake msg1 agent ID truncated")
	}
	agentID := string(msg1Raw[1 : 1+idLen])
	noiseMsg1 := msg1Raw[1+idLen:]

	// Look up agent's public key from config
	agentCfg, ok := m.cfg.Agents[agentID]
	if !ok {
		return "", nil, fmt.Errorf("unknown agent: %s", agentID)
	}

	agentPubKey, err := parsePublicKey(agentCfg.PublicKey)
	if err != nil {
		return "", nil, fmt.Errorf("parse agent %s public key: %w", agentID, err)
	}

	// Create Noise-KK responder
	session, err := gwNoise.NewNoiseKK("responder", m.gatewayKey, agentPubKey, []byte("gateway-v1"))
	if err != nil {
		return "", nil, fmt.Errorf("create noise session: %w", err)
	}

	// Process msg1
	_, err = session.ReadHandshakeMessage(noiseMsg1)
	if err != nil {
		return "", nil, fmt.Errorf("noise read msg1: %w", err)
	}

	// Write msg2
	msg2, err := session.WriteHandshakeMessage(nil)
	if err != nil {
		return "", nil, fmt.Errorf("noise write msg2: %w", err)
	}

	if err := ws.WriteMessage(websocket.BinaryMessage, msg2); err != nil {
		return "", nil, fmt.Errorf("send handshake msg2: %w", err)
	}

	if !session.IsHandshakeComplete() {
		return "", nil, fmt.Errorf("handshake not complete after 2 messages")
	}

	return agentID, session, nil
}

// readFrame reads a Noise-encrypted JSON frame from the WebSocket.
func (m *Manager) readFrame(conn *agentConn) (*types.Frame, error) {
	_, ciphertext, err := conn.ws.ReadMessage()
	if err != nil {
		return nil, err
	}

	plaintext, err := conn.noise.Decrypt(ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	var frame types.Frame
	if err := json.Unmarshal(plaintext, &frame); err != nil {
		return nil, fmt.Errorf("unmarshal frame: %w", err)
	}
	return &frame, nil
}

// sendFrame sends a Noise-encrypted JSON frame over the WebSocket.
func (m *Manager) sendFrame(conn *agentConn, frameType types.FrameType, payload any) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	frame := types.Frame{
		Type:    frameType,
		Payload: payloadJSON,
	}
	frameJSON, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("marshal frame: %w", err)
	}

	encrypted, err := conn.noise.Encrypt(frameJSON, nil)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}

	conn.sendMu.Lock()
	defer conn.sendMu.Unlock()
	conn.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	return conn.ws.WriteMessage(websocket.BinaryMessage, encrypted)
}

// deliverPrompt sends a prompt envelope to a connected agent.
func (m *Manager) deliverPrompt(conn *agentConn, env types.PromptEnvelope) error {
	// Prepend provenance header
	env.Content = router.FormatProvenance(env.Source) + "\n\n" + env.Content

	return m.sendFrame(conn, types.FrameDeliver, types.DeliverPayload{
		Seq:      env.Seq,
		Envelope: env,
	})
}

// readLoop reads frames from a connected agent.
func (m *Manager) readLoop(ctx context.Context, conn *agentConn) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn.ws.SetReadDeadline(time.Now().Add(readTimeout))
		frame, err := m.readFrame(conn)
		if err != nil {
			if ctx.Err() != nil {
				return // context cancelled
			}
			log.Printf("[agent-mgr] agent %s: read error: %v", conn.agentID, err)
			return
		}

		switch frame.Type {
		case types.FrameAck:
			var ack types.AckPayload
			json.Unmarshal(frame.Payload, &ack)
			m.store.AckPrompt(conn.agentID, ack.Seq)
			log.Printf("[agent-mgr] agent %s: acked seq %d", conn.agentID, ack.Seq)

		case types.FrameResponse:
			var resp types.ResponsePayload
			json.Unmarshal(frame.Payload, &resp)
			log.Printf("[agent-mgr] agent %s: response for %s (%d bytes)",
				conn.agentID, resp.MessageID, len(resp.Content))
			m.mu.RLock()
			handler := m.responseHandler
			m.mu.RUnlock()
			if handler != nil {
				handler(conn.agentID, resp.MessageID, resp.Content, resp.Metadata)
			}

		case types.FramePing:
			m.sendFrame(conn, types.FramePong, nil)

		case types.FramePong:
			// heartbeat response from agent, connection is alive

		case types.FrameScheduleCreate:
			var payload types.ScheduleCreatePayload
			json.Unmarshal(frame.Payload, &payload)
			m.handleScheduleCreate(conn, payload)

		case types.FrameScheduleList:
			var payload types.ScheduleListPayload
			json.Unmarshal(frame.Payload, &payload)
			m.handleScheduleList(conn, payload)

		case types.FrameScheduleUpdate:
			var payload types.ScheduleUpdatePayload
			json.Unmarshal(frame.Payload, &payload)
			m.handleScheduleUpdate(conn, payload)

		case types.FrameScheduleDelete:
			var payload types.ScheduleDeletePayload
			json.Unmarshal(frame.Payload, &payload)
			m.handleScheduleDelete(conn, payload)

		default:
			log.Printf("[agent-mgr] agent %s: unhandled frame type: %s", conn.agentID, frame.Type)
		}
	}
}

// pingLoop sends periodic Noise-encrypted ping frames to keep the connection alive.
func (m *Manager) pingLoop(ctx context.Context, conn *agentConn) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.sendFrame(conn, types.FramePing, nil); err != nil {
				log.Printf("[agent-mgr] agent %s: ping failed: %v", conn.agentID, err)
				return
			}
		}
	}
}

// notifyAgent wakes up a connected agent to deliver pending prompts.
func (m *Manager) notifyAgent(agentID string) {
	m.mu.RLock()
	conn, ok := m.conns[agentID]
	m.mu.RUnlock()
	if !ok {
		return
	}

	// Get the latest pending prompt
	pending := m.store.GetPendingPrompts(agentID, 0)
	if len(pending) == 0 {
		return
	}

	// Deliver the latest one (agent already has older ones)
	latest := pending[len(pending)-1]
	if err := m.deliverPrompt(conn, latest); err != nil {
		log.Printf("[agent-mgr] agent %s: notify delivery failed: %v", agentID, err)
	}
}

// --- Scheduler Skill Handlers ---

// handleScheduleCreate processes a schedule_create frame from an agent.
func (m *Manager) handleScheduleCreate(conn *agentConn, payload types.ScheduleCreatePayload) {
	job, err := m.scheduler.CreateJob(conn.agentID, payload.Name, payload.Cron, payload.OnceAt, payload.Prompt, payload.ResponseChannel, payload.Metadata)
	if err != nil {
		m.sendFrame(conn, types.FrameScheduleResult, types.ScheduleResultPayload{
			RequestID: payload.RequestID,
			Success:   false,
			Error:     err.Error(),
		})
		return
	}
	log.Printf("[agent-mgr] agent %s: created job %s (%s)", conn.agentID, job.ID, job.Name)
	m.sendFrame(conn, types.FrameScheduleResult, types.ScheduleResultPayload{
		RequestID: payload.RequestID,
		Success:   true,
		JobID:     job.ID,
	})
}

// handleScheduleList processes a schedule_list frame from an agent.
func (m *Manager) handleScheduleList(conn *agentConn, payload types.ScheduleListPayload) {
	jobs := m.scheduler.ListJobs(conn.agentID)
	m.sendFrame(conn, types.FrameScheduleResult, types.ScheduleResultPayload{
		RequestID: payload.RequestID,
		Success:   true,
		Jobs:      derefJobs(jobs),
	})
}

// handleScheduleUpdate processes a schedule_update frame from an agent.
func (m *Manager) handleScheduleUpdate(conn *agentConn, payload types.ScheduleUpdatePayload) {
	job, err := m.scheduler.UpdateJob(conn.agentID, payload.JobID, payload.Name, payload.Cron, payload.OnceAt, payload.Prompt)
	if err != nil {
		m.sendFrame(conn, types.FrameScheduleResult, types.ScheduleResultPayload{
			RequestID: payload.RequestID,
			Success:   false,
			Error:     err.Error(),
		})
		return
	}
	log.Printf("[agent-mgr] agent %s: updated job %s", conn.agentID, job.ID)
	m.sendFrame(conn, types.FrameScheduleResult, types.ScheduleResultPayload{
		RequestID: payload.RequestID,
		Success:   true,
		JobID:     job.ID,
	})
}

// handleScheduleDelete processes a schedule_delete frame from an agent.
func (m *Manager) handleScheduleDelete(conn *agentConn, payload types.ScheduleDeletePayload) {
	err := m.scheduler.DeleteJob(conn.agentID, payload.JobID)
	if err != nil {
		m.sendFrame(conn, types.FrameScheduleResult, types.ScheduleResultPayload{
			RequestID: payload.RequestID,
			Success:   false,
			Error:     err.Error(),
		})
		return
	}
	log.Printf("[agent-mgr] agent %s: deleted job %s", conn.agentID, payload.JobID)
	m.sendFrame(conn, types.FrameScheduleResult, types.ScheduleResultPayload{
		RequestID: payload.RequestID,
		Success:   true,
		JobID:     payload.JobID,
	})
}

// derefJobs converts []*types.Job to []types.Job for serialization.
func derefJobs(jobs []*types.Job) []types.Job {
	result := make([]types.Job, len(jobs))
	for i, j := range jobs {
		result[i] = *j
	}
	return result
}

// parsePublicKey parses "ed25519:base64..." format into raw bytes.
// For Noise, we need X25519 keys. Ed25519 → X25519 conversion is a future enhancement.
// For MVP, we store X25519 public keys directly.
func parsePublicKey(keyStr string) ([]byte, error) {
	parts := strings.SplitN(keyStr, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("key format must be 'type:base64data'")
	}
	data, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}
	return data, nil
}

// IsConnected returns whether an agent is currently connected.
func (m *Manager) IsConnected(agentID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.conns[agentID]
	return ok
}

// ConnectedAgents returns the IDs of all currently connected agents.
func (m *Manager) ConnectedAgents() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.conns))
	for id := range m.conns {
		ids = append(ids, id)
	}
	return ids
}

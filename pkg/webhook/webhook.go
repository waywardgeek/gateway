// Package webhook provides the HTTP webhook channel for the AI Gateway.
// External services send prompts via POST with HMAC-SHA256 signature verification.
// Supports async (202 Accepted) and sync (block until agent responds) modes.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/waywardgeek/gateway/pkg/config"
	"github.com/waywardgeek/gateway/pkg/router"
)

const (
	defaultTimeout = 30 * time.Second
	maxBodySize    = 64 * 1024 // 64KB
)

// WebhookRequest is the expected JSON body from external services.
type WebhookRequest struct {
	Content     string            `json:"content"`
	UserID      string            `json:"user_id,omitempty"`
	DisplayName string            `json:"display_name,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// WebhookResponse is returned for sync-mode requests.
type WebhookResponse struct {
	MessageID string `json:"message_id"`
	Content   string `json:"content"`
	Status    string `json:"status"` // "ok", "timeout", "error"
}

// Handler manages webhook endpoints for the gateway.
type Handler struct {
	mu       sync.RWMutex
	channels map[string]*webhookChannel // webhook_path → channel
	pending  map[string]chan string      // message_id → response channel (for sync mode)
}

type webhookChannel struct {
	channelID string
	cfg       config.ChannelConfig
	secret    []byte
	router    *router.Router
	timeout   time.Duration
}

// NewHandler creates a new webhook handler.
func NewHandler(cfg *config.Config, r *router.Router) *Handler {
	h := &Handler{
		channels: make(map[string]*webhookChannel),
		pending:  make(map[string]chan string),
	}

	for id, chCfg := range cfg.Channels {
		if chCfg.Type != "webhook" {
			continue
		}
		if chCfg.WebhookPath == "" {
			log.Printf("[webhook] warning: channel %s has no webhook_path, skipping", id)
			continue
		}

		secret := []byte(os.Getenv(chCfg.WebhookSecretEnv))
		if len(secret) == 0 {
			log.Printf("[webhook] warning: channel %s secret env %q is empty (HMAC disabled)", id, chCfg.WebhookSecretEnv)
		}

		timeout := defaultTimeout
		if chCfg.ResponseTimeoutSeconds > 0 {
			timeout = time.Duration(chCfg.ResponseTimeoutSeconds) * time.Second
		}

		h.channels[chCfg.WebhookPath] = &webhookChannel{
			channelID: id,
			cfg:       chCfg,
			secret:    secret,
			router:    r,
			timeout:   timeout,
		}
		log.Printf("[webhook] registered %s → agent %s (path=%s, mode=%s)",
			id, chCfg.RouteTo, chCfg.WebhookPath, chCfg.ResponseMode)
	}

	return h
}

// RegisterRoutes adds webhook HTTP handlers to the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	for path := range h.channels {
		path := path // capture
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			h.handleWebhook(w, r, path)
		})
	}
}

// DeliverResponse routes an agent response to a waiting sync request.
// Returns true if a sync waiter was found.
func (h *Handler) DeliverResponse(messageID, content string) bool {
	h.mu.RLock()
	ch, ok := h.pending[messageID]
	h.mu.RUnlock()
	if !ok {
		return false
	}

	select {
	case ch <- content:
		return true
	default:
		return false
	}
}

func (h *Handler) handleWebhook(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	wc := h.channels[path]
	if wc == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Read body
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	// Verify HMAC signature if secret is configured
	if len(wc.secret) > 0 {
		sig := r.Header.Get("X-Signature-256")
		if sig == "" {
			http.Error(w, "missing X-Signature-256 header", http.StatusUnauthorized)
			return
		}
		if !verifyHMAC(wc.secret, body, sig) {
			http.Error(w, "invalid signature", http.StatusForbidden)
			return
		}
	}

	// Parse request
	var req WebhookRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	// Set defaults
	if req.UserID == "" {
		req.UserID = "webhook"
	}
	if req.DisplayName == "" {
		req.DisplayName = wc.channelID
	}

	log.Printf("[webhook] %s: message from %s: %.80s", wc.channelID, req.UserID, req.Content)

	// Route the message
	err = wc.router.Deliver(wc.channelID, req.UserID, req.DisplayName, req.Content, req.Metadata)
	if err != nil {
		log.Printf("[webhook] %s: route error: %v", wc.channelID, err)
		http.Error(w, "routing error", http.StatusInternalServerError)
		return
	}

	// Sync mode: block until agent responds
	if wc.cfg.ResponseMode == "sync" {
		h.handleSync(w, wc)
		return
	}

	// Async mode: return 202 immediately
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(WebhookResponse{Status: "accepted"})
}

// handleSync waits for the agent's response or times out.
func (h *Handler) handleSync(w http.ResponseWriter, wc *webhookChannel) {
	// Get the message ID from the store — it was the last enqueued for this agent.
	// For sync mode, we need the router to return the message ID.
	// KISS approach: use the pending map with a timeout.

	// Create a response channel keyed by the webhook channel ID + timestamp
	// since we may not know the exact message ID here.
	// Better approach: modify Deliver to return the message ID.
	// For now, use a simple pending channel keyed by channel ID (one sync request at a time).
	waitKey := wc.channelID
	ch := make(chan string, 1)

	h.mu.Lock()
	h.pending[waitKey] = ch
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.pending, waitKey)
		h.mu.Unlock()
	}()

	select {
	case content := <-ch:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(WebhookResponse{
			Content: content,
			Status:  "ok",
		})
	case <-time.After(wc.timeout):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGatewayTimeout)
		json.NewEncoder(w).Encode(WebhookResponse{
			Status: "timeout",
		})
	}
}

// verifyHMAC checks the HMAC-SHA256 signature of a request body.
// Expected format: "sha256=hex_digest"
func verifyHMAC(secret, body []byte, signature string) bool {
	// Strip "sha256=" prefix if present
	if len(signature) > 7 && signature[:7] == "sha256=" {
		signature = signature[7:]
	}

	sigBytes, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	expected := mac.Sum(nil)

	return hmac.Equal(sigBytes, expected)
}

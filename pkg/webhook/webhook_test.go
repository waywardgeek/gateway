package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/waywardgeek/gateway/pkg/config"
	"github.com/waywardgeek/gateway/pkg/router"
	"github.com/waywardgeek/gateway/pkg/store"
)

func testConfig(secret string) *config.Config {
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Listen:  ":0",
			DataDir: ".",
		},
		Agents: map[string]config.AgentConfig{
			"test-agent": {
				DisplayName: "Test",
				PublicKey:   "x25519:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			},
		},
		Channels: map[string]config.ChannelConfig{
			"test-webhook": {
				Type:             "webhook",
				RouteTo:          "test-agent",
				Trust:            "trusted",
				WebhookPath:      "/hook/test",
				WebhookSecretEnv: "TEST_WEBHOOK_SECRET",
				ResponseMode:     "async",
			},
			"sync-webhook": {
				Type:                   "webhook",
				RouteTo:                "test-agent",
				Trust:                  "trusted",
				WebhookPath:            "/hook/sync",
				WebhookSecretEnv:       "TEST_WEBHOOK_SECRET",
				ResponseMode:           "sync",
				ResponseTimeoutSeconds: 1,
			},
		},
		Scheduler: config.SchedulerConfig{Jobs: map[string]config.StaticJobConfig{}},
	}
	return cfg
}

func signBody(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestAsyncWebhook(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_SECRET", "test-secret-123")

	cfg := testConfig("test-secret-123")
	st, _ := store.New(t.TempDir())
	r := router.New(cfg, st)
	h := NewHandler(cfg, r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(WebhookRequest{
		Content:     "Hello from webhook!",
		UserID:      "deephold-server",
		DisplayName: "Deephold",
	})

	req, _ := http.NewRequest("POST", srv.URL+"/hook/test", bytes.NewReader(body))
	req.Header.Set("X-Signature-256", signBody([]byte("test-secret-123"), body))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202", resp.StatusCode)
	}

	var result WebhookResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Status != "accepted" {
		t.Errorf("status = %q, want %q", result.Status, "accepted")
	}

	// Verify message was routed to agent
	pending := st.GetPendingPrompts("test-agent", 0)
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending prompt, got %d", len(pending))
	}
	if pending[0].Source.UserID != "deephold-server" {
		t.Errorf("user_id = %q, want %q", pending[0].Source.UserID, "deephold-server")
	}
}

func TestWebhookBadSignature(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_SECRET", "test-secret-123")

	cfg := testConfig("test-secret-123")
	st, _ := store.New(t.TempDir())
	r := router.New(cfg, st)
	h := NewHandler(cfg, r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(WebhookRequest{Content: "evil message"})

	// Wrong signature
	req, _ := http.NewRequest("POST", srv.URL+"/hook/test", bytes.NewReader(body))
	req.Header.Set("X-Signature-256", "sha256=0000000000000000000000000000000000000000000000000000000000000000")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestWebhookMissingSignature(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_SECRET", "test-secret-123")

	cfg := testConfig("test-secret-123")
	st, _ := store.New(t.TempDir())
	r := router.New(cfg, st)
	h := NewHandler(cfg, r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(WebhookRequest{Content: "no sig"})
	req, _ := http.NewRequest("POST", srv.URL+"/hook/test", bytes.NewReader(body))
	// No signature header

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestWebhookMethodNotAllowed(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_SECRET", "test-secret-123")

	cfg := testConfig("test-secret-123")
	st, _ := store.New(t.TempDir())
	r := router.New(cfg, st)
	h := NewHandler(cfg, r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/hook/test")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestSyncWebhookTimeout(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_SECRET", "test-secret-123")

	cfg := testConfig("test-secret-123")
	st, _ := store.New(t.TempDir())
	r := router.New(cfg, st)
	h := NewHandler(cfg, r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(WebhookRequest{Content: "sync test"})
	req, _ := http.NewRequest("POST", srv.URL+"/hook/sync", bytes.NewReader(body))
	req.Header.Set("X-Signature-256", signBody([]byte("test-secret-123"), body))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	// Should timeout (1 second configured) since no agent responds
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", resp.StatusCode)
	}

	var result WebhookResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Status != "timeout" {
		t.Errorf("status = %q, want %q", result.Status, "timeout")
	}
}

func TestSyncWebhookResponse(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_SECRET", "test-secret-123")

	cfg := testConfig("test-secret-123")
	st, _ := store.New(t.TempDir())
	r := router.New(cfg, st)
	h := NewHandler(cfg, r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Simulate agent responding in background
	done := make(chan struct{})
	go func() {
		defer close(done)
		body, _ := json.Marshal(WebhookRequest{Content: "sync request"})
		req, _ := http.NewRequest("POST", srv.URL+"/hook/sync", bytes.NewReader(body))
		req.Header.Set("X-Signature-256", signBody([]byte("test-secret-123"), body))

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("request: %v", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}

		var result WebhookResponse
		json.NewDecoder(resp.Body).Decode(&result)
		if result.Status != "ok" {
			t.Errorf("status = %q, want %q", result.Status, "ok")
		}
		if result.Content != "Agent says hello!" {
			t.Errorf("content = %q, want %q", result.Content, "Agent says hello!")
		}
	}()

	// Wait a bit for the HTTP request to register the sync waiter, then deliver
	for i := 0; i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
		if h.DeliverResponse("sync-webhook", "Agent says hello!") {
			break
		}
	}

	<-done
}

func TestWebhookEmptyContent(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_SECRET", "test-secret-123")

	cfg := testConfig("test-secret-123")
	st, _ := store.New(t.TempDir())
	r := router.New(cfg, st)
	h := NewHandler(cfg, r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(WebhookRequest{Content: ""})
	req, _ := http.NewRequest("POST", srv.URL+"/hook/test", bytes.NewReader(body))
	req.Header.Set("X-Signature-256", signBody([]byte("test-secret-123"), body))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

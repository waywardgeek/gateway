package twilio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/waywardgeek/gateway/pkg/config"
	"github.com/waywardgeek/gateway/pkg/router"
)

// Channel manages Twilio ConversationRelay calls.
type Channel struct {
	mu          sync.RWMutex
	cfg         config.TwilioConfig
	router      *router.Router
	sessions    map[string]*CallSession // callSid → session
	accountSID  string
	authToken   string
}

// New creates a new Twilio channel.
func New(cfg config.TwilioConfig, r *router.Router) *Channel {
	return &Channel{
		cfg:      cfg,
		router:   r,
		sessions: make(map[string]*CallSession),
	}
}

// Start initializes the channel and starts background cleanup.
func (c *Channel) Start(ctx context.Context) error {
	c.accountSID = os.Getenv(c.cfg.AccountSIDEnv)
	c.authToken = os.Getenv(c.cfg.AuthTokenEnv)

	if c.accountSID == "" || c.authToken == "" {
		return fmt.Errorf("twilio credentials not found in environment")
	}

	// Clean up ended sessions every 5 minutes
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.pruneSessions()
			case <-ctx.Done():
				return
			}
		}
	}()

	log.Printf("[twilio] initialized with phone number %s", c.cfg.PhoneNumber)
	return nil
}

// MakeCall initiates an outbound call via Twilio REST API.
func (c *Channel) MakeCall(agentName, targetPhone, targetName, announcement string) (string, error) {
	// Create session
	session := &CallSession{
		AgentName:    agentName,
		TargetPhone:  targetPhone,
		TargetName:   targetName,
		Announcement: announcement,
		Status:       "ringing",
		StartedAt:    time.Now(),
		ResponseChan: make(chan string, 100),
	}

	// Twilio REST API URL
	apiURL := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Calls.json", c.accountSID)

	// Callback URL for TwiML
	// We'll use a temporary ID for the session until we get the CallSID from Twilio
	tempID := fmt.Sprintf("temp-%d", time.Now().UnixNano())
	c.mu.Lock()
	c.sessions[tempID] = session
	c.mu.Unlock()

	callbackURL := fmt.Sprintf("%s/twilio/twiml?session_id=%s", c.cfg.BaseURL, tempID)

	data := url.Values{}
	data.Set("To", targetPhone)
	data.Set("From", c.cfg.PhoneNumber)
	data.Set("Url", callbackURL)

	log.Printf("[twilio] calling %s (%s)", targetPhone, targetName)

	req, err := http.NewRequest("POST", apiURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}

	req.SetBasicAuth(c.accountSID, c.authToken)
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[twilio] API error %s: %s", resp.Status, string(body))
		return "", fmt.Errorf("twilio API returned status %s", resp.Status)
	}

	// Parse CallSID from response
	var result struct {
		SID string `json:"sid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode twilio response: %w", err)
	}

	// Update session with real CallSID, keep tempID as alias for TwiML callback
	c.mu.Lock()
	session.CallSID = result.SID
	c.sessions[result.SID] = session
	// Don't delete tempID — Twilio's TwiML callback will use it
	c.mu.Unlock()

	log.Printf("[twilio] initiated call %s to %s (%s)", result.SID, targetPhone, targetName)
	return result.SID, nil
}

// GetSession retrieves a session by CallSID or temp session_id.
func (c *Channel) GetSession(id string) *CallSession {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessions[id]
}

// DeliverResponse routes an agent response to an active call session.
func (c *Channel) DeliverResponse(callSID, content string) bool {
	session := c.GetSession(callSID)
	if session == nil {
		return false
	}

	session.mu.Lock()
	ws := session.WSConn
	session.mu.Unlock()

	if ws == nil {
		// If WebSocket not yet connected, we can't stream tokens.
		// But we can buffer the response? 
		// Actually, the agent responds to a "prompt" message from the WS.
		// So the WS should be active.
		return false
	}

	// Send to response channel for the WS loop to pick up and stream
	select {
	case session.ResponseChan <- content:
		return true
	default:
		return false
	}
}

// pruneSessions removes ended sessions older than 30 minutes.
func (c *Channel) pruneSessions() {
	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := time.Now().Add(-30 * time.Minute)
	for id, s := range c.sessions {
		s.mu.Lock()
		ended := s.Status == "ended" && s.StartedAt.Before(cutoff)
		s.mu.Unlock()
		if ended {
			delete(c.sessions, id)
		}
	}
}

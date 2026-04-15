package twilio

import (
	"fmt"
	"net/http"
	"strings"
)

// RegisterRoutes registers the Twilio HTTP handlers.
func (c *Channel) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/twilio/twiml", c.HandleTwiML)
	mux.HandleFunc("/twilio/ws", c.HandleWebSocket)
}

// HandleTwiML returns the TwiML for a call.
func (c *Channel) HandleTwiML(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "missing session_id", http.StatusBadRequest)
		return
	}

	session := c.GetSession(sessionID)
	if session == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// Build WebSocket URL
	wsURL := strings.Replace(c.cfg.BaseURL, "http", "ws", 1) + "/twilio/ws"

	// Default values
	voice := c.cfg.DefaultVoice
	if voice == "" {
		voice = "Google.en-US-Neural2-C"
	}
	ttsProvider := c.cfg.DefaultTTSProvider
	if ttsProvider == "" {
		ttsProvider = "google"
	}
	transcriptionProvider := c.cfg.DefaultTranscriptionProvider
	if transcriptionProvider == "" {
		transcriptionProvider = "deepgram"
	}

	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<Response>
  <Connect>
    <ConversationRelay 
      url="%s" 
      welcomeGreeting="" 
      voice="%s" 
      ttsProvider="%s" 
      transcriptionProvider="%s" 
      interruptible="true">
      <Parameter name="session_id" value="%s" />
    </ConversationRelay>
  </Connect>
</Response>`, wsURL, voice, ttsProvider, transcriptionProvider, sessionID)
}

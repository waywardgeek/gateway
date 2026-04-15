package twilio

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/waywardgeek/gateway/pkg/types"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Twilio Message Types
type twilioMsg struct {
	Type         string `json:"type"`
	CallSid      string `json:"callSid,omitempty"`
	VoicePrompt  string `json:"voicePrompt,omitempty"`
	Last         bool   `json:"last,omitempty"`
	Token        string `json:"token,omitempty"`
	Text         string `json:"text,omitempty"`
	CustomParams map[string]string `json:"customParameters,omitempty"`
}

func (c *Channel) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[twilio-ws] upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	var session *CallSession

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[twilio-ws] read error: %v", err)
			break
		}

		var msg twilioMsg
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("[twilio-ws] unmarshal error: %v", err)
			continue
		}

		switch msg.Type {
		case "setup":
			sessionID := msg.CustomParams["session_id"]
			session = c.GetSession(sessionID)
			if session == nil {
				log.Printf("[twilio-ws] session %s not found", sessionID)
				return
			}

			session.mu.Lock()
			session.WSConn = conn
			session.Status = "connected"
			session.mu.Unlock()

			log.Printf("[twilio-ws] call %s connected for agent %s", session.CallSID, session.AgentName)

			// Start response loop
			go c.responseLoop(session)

			// Send initial announcement
			if session.Announcement != "" {
				c.streamText(session, session.Announcement)
				session.AddTranscript("puffin", session.Announcement)
			}

		case "prompt":
			if session == nil {
				continue
			}
			log.Printf("[twilio-ws] prompt from %s: %s", session.TargetName, msg.VoicePrompt)
			session.AddTranscript(session.TargetName, msg.VoicePrompt)

			// Prefix content with voice call context so the agent knows this is a phone call
			voiceContext := fmt.Sprintf("[Voice call with %s — respond conversationally, no emoji, no markdown] %s", session.TargetName, msg.VoicePrompt)

			// Forward to agent via DeliverEnvelope (no channel config needed)
			env := types.PromptEnvelope{
				MessageID: uuid.Must(uuid.NewV7()).String(),
				AgentID:   session.AgentName,
				Source: types.PromptSource{
					Type:        "twilio",
					ChannelID:   "twilio",
					UserID:      session.TargetPhone,
					DisplayName: session.TargetName,
					Trust:       types.TrustTrusted,
				},
				Timestamp:    time.Now().UTC(),
				Content:      voiceContext,
				ResponseMode: types.ResponseAsync,
				Metadata: map[string]string{
					"twilio_call_sid": session.CallSID,
					"twilio_target":   session.TargetName,
				},
			}
			c.router.DeliverEnvelope(env)

		case "interrupt":
			log.Printf("[twilio-ws] call %s interrupted", session.CallSID)
			// TODO: handle interruption (e.g. stop streaming current response)

		case "error":
			log.Printf("[twilio-ws] twilio error: %s", string(message))

		case "dtmf":
			log.Printf("[twilio-ws] DTMF: %s", msg.Token)
		}
	}

	if session != nil {
		session.mu.Lock()
		session.Status = "ended"
		session.WSConn = nil
		session.mu.Unlock()
		log.Printf("[twilio-ws] call %s ended", session.CallSID)
	}
}

func (c *Channel) streamText(session *CallSession, text string) {
	session.mu.Lock()
	conn := session.WSConn
	session.mu.Unlock()

	if conn == nil {
		return
	}

	// Twilio ConversationRelay expects "text" type messages with "token" field for TTS.
	msg := map[string]interface{}{
		"type":  "text",
		"token": text,
		"last":  true,
	}
	
	data, _ := json.Marshal(msg)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("[twilio-ws] write error: %v", err)
	}
}

func (c *Channel) responseLoop(session *CallSession) {
	for {
		session.mu.Lock()
		status := session.Status
		session.mu.Unlock()

		if status == "ended" {
			return
		}

		select {
		case content := <-session.ResponseChan:
			c.streamText(session, content)
			session.AddTranscript("puffin", content)
		case <-time.After(1 * time.Second):
			// Just loop and check status
		}
	}
}

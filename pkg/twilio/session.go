package twilio

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// CallSession represents an active or recent Twilio call.
type CallSession struct {
	CallSID      string
	AgentName    string // which agent handles this call
	TargetPhone  string
	TargetName   string // "mom", "bill" — for context
	Announcement string // initial message to deliver
	Transcript   []TranscriptEntry
	WSConn       *websocket.Conn
	StartedAt    time.Time
	Status       string // "ringing", "connected", "ended"
	
	// Channel to receive agent response tokens
	ResponseChan chan string
	
	mu sync.Mutex
}

type TranscriptEntry struct {
	Speaker   string // "puffin" or person name
	Text      string
	Timestamp time.Time
}

func (s *CallSession) AddTranscript(speaker, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Transcript = append(s.Transcript, TranscriptEntry{
		Speaker:   speaker,
		Text:      text,
		Timestamp: time.Now(),
	})
}

func (s *CallSession) GetTranscript() []TranscriptEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Transcript
}

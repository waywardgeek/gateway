// Package types defines the core data structures for the AI Gateway.
package types

import (
	"encoding/json"
	"time"
)

// PromptEnvelope is the canonical message format flowing through the gateway.
type PromptEnvelope struct {
	MessageID      string            `json:"message_id"`
	Seq            int64             `json:"seq"`
	AgentID        string            `json:"agent_id"`
	Source         PromptSource      `json:"source"`
	Timestamp      time.Time         `json:"timestamp"`
	ConversationID string            `json:"conversation_id,omitempty"`
	Content        string            `json:"content"`
	ResponseMode   ResponseMode      `json:"response_mode"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// PromptSource identifies where a prompt came from.
type PromptSource struct {
	Type        string `json:"type"`        // "discord", "noise", "webhook", "scheduler"
	ChannelID   string `json:"channel_id"`  // config channel ID
	UserID      string `json:"user_id"`     // source user identifier
	DisplayName string `json:"display_name"`
	Trust       Trust  `json:"trust"`       // "owner", "trusted", "external"
}

// ResponseMode controls how the gateway handles agent responses.
type ResponseMode string

const (
	ResponseAsync         ResponseMode = "async"
	ResponseSync          ResponseMode = "sync"
	ResponseFireAndForget ResponseMode = "fire_and_forget"
)

// Trust level for prompt sources.
type Trust string

const (
	TrustOwner    Trust = "owner"
	TrustTrusted  Trust = "trusted"
	TrustExternal Trust = "external"
)

// --- WebSocket Frame Types ---

// Frame is the top-level structure for all WebSocket messages.
type Frame struct {
	Type    FrameType       `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// FrameType identifies the kind of WebSocket frame.
type FrameType string

// Agent → Gateway frame types
const (
	FrameHello          FrameType = "hello"
	FrameAck            FrameType = "ack"
	FrameResponse       FrameType = "response"
	FrameScheduleCreate FrameType = "schedule_create"
	FrameScheduleList   FrameType = "schedule_list"
	FrameScheduleUpdate FrameType = "schedule_update"
	FrameScheduleDelete FrameType = "schedule_delete"
	FramePing           FrameType = "ping"
)

// Gateway → Agent frame types
const (
	FrameWelcome        FrameType = "welcome"
	FrameDeliver        FrameType = "deliver"
	FrameScheduleResult FrameType = "schedule_result"
	FramePong           FrameType = "pong"
	FrameError          FrameType = "error"
)

// HelloPayload is sent by the agent on connect.
type HelloPayload struct {
	AgentID     string `json:"agent_id"`
	Version     string `json:"version"`
	LastAckedSeq int64 `json:"last_acked_seq"`
}

// WelcomePayload is sent by the gateway after successful auth.
type WelcomePayload struct {
	AgentID       string `json:"agent_id"`
	QueuedCount   int    `json:"queued_count"`
	ServerVersion string `json:"server_version"`
}

// AckPayload acknowledges receipt of a delivered message.
type AckPayload struct {
	Seq int64 `json:"seq"`
}

// DeliverPayload wraps a prompt envelope for delivery to an agent.
type DeliverPayload struct {
	Seq      int64          `json:"seq"`
	Envelope PromptEnvelope `json:"envelope"`
}

// ResponsePayload is the agent's response to a delivered prompt.
type ResponsePayload struct {
	MessageID string            `json:"message_id"`
	Content   string            `json:"content"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// ErrorPayload is sent when the gateway encounters an error.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// --- Scheduler Types ---

// Job represents a scheduled job (static from config or dynamic from agents).
type Job struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	OwnerAgent      string       `json:"owner_agent"`
	Source          string       `json:"source"` // "static" or "dynamic"
	Schedule        JobSchedule  `json:"schedule"`
	Prompt          string       `json:"prompt"`
	RouteTo         string       `json:"route_to"`
	ResponseChannel string       `json:"response_channel,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
}

// JobSchedule defines when a job fires.
type JobSchedule struct {
	Type   string    `json:"type"` // "cron" or "once"
	Cron   string    `json:"cron,omitempty"`
	OnceAt time.Time `json:"once_at,omitempty"`
}

// ScheduleCreatePayload is sent by an agent to create a scheduled job.
type ScheduleCreatePayload struct {
	RequestID       string `json:"request_id"`
	Name            string `json:"name"`
	Cron            string `json:"cron,omitempty"`
	OnceAt          string `json:"once_at,omitempty"`
	Prompt          string `json:"prompt"`
	ResponseChannel string `json:"response_channel,omitempty"`
}

// ScheduleListPayload requests a list of this agent's jobs.
type ScheduleListPayload struct {
	RequestID string `json:"request_id"`
}

// ScheduleUpdatePayload updates fields on an existing job.
type ScheduleUpdatePayload struct {
	RequestID string  `json:"request_id"`
	JobID     string  `json:"job_id"`
	Name      *string `json:"name,omitempty"`
	Cron      *string `json:"cron,omitempty"`
	OnceAt    *string `json:"once_at,omitempty"`
	Prompt    *string `json:"prompt,omitempty"`
}

// ScheduleDeletePayload deletes a scheduled job.
type ScheduleDeletePayload struct {
	RequestID string `json:"request_id"`
	JobID     string `json:"job_id"`
}

// ScheduleResultPayload is the gateway's response to a scheduler operation.
type ScheduleResultPayload struct {
	RequestID string `json:"request_id"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	Jobs      []Job  `json:"jobs,omitempty"`
}

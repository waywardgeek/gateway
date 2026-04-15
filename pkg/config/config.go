// Package config parses and provides the gateway configuration.
// The config file is the single source of truth for all routing.
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config is the top-level gateway configuration.
type Config struct {
	Gateway   GatewayConfig            `json:"gateway"`
	Agents    map[string]AgentConfig   `json:"agents"`
	Channels  map[string]ChannelConfig `json:"channels"`
	Scheduler SchedulerConfig          `json:"scheduler"`
	Twilio    *TwilioConfig            `json:"twilio,omitempty"`
}

// GatewayConfig holds server-level settings.
type GatewayConfig struct {
	Listen   string    `json:"listen"`
	DataDir  string    `json:"data_dir"`
	Hostname string    `json:"hostname"`
	TLS      TLSConfig `json:"tls"`
}

// TwilioConfig holds settings for the Twilio channel.
type TwilioConfig struct {
	AccountSIDEnv                string `json:"account_sid_env"`
	AuthTokenEnv                 string `json:"auth_token_env"`
	PhoneNumber                  string `json:"phone_number"`
	BaseURL                      string `json:"base_url"`
	DefaultVoice                 string `json:"default_voice,omitempty"`
	DefaultTTSProvider           string `json:"default_tts_provider,omitempty"`
	DefaultTranscriptionProvider string `json:"default_transcription_provider,omitempty"`
}

// TLSConfig holds TLS settings.
type TLSConfig struct {
	Mode string `json:"mode"` // "cloudflare", "acme", "none"
}

// AgentConfig describes a connectable AI agent.
type AgentConfig struct {
	DisplayName           string   `json:"display_name"`
	PublicKey             string   `json:"public_key"` // "ed25519:base64..."
	SafeMode              bool     `json:"safe_mode"`
	Local                 bool     `json:"local"` // runs on same machine
	Skills                []string `json:"skills"`
	ReconnectGraceSeconds int      `json:"reconnect_grace_seconds"`
}

// ChannelConfig describes an inbound channel (Discord, webhook, noise, etc).
type ChannelConfig struct {
	Type                   string        `json:"type"` // "discord", "noise", "webhook"
	RouteTo                string        `json:"route_to"`
	Trust                  string        `json:"trust"` // "owner", "trusted", "external"
	Policy                 PolicyConfig  `json:"policy"`
	ResponseMode           string        `json:"response_mode,omitempty"` // default: "async"
	ResponseTimeoutSeconds int           `json:"response_timeout_seconds,omitempty"`

	// Discord-specific
	TokenEnv       string   `json:"token_env,omitempty"`
	GuildID        string   `json:"guild_id,omitempty"`
	ListenChannels []string `json:"listen_channels,omitempty"`

	// Noise-specific (direct client connections)
	PublicKey string `json:"public_key,omitempty"`

	// Webhook-specific
	WebhookPath      string `json:"webhook_path,omitempty"`
	WebhookSecretEnv string `json:"webhook_secret_env,omitempty"`
}

// PolicyConfig defines per-channel security policy.
type PolicyConfig struct {
	MaxMessageLength int    `json:"max_message_length,omitempty"`
	RateLimit        string `json:"rate_limit,omitempty"` // e.g. "10/min"
	AllowedTools     string `json:"allowed_tools,omitempty"` // "full", "safe-only", "none"
}

// SchedulerConfig holds static scheduled jobs.
type SchedulerConfig struct {
	Jobs map[string]StaticJobConfig `json:"jobs"`
}

// StaticJobConfig is a scheduled job defined in the config file.
type StaticJobConfig struct {
	Schedule        string `json:"schedule"` // cron expression
	RouteTo         string `json:"route_to"`
	Prompt          string `json:"prompt"`
	ResponseChannel string `json:"response_channel,omitempty"`
}

// Load reads and parses a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}

// Validate checks the config for logical errors.
func (c *Config) Validate() error {
	if c.Gateway.Listen == "" {
		return fmt.Errorf("gateway.listen is required")
	}
	if c.Gateway.DataDir == "" {
		return fmt.Errorf("gateway.data_dir is required")
	}

	// Validate agents
	for id, agent := range c.Agents {
		if agent.PublicKey == "" {
			return fmt.Errorf("agent %q: public_key is required", id)
		}
	}

	// Validate channels reference existing agents
	for id, ch := range c.Channels {
		if ch.RouteTo == "" {
			return fmt.Errorf("channel %q: route_to is required", id)
		}
		if _, ok := c.Agents[ch.RouteTo]; !ok {
			return fmt.Errorf("channel %q: route_to agent %q not found in agents", id, ch.RouteTo)
		}
		if ch.Trust == "" {
			return fmt.Errorf("channel %q: trust is required", id)
		}
	}

	// Validate scheduler jobs reference existing agents
	for name, job := range c.Scheduler.Jobs {
		if job.RouteTo == "" {
			return fmt.Errorf("scheduler job %q: route_to is required", name)
		}
		if _, ok := c.Agents[job.RouteTo]; !ok {
			return fmt.Errorf("scheduler job %q: route_to agent %q not found in agents", name, job.RouteTo)
		}
	}

	// Validate Twilio config if present
	if c.Twilio != nil {
		if c.Twilio.AccountSIDEnv == "" {
			return fmt.Errorf("twilio.account_sid_env is required")
		}
		if c.Twilio.AuthTokenEnv == "" {
			return fmt.Errorf("twilio.auth_token_env is required")
		}
		if c.Twilio.PhoneNumber == "" {
			return fmt.Errorf("twilio.phone_number is required")
		}
		if c.Twilio.BaseURL == "" {
			return fmt.Errorf("twilio.base_url is required")
		}
	}

	return nil
}

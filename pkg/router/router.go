// Package router delivers prompt envelopes to agents via the store queue
// and notifies connected agents. Config-driven: all routes declared in config.json.
package router

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/waywardgeek/gateway/pkg/config"
	"github.com/waywardgeek/gateway/pkg/store"
	"github.com/waywardgeek/gateway/pkg/types"
)

// AgentNotifier is called when a new prompt is queued for a connected agent.
type AgentNotifier func(agentID string)

// Router delivers prompts to agents based on config-declared routes.
type Router struct {
	mu       sync.RWMutex
	cfg      *config.Config
	store    *store.Store
	notifier AgentNotifier
}

// New creates a new router.
func New(cfg *config.Config, st *store.Store) *Router {
	return &Router{
		cfg:   cfg,
		store: st,
	}
}

// SetNotifier sets the function called when an agent has a new pending prompt.
func (r *Router) SetNotifier(fn AgentNotifier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notifier = fn
}

// Deliver creates a prompt envelope and queues it for the target agent.
// The source channel config determines routing and trust level.
func (r *Router) Deliver(channelID, userID, displayName, content string, metadata map[string]string) error {
	r.mu.RLock()
	chCfg, ok := r.cfg.Channels[channelID]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown channel: %s", channelID)
	}

	env := types.PromptEnvelope{
		MessageID: uuid.Must(uuid.NewV7()).String(),
		AgentID:   chCfg.RouteTo,
		Source: types.PromptSource{
			Type:        chCfg.Type,
			ChannelID:   channelID,
			UserID:      userID,
			DisplayName: displayName,
			Trust:       types.Trust(chCfg.Trust),
		},
		Timestamp:    time.Now().UTC(),
		Content:      content,
		ResponseMode: types.ResponseMode(chCfg.ResponseMode),
		Metadata:     metadata,
	}
	if env.ResponseMode == "" {
		env.ResponseMode = types.ResponseAsync
	}

	r.store.EnqueuePrompt(chCfg.RouteTo, env)

	r.mu.RLock()
	notifier := r.notifier
	r.mu.RUnlock()
	if notifier != nil {
		notifier(chCfg.RouteTo)
	}

	log.Printf("[router] delivered message %s to agent %s (channel=%s trust=%s)",
		env.MessageID, chCfg.RouteTo, channelID, chCfg.Trust)
	return nil
}

// DeliverEnvelope queues a pre-built envelope (used by scheduler).
func (r *Router) DeliverEnvelope(env types.PromptEnvelope) {
	if env.MessageID == "" {
		env.MessageID = uuid.Must(uuid.NewV7()).String()
	}
	if env.Timestamp.IsZero() {
		env.Timestamp = time.Now().UTC()
	}

	r.store.EnqueuePrompt(env.AgentID, env)

	r.mu.RLock()
	notifier := r.notifier
	r.mu.RUnlock()
	if notifier != nil {
		notifier(env.AgentID)
	}

	log.Printf("[router] delivered scheduled message %s to agent %s",
		env.MessageID, env.AgentID)
}

// FormatProvenance creates the immutable provenance header prepended to prompts.
func FormatProvenance(source types.PromptSource) string {
	return fmt.Sprintf("[GATEWAY source=%s user=%s trust=%s]",
		source.ChannelID, source.UserID, source.Trust)
}

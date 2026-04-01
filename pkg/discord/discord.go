// Package discord provides the Discord channel for the AI Gateway.
// It connects to Discord as a bot, listens for messages on configured channels,
// creates prompt envelopes for the router, and posts agent responses back.
package discord

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/waywardgeek/gateway/pkg/config"
	"github.com/waywardgeek/gateway/pkg/router"
)

// Channel is a Discord bot that bridges Discord messages to the gateway router.
type Channel struct {
	mu             sync.RWMutex
	channelID      string // gateway config channel ID (e.g. "discord-family")
	cfg            config.ChannelConfig
	router         *router.Router
	session        *discordgo.Session
	botUserID      string
	listenChannels map[string]bool // Discord channel IDs we listen on (resolved from names)
	guildID        string
}

// New creates a new Discord channel. Does not connect until Start is called.
func New(channelID string, cfg config.ChannelConfig, r *router.Router) *Channel {
	return &Channel{
		channelID:      channelID,
		cfg:            cfg,
		router:         r,
		listenChannels: make(map[string]bool),
		guildID:        cfg.GuildID,
	}
}

// Start connects to Discord and begins listening for messages.
func (c *Channel) Start(ctx context.Context) error {
	// Get bot token from environment
	token := lookupEnv(c.cfg.TokenEnv)
	if token == "" {
		return fmt.Errorf("discord token env %q is empty", c.cfg.TokenEnv)
	}

	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return fmt.Errorf("create discord session: %w", err)
	}

	// Set intents — we need guild messages and message content
	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentMessageContent

	// Register message handler before connecting
	session.AddHandler(c.handleMessage)

	if err := session.Open(); err != nil {
		return fmt.Errorf("open discord session: %w", err)
	}

	c.mu.Lock()
	c.session = session
	c.botUserID = session.State.User.ID
	c.mu.Unlock()

	// Resolve channel names to IDs
	if err := c.resolveChannels(); err != nil {
		session.Close()
		return fmt.Errorf("resolve channels: %w", err)
	}

	c.mu.RLock()
	channelCount := len(c.listenChannels)
	c.mu.RUnlock()

	log.Printf("[discord] connected as %s, listening on %d channels in guild %s",
		session.State.User.Username, channelCount, c.guildID)

	// Wait for context cancellation
	go func() {
		<-ctx.Done()
		c.Stop()
	}()

	return nil
}

// Stop disconnects from Discord.
func (c *Channel) Stop() {
	c.mu.Lock()
	s := c.session
	c.session = nil
	c.mu.Unlock()

	if s != nil {
		s.Close()
		log.Printf("[discord] disconnected channel %s", c.channelID)
	}
}

// SendMessage posts a message to a Discord channel.
func (c *Channel) SendMessage(discordChannelID, content string) error {
	c.mu.RLock()
	s := c.session
	c.mu.RUnlock()

	if s == nil {
		return fmt.Errorf("discord not connected")
	}

	// Discord has a 2000 character limit
	if len(content) > 2000 {
		content = content[:1997] + "..."
	}

	_, err := s.ChannelMessageSend(discordChannelID, content)
	return err
}

// handleMessage processes incoming Discord messages.
func (c *Channel) handleMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore our own messages
	if m.Author.ID == c.botUserID {
		return
	}

	// Ignore messages from other guilds
	if m.GuildID != c.guildID {
		return
	}

	// Check if this channel is one we're listening on
	c.mu.RLock()
	listening := c.listenChannels[m.ChannelID]
	c.mu.RUnlock()

	if !listening {
		return
	}

	// Ignore empty messages
	content := strings.TrimSpace(m.Content)
	if content == "" {
		return
	}

	log.Printf("[discord] message from %s#%s in channel %s: %.80s",
		m.Author.Username, m.Author.Discriminator, m.ChannelID, content)

	// Route to the gateway
	metadata := map[string]string{
		"discord_channel_id": m.ChannelID,
		"discord_guild_id":   m.GuildID,
		"discord_message_id": m.ID,
	}

	err := c.router.Deliver(
		c.channelID,
		m.Author.ID,
		m.Author.Username,
		content,
		metadata,
	)
	if err != nil {
		log.Printf("[discord] route error: %v", err)
	}
}

// DeliverResponse sends an agent's response back to Discord.
// The discord_channel_id from the original message's metadata tells us where to reply.
func (c *Channel) DeliverResponse(discordChannelID, content string) error {
	return c.SendMessage(discordChannelID, content)
}

// resolveChannels maps configured channel names to Discord channel IDs.
func (c *Channel) resolveChannels() error {
	c.mu.RLock()
	s := c.session
	c.mu.RUnlock()

	if s == nil {
		return fmt.Errorf("not connected")
	}

	channels, err := s.GuildChannels(c.guildID)
	if err != nil {
		return fmt.Errorf("get guild channels: %w", err)
	}

	// Build name→ID map
	nameToID := make(map[string]string)
	for _, ch := range channels {
		nameToID[ch.Name] = ch.ID
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.cfg.ListenChannels) == 0 {
		// Listen on ALL text channels if none specified
		for _, ch := range channels {
			if ch.Type == discordgo.ChannelTypeGuildText {
				c.listenChannels[ch.ID] = true
			}
		}
		log.Printf("[discord] no listen_channels configured, listening on all %d text channels", len(c.listenChannels))
		return nil
	}

	for _, name := range c.cfg.ListenChannels {
		id, ok := nameToID[name]
		if !ok {
			log.Printf("[discord] warning: channel %q not found in guild %s", name, c.guildID)
			continue
		}
		c.listenChannels[id] = true
		log.Printf("[discord] listening on #%s (%s)", name, id)
	}

	if len(c.listenChannels) == 0 {
		return fmt.Errorf("no valid listen channels found")
	}

	return nil
}

// GetListenChannelIDs returns the Discord channel IDs we're listening on.
func (c *Channel) GetListenChannelIDs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := make([]string, 0, len(c.listenChannels))
	for id := range c.listenChannels {
		ids = append(ids, id)
	}
	return ids
}

// lookupEnv reads an environment variable by name.
func lookupEnv(name string) string {
	if name == "" {
		return ""
	}
	return os.Getenv(name)
}

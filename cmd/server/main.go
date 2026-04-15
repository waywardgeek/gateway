package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	noiselib "github.com/flynn/noise"
	"github.com/waywardgeek/gateway/pkg/agent"
	"github.com/waywardgeek/gateway/pkg/config"
	gwDiscord "github.com/waywardgeek/gateway/pkg/discord"
	gwNoise "github.com/waywardgeek/gateway/pkg/noise"
	"github.com/waywardgeek/gateway/pkg/router"
	"github.com/waywardgeek/gateway/pkg/scheduler"
	"github.com/waywardgeek/gateway/pkg/store"
	"github.com/waywardgeek/gateway/pkg/twilio"
	"github.com/waywardgeek/gateway/pkg/types"
	"github.com/waywardgeek/gateway/pkg/webhook"

	"github.com/google/uuid"
)

func main() {
	configPath := flag.String("config", "config.json", "path to config file")
	genKeys := flag.Bool("gen-keys", false, "generate a new keypair and exit")
	flag.Parse()

	if *genKeys {
		generateKeys()
		return
	}

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	log.Printf("config loaded: %d agents, %d channels, %d scheduled jobs",
		len(cfg.Agents), len(cfg.Channels), len(cfg.Scheduler.Jobs))

	// Load or generate gateway keypair
	gatewayKey, err := loadOrGenerateKey(cfg.Gateway.DataDir)
	if err != nil {
		log.Fatalf("gateway key: %v", err)
	}
	log.Printf("gateway public key: x25519:%s",
		encodePublicKey(gatewayKey.Public))

	// Initialize store
	st, err := store.New(cfg.Gateway.DataDir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}

	// Initialize router
	r := router.New(cfg, st)

	// Initialize scheduler (before agent manager, which needs it)
	sched := scheduler.New(cfg, r, st)

	// Initialize agent manager
	mgr := agent.NewManager(cfg, st, r, sched, gatewayKey)

	// Start scheduler
	sched.Start()

	// Start Discord channels
	ctx, ctxCancel := context.WithCancel(context.Background())
	discordChannels := make(map[string]*gwDiscord.Channel)
	for id, chCfg := range cfg.Channels {
		if chCfg.Type == "discord" {
			dc := gwDiscord.New(id, chCfg, r)
			if err := dc.Start(ctx); err != nil {
				log.Printf("warning: discord channel %s failed to start: %v", id, err)
				continue
			}
			discordChannels[id] = dc
		}
	}

	// Start webhook channels
	webhookHandler := webhook.NewHandler(cfg, r)

	// Pending chat responses (for /api/chat sync endpoint)
	chatPending := make(map[string]chan string) // messageID → response channel
	var chatMu sync.Mutex

	// Start Twilio channel
	var twilioChannel *twilio.Channel
	if cfg.Twilio != nil {
		twilioChannel = twilio.New(*cfg.Twilio, r)
		if err := twilioChannel.Start(ctx); err != nil {
			log.Printf("warning: twilio channel failed to start: %v", err)
			twilioChannel = nil
		}
	}

	// Wire response routing: agent responses → source channel (Discord or webhook)
	mgr.SetResponseHandler(func(agentID, messageID, content string, metadata map[string]string) {
		// Check for pending /api/chat response first
		chatMu.Lock()
		if ch, ok := chatPending[messageID]; ok {
			delete(chatPending, messageID)
			chatMu.Unlock()
			ch <- content
			log.Printf("[response] sent chat API response for %s (%d bytes)", messageID, len(content))
			return
		}
		chatMu.Unlock()

		// Try webhook sync first (returns true if a waiter was found)
		env := st.LookupMessage(messageID)
		if env != nil {
			// Check if a webhook sync waiter exists for this channel
			if webhookHandler.DeliverResponse(env.Source.ChannelID, content) {
				log.Printf("[response] sent sync webhook response for %s (%d bytes)", messageID, len(content))
				return
			}
		}

		// Check for DM delivery — discord_dm_user in envelope metadata
		dmUserID := ""
		if env != nil {
			dmUserID = env.Metadata["discord_dm_user"]
		}
		if dmUserID != "" {
			// Find a Discord channel to send the DM through
			for chID, dc := range discordChannels {
				if err := dc.SendDM(dmUserID, content); err != nil {
					log.Printf("[response] discord DM error: %v", err)
				} else {
					log.Printf("[response] sent DM to user %s via %s (%d bytes)", dmUserID, chID, len(content))
				}
				return // only need one Discord session to send a DM
			}
			log.Printf("[response] no Discord channel available for DM to user %s", dmUserID)
			return
		}

		// Determine which gateway channel to use
		targetChannelID := ""
		discordChID := ""
		if env != nil {
			targetChannelID = env.Source.ChannelID
			discordChID = env.Metadata["discord_channel_id"]
			// For scheduler-originated messages, use response_channel
			if rc, ok := env.Metadata["response_channel"]; ok && rc != "" {
				targetChannelID = rc
			}
		}

		// Route to Discord
		if targetChannelID != "" {
			if dc, ok := discordChannels[targetChannelID]; ok {
				// Fall back to first channel if no specific Discord channel ID
				if discordChID == "" {
					discordChID = dc.FirstChannelID()
				}
				if discordChID != "" {
					if err := dc.DeliverResponse(discordChID, content); err != nil {
						log.Printf("[response] discord send error: %v", err)
					} else {
						log.Printf("[response] sent to discord channel %s (%d bytes)", discordChID, len(content))
					}
					return
				}
			}
		}

		// Route to Twilio
		if twilioChannel != nil {
			callSID := ""
			if env != nil {
				callSID = env.Metadata["twilio_call_sid"]
			}
			if callSID != "" {
				if twilioChannel.DeliverResponse(callSID, content) {
					log.Printf("[response] sent to twilio call %s (%d bytes)", callSID, len(content))
					return
				}
			}
		}

		log.Printf("[response] no route for response to message %s", messageID)
	})

	// Set up HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ws", mgr.HandleWebSocket)
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":"%s","agents_connected":%d}`,
			agent.Version, len(mgr.ConnectedAgents()))
	})

	// Chat API: synchronous request-response to any connected agent
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, hr *http.Request) {
		if hr.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Agent   string `json:"agent"`
			Message string `json:"message"`
			Sender  string `json:"sender"`
		}
		if err := json.NewDecoder(hr.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if req.Agent == "" || req.Message == "" {
			http.Error(w, `"agent" and "message" are required`, http.StatusBadRequest)
			return
		}
		if req.Sender == "" {
			req.Sender = "api"
		}

		// Build envelope and deliver directly (no config channel needed)
		messageID := uuid.Must(uuid.NewV7()).String()
		env := types.PromptEnvelope{
			MessageID: messageID,
			AgentID:   req.Agent,
			Source: types.PromptSource{
				Type:        "api",
				ChannelID:   "api-chat",
				UserID:      req.Sender,
				DisplayName: req.Sender,
				Trust:       types.TrustOwner,
			},
			Timestamp:    time.Now().UTC(),
			Content:      req.Message,
			ResponseMode: types.ResponseSync,
		}

		// Register response channel before delivering
		ch := make(chan string, 1)
		chatMu.Lock()
		chatPending[messageID] = ch
		chatMu.Unlock()

		r.DeliverEnvelope(env)

		// Wait for response with timeout
		select {
		case content := <-ch:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"response":   content,
				"message_id": messageID,
			})
		case <-time.After(120 * time.Second):
			chatMu.Lock()
			delete(chatPending, messageID)
			chatMu.Unlock()
			http.Error(w, "timeout waiting for agent response", http.StatusGatewayTimeout)
		}
	})

	// Register webhook endpoints
	webhookHandler.RegisterRoutes(mux)

	// Register Twilio endpoints
	if twilioChannel != nil {
		twilioChannel.RegisterRoutes(mux)
		
		// Add /api/calls endpoint for Puffin to trigger calls
		mux.HandleFunc("/api/calls", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}

			var req struct {
				AgentName    string `json:"agent"`
				TargetPhone  string `json:"phone"`
				TargetName   string `json:"name"`
				Announcement string `json:"announcement"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}

			callSID, err := twilioChannel.MakeCall(req.AgentName, req.TargetPhone, req.TargetName, req.Announcement)
			if err != nil {
				log.Printf("[api] make call error: %v", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"call_sid": callSID})
		})
	}

	srv := &http.Server{
		Addr:    cfg.Gateway.Listen,
		Handler: mux,
	}

	// Auto-save ticker
	saveTicker := time.NewTicker(5 * time.Minute)
	go func() {
		for range saveTicker.C {
			if err := st.SaveAll(); err != nil {
				log.Printf("auto-save error: %v", err)
			} else {
				log.Printf("auto-saved state")
			}
		}
	}()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("gateway listening on %s", cfg.Gateway.Listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	<-sigCh
	log.Println("shutting down...")

	saveTicker.Stop()
	sched.Stop()
	ctxCancel() // stops Discord channels

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)

	if err := st.SaveAll(); err != nil {
		log.Printf("final save error: %v", err)
	}
	log.Println("gateway stopped")
}

// generateKeys prints a new keypair to stdout.
func generateKeys() {
	key, err := gwNoise.GenerateKeypair()
	if err != nil {
		log.Fatalf("generate keypair: %v", err)
	}
	fmt.Printf("Public key:  x25519:%s\n", encodePublicKey(key.Public))
	fmt.Printf("Private key: x25519:%s\n", encodePublicKey(key.Private))
	fmt.Println("\nAdd the public key to config.json under the appropriate agent/channel.")
	fmt.Println("Store the private key securely — it is used for the Noise handshake.")
}

// loadOrGenerateKey loads the gateway's keypair from disk, or generates one.
func loadOrGenerateKey(dataDir string) (noiselib.DHKey, error) {
	keyPath := dataDir + "/gateway-key.json"
	data, err := os.ReadFile(keyPath)
	if err == nil {
		// Parse existing key
		var stored struct {
			Public  string `json:"public"`
			Private string `json:"private"`
		}
		if err := json.Unmarshal(data, &stored); err == nil {
			pub, err1 := base64Decode(stored.Public)
			priv, err2 := base64Decode(stored.Private)
			if err1 == nil && err2 == nil {
				log.Printf("loaded gateway key from %s", keyPath)
				return noiselib.DHKey{Public: pub, Private: priv}, nil
			}
		}
	}

	// Generate new keypair
	key, err := gwNoise.GenerateKeypair()
	if err != nil {
		return noiselib.DHKey{}, err
	}

	// Save to disk
	keyJSON := fmt.Sprintf(`{"public":"%s","private":"%s"}`,
		encodePublicKey(key.Public), encodePublicKey(key.Private))
	if err := os.WriteFile(keyPath, []byte(keyJSON), 0600); err != nil {
		return noiselib.DHKey{}, fmt.Errorf("save gateway key: %w", err)
	}

	log.Printf("generated new gateway key, saved to %s", keyPath)
	return key, nil
}

func encodePublicKey(key []byte) string {
	return base64.StdEncoding.EncodeToString(key)
}

func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

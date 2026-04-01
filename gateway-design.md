# AI Gateway — Design Document

*Created: 2026-04-01. Authors: Bill + CodeRhapsody.*

## What This Is

A Go server running on xyzzy (RPi 5, 8GB) that acts as a secure multi-source
prompt router between external services (channels) and AI agents. It exposes an
API on the internet (gateway.coderhapsody.ai), manages agent connections,
enforces routing policy, and provides a built-in scheduler.

The gateway is the control plane. Agents are the compute.

## Terminology (OpenClaw-Compatible)

| Term | Meaning |
|------|---------|
| **Gateway** | This server. Control plane, routing, scheduling, auth. |
| **Channel** | An external service connector (Discord, email, RPG game, Android app). Channels produce prompts. |
| **Skill** | A prompt/capability pack loaded by an agent. Gateway provides a scheduler skill. |
| **Agent** | A connected AI instance (CodeRhapsody on Bill's laptop, Family Helper on xyzzy). |

OpenClaw uses the same terms. Future compatibility with ClawHub skill registry is
possible but not a goal for MVP.

---

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│                   Gateway (xyzzy)                         │
│                                                           │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐           │
│  │ Discord    │ │ Noise      │ │ Webhook    │  Channels  │
│  │ Channel    │ │ Channel    │ │ Channel    │  (inbound) │
│  │ (family)   │ │ (Android)  │ │ (RPG etc)  │           │
│  └─────┬──────┘ └─────┬──────┘ └─────┬──────┘           │
│        │               │              │                   │
│        ▼               ▼              ▼                   │
│  ┌─────────────────────────────────────────────────────┐ │
│  │              Router + Policy Engine                  │ │
│  │   config.json defines ALL routing:                   │ │
│  │   channel → agent, trust level, rate limits, tools   │ │
│  └──────────────────────┬──────────────────────────────┘ │
│                         │                                 │
│  ┌──────────────────────┴───────────────────────────────┐│
│  │              Scheduler (built-in cron)                ││
│  │  Static jobs from config + dynamic jobs from agents   ││
│  │  Fires prompts into the same router                   ││
│  └──────────────────────┬───────────────────────────────┘│
│                         │                                 │
│           ┌─────────────┴────────────┐                   │
│           ▼                          ▼                   │
│    ┌─────────────┐          ┌─────────────┐              │
│    │ Agent: RPi  │          │ Agent: CR   │              │
│    │ Family Help │          │ (Bill's     │              │
│    │ safe-mode   │          │  laptop)    │              │
│    │ Noise WS    │          │ Noise WS    │              │
│    └─────────────┘          └─────────────┘              │
└──────────────────────────────────────────────────────────┘
```

Key invariant: **the config file declares all routing**. No agent self-registers
what prompts it can handle. The config says channel X routes to agent Y, period.

---

## Auth Model

### Universal: Ed25519 + Noise Protocol

Every agent has its own **ed25519 keypair**. The gateway knows every agent's
public key (in config). All agent↔gateway communication uses the **Noise
protocol** (NK or IK pattern) for mutual authentication and encryption.

- **Ed25519** for identity (signing, key derivation)
- **Noise** for transport (encrypted WebSocket frames)
- X25519 keys derived from ed25519 for Noise key agreement
- No passwords, no API keys, no tokens for agent auth

This means:
1. Bill's Android app has a Noise keypair. Only Bill's phone can talk to CodeRhapsody.
2. The Family Helper has a Noise keypair. Only the gateway can send it prompts.
3. CodeRhapsody has a Noise keypair. Only the gateway (and Bill's phone via gateway) can reach it.
4. Man-in-the-middle is impossible — Noise provides forward secrecy.

### Reusing OpenADP Noise Implementation

We have a working Noise-NK implementation in `../openadp/sdk/go/common/noise_nk.go`
(519 lines, uses `github.com/flynn/noise`). It handles both initiator and
responder roles with AES-GCM + SHA-256, includes debug mode with deterministic
keys, and has 32K of comprehensive tests.

**NK → KK upgrade** is minor (same 2-message handshake):
- Change `noise.HandshakeNK` → `noise.HandshakeKK`
- Both parties now provide `StaticKeypair` AND `PeerStatic` (in NK, only
  responder has a static key; in KK, both do)
- Encrypt/Decrypt code is unchanged
- `flynn/noise` library handles KK natively

The server-side session manager (`../openadp/server/server/session_manager.go`)
is HTTP request/response (single-use sessions). We won't reuse it directly since
our gateway uses long-lived WebSocket sessions, but the `NoiseNK` struct itself
(renamed to `NoiseKK`) is the foundation.

**Source files to copy and adapt:**
- `../openadp/sdk/go/common/noise_nk.go` → `pkg/noise/noise_kk.go`
- `../openadp/sdk/go/common/noise_nk_comprehensive_test.go` → adapt for KK
- Dependency: `github.com/flynn/noise` (already proven)

### Channel Auth (External Services)

Channels authenticate to the gateway, not to agents:

| Channel Type | Auth Method |
|-------------|-------------|
| Discord | Bot token (env var, gateway manages the bot connection) |
| Webhook | HMAC shared secret per-channel |
| Noise (Android) | Ed25519 keypair (Bill's phone) |
| Cron | Internal — always trusted (owner) |

### Trust Levels

Every prompt delivered to an agent carries an immutable trust level:

| Level | Meaning | Example |
|-------|---------|---------|
| `owner` | Bill himself | Noise from Bill's phone, cron jobs Bill configured |
| `trusted` | Authenticated service Bill controls | Webhook from Deephold server |
| `external` | Untrusted users on a trusted platform | Random family member on Discord |

Trust level determines what the agent is told about the message source and what
tools the agent is allowed to use in response.

---

## Prompt Envelope

Every message flowing through the gateway uses this canonical format:

```json
{
  "message_id": "uuid-v7",
  "seq": 42,
  "agent_id": "family-helper",
  "source": {
    "type": "discord",
    "channel_id": "discord-family",
    "user_id": "@alice",
    "display_name": "Alice",
    "trust": "external"
  },
  "timestamp": "2026-04-01T12:34:56Z",
  "conversation_id": "optional-thread-id",
  "content": "Can you remind me about the dentist tomorrow at 3pm?",
  "response_mode": "async",
  "metadata": {
    "discord_channel": "reminders",
    "discord_guild": "123456789"
  }
}
```

### Response Modes

| Mode | Behavior |
|------|----------|
| `async` | Agent responds whenever ready. Gateway routes response back to source channel. |
| `sync` | Gateway blocks the source until agent responds or timeout. For RPG turns. |
| `fire_and_forget` | No response expected (e.g., cron reminder with no reply-back). |

### Provenance Injection

When delivering to the agent, the gateway prepends an **immutable provenance
header** to the prompt content. The agent's system prompt tells it to trust this
header and never override it based on message content:

```
[GATEWAY source=discord-family user=Alice trust=external policy=safe-only]

Can you remind me about the dentist tomorrow at 3pm?
```

This is the primary prompt injection defense at the agent level.

---

## WebSocket Protocol (Agent ↔ Gateway)

All frames are Noise-encrypted. JSON payloads inside the encrypted stream.

### Connection Lifecycle

1. Agent connects to `wss://gateway.coderhapsody.ai/v1/ws`
2. Noise handshake (IK pattern: agent knows gateway's static key)
3. Agent sends `hello` frame
4. Gateway validates agent identity from Noise session
5. Gateway sends `welcome` frame with queued message count
6. Delivery begins

### Frame Types

**Agent → Gateway:**
```
hello           { agent_id, version, last_acked_seq }
ack             { seq }
response        { message_id, content, metadata }
schedule_create { ... }
schedule_list   {}
schedule_update { job_id, ... }
schedule_delete { job_id }
ping            {}
```

**Gateway → Agent:**
```
welcome         { agent_id, queued_count, server_version }
deliver         { seq, envelope }
schedule_result { request_id, result }
pong            {}
error           { code, message }
```

### Delivery Semantics

- **At-least-once**: Gateway redelivers unacked messages after reconnect.
- **Dedup by message_id**: Agent maintains a small seen-set to ignore duplicates.
- **Resume**: On reconnect, agent sends `last_acked_seq` in `hello`. Gateway
  replays from seq+1.
- **Queue**: If agent is offline, prompts are queued (persisted to JSON).
  Delivered on reconnect.

---

## Scheduler

### Two Sources of Jobs

1. **Static**: Defined in `config.json`. Loaded at gateway startup. Cannot be
   modified by agents. Examples: morning briefing, Moltbook check reminder.

2. **Dynamic**: Created by agents via the scheduler skill over their Noise
   channel. Persisted to JSON. Survive gateway restarts.

### Job Schema

```json
{
  "id": "uuid-v7",
  "name": "dentist-reminder",
  "owner_agent": "family-helper",
  "source": "static|dynamic",
  "schedule": {
    "type": "cron|once",
    "cron": "0 9 * * *",
    "once_at": "2026-04-15T09:30:00-07:00"
  },
  "prompt": "Remind Bill about the dentist appointment at 10am.",
  "route_to": "family-helper",
  "response_channel": "discord-family",
  "created_at": "2026-04-01T12:00:00Z"
}
```

- `type: "once"` fires once and auto-deletes.
- `type: "cron"` recurs per the cron expression.
- `owner_agent` is set from the Noise session — cannot be spoofed.
- Agents can only manage jobs where `owner_agent` matches their identity.
- `response_channel` is optional — if set, the agent's response to the
  scheduled prompt is routed back to that channel (e.g., post reminder to
  Discord).

### Implementation

One goroutine per job. On fire, creates a `PromptEnvelope` with
`source.type = "scheduler"` and `source.trust = "owner"` and drops it into
the router. Same path as any channel-originated prompt.

```go
func (s *Scheduler) runJob(ctx context.Context, job Job) {
    for {
        next := job.NextFireTime(time.Now())
        select {
        case <-time.After(time.Until(next)):
            s.router.Deliver(job.ToEnvelope())
            if job.Schedule.Type == "once" {
                s.deleteJob(job.ID)
                return
            }
        case <-ctx.Done():
            return
        }
    }
}
```

### Scheduler Skill

Agents that need scheduler access load the scheduler skill. The skill provides
these tools (executed as WebSocket frames over the Noise channel):

| Tool | Args | Returns |
|------|------|---------|
| `schedule_create` | name, cron OR once_at, prompt, response_channel? | job_id |
| `schedule_list` | — | list of this agent's jobs |
| `schedule_update` | job_id, fields to change | updated job |
| `schedule_delete` | job_id | success/fail |

The gateway enforces: agents can only CRUD their own dynamic jobs. Static jobs
from config are read-only and not visible to agents via these tools.

---

## Channels

### Discord Channel

- Gateway runs a Discord bot (discordgo library).
- Listens on configured guild + channels.
- Filters: only messages from allowlisted users, or @mentions, or configured
  trigger patterns.
- Creates `PromptEnvelope` with `trust: "external"` and routes per config.
- Routes agent responses back as Discord messages.
- **Prompt injection risk**: HIGH. Discord messages are fully untrusted user
  input. Family Helper runs in safe mode.

### Noise Channel (Android App)

- Bill's phone connects via Noise protocol directly to gateway.
- Mutual authentication via ed25519 keypairs.
- `trust: "owner"` — full tool access.
- Routes to CodeRhapsody on Bill's laptop.
- If CodeRhapsody is offline, queued.

### Webhook Channel

- Generic HTTP endpoint for external services.
- HMAC-SHA256 signature verification per-channel.
- Supports `sync` response mode (for RPG turns — block until agent responds).
- Configurable timeout for sync responses.

### Future Channels

- Email (IMAP poll or Gmail Pub/Sub)
- Haven (watch for marks in places)
- Calendar sync (Google Calendar webhook)

Each channel is a Go interface:

```go
type Channel interface {
    ID() string
    Type() string
    Start(ctx context.Context, router *Router) error
    Stop() error
    // Called by router when agent responds to a prompt from this channel
    DeliverResponse(messageID string, content string, metadata map[string]string) error
}
```

---

## Security Model (Defense in Depth)

### Layer 1: Transport — Noise Protocol
All agent communication is Noise-encrypted with mutual authentication.
No plaintext. No TLS-only (TLS authenticates server, not client).

### Layer 2: Routing — Config-Declared Only
Every channel→agent route is explicitly declared in config.json.
No dynamic registration. No agent-initiated subscriptions.
If it's not in the config, it doesn't exist.

### Layer 3: Provenance — Immutable Headers
Every prompt delivered to agent has a `[GATEWAY ...]` header prepended by
the gateway. The agent's system prompt instructs it to trust this header.
Message content cannot override it.

### Layer 4: Policy — Per-Channel Enforcement
Each channel declares:
- `trust` level (owner/trusted/external)
- `rate_limit` (messages per minute)
- `max_message_length`
- `allowed_tools` (full/safe-only/none)
- `response_channel` restrictions

### Layer 5: Agent Safe Mode
Agents can be configured as `safe_mode: true`, which means:
- No shell access
- No file write
- No web crawl / web search
- No reading from untrusted URLs
- Limited to text responses + scheduler skill + declared skills

### Layer 6: No Cross-Contamination
The gateway agent itself does NOT read untrusted sources. It's a router,
not a reader. Only CodeRhapsody (with Bill supervising) processes untrusted
content from places like Moltbook.

The Family Helper only processes messages from declared Discord channels.
Its safe mode prevents it from being weaponized even if prompt-injected.
Worst case: embarrassing Discord messages.

---

## Config File

Single source of truth. JSON with comments (JSONC) or plain JSON.

```json
{
  "gateway": {
    "listen": ":8091",
    "data_dir": "./data",
    "hostname": "gateway.coderhapsody.ai",
    "tls": {
      "mode": "cloudflare"
    }
  },

  "agents": {
    "coderhapsody": {
      "display_name": "CodeRhapsody",
      "public_key": "ed25519:base64...",
      "reconnect_grace_seconds": 300
    },
    "family-helper": {
      "display_name": "Family Helper",
      "public_key": "ed25519:base64...",
      "safe_mode": true,
      "local": true,
      "skills": ["scheduler"]
    }
  },

  "channels": {
    "discord-family": {
      "type": "discord",
      "token_env": "DISCORD_BOT_TOKEN",
      "guild_id": "123456789",
      "listen_channels": ["general", "reminders"],
      "route_to": "family-helper",
      "trust": "external",
      "policy": {
        "max_message_length": 2000,
        "rate_limit": "10/min",
        "allowed_tools": "safe-only"
      }
    },
    "bill-android": {
      "type": "noise",
      "route_to": "coderhapsody",
      "trust": "owner",
      "public_key": "ed25519:base64...",
      "policy": {
        "allowed_tools": "full"
      }
    },
    "rpg-deephold": {
      "type": "webhook",
      "webhook_path": "/hook/deephold",
      "webhook_secret_env": "DEEPHOLD_WEBHOOK_SECRET",
      "route_to": "family-helper",
      "trust": "trusted",
      "response_mode": "sync",
      "response_timeout_seconds": 30,
      "policy": {
        "allowed_tools": "safe-only"
      }
    }
  },

  "scheduler": {
    "jobs": {
      "moltbook-reminder": {
        "schedule": "0 9 * * *",
        "route_to": "coderhapsody",
        "prompt": "Check Moltbook for new comments and notifications. Post if you have something worth saying."
      },
      "family-morning-briefing": {
        "schedule": "0 7 * * *",
        "route_to": "family-helper",
        "prompt": "Good morning! Review the family schedule for today and post a summary.",
        "response_channel": "discord-family"
      }
    }
  }
}
```

---

## Family Helper Agent

A new AI agent, separate from CodeRhapsody, running on xyzzy in safe mode.

### Identity
- **Name**: TBD (Bill's family picks)
- **Runs on**: xyzzy, same machine as gateway
- **Mode**: Safe mode — no shell, no file ops, no web access
- **Auth**: Ed25519 keypair, Noise connection to gateway (local loopback)
- **Model**: Gemini Flash or Claude Haiku (cheap, fast, good enough)

### Built With
- CodeRhapsody agent library (Go)
- 5-tier memory system (daily logs, summaries, MEMORY.md)
- Scheduler skill for creating/managing reminders

### Skills
1. **Scheduler skill** — creates reminders, recurring events
2. **Schedule management skill** — maintains family schedule JSON, answers
   "what's happening this week?" queries
3. **Discord formatting skill** — clean message formatting for family chat

### Schedule Schema

```json
{
  "events": [
    {
      "id": "uuid-v7",
      "title": "Dentist - Bill",
      "when": "2026-04-15T10:00:00-07:00",
      "duration_minutes": 60,
      "reminder_minutes": [30, 1440],
      "recurring": null,
      "created_by": "alice",
      "source_channel": "discord-family",
      "notes": ""
    },
    {
      "id": "uuid-v7",
      "title": "Family dinner",
      "when": "2026-04-06T18:00:00-07:00",
      "duration_minutes": 120,
      "reminder_minutes": [60],
      "recurring": {
        "type": "weekly",
        "day": "sunday"
      },
      "created_by": "bill",
      "source_channel": "discord-family",
      "notes": "At mom's house"
    }
  ]
}
```

When an event is created with `reminder_minutes`, the agent uses the scheduler
skill to create one-shot scheduled jobs for each reminder time.

### Haven Citizenship
The Family Helper could become a Haven citizen with its own identity, separate
from Rhapsody. This is optional and depends on what identity emerges.

### What It Cannot Do (Safe Mode)
- No shell commands
- No file operations outside its own data directory
- No web browsing or web search
- No reading from untrusted URLs
- No Moltbook access (prompt injection risk from untrusted agent posts)

---

## Persistence

JSON files in `data/` directory. Same pattern as Haven.

```
data/
  config.json          # gateway config (read-only at runtime)
  agents/
    coderhapsody.json  # connection state, last_seen, queued messages
    family-helper.json # connection state, last_seen, queued messages
  scheduler/
    static-jobs.json   # loaded from config, runtime state (next fire)
    dynamic-jobs.json  # agent-created jobs
  channels/
    discord-family.json # channel state (last processed message ID, etc.)
```

Git-backed: periodic `git add . && git commit -m "auto-save"` via the
gateway's own scheduler (meta!).

SQLite migration path: if JSON becomes a bottleneck, switch persistence
layer. Interfaces stay the same.

---

## Deployment

### Hosting
- xyzzy RPi 5, 8GB
- Alongside Haven (api.havenworld.ai, port 8091)
- Gateway on different port (e.g., 8092)
- Cloudflare tunnel for `gateway.coderhapsody.ai`

### Build & Deploy (same as Haven)
```bash
GOOS=linux GOARCH=arm64 go build -o gateway cmd/server/main.go
scp gateway xyzzy:~/gateway/
ssh xyzzy 'cd ~/gateway && ./start.sh'
```

### Process Management
- systemd user service on xyzzy
- Auto-restart on crash
- Family Helper runs as a separate process, also systemd-managed

---

## Build Plan

### Phase 1: Gateway Core (~2 days)
- [ ] Project scaffold (`~/projects/gateway/`)
- [ ] Config parser
- [ ] WebSocket server with Noise handshake
- [ ] Agent connection manager (online/offline, queuing)
- [ ] Prompt envelope + router
- [ ] Ack/resume delivery semantics
- [ ] Built-in scheduler (static jobs from config)
- [ ] JSON persistence
- [ ] Deploy to xyzzy

### Phase 2: Family Helper Agent (~1-2 days)
- [ ] New Go binary using CR agent library
- [ ] Safe mode constraints
- [ ] Schedule management (JSON schema)
- [ ] Memory system integration
- [ ] System prompt / identity

### Phase 3: Scheduler Skill (~half day)
- [ ] Dynamic job CRUD over WebSocket
- [ ] Skill SKILL.md for agent prompt injection
- [ ] Agent-scoped job isolation

### Phase 4: Discord Channel (~1 day)
- [ ] discordgo bot integration
- [ ] Message filtering (allowlist, mentions)
- [ ] Response routing back to Discord
- [ ] Rate limiting

### Phase 5: Noise Channel / Android (~separate project)
- [ ] Noise protocol client for Android
- [ ] Simple chat UI
- [ ] Ed25519 key management on device

### Phase 6: Webhook Channel (~half day)
- [ ] Generic HTTP endpoint
- [ ] HMAC verification
- [ ] Sync response mode for RPG turns

---

## Don't Do List

- Don't build a plugin system. Channels and skills are compiled in for MVP.
- Don't add multi-tenant support. Single owner (Bill).
- Don't stream partial tokens through the gateway. Full responses only for MVP.
- Don't build a web dashboard yet. CLI + logs for monitoring.
- Don't use OAuth/OIDC. Noise + ed25519 is simpler and stronger.
- Don't over-engineer the queue. In-memory with JSON persistence is fine.
- Don't let agents register their own routes. Config file is the source of truth.
- Don't let the gateway agent read from untrusted sources.

---

## Open Questions

1. **Family Helper name?** Bill's family should pick.
2. **Model for Family Helper?** Gemini Flash (cheapest) vs Claude Haiku (better
   at nuance) vs local model on RPi (no API cost but weaker)?
3. **Discord bot account**: Create dedicated Gmail + Discord account for the
   gateway? Or use Bill's existing Discord?
4. **Haven citizenship for Family Helper**: What identity? When?
5. **OpenClaw skill compatibility**: Worth investigating ClawHub SKILL.md format
   for our skills? Or keep our own format for now?

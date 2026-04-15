# Gateway TODO

## Twilio Channel (post-v1)

1. **`/api/calls` has no auth** — anyone who can reach the gateway HTTP port can initiate Twilio calls. Add a shared secret header check or reuse the Noise-KK agent auth.

2. **`responseLoop` polls with 1s timer** — replace with a `done` channel for cleaner shutdown instead of polling session status.

3. **Token streaming to Twilio** — currently sends complete responses. Stream tokens for lower TTS latency.

4. **Welcome greeting in TwiML** — currently empty string, relying on announcement via WebSocket. Could set TwiML `welcomeGreeting` attribute instead for faster initial speech.

5. **Cloudflare routing for coderhapsody.ai** — `gateway.coderhapsody.ai` returns 301 from Cloudflare despite correct tunnel config. Using `gateway.havenworld.ai` as workaround. Investigate SSL/TLS settings or page rules on coderhapsody.ai domain.

## Done

- ✅ Twilio ConversationRelay integration (outbound calls, TwiML, WebSocket)
- ✅ Voice call context prefix for agent prompts (no emoji/markdown in TTS)
- ✅ Twilio API error body logging
- ✅ Session pruning (background goroutine)

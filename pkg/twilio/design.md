# Twilio Module — Design Document

## Executive Summary

The `twilio` module serves as a sophisticated voice-to-agent bridge, enabling AI agents to participate in real-time, bidirectional phone conversations. It leverages Twilio's **ConversationRelay** feature, which establishes a low-latency WebSocket connection between Twilio's telephony infrastructure and the gateway. This integration transforms a standard phone call into a streaming text interface, where Twilio handles the heavy lifting of speech-to-text (STT) and text-to-speech (TTS), while the gateway orchestrates the dialogue flow and agent interaction.

The module is designed to manage the entire lifecycle of a voice call, from initial outbound dialing via the Twilio REST API to the complex state management required for real-time audio streaming, handling user interruptions, and maintaining a persistent transcript of the conversation.

## File Inventory

- [session.go](session.go): Defines the `CallSession` and `TranscriptEntry` structures. It is responsible for tracking the state of individual calls, including the active WebSocket connection, the conversation history, and the communication channels used to stream agent responses.
- [twilio.go](twilio.go): Implements the core `Channel` controller. It manages the lifecycle of all active sessions, handles outbound call initiation through the Twilio REST API, and provides the mechanism for routing agent replies back to the appropriate voice session.
- [twiml.go](twiml.go): Contains the HTTP handlers for Twilio's webhook callbacks. It generates the TwiML (Twilio Markup Language) instructions that direct Twilio to establish the ConversationRelay WebSocket connection back to the gateway.
- [websocket.go](websocket.go): Manages the bidirectional WebSocket communication with Twilio. It implements the ConversationRelay protocol, translating incoming speech transcripts into gateway prompts and streaming agent text responses back to Twilio for vocalization.

## Architecture and Data Flow

The `twilio` module operates as a specialized external channel within the gateway ecosystem. Unlike simple text-based channels, it requires a multi-stage handshake to bridge the gap between synchronous REST APIs and asynchronous streaming WebSockets.

### The Outbound Call Lifecycle

The process begins when an external trigger or internal gateway logic invokes the `MakeCall` function. Because Twilio does not provide a unique `CallSID` until the REST request is processed, the module generates a temporary session ID to track the call's initial state. It then sends a POST request to Twilio's Calls API, providing a callback URL that includes this temporary identifier.

When the call connects, Twilio makes an HTTP request to the gateway's `/twilio/twiml` endpoint. The module uses the temporary ID to retrieve the pending session and responds with a TwiML block. This XML instructs Twilio to open a WebSocket connection to the gateway's `/twilio/ws` endpoint, passing the session ID as a parameter. Once the WebSocket is upgraded, the module associates the permanent `CallSID` with the session and begins the real-time interaction loop.

### Real-Time Interaction and Streaming

Once the WebSocket is established, the module enters a continuous event-processing state. When the caller speaks, Twilio's STT engine generates a transcript and sends it as a `prompt` message over the WebSocket. The module captures this text, wraps it in a `PromptEnvelope` with high trust, and delivers it to the central `[pkg/router](../router/design.md)`. To ensure the AI agent provides a high-quality voice experience, the module injects specific context instructions, such as requesting conversational language and the avoidance of markdown or emojis.

When the AI agent generates a response, the gateway routes it back to the `twilio` module's `DeliverResponse` method. This method places the text into a session-specific channel, which is monitored by a dedicated response loop. This loop then packages the text into the ConversationRelay protocol format and sends it back over the WebSocket, where Twilio's TTS engine speaks it to the caller.

## Interface Implementations

The `twilio.Channel` follows the architectural pattern of a gateway channel, though it does not currently implement a formal shared interface. It acts as a producer of prompts for the `[pkg/router](../router/design.md)` and a consumer of agent responses delivered via the `DeliverResponse` method. It integrates with the system's HTTP server by registering its own routes for TwiML and WebSocket handling.

## Public API

### The Channel Controller

The `Channel` struct is the primary entry point for the module, managing configuration and session state.

- `New(cfg config.TwilioConfig, r *router.Router) *Channel`: Initializes a new channel with the provided Twilio configuration and a reference to the system router.
- `Start(ctx context.Context) error`: Validates environment credentials and launches a background goroutine to periodically prune stale or ended sessions.
- `RegisterRoutes(mux *http.ServeMux)`: Attaches the `/twilio/twiml` and `/twilio/ws` handlers to the system's HTTP multiplexer.
- `MakeCall(agentName, targetPhone, targetName, announcement string) (string, error)`: Initiates an outbound call to a specific phone number, assigning a target agent and an optional initial greeting.
- `DeliverResponse(callSID, content string) bool`: Routes an agent's text response to an active call session for real-time TTS playback.

### Call Session Management

The `CallSession` tracks the granular state of a single conversation.

- `AddTranscript(speaker, text string)`: Appends a new message to the call's internal history, identifying whether it came from the agent or the caller.
- `GetTranscript() []TranscriptEntry`: Retrieves the full chronological history of the conversation for auditing or context purposes.

## Implementation Details

### Session Aliasing and Persistence
The module employs a dual-key mapping strategy to handle the transition from temporary session IDs to permanent Twilio `CallSIDs`. This ensures that the TwiML callback and the subsequent WebSocket upgrade can always be correctly mapped back to the original call request, even if they arrive out of order. While sessions are currently stored in-memory, the architecture is designed to allow for future persistence in a shared store.

### Voice Context Injection
To bridge the gap between text-based AI models and voice-based interaction, the module automatically prepends a context header to every user prompt. This header explicitly instructs the agent that it is engaged in a voice call and should avoid visual artifacts like markdown, emojis, or complex lists that would sound unnatural when spoken by a TTS engine.

### ConversationRelay Protocol Handling
The WebSocket implementation handles several critical Twilio-specific message types:
- **setup**: The initial handshake that provides call metadata and establishes the session.
- **prompt**: Triggered when Twilio's STT engine detects a completed utterance from the caller.
- **interrupt**: Received if the user begins speaking while the agent is still streaming a response, allowing the system to potentially truncate the current output.
- **dtmf**: Captured when the user presses keys on their phone, providing a secondary input channel.

### Concurrency and Thread Safety
The module uses a combination of `sync.RWMutex` for the global session map and `sync.Mutex` for individual `CallSession` objects. This ensures that concurrent HTTP requests, WebSocket events, and agent response deliveries can occur safely without data races. The use of Go channels for streaming agent responses provides a clean, thread-safe bridge between the gateway's response logic and the Twilio WebSocket loop.

## Dependencies

- `[pkg/config](../config/design.md)`: Provides the necessary credentials, phone numbers, and default TTS/STT provider settings.
- `[pkg/router](../router/design.md)`: Used to deliver transcribed speech prompts into the gateway's central routing engine.
- `[pkg/types](../types/design.md)`: Defines the `PromptEnvelope` and `Trust` levels used for all internal message passing.
- `github.com/gorilla/websocket`: Provides the underlying transport for the ConversationRelay connection.

## Technical Debt and Future Work

- **Advanced Interruption Handling**: While the module receives `interrupt` signals, it does not yet actively signal the AI agent to stop generating or clear the local TTS buffer, which can lead to "talking over" the user.
- **Inbound Call Support**: The current logic is optimized for outbound dialing. Supporting inbound calls would require a mapping system to route incoming numbers to specific agents based on configuration.
- **DTMF Integration**: Keypad inputs are currently logged but not forwarded to agents as structured data, limiting the ability to build IVR-like experiences.
- **Session Persistence**: Because sessions are in-memory, a gateway restart will terminate all active calls and lose their transcripts. Moving session state to the `[pkg/store](../store/design.md)` would improve reliability.
- **Media Streaming**: The module currently uses the text-based `ConversationRelay`. For use cases requiring custom STT/TTS or raw audio analysis, transitioning to Twilio's raw `<Stream>` API may be necessary.

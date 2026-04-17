# Server Module Design

## Executive Summary

The `server` module serves as the central nervous system and primary entry point for the CodeRhapsody Gateway. Its fundamental purpose is to orchestrate the lifecycle of the entire system, acting as the glue that binds together the various communication channels, the internal routing engine, the persistence layer, and the secure agent management system. It is responsible for bootstrapping the environment, loading and validating the global configuration, establishing the gateway's cryptographic identity, and managing the graceful transition between operational states. Beyond initialization, the server maintains the primary HTTP infrastructure, providing the WebSocket endpoints for AI agents and exposing a suite of administrative and integration APIs that allow external services to interact with the gateway in both synchronous and asynchronous patterns. By centralizing the complex logic of response routing and state synchronization, the server module ensures that the gateway operates as a cohesive, reliable, and secure control plane.

## File Inventory

*   [main.go](main.go): The comprehensive application entry point. It handles command-line flag parsing, configuration loading, component initialization, the global response routing loop, and the main event loop for graceful shutdown.

## Architecture and Data Flow

The architecture of the server is designed around a strictly ordered initialization sequence that ensures all dependencies are satisfied before the gateway begins accepting external traffic. This process begins with the loading of the `config.json` file, which defines the entire operational topology of the system. Once the configuration is validated, the server establishes its cryptographic identity by either loading an existing Ed25519 keypair from disk or generating a new one if none is found. This identity is critical for the Noise-KK handshake, as it allows connecting agents to verify they are communicating with the legitimate gateway.

With its identity established, the server initializes the `store` package to provide a persistent foundation for message queues and agent states. It then constructs the internal `router` and the `scheduler`, the latter of which is responsible for managing both static, configuration-defined jobs and dynamic, agent-created tasks. The `agent.Manager` is then initialized, consuming the gateway's keypair and the previously created components to handle the secure WebSocket connections from AI agents. Finally, the server activates the external service connectors—Discord, Webhooks, and Twilio—wiring them into the router so that inbound prompts can begin flowing through the system.

### The Global Response Loop

The most critical operational component of the server is the global response handler, which is registered with the `agent.Manager`. This handler acts as the central dispatch for all messages originating from AI agents. When an agent sends a response frame, the server inspects the message metadata to determine its provenance and intended destination. 

The routing logic follows a prioritized sequence:
1.  **Synchronous API Requests**: The server first checks an internal, thread-safe map of pending synchronous chat requests. If the message ID matches a waiter, the response is delivered directly to the waiting Go channel, unblocking the HTTP request.
2.  **Synchronous Webhooks**: If no API waiter is found, the server consults the `webhook` module to see if a synchronous webhook request is waiting for a response on a specific channel.
3.  **Discord Direct Messages**: If the metadata indicates the message was a response to a Discord DM, the server identifies an active Discord channel session and routes the response back to the user via a direct message.
4.  **Channel-Specific Routing**: For messages originating from standard channels (like Discord or Twilio), the server uses the metadata to route the response back to the specific channel or call session. This includes handling the `response_channel` override, which allows agents to redirect their output to a different channel than the one that provided the prompt.

This centralized routing logic ensures that regardless of how a prompt entered the system, the agent's response is reliably delivered back to the correct user or service, maintaining the illusion of a continuous, stateful conversation across disparate platforms.

## Interface Implementations

While the `server` module primarily acts as a consumer of interfaces, it implements the `Gateway` role in the system's security and communication model. It provides the concrete implementation of the response handler callback required by the `agent.Manager`, bridging the gap between the asynchronous agent network and the various synchronous and asynchronous external channels. It also implements the standard `http.Handler` interface through its use of the `http.ServeMux`, exposing the gateway's capabilities over the network.

## Public API

The server exposes a comprehensive set of HTTP endpoints that serve as the "front door" for both AI agents and external integrations.

### Agent Communication
*   **`GET /v1/ws`**: This is the primary WebSocket endpoint for AI agents. It requires a full Noise protocol handshake for mutual authentication and encryption. Every message exchanged over this connection is framed and encrypted, ensuring that agent communication remains private and tamper-proof.

### Integration APIs
*   **`POST /api/chat`**: A high-level, synchronous API that allows external tools to send a prompt to a specific agent and wait for the response. This endpoint abstracts away the complexity of the gateway's internal queuing and routing, providing a simple request-response pattern for traditional web applications.
*   **`POST /api/calls`**: Specifically for Twilio integrations, this endpoint allows the gateway to trigger outbound phone calls. It accepts parameters for the target agent, the phone number, and an optional initial announcement, facilitating agent-initiated voice interactions.

### System Observability
*   **`GET /v1/status`**: A lightweight health check endpoint that returns the current server version and the number of active agent connections. This is used by monitoring tools to ensure the gateway is operational and healthy.

### Dynamic Channel Endpoints
*   **`/webhook/*`**: These routes are dynamically managed by the `webhook` module, allowing for the registration of multiple independent webhook endpoints as defined in the system configuration.
*   **`/twilio/*`**: These endpoints handle the TwiML and status callbacks from Twilio, managing the complex state of real-time voice conversations.

## Implementation Details

### State Management and Concurrency

The server manages several critical pieces of internal state, most notably the `chatPending` map used for tracking synchronous API requests. This map is protected by a `sync.Mutex` to ensure thread safety during high-concurrency scenarios where multiple API requests and agent responses may be processed simultaneously. 

To ensure data durability, the server runs an "auto-save" goroutine. Every five minutes, this background process triggers a full state persistence to disk via the `store` package. This ensures that even in the event of an unexpected crash, the system can recover its message queues and dynamic scheduler jobs with minimal data loss.

### Graceful Shutdown Sequence

The server is designed to handle termination signals (`SIGINT` and `SIGTERM`) with a rigorous shutdown sequence that preserves system integrity. Upon receiving a signal, the server:
1.  Stops the auto-save ticker to prevent concurrent disk writes during shutdown.
2.  Shuts down the scheduler to stop any pending or recurring jobs.
3.  Cancels the global context, which signals all external channel connectors (such as Discord and Twilio) to close their connections and clean up resources.
4.  Initiates a graceful shutdown of the HTTP server with a five-second timeout, allowing active requests to complete.
5.  Performs a final, synchronous save of the entire system state to disk, ensuring that all pending work is captured before the process exits.

## Auth Model

The server is the ultimate arbiter of trust and identity within the gateway ecosystem. It implements a multi-layered authentication model:

1.  **Gateway Identity**: The server maintains its own Ed25519 keypair, which serves as its cryptographic signature. This identity is used as the responder in all Noise handshakes, allowing agents to verify the gateway's authenticity.
2.  **Agent Authentication**: The server enforces strict authentication for all agent connections. An agent's public key must be explicitly listed in the `config.json` file; any connection attempt from an unknown or unconfigured key is immediately rejected during the Noise handshake.
3.  **Channel Authentication**: Each external channel implements its own platform-specific authentication, such as Discord bot tokens, HMAC signatures for webhooks, or Twilio request validation. The server ensures these credentials are correctly loaded and utilized by the respective channel managers.
4.  **Trust Assignment**: One of the server's most critical security functions is the assignment of an immutable `TrustLevel` to every `PromptEnvelope`. This level is determined by the configuration of the source channel and is carried with the message throughout its lifecycle, allowing agents to make informed decisions about which tools and capabilities are safe to execute for a given prompt.

## Dependencies

The server module is the most highly-coupled component in the project, as it must coordinate the activities of almost every other package:

*   [pkg/agent](../pkg/agent/design.md): Manages the lifecycle and security of agent WebSocket sessions.
*   [pkg/config](../pkg/config/design.md): Provides the authoritative configuration and routing topology.
*   [pkg/discord](../pkg/discord/design.md): Handles the integration with the Discord chat platform.
*   [pkg/noise](../pkg/noise/design.md): Implements the underlying cryptographic transport for agent communication.
*   [pkg/router](../pkg/router/design.md): Dispatches inbound prompts to the appropriate agent queues.
*   [pkg/scheduler](../pkg/scheduler/design.md): Manages time-based job execution and agent-initiated tasks.
*   [pkg/store](../pkg/store/design.md): Provides the persistent storage layer for queues and state.
*   [pkg/twilio](../pkg/twilio/design.md): Orchestrates real-time voice and SMS interactions.
*   [pkg/types](../pkg/types/design.md): Defines the canonical data structures used for system-wide communication.
*   [pkg/webhook](../pkg/webhook/design.md): Manages inbound HTTP webhooks and synchronous response waiting.

## Technical Debt and Future Work

While the server module is robust, several areas are identified for future improvement:
*   **Response Routing Refinement**: The response routing logic in `main.go` has grown complex as more channels have been added. Refactoring this into a dedicated `ResponseRouter` component would improve maintainability and testability.
*   **Dynamic Configuration**: Currently, the gateway requires a full restart to apply configuration changes. Implementing a mechanism for dynamic configuration reloading would significantly improve the system's availability and operational flexibility.
*   **Enhanced Telemetry**: The addition of structured logging, distributed tracing, and real-time metrics (e.g., via Prometheus) would provide deeper insights into the gateway's performance, latency, and error rates, facilitating more proactive system management.

# Agent Module Design

The `agent` module serves as the secure, stateful communication gateway for AI agents. Its primary responsibility is to manage long-lived WebSocket connections that are protected by mutual cryptographic authentication. It acts as the bridge between the gateway's internal message-passing architecture and the external compute power of the agents, ensuring that prompts are delivered reliably and responses are routed back to their originating channels. By abstracting the complexities of encryption, framing, and at-least-once delivery, the module provides a clean, high-level interface for the rest of the system to interact with AI agents.

## File Inventory

- [manager.go](manager.go): The core implementation of the `Manager` and `agentConn` types. This file contains the logic for WebSocket upgrading, the Noise-KK handshake, frame encryption/decryption, and the primary message delivery and read loops.
- [manager_test.go](manager_test.go): A comprehensive test suite that validates the entire connection lifecycle, including the cryptographic handshake, message queuing, delivery reliability, and the scheduler skill's isolation and persistence.

## Architecture and Data Flow

The `agent` module is designed as a stateful connection manager that maintains a thread-safe registry of active agent sessions. It operates on a push-based model for message delivery while simultaneously handling asynchronous requests and responses from the agents.

### Connection Lifecycle and Handshake

When an agent initiates a connection to the `/v1/ws` endpoint, the `Manager` upgrades the HTTP request to a WebSocket. Before any application-level data is exchanged, the connection must pass a rigorous Noise-KK handshake. This process is unique because it occurs over raw binary WebSocket messages rather than the standard JSON framing used for steady-state communication.

The handshake begins with the agent (the initiator) sending a binary message containing its agent ID and the first Noise handshake message. The `Manager` (the responder) extracts the agent ID, looks up the corresponding public key in the system configuration, and completes the handshake. This ensures mutual authentication: the gateway proves its identity to the agent, and the agent proves its identity to the gateway. Once the handshake is complete, a secure, encrypted session is established, and the connection is registered in the `Manager`'s internal map. If an agent with the same ID is already connected, the existing session is gracefully terminated to maintain a single authoritative connection per agent.

### The Steady-State Exchange

After the handshake, the agent and gateway enter a structured exchange:
1.  **Hello/Welcome**: The agent sends a `hello` frame containing its version and the sequence number of the last message it successfully acknowledged. The gateway responds with a `welcome` frame, providing its own version and the count of pending messages in the agent's queue.
2.  **Catch-up Delivery**: The gateway immediately retrieves all unacknowledged prompts from the [store](../store/design.md) and delivers them to the agent in sequence.
3.  **Dual-Loop Operation**: The connection then settles into two primary goroutines. A read loop continuously decrypts and processes incoming frames from the agent (such as responses, acks, or scheduling commands), while a ping loop ensures connection liveness by sending periodic heartbeats.

### Message Delivery and Provenance

When the [router](../router/design.md) receives a prompt for an agent, it persists the message and notifies the `Manager`. If the agent is connected, the `Manager` retrieves the prompt and prepends a standardized provenance header. This header (e.g., `[GATEWAY source=discord user=Alice trust=external]`) provides the agent with critical context about the message's origin and the trust level assigned by the gateway. The message is then wrapped in a `deliver` frame, encrypted, and sent over the WebSocket.

## Interface Implementations

The `agent` module is a central integration point and implements several key interfaces:

- **Agent Notifier**: The `Manager` implements the `notifyAgent` callback defined by the [router](../router/design.md). This allows the router to signal the manager whenever new work is available for a specific agent, bridging the gap between synchronous message queuing and asynchronous delivery.
- **Response Handler**: The `Manager` accepts a `ResponseHandler` callback, typically provided by the main server. This callback is invoked whenever an agent sends a `response` frame, allowing the system to route the agent's output back to the original communication channel (e.g., Discord, Twilio, or a Webhook).

## Public API

### The Manager

The `Manager` is the primary entry point for the module. It is initialized with the system configuration, the persistent store, the router, and the scheduler.

- `NewManager(...)`: Constructs the manager and registers it as the router's notifier.
- `HandleWebSocket(w, r)`: The HTTP handler that manages the entire lifecycle of an agent connection, from the initial upgrade to the final disconnection.
- `SetResponseHandler(fn)`: Configures the global callback for processing agent responses.
- `IsConnected(agentID)`: A thread-safe method to check if a specific agent is currently online.
- `ConnectedAgents()`: Returns a list of all currently active agent IDs.

### Frame Protocol

Communication uses a framed JSON protocol over Noise-encrypted binary WebSocket messages. The protocol defines several frame types:

- **Gateway to Agent**: `welcome` (session start), `deliver` (prompt delivery), `schedule_result` (status of a scheduling request), `pong` (heartbeat), and `error`.
- **Agent to Gateway**: `hello` (initialization), `ack` (message confirmation), `response` (prompt reply), `schedule_create/list/update/delete` (job management), and `ping` (heartbeat).

## Implementation Details

### Security: Noise-KK and Mutual Trust

The module's security model is built on the Noise Protocol Framework, specifically the KK pattern. This pattern requires both parties to have pre-shared knowledge of each other's static public keys. The gateway's key is loaded at startup, and agent public keys are defined in the system configuration. 

The handshake uses a specific binary format to bootstrap the encrypted session: `[1-byte agent-id-length][agent-id-bytes][noise-handshake-payload]`. This allows the gateway to identify the connecting agent before it can even decrypt the first message. Once the handshake is finalized, all subsequent communication is encrypted and authenticated using AES-GCM, providing confidentiality, integrity, and forward secrecy.

### Reliability: At-Least-Once Delivery

To ensure that no prompts are lost due to network interruptions or agent restarts, the module implements an at-least-once delivery guarantee. Every prompt is assigned a monotonically increasing sequence number per agent. The `Manager` tracks these sequence numbers and requires the agent to explicitly `ack` each one. These acknowledgments are persisted in the [store](../store/design.md). If a connection is lost, the gateway will automatically replay all unacknowledged messages upon the agent's next successful handshake, ensuring a seamless and reliable work queue.

### The Scheduler Skill

The `agent` module provides the gateway-side implementation of the "Scheduler Skill," allowing agents to manage their own autonomous tasks. When an agent sends a scheduling frame (e.g., `schedule_create`), the `Manager` validates the request and delegates it to the [scheduler](../scheduler/design.md) module. 

Crucially, the `Manager` enforces strict ownership isolation: agents can only create, list, update, or delete jobs that are associated with their own authenticated agent ID. This prevents one agent from interfering with the scheduled tasks of another. The results of these operations are communicated back to the agent via `schedule_result` frames, maintaining a clear request-response pattern over the asynchronous WebSocket.

## Dependencies

- [pkg/config](../config/design.md): Provides the authoritative source for agent identities and public keys.
- [pkg/noise](../noise/design.md): Supplies the underlying Noise Protocol implementation for secure transport.
- [pkg/router](../router/design.md): The source of all inbound prompts and the definer of the provenance header format.
- [pkg/scheduler](../scheduler/design.md): Manages the execution and persistence of both static and dynamic jobs.
- [pkg/store](../store/design.md): Persists message queues and agent acknowledgment states to survive restarts.
- [pkg/types](../types/design.md): Defines the shared data structures for the framing protocol and prompt envelopes.

## Technical Debt and Future Work

- **Streaming Responses**: The current architecture delivers agent responses as monolithic blocks. Implementing support for streaming partial tokens would significantly reduce perceived latency for long-form agent outputs.
- **Key Management**: There is currently no automated mechanism for key rotation. Updating keys requires manual configuration changes and a service restart.
- **Flow Control**: The manager currently delivers all pending messages immediately upon reconnection. A window-based flow control mechanism would prevent overwhelming agents that have a large backlog of work.
- **Identity Derivation**: The system currently uses X25519 keys directly for Noise. A more robust approach would be to derive these keys from Ed25519 identity keys, aligning with common cryptographic standards.

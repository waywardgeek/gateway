# Router Module — Design Document

## Executive Summary

The `router` module serves as the central dispatching hub and security gatekeeper of the AI Gateway. Its primary mission is to receive incoming prompts from diverse sources—including external communication channels like Discord or Twilio, the internal scheduler, or direct API calls—and transform them into a standardized, secure format for agent consumption. 

By wrapping every message in a canonical `PromptEnvelope` and assigning a configuration-defined trust level, the router ensures that AI agents receive consistent, authenticated, and traceable work items. The module is strictly configuration-driven, relying on the system's central topology to determine routing rules and security policies, thereby preventing unauthorized access or unpredictable message flow. It acts as the critical bridge between the inbound communication layers and the persistent message store where agents retrieve their tasks.

## File Inventory

- [router.go](router.go): The sole source file for the module, containing the `Router` struct, the delivery logic for both raw and pre-constructed messages, and the provenance formatting utility.

## Architecture and Data Flow

The router is positioned at the intersection of three major system components, facilitating a unidirectional flow of data from sources to agents:

1.  **Inbound Sources (Channels and Scheduler)**: External connectors and internal services call the router when they have a message to deliver. These sources provide raw content and metadata, which the router validates against the system configuration.
2.  **Configuration**: The router consults the `[pkg/config](../config/design.md)` module to look up routing rules, target agents, and trust levels for each channel. This ensures that the system's behavior is entirely predictable and declared upfront.
3.  **Persistence Layer**: The router persists every delivered message into the `[pkg/store](../store/design.md)`'s per-agent queues. This decoupling ensures that messages are not lost if an agent is temporarily disconnected.

When a message arrives at a channel, the following sequence occurs:
1.  The channel calls `router.Deliver()` with the raw message content and metadata.
2.  The router performs a lookup in the `config.Config` to find the target `AgentID` and the assigned `Trust` level for that specific channel.
3.  It generates a unique `MessageID` using UUID v7 and constructs a `PromptEnvelope`, incorporating the source information and the current UTC timestamp.
4.  The envelope is enqueued in the `store` for the target agent.
5.  The router triggers an `AgentNotifier` callback (if one is registered) to alert the agent manager that new work is available, enabling real-time delivery over active WebSocket connections.

For internal sources like the scheduler, the router provides `DeliverEnvelope()`. This method accepts a pre-constructed envelope, allowing the scheduler to bypass channel lookup while still benefiting from the centralized queuing, timestamping, and notification system.

## Interface Implementations

The `router` module does not implement any external interfaces. Instead, it defines the `AgentNotifier` function type:

- `type AgentNotifier func(agentID string)`

This callback is implemented by the `[pkg/agent](../agent/design.md)` manager. This design allows the router to remain decoupled from the specifics of WebSocket management while still providing the necessary signaling for low-latency message delivery.

## Public API

### The Router Struct

The `Router` struct is the primary entry point for the module. It is designed to be thread-safe and is typically initialized once during the server bootstrap process.

- `New(cfg *config.Config, st *store.Store) *Router`: This constructor initializes a new router with references to the system configuration and the message store.
- `SetNotifier(fn AgentNotifier)`: This method registers the callback function used to signal the arrival of new messages. It is protected by a mutex to allow safe updates during runtime.

### Delivery Methods

- `Deliver(channelID, userID, displayName, content string, metadata map[string]string) error`: This is the primary method used by external channels. It performs the configuration lookup, constructs the `PromptEnvelope`, and enqueues it. It returns an error if the `channelID` is not recognized in the configuration, acting as a first line of defense against misconfigured or unauthorized inputs.
- `DeliverEnvelope(env types.PromptEnvelope)`: This method is used for internal delivery, such as by the `[pkg/scheduler](../scheduler/design.md)`. It ensures the envelope has a valid `MessageID` and `Timestamp` before enqueuing it, providing a consistent path for internally generated prompts.

### Utilities

- `FormatProvenance(source types.PromptSource) string`: This static utility function generates a standardized `[GATEWAY ...]` header. This header is an immutable record of the message's origin and trust level. Agents are instructed to treat this header as the absolute truth, which is a core component of the system's defense against prompt injection.

## Implementation Details

### UUID v7 for Message IDs
The router utilizes UUID v7 for all `MessageID` generation. Unlike random UUIDs, UUID v7 is time-ordered. This provides two significant benefits: it allows the store and agents to sort messages chronologically without needing a separate index, and it significantly improves the performance of database and file-system operations by maintaining locality of reference.

### Provenance Injection and Security
The `FormatProvenance` function is a critical security feature. By standardizing the provenance header, the gateway ensures that agents receive consistent and unforgeable information about the "who" and "how" of a prompt. The system prompt for every agent instructs them to prioritize this header over any other content, forming a primary defense against malicious users attempting to escalate their trust level or spoof their identity.

### Concurrency and Thread Safety
The `Router` is built for high-concurrency environments. It uses a `sync.RWMutex` to protect its internal state, specifically the `notifier` callback and the reference to the configuration. This allows multiple channels to deliver messages simultaneously while the notifier is being updated or the system is undergoing a configuration refresh. The use of a read-write lock ensures that message delivery (the hot path) is not unnecessarily blocked by infrequent configuration updates.

## Dependencies

- `[pkg/config](../config/design.md)`: Provides the authoritative routing rules and trust assignments.
- `[pkg/store](../store/design.md)`: Handles the persistence and queuing of messages for agents.
- `[pkg/types](../types/design.md)`: Defines the canonical `PromptEnvelope` and `PromptSource` structures used throughout the system.

## Technical Debt and Future Work

- **Dynamic Routing**: The current implementation is strictly static, based on `config.json`. Future requirements may necessitate dynamic routing based on agent load, message content, or real-time availability.
- **Rate Limiting**: While the gateway architecture accounts for rate limiting, the current router implementation does not yet enforce per-channel or per-user limits. Integrating this logic into the `Deliver` method would provide a more robust defense against denial-of-service attacks.
- **Circuit Breaking**: If an agent's queue in the store grows beyond a certain threshold, the router could implement back-pressure or circuit-breaking logic to prevent system-wide memory exhaustion, although the persistent nature of the store currently mitigates this risk.

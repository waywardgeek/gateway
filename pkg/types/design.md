# Types Module Design

## Executive Summary

The `types` module serves as the foundational schema registry and the "lingua franca" of the CodeRhapsody Gateway. Its primary purpose is to define the canonical data structures that facilitate seamless communication between external communication channels, the central routing engine, and the connected AI agents. By centralizing these definitions, the module ensures strict type safety, consistent JSON serialization, and a unified mental model across the entire codebase.

The module's design is centered around the `PromptEnvelope`, a rich container that standardizes every message flowing through the system. It also defines the framed WebSocket protocol used for secure agent-gateway interactions and the data structures required for the internal scheduling engine. As a passive dependency, the `types` module contains no business logic or state, acting instead as the authoritative contract that decouples the system's various components while maintaining a rigorous security and delivery model.

## File Inventory

- [types.go](types.go): The single source of truth for all shared data structures, including prompt envelopes, WebSocket protocol frames, and scheduler job definitions.

## Architecture and Data Flow

The `types` module is the bedrock upon which all other modules are built. It defines the structures that represent the state of the system and the messages that drive its behavior.

The lifecycle of a message typically begins when an external channel (such as Discord or a Webhook) receives an input. The channel module translates this input into a `PromptEnvelope`, populating it with a unique `MessageID`, the target `AgentID`, and a `PromptSource` that captures the provenance of the message, including the user's identity and the assigned `Trust` level.

Once the `PromptEnvelope` is handed to the router, it is persisted and assigned a sequence number (`Seq`). This sequence number is critical for the "at-least-once" delivery guarantee. When the agent manager is ready to deliver the message, it wraps the envelope in a `DeliverPayload` and encapsulates it within a WebSocket `Frame`.

The agent receives the `Frame`, identifies it as a `FrameDeliver` type, and extracts the `PromptEnvelope`. After processing the prompt, the agent sends back a `ResponsePayload` inside a `FrameResponse`. The gateway uses the `MessageID` in the response to correlate it with the original request and route it back to the originating channel, guided by the `ResponseMode` (async, sync, or fire-and-forget) specified in the original envelope.

For scheduled tasks, the `Job` structure defines the execution parameters. When a job fires, the scheduler uses the job's configuration to synthesize a new `PromptEnvelope`, which then follows the standard routing and delivery path.

## Interface Implementations

The `types` module does not implement any external interfaces. Instead, it defines the concrete data contracts that other modules must implement or consume. For example, the `AgentNotifier` interface in the router module and the various channel delivery methods all rely on the `PromptEnvelope` and `ResponsePayload` structures defined here.

## Public API

The `types` module exports several categories of data structures that define the system's operational boundaries.

### Message Envelopes and Provenance

The `PromptEnvelope` is the most critical structure in the system. It contains the raw `Content` of the message alongside metadata such as the `Timestamp`, `ConversationID`, and a `Metadata` map for extensible context. The nested `PromptSource` structure provides the "who" and "where" of the message, including the `Type` of channel, the `ChannelID`, and the `Trust` level.

The `Trust` type is an enum (`owner`, `trusted`, `external`) that allows agents to perform capability-based security checks. The `ResponseMode` enum (`async`, `sync`, `fire_and_forget`) dictates how the gateway should handle the lifecycle of the request, particularly whether it should block an incoming HTTP request or simply queue the message for later delivery.

### WebSocket Protocol Frames

The gateway communicates with agents using a framed JSON protocol. The `Frame` struct is the top-level container, featuring a `Type` field and a `Payload` of type `json.RawMessage`. This design allows the receiver to inspect the frame type before unmarshaling the payload into a specific structure.

Frame types are divided into those sent by the agent (e.g., `hello`, `ack`, `response`, `schedule_create`) and those sent by the gateway (e.g., `welcome`, `deliver`, `schedule_result`, `error`). Each frame type has a corresponding payload struct, such as `HelloPayload` for agent identification and `AckPayload` for confirming message receipt.

### Scheduler and Job Definitions

The `Job` structure represents both static jobs defined in the system configuration and dynamic jobs created by agents at runtime. It includes the `Schedule` (which can be a cron expression or a one-shot timestamp), the `Prompt` to be executed, and the `RouteTo` agent.

Agents can manage their own jobs using a set of scheduler-specific frames. The `ScheduleCreatePayload`, `ScheduleUpdatePayload`, and `ScheduleDeletePayload` structures allow agents to perform CRUD operations on their tasks, while the `ScheduleResultPayload` provides the gateway's response to these requests.

## Implementation Details

### Narrative Logic and Serialization

The module relies heavily on Go's `encoding/json` package for serialization. Every exported field is decorated with `json` tags to ensure consistent naming across the network. The use of `json.RawMessage` in the `Frame` struct is a deliberate architectural choice to support a polymorphic messaging system while maintaining type safety during the second stage of unmarshaling.

The `Seq` field (sequence number) is an `int64` that increments for every message delivered to a specific agent. This field, combined with the `LastAckedSeq` in the `HelloPayload` and the `AckPayload`, forms the basis of the gateway's reliability model. It allows the system to resume delivery from the last known-good state after a connection interruption.

The `Trust` and `ResponseMode` types are implemented as string aliases with constant values. This provides the benefits of type safety within the Go code while ensuring that the serialized JSON remains human-readable and easy to debug.

### Concurrency and Thread Safety

As a collection of pure data structures, the `types` module does not manage its own concurrency. It is the responsibility of the consuming modules (like the router, store, and agent manager) to ensure that access to these structures is synchronized using mutexes or channels when they are shared across goroutines.

## Dependencies

The `types` module is designed to be a leaf node in the project's dependency graph. It depends only on the Go standard library:
- `encoding/json`: For all serialization and deserialization tasks.
- `time`: For timestamps and scheduling definitions.

This minimalist dependency profile ensures that the `types` module can be imported by any other package in the system without risk of circular dependencies.

## Technical Debt and Future Work

While the current implementation is robust and serves the gateway's needs, several areas for improvement have been identified:

- **Validation Logic**: The structures currently lack built-in validation. Adding a `Validate() error` method to key payloads would allow the gateway to catch malformed requests at the edge of the system.
- **Protocol Versioning**: As the system evolves, the WebSocket protocol may need to support versioning to allow for breaking changes without forcing all agents to upgrade simultaneously.
- **Binary Serialization**: For high-frequency or bandwidth-constrained environments, exploring a binary serialization format like Protobuf or MsgPack could offer performance benefits over JSON.
- **Rich Content Types**: The `Content` field is currently a simple string. Future iterations might benefit from a more structured content type that can handle multi-modal data (e.g., images, audio, or tool-call results) more natively.

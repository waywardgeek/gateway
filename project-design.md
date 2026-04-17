# CodeRhapsody Gateway Project Design

## Executive Summary

The CodeRhapsody Gateway is a high-performance, secure control plane and communication hub designed to orchestrate interactions between external platforms and intelligent AI agents. Its primary mission is to provide a unified, reliable, and secure bridge that allows AI agents to seamlessly interact with users across various channels—such as Discord, Twilio voice calls, and HTTP webhooks—while maintaining strict control over routing, trust, and state persistence. By centralizing the complexity of protocol translation, cryptographic authentication, and message queuing, the gateway frees the AI agents to focus entirely on computation and reasoning. The system is built with a strong emphasis on configuration-driven behavior, ensuring that all routing topologies and security policies are explicitly declared and validated at startup, preventing unauthorized access or unpredictable dynamic routing.

## System Architecture

The architecture of the gateway is fundamentally centered around a message-passing, event-driven model that acts as a secure intermediary between inbound communication channels and outbound agent connections. The philosophy of the system is heavily influenced by the concept of a centralized, immutable control plane. Rather than allowing agents to dictate their own routing or capabilities, the gateway enforces a strict, configuration-defined topology. Every message that enters the system is subjected to rigorous validation, assigned a definitive trust level, and wrapped in a standardized envelope before being queued for delivery.

At the core of the system lies the router, which serves as the central dispatching hub. It receives raw inputs from various external connectors—such as the Discord bot listener, the Twilio voice bridge, or the Webhook HTTP server—and translates them into a canonical format. This canonical message is then persisted to a lightweight, JSON-based storage layer, ensuring at-least-once delivery semantics even in the face of network interruptions or agent restarts. 

The connection to the AI agents is managed through long-lived, secure WebSockets. The gateway employs the Noise Protocol Framework (specifically the Noise-KK pattern) to establish mutually authenticated, encrypted tunnels with each agent. This cryptographic foundation ensures that the gateway can definitively verify the identity of every connected agent based on pre-shared public keys, eliminating the need for traditional, easily compromised API tokens. Once authenticated, the agent manager retrieves pending messages from the persistent store and delivers them over the secure channel.

Furthermore, the architecture incorporates an internal scheduling engine that allows for both static, configuration-defined jobs and dynamic, agent-created tasks. This scheduler integrates directly with the central router, treating time-based events with the same rigorous provenance and trust evaluation as external messages. This cohesive design ensures that whether a prompt originates from a user on Discord, a caller on a phone line, or an internal cron job, it flows through the exact same security and routing pipeline before reaching the AI agent.

## Interface & Contract Map

The gateway relies on several critical interfaces and data contracts that define the boundaries between its internal modules. These contracts ensure that components remain decoupled while enforcing strict data validation and security policies.

The most fundamental contract in the system is the configuration structure defined by the config module. This is not a traditional Go interface, but rather the authoritative data schema that dictates the behavior of the entire gateway. The router, scheduler, and all channel managers consume this configuration to determine their routing logic, trust assignments, and operational parameters. The configuration guarantees that the system's topology is fully resolved and valid before any network connections are established.

The central communication contract is the Prompt Envelope, defined within the types module. This structure encapsulates every message flowing through the system. It carries the raw content of the prompt alongside critical metadata, including a unique message identifier, a sequence number for delivery tracking, the target agent identifier, and the immutable trust level assigned by the gateway. Every inbound channel must construct a valid Prompt Envelope, and the router consumes these envelopes to manage delivery.

For real-time agent communication, the system relies on a framed WebSocket protocol, also defined in the types module. This protocol specifies the exact JSON structures for various frame types, such as delivery payloads, acknowledgments, and scheduling commands. The agent manager implements the server side of this protocol, while the connected AI agents consume it.

The router defines an Agent Notifier callback interface, which bridges the gap between the synchronous queuing of messages and the asynchronous delivery over WebSockets. The agent manager implements this callback, allowing the router to signal when new work is available for a specific agent, prompting the manager to wake up the corresponding connection and begin delivery.

Finally, the system utilizes an implicit Channel interface for its external connectors. While not strictly defined as a shared Go interface, modules like Discord, Twilio, and Webhook all follow a consistent pattern. They initialize with a specific configuration, start a background listening process, consume the router to deliver inbound messages, and provide a mechanism for the central server to route agent responses back to the originating platform.

## Module Map

### Categorized Tree View

*   **Core Infrastructure**
    *   [cmd/server](cmd/server/design.md)
    *   [pkg/config](pkg/config/design.md)
    *   [pkg/types](pkg/types/design.md)
    *   [pkg/store](pkg/store/design.md)
*   **Routing and Execution**
    *   [pkg/router](pkg/router/design.md)
    *   [pkg/scheduler](pkg/scheduler/design.md)
*   **Agent Communication**
    *   [pkg/agent](pkg/agent/design.md)
    *   [pkg/noise](pkg/noise/design.md)
*   **External Channels**
    *   [pkg/discord](pkg/discord/design.md)
    *   [pkg/twilio](pkg/twilio/design.md)
    *   [pkg/webhook](pkg/webhook/design.md)

### Detailed Module Summaries

#### cmd/server

The server module is the entry point and orchestrator of the entire gateway. It is responsible for bootstrapping the system, loading the configuration, initializing the persistence layer, and wiring together the router, scheduler, and agent manager. It manages the global response loop, routing agent replies back to their originating channels, and handles the graceful shutdown sequence. The server maintains the central HTTP multiplexer and coordinates the lifecycle of all external connectors.

Internally, the server manages a thread-safe map to track synchronous API requests, ensuring that responses are correctly routed back to waiting HTTP clients. It also runs an auto-save goroutine that periodically triggers a full state persistence to disk, guaranteeing that dynamic jobs and message queues survive server restarts.

The server implements the "Gateway" role in the system's security model, maintaining its own Ed25519 keypair for Noise-encrypted communication. It consumes almost every other package in the project, acting as the central hub that wires the system together.

#### pkg/config

The config module is the foundational source of truth for the gateway. It provides a structured, type-safe representation of the system's routing topology, security policies, and operational parameters. It is responsible for loading the JSON configuration file and performing deep validation to ensure that all routes are valid, all agents have cryptographic identities, and all channels have assigned trust levels before the system is allowed to start.

This module is a passive component that does not maintain complex internal state beyond the parsed configuration tree. It acts as the data contract for the entire system, ensuring that the gateway remains a predictable and secure control plane.

The config module does not implement external interfaces but defines the data structures that other modules use to implement their logic. It is consumed by the router, scheduler, and all channel managers to guide their behavior.

#### pkg/types

The types module serves as the central schema registry. It defines the canonical data structures used across the system, including the Prompt Envelope, the WebSocket protocol frames, and the scheduler job definitions. By centralizing these definitions, the module ensures type safety and consistent serialization without introducing circular dependencies between other packages.

Like the config module, the types module is a passive dependency that does not contain logic or maintain state. It defines the "language" spoken by the components, ensuring that data flows seamlessly between external channels, the central router, and the connected AI agents.

The core of the module is the Prompt Envelope, which encapsulates every message flowing through the system with rich metadata regarding its source, trust level, and expected response behavior. It is consumed by almost every other package in the system.

#### pkg/store

The store module provides a lightweight, JSON-based persistence layer. It manages the state of connected agents, their message delivery queues, and dynamic scheduler jobs. Operating as an in-memory database with a write-behind persistence model, it ensures that messages are safely queued for at-least-once delivery and that agent-created jobs survive system restarts. It utilizes a hierarchical directory structure for easy debugging and versioning.

Internally, the store maintains thread-safe maps for agent states, dynamic jobs, and a transient message index. The message index links message IDs to their original Prompt Envelopes, which is critical for routing asynchronous responses back to their correct source channels.

The store module does not implement external interfaces but serves as a concrete utility used by the gateway's core logic and the scheduler. It consumes the types module to understand the structures it is persisting.

#### pkg/router

The router module is the central dispatching hub. It receives incoming prompts from external channels or the internal scheduler, wraps them in a standardized Prompt Envelope, and assigns the configuration-defined trust level. It then persists the envelope into the store's per-agent queues and triggers notifications to the agent manager. The router is strictly config-driven and enforces the system's security boundaries by standardizing the provenance header of every message.

The router maintains minimal internal state, primarily a thread-safe reference to the system configuration and a registered notification callback. This ensures that it can safely handle concurrent delivery requests from multiple channels.

The router defines an Agent Notifier callback interface, which is implemented by the agent manager to bridge the gap between message queuing and real-time WebSocket delivery. It consumes the config module for routing rules and the store module for message persistence.

#### pkg/scheduler

The scheduler module provides a built-in, cron-like execution engine. It manages both static jobs defined in the configuration and dynamic jobs created by agents at runtime. Each job runs in its own goroutine, and when a job fires, the scheduler constructs a Prompt Envelope and delivers it to the router. This allows for automated workflows and periodic agent tasks, all routed with the same trust and provenance as external messages.

Internally, the scheduler maintains a thread-safe map of cancellation functions, allowing it to manage the lifecycle of job goroutines dynamically. This ensures that jobs can be stopped or updated immediately without affecting others.

The scheduler module does not implement external interfaces but is a standalone service used by the agent manager and the main server. It consumes the config module for static job definitions, the store module for dynamic job persistence, and the router module for prompt delivery.

#### pkg/agent

The agent module manages the long-lived, secure WebSocket connections to the AI agents. It handles the entire connection lifecycle, from the initial HTTP upgrade and cryptographic handshake to the steady-state message exchange loop. It retrieves pending messages from the store, encrypts them, and ensures at-least-once delivery by tracking agent acknowledgments. It also implements the gateway-side logic for the scheduler skill, allowing agents to manage their dynamic jobs.

The agent module operates as a stateful connection manager, maintaining a thread-safe map of active WebSocket sessions. It ensures that only one active session exists per agent, terminating older connections if necessary.

The agent manager implements the Agent Notifier callback interface defined by the router, allowing it to wake up connections when new messages are queued. It consumes the noise module for secure transport, the store module for retrieving queued messages, and the scheduler module for handling agent-initiated job requests.

#### pkg/noise

The noise module provides the secure, mutually authenticated transport layer using the Noise Protocol Framework. It implements the Noise-KK pattern, ensuring that both the gateway and the connecting agents authenticate each other using pre-shared Ed25519 public keys. This module handles the complex cryptographic state machine, providing encryption, integrity, and forward secrecy for all agent-to-gateway communications.

Internally, the module manages the handshake state and transitions to holding send and receive cipher states once the handshake is finalized. It is designed to be used within a single goroutine, avoiding unnecessary locking overhead for typical WebSocket read/write loops.

The noise module does not implement external interfaces but serves as a low-level cryptographic utility used by the agent module. It relies on external cryptographic libraries to implement the Noise Protocol Framework.

#### pkg/discord

The discord module provides the integration with the Discord chat platform. It runs a bot that listens for messages in configured guilds and channels, transforms them into Prompt Envelopes, and routes them to the appropriate agents. It also handles the complex logic of delivering agent responses back to specific channels or as direct messages, managing the underlying Discord session and rate limits.

The module maintains a thread-safe state containing the active Discord session, the bot's user ID, and a map of monitored channel IDs. This ensures that the bot can safely handle multiple concurrent messages and response delivery requests.

While not explicitly implementing a named interface, the discord module follows the architectural pattern of a gateway channel. It consumes the router module to deliver inbound messages and provides a mechanism for the central server to route agent responses back to Discord.

#### pkg/twilio

The twilio module acts as a voice-to-agent bridge, enabling real-time phone conversations. It leverages Twilio's ConversationRelay feature to establish a bidirectional WebSocket connection for low-latency speech-to-text and text-to-speech. It manages outbound call initiation, TwiML orchestration, and the complex state machine required to handle real-time audio interactions, interruptions, and transcripts.

Internally, the module maintains a thread-safe map of active call sessions, tracking their transcripts and the channels used for streaming agent responses. It handles session aliasing to bridge the gap between REST API calls and TwiML callbacks.

The twilio module follows the architectural pattern of a gateway channel, consuming the router module to deliver transcribed speech to agents. It provides a delivery method for the gateway to route agent text responses back to the active call for text-to-speech playback.

#### pkg/webhook

The webhook module provides an HTTP-based inbound channel for external services. It handles incoming POST requests, verifies HMAC signatures for security, and supports both asynchronous and synchronous response patterns. For synchronous requests, it manages a pending state map, blocking the HTTP response until the agent provides a reply, effectively turning the asynchronous agent network into a synchronous API endpoint.

The module maintains a thread-safe pending map to track synchronous requests, ensuring that agent responses are correctly routed back to the waiting HTTP clients. It handles timeouts to prevent hanging requests if an agent is slow or unresponsive.

The webhook module functions as a logical channel within the gateway ecosystem, consuming the router module to inject prompts. It provides a mechanism for the gateway's main loop to deliver responses back to waiting HTTP clients.

## Integration Patterns & Workflows

The gateway relies on several complex, cross-module workflows to achieve its goals. Understanding these patterns is critical for navigating the system's architecture.

### The Synchronous Webhook Workflow

This workflow demonstrates how the gateway bridges the gap between synchronous HTTP clients and the asynchronous, queue-based agent network. When an external service sends an HTTP POST request to a configured webhook endpoint, the webhook module first verifies the HMAC signature to ensure authenticity. Once validated, it parses the payload and calls the router to deliver the message. The router looks up the channel configuration, constructs a Prompt Envelope, assigns the appropriate trust level, and persists the message into the store's queue for the target agent. Crucially, the webhook module then registers a waiter in its internal pending map and blocks the HTTP request. 

Meanwhile, the router notifies the agent manager that a new message is available. The agent manager retrieves the envelope from the store, encrypts it using the established Noise session, and sends it over the WebSocket to the agent. The agent processes the prompt and eventually sends a response frame back to the gateway. The agent manager receives this frame and passes it to the global response handler defined in the server module. The server inspects the response metadata, identifies that it originated from a synchronous webhook, and calls the webhook module's delivery function. This unblocks the waiting HTTP request, returning the agent's response directly to the external service.

### The Voice Conversation Workflow

The Twilio integration represents the most complex real-time workflow in the system. It begins when the gateway initiates an outbound call via the Twilio REST API, providing a callback URL. When the call connects, Twilio hits the webhook endpoint in the twilio module, which responds with TwiML instructing Twilio to open a ConversationRelay WebSocket connection back to the gateway. Once this WebSocket is established, the module manages a continuous, bidirectional stream of events.

When the user speaks, Twilio's speech-to-text engine sends a prompt message over the WebSocket. The twilio module wraps this text in a Prompt Envelope, injecting specific voice context instructions (e.g., "respond conversationally, no emoji"), and delivers it to the router. The router queues the message, and the agent manager delivers it to the AI agent. When the agent responds, the global response handler routes the text back to the specific active call session in the twilio module. The module then sends this text as a message over the Twilio WebSocket, which Twilio's text-to-speech engine speaks to the user. This entire cycle must happen with minimal latency to maintain a natural conversational flow, requiring careful concurrency management within the twilio module to handle potential interruptions if the user speaks while the agent is still generating its response.

### The Dynamic Scheduling Workflow

This workflow illustrates how agents can interact with the gateway's internal systems to manage their own tasks. An agent decides it needs to perform a task in the future and sends a schedule creation frame over its secure WebSocket connection. The agent manager receives this frame, validates that the agent is authorized to create jobs, and passes the request to the scheduler module.

The scheduler module creates a new dynamic job, persisting it immediately to the store to ensure it survives any potential gateway restarts. It then calculates the next execution time based on the provided cron expression or one-shot timestamp and launches a dedicated goroutine to wait for that precise moment. When the time arrives, the goroutine fires, constructing a Prompt Envelope with a specific scheduler source type and an owner trust level. This envelope is delivered to the router, which queues it for the agent just like any external message. The agent receives the scheduled prompt, processes it, and the cycle continues, allowing agents to build complex, time-based autonomous behaviors.

## Dependency Overview

The dependency graph of the gateway is designed to be strictly hierarchical, preventing circular dependencies and ensuring clear boundaries of responsibility.

At the base of the hierarchy are the types and config modules. These packages contain no complex logic and depend only on the Go standard library. They define the data structures and configuration schemas that are universally consumed by the rest of the system.

The store and noise modules sit slightly above the base. The store depends on the types module to understand the structures it is persisting, while the noise module relies on external cryptographic libraries to implement the Noise Protocol Framework. Both are utilized as foundational utilities by higher-level components.

The router and scheduler form the core operational layer. The router depends heavily on the config module to make routing decisions and the store module to persist messages. The scheduler depends on the config module for static jobs, the store for dynamic jobs, and the router to deliver prompts when jobs fire.

The agent module and the various channel modules (discord, twilio, webhook) represent the communication layer. They all depend on the router to inject messages into the system. The agent module additionally depends on the noise module for secure transport, the store for retrieving queued messages, and the scheduler for handling agent-initiated job requests.

Finally, the server module sits at the very top of the hierarchy. It imports almost every other package in the project to bootstrap the system, wire the components together, and manage the global lifecycle and response routing logic. This architectural layering ensures that the core routing and persistence mechanisms remain entirely decoupled from the specific details of any external communication platform.

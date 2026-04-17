# Store Module — Design Document

## Executive Summary

The `store` module provides a lightweight, JSON-based persistence layer for the CodeRhapsody Gateway. It is designed to be simple, human-readable, and robust, following a "write-behind" persistence model where state is maintained in-memory for high-performance access and periodically or explicitly flushed to disk. By using standard JSON files in a hierarchical directory structure, the module ensures that the system's state—including message queues and dynamic jobs—is easy to inspect, debug, and manage using standard version control tools like Git.

The module solves the critical problem of state persistence in an asynchronous, event-driven system. It ensures that messages destined for AI agents are never lost due to network interruptions or server restarts, and it allows agents to create and manage long-running scheduled tasks that survive system reboots.

## File Inventory

- [store.go](store.go): The core implementation of the persistence layer. It manages the in-memory state, handles thread-safe access via mutexes, and performs the disk I/O operations for loading and saving JSON data.
- [store_test.go](store_test.go): A comprehensive test suite that validates the logic for message queuing, sequence number management, acknowledgment processing, and job CRUD operations.

## Architecture and Data Flow

The `store` module acts as the central repository for all transient and persistent state within the gateway. It is initialized with a root data directory and automatically discovers and loads existing state during startup.

Internally, the `Store` maintains three primary in-memory maps:
- **Agents**: A map of agent identifiers to their respective `AgentState`, which includes their message delivery queue and the last assigned sequence number.
- **Jobs**: A map of job identifiers to dynamic scheduler jobs created by agents.
- **Message Index**: A transient, in-memory index that maps unique message IDs to their original `PromptEnvelope`. This index is vital for the gateway's ability to route asynchronous agent responses back to their originating source channels.

Data flow typically follows these patterns:
1. **Inbound Message Flow**: When the router receives a message, it calls `EnqueuePrompt`. The store assigns a monotonically increasing sequence number, appends the message to the agent's queue, and adds it to the `messageIndex`.
2. **Outbound Delivery**: The agent manager calls `GetPendingPrompts` to retrieve messages that need to be sent to an agent.
3. **Acknowledgment**: Once an agent confirms receipt, `AckPrompt` is called, which prunes the agent's queue up to the acknowledged sequence number.
4. **Response Routing**: When an agent sends a response, the gateway uses `LookupMessage` to retrieve the original prompt's metadata, ensuring the response reaches the correct user or channel.
5. **Persistence Cycle**: State is flushed to disk through explicit calls to `SaveAgentState`, `SaveJobs`, or `SaveAll`, typically triggered by the server's lifecycle management or periodic auto-save routines.

## Interface Implementations

The `store` module does not implement any external interfaces defined in other packages. Instead, it provides a concrete, high-performance utility that is consumed by the `router`, `scheduler`, and `agent` modules. It serves as the foundational data layer upon which these higher-level components build their logic.

## Public API

The `Store` struct provides a thread-safe, comprehensive API for managing the gateway's state.

### Lifecycle and Persistence
The store is created using `New(dataDir string)`, which ensures the necessary directory structure exists and loads all existing agent and job data. Persistence is managed through `SaveAll()`, which flushes all in-memory state to disk, or more granular functions like `SaveAgentState(agentID string)` and `SaveJobs()`. These functions use formatted JSON to ensure the files remain human-readable.

### Agent and Queue Management
The module provides `GetAgentState(agentID string)` to retrieve or initialize an agent's persistent state. Message delivery is handled through `EnqueuePrompt(agentID string, env types.PromptEnvelope)`, which manages sequence numbering, and `AckPrompt(agentID string, seq int64)`, which handles queue pruning. For delivery and redelivery, `GetPendingPrompts(agentID string, afterSeq int64)` allows the caller to retrieve all messages that have not yet been acknowledged by the agent.

### Message Lookup
The `LookupMessage(messageID string)` function is a critical "front door" for response routing. It allows the system to find the original `PromptEnvelope` for any given message ID, enabling the gateway to bridge the gap between asynchronous agent processing and the original request source.

### Scheduler Job Management
The store manages dynamic, agent-created jobs through a set of CRUD operations. `SaveJob(job *types.Job)` and `DeleteJob(jobID string)` manage the in-memory state, while `GetDynamicJobs()` and `GetAgentJobs(agentID string)` provide filtered views of the active jobs. These jobs are persisted to a single `dynamic-jobs.json` file via `SaveJobs()`.

## Implementation Details

### Directory Structure
The store organizes its data into three primary subdirectories within the configured root:
- `agents/`: Stores one JSON file per agent (e.g., `agent-id.json`). Each file contains the `AgentState` struct, including the `Queue` of `PromptEnvelope` objects.
- `scheduler/`: Contains `dynamic-jobs.json`, a serialized array of all agent-created jobs.
- `channels/`: A reserved directory for future use by channel-specific persistence needs.

### Thread Safety
Concurrency is managed using a `sync.RWMutex`. Read-heavy operations like looking up messages or retrieving pending prompts use `RLock` to allow concurrent access, while operations that modify the queues or job maps use `Lock` to ensure data integrity.

### Sequence Management
The `AgentState` tracks a `LastSeq` counter. Every time a prompt is enqueued, this counter is incremented and assigned to the `PromptEnvelope`. This sequence number is used by the agent protocol to acknowledge messages, allowing the store to safely remove them from the persistent queue only after the agent has confirmed receipt.

### Internal Helpers
The module uses a private `writeJSON(relPath string, v any)` helper to handle the boilerplate of marshaling data with indentation and writing it to the correct file path. This ensures consistency across all persisted files.

## Dependencies

- [pkg/types](../types/design.md): The store is heavily dependent on the canonical data structures defined in the types package, specifically `PromptEnvelope` and `Job`. It uses these types for both in-memory management and JSON serialization.

## Technical Debt and Future Work

- **Index Eviction**: The `messageIndex` currently grows as messages are enqueued. While entries are cleared when acknowledged in some workflows, a more robust eviction or TTL mechanism is needed to prevent memory growth in long-running instances with high message volume.
- **Atomic File Writes**: The current implementation uses `os.WriteFile`, which is not atomic. If the system crashes during a write, a JSON file could be left in a corrupted state. Implementing a "write-to-temp-and-rename" pattern would significantly improve reliability.
- **SQLite Migration**: As the number of agents and messages grows, the overhead of reading and writing large JSON files may become a bottleneck. Transitioning to a SQLite backend would provide better performance and more complex querying capabilities while remaining a single-file, zero-config solution.

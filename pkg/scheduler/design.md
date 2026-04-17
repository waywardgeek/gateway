# Scheduler Module — Design Document

## Executive Summary

The `scheduler` module provides a built-in, cron-like execution engine for the CodeRhapsody Gateway. Its primary purpose is to enable both static, configuration-defined tasks and dynamic, agent-created jobs to fire prompts into the system's routing pipeline at specified intervals or at a single point in the future. By treating time-based events as first-class message sources, the scheduler allows AI agents to build autonomous behaviors, recurring workflows, and delayed responses without requiring external infrastructure. Every job managed by the scheduler eventually results in a standardized `PromptEnvelope` being delivered to the central router, ensuring that scheduled prompts are subject to the same security, trust, and delivery guarantees as messages from any other channel.

## File Inventory

- [scheduler.go](scheduler.go): The primary implementation file containing the `Scheduler` struct, the job lifecycle management logic, and the core execution loops for both static and dynamic jobs.

## Architecture and Data Flow

The scheduler operates as a stateful, background service that acts as an internal event producer for the gateway. It is designed around a decentralized execution model where every active job is managed by its own dedicated goroutine, ensuring that a slow-running or complex schedule calculation for one job does not delay the execution of others.

The lifecycle of data within the scheduler begins during initialization, where it concurrently loads static job definitions from the system configuration and dynamic job states from the persistent store. For each job, the scheduler calculates the next required fire time and enters a waiting state. When a job's timer expires, the scheduler constructs a `PromptEnvelope` populated with the job's specific prompt content and metadata. This envelope is then handed off to the central router for delivery to the target agent.

For dynamic jobs created by agents, the flow includes an additional persistence step. When an agent sends a scheduling command over its WebSocket connection, the agent manager invokes the scheduler's API. The scheduler validates the request, generates a unique identifier, and immediately persists the job to the store before launching its execution goroutine. This ensures that agent-created tasks survive gateway restarts and are consistently recovered during the boot sequence.

## Interface Implementations

The `scheduler` module does not implement any external interfaces defined in other packages. It is a standalone service that provides a concrete API consumed by the `agent.Manager` for dynamic job control and by the `cmd/server` for lifecycle management.

## Public API

The `Scheduler` provides a thread-safe public API for managing the lifecycle of both the service itself and the individual jobs it orchestrates.

The service is initialized via the `New` function, which requires references to the system configuration, the central router, and the persistence store. The `Start` method triggers the loading of all jobs and begins their execution loops, while the `Stop` method provides a graceful shutdown mechanism by canceling the global context, which in turn terminates all individual job goroutines.

For dynamic job management, the scheduler exposes `CreateJob`, `UpdateJob`, and `DeleteJob`. These methods are designed to be called by the agent manager on behalf of connected agents. They enforce ownership boundaries, ensuring that an agent can only modify or delete jobs it originally created. The `ListJobs` method allows an agent to query its current set of active scheduled tasks.

The API uses a combination of explicit parameters and pointers for updates (e.g., `*string` for optional fields) to allow for partial modifications of job definitions without requiring the caller to provide the full state of the job.

## Implementation Details

### Job Execution and Concurrency

The core of the scheduler's logic resides in the `runJob` method, which serves as the entry point for every job's goroutine. This method implements a robust execution loop that uses a `select` statement to multiplex between a timer (representing the next fire time) and a cancellation signal from the job's context. This design allows the scheduler to respond immediately to job deletions or updates by simply canceling the associated context, which cleanly terminates the goroutine.

The scheduler maintains a thread-safe map of these cancellation functions, indexed by job ID. Access to this map is protected by a `sync.Mutex`, ensuring that concurrent requests to create or delete jobs do not lead to race conditions or leaked goroutines.

### Schedule Calculation

The scheduler supports two types of timing logic: one-shot and recurring. One-shot jobs use a simple timestamp and are automatically purged from both the internal state and the persistent store once they fire. Recurring jobs use a simplified cron-like expression.

The current `parseCronNext` implementation is optimized for the most common use case: daily fixed-time execution (e.g., "30 9 * * *"). It parses the minute and hour fields and calculates the next occurrence of that time, correctly handling the transition to the next calendar day if the specified time has already passed for the current day.

### Provenance and Trust Injection

A critical responsibility of the scheduler is the correct construction of the `PromptEnvelope` during the `fireJob` sequence. The scheduler injects a specific `PromptSource` with the type set to "scheduler" and the trust level set to `TrustOwner`. This high trust level reflects that the job was either defined by the system administrator in the configuration or created by an authenticated agent. 

Furthermore, the scheduler supports metadata propagation. Any metadata defined in the job (such as a target Discord user ID or a specific conversation context) is merged into the `PromptEnvelope`'s metadata map. This allows the scheduler to trigger prompts that carry the necessary context for the agent to respond correctly to the original source of the request, even if that source is no longer actively connected.

## Dependencies

- `[pkg/config](../config/design.md)`: Consumed during startup to load static job definitions.
- `[pkg/router](../router/design.md)`: The destination for all prompts generated by firing jobs.
- `[pkg/store](../store/design.md)`: Used for the persistence and retrieval of dynamic, agent-created jobs.
- `[pkg/types](../types/design.md)`: Provides the canonical definitions for `Job`, `JobSchedule`, and `PromptEnvelope`.

## Technical Debt and Future Work

- **Cron Parser Sophistication**: The current cron parser is limited to daily fixed times. Integrating a more robust cron library would enable complex schedules such as "every 5 minutes" or "first Monday of the month."
- **Execution History**: The system currently lacks a history of job executions. Adding an audit log of when jobs fired and whether they were successfully delivered to the router would improve observability.
- **Misfire Policy**: There is currently no logic to handle jobs that were supposed to fire while the gateway was offline. Implementing a configurable misfire policy (e.g., "fire immediately on startup" vs. "skip to next") would improve the reliability of time-sensitive tasks.
- **Immediate Durability**: While dynamic jobs are saved to the store's in-memory state immediately, they rely on the global store's background save ticker for disk persistence. Adding an explicit flush after job creation would improve durability in the event of a crash immediately following a job creation.

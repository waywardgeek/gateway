# Webhook Module — Design Document

The `webhook` module provides a secure, HTTP-based inbound channel for the CodeRhapsody Gateway. It serves as a bridge for external services—such as games, monitoring tools, or third-party web applications—to inject prompts into the gateway's routing pipeline. By supporting both asynchronous and synchronous response patterns, the module allows the gateway to act either as a fire-and-forget message receiver or as a traditional synchronous API endpoint. Security is a primary concern, and the module enforces authenticity through HMAC-SHA256 signature verification, ensuring that only authorized callers can interact with the system's AI agents.

## File Inventory

- [webhook.go](webhook.go): Contains the core implementation of the webhook handler, including HTTP route management, HMAC verification logic, and the state management for synchronous request-response cycles.
- [webhook_test.go](webhook_test.go): Provides a comprehensive test suite that validates security enforcement, request parsing, routing integration, and both synchronous and asynchronous response modes.

## Architecture and Data Flow

The `webhook` module is architected around a central `Handler` that manages multiple independent webhook endpoints, each corresponding to a specific gateway channel.

The lifecycle begins during the gateway's initialization phase. The `Handler` is instantiated with the system's global configuration and a reference to the [router](../router/design.md). It scans the configuration for all channels of type `webhook` and prepares an internal map of `webhookChannel` objects. Each object stores the specific path, the HMAC secret (retrieved from environment variables), and the desired response timeout for that channel.

Once initialized, the `Handler` registers its endpoints with the gateway's main HTTP multiplexer via the `RegisterRoutes` method. Each configured path is mapped to a generic handler function that identifies the specific channel context based on the request URL.

When an inbound POST request arrives, the data flow follows a strict validation and processing pipeline. First, the handler enforces a maximum body size (currently 64KB) to prevent denial-of-service attacks. If a secret is configured for the channel, the handler extracts the `X-Signature-256` header and verifies the HMAC-SHA256 signature of the request body. Requests with missing or invalid signatures are immediately rejected.

After security validation, the JSON body is parsed into a `WebhookRequest` structure. The handler applies default values for the user identity and display name if they are not provided in the payload. The validated prompt is then passed to the [router](../router/design.md), which handles the persistence and delivery to the target AI agent.

The final stage of the flow depends on the channel's configured response mode. In asynchronous mode, the handler returns a `202 Accepted` status immediately, and the agent's eventual response is handled by the gateway's general response logic (e.g., posting to Discord). In synchronous mode, the handler blocks the HTTP request and registers a waiter in a thread-safe map. It then waits for the agent's response to be delivered back to the module or for a timeout to occur.

## Interface Implementations

While the `webhook` module does not implement a formal Go interface defined in a shared package, it fulfills the role of a "Channel" as described in the [project-design.md](../../project-design.md). It acts as a source of prompts for the [router](../router/design.md) and provides a `DeliverResponse` method that the central server uses to route agent replies back to waiting synchronous clients. This pattern ensures that the webhook module remains decoupled from the specific transport details of other channels while participating in the gateway's unified message flow.

## Public API

### Handler

The `Handler` is the primary entry point and coordinator for the module. It is designed to be thread-safe and manage the lifecycle of all webhook interactions.

- `NewHandler(cfg *config.Config, r *router.Router) *Handler`: This constructor initializes the handler by processing the gateway configuration and setting up the internal channel and pending request maps.
- `RegisterRoutes(mux *http.ServeMux)`: This method attaches the appropriate HTTP handlers to the provided multiplexer for every configured webhook path.
- `DeliverResponse(messageID, content string) bool`: This method is called by the gateway's central response dispatcher. It attempts to find a waiting synchronous request associated with the given identifier and delivers the agent's response content. It returns `true` if a waiter was successfully notified.

### Data Contracts

- `WebhookRequest`: This structure defines the expected JSON payload for incoming prompts. it includes the `content` of the prompt, an optional `user_id`, a `display_name`, and a map of `metadata`.
- `WebhookResponse`: This structure is used for both synchronous and asynchronous responses. It contains the agent's `content` (for successful sync requests), the `message_id` (if applicable), and a `status` string which can be "ok", "accepted", "timeout", or "error".

## Implementation Details

### HMAC Verification

The module implements security through HMAC-SHA256 signatures. Callers are expected to provide a signature in the `X-Signature-256` header, calculated by hashing the raw request body with a shared secret. The verification logic is robust against timing attacks, using `hmac.Equal` for constant-time comparison. The handler also supports signatures with or without the `sha256=` prefix for compatibility with various webhook providers.

### Synchronous Request Management

The management of synchronous requests is handled through a `pending` map that stores Go channels. When a synchronous request is processed, the handler creates a buffered channel and stores it in the map, keyed by the `channelID`. The handler then enters a `select` block, waiting on either the response channel or a timer.

Currently, the implementation uses the `channelID` as the key for the pending map. This design choice implies a "Keep It Simple" (KISS) approach where only one synchronous request is expected to be active per webhook channel at a time. If multiple concurrent requests arrive for the same channel, they will contend for the same waiter slot.

### Error Handling and Timeouts

The module provides clear HTTP status codes for various failure modes, including `401 Unauthorized` for missing signatures, `403 Forbidden` for invalid signatures, and `405 Method Not Allowed` for non-POST requests. In synchronous mode, if the agent fails to respond within the configured window (defaulting to 30 seconds), the module returns a `504 Gateway Timeout` with a JSON body indicating the timeout status.

## Dependencies

- [pkg/config](../config/design.md): Provides the channel definitions, paths, and secret environment variable names.
- [pkg/router](../router/design.md): Receives the validated prompts for delivery to agents.
- [pkg/types](../types/design.md): Defines the standard message structures used throughout the gateway.

## Technical Debt and Future Work

- **Request-Level Correlation**: The current synchronous implementation keys the pending map by `channelID`, which limits the channel to one active synchronous request at a time. Future iterations should transition to using a unique request or message ID to support high-concurrency synchronous workloads.
- **Configurable Body Limits**: The 64KB request body limit is currently hard-coded. This should be moved into the channel configuration to allow for larger payloads (such as base64-encoded images or documents) on a per-channel basis.
- **Enhanced Error Payloads**: While the module returns appropriate HTTP status codes, the JSON response bodies could be enriched with more detailed error messages to assist external developers in debugging integration issues.

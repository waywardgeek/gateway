# Discord Channel Module

## Executive Summary

The `discord` module provides the Discord integration for the AI Gateway, serving as a bidirectional bridge between the Discord chat platform and the gateway's internal routing system. It operates as a Discord bot that listens for messages in configured guilds and channels, transforms them into canonical `PromptEnvelope` structures, and routes them to the appropriate AI agents via the central `router`. Conversely, it handles the delivery of agent responses back to the originating Discord channels or as direct messages to users. The module is designed to be configuration-driven, resolving human-readable channel names into Discord IDs at startup and enforcing guild-level isolation to ensure the bot only interacts with authorized environments.

## File Inventory

- [discord.go](discord.go): The complete implementation of the Discord channel, encompassing bot lifecycle management, message event handling, channel resolution, and response delivery logic.

## Architecture and Data Flow

The module is architected around the `Channel` struct, which encapsulates the state and logic for a single Discord bot instance. It leverages the `discordgo` library to maintain a persistent connection to the Discord API.

The lifecycle begins with initialization via the `New` function, which accepts the gateway's channel configuration and a reference to the central router. When the `Start` method is invoked, the bot retrieves its authentication token from the environment, establishes a session with the necessary intents (guild messages, message content, and direct messages), and registers its internal message handler.

A critical phase of the startup process is channel resolution. The bot queries the configured Discord guild to map human-readable channel names from the configuration into unique Discord channel IDs. This mapping is cached internally to allow for efficient filtering of incoming messages.

The inbound data flow is triggered by Discord `MessageCreate` events. The `handleMessage` function performs a series of validations: it ignores messages from the bot itself, filters out messages from outside the target guild or unmonitored channels (while always permitting direct messages), and discards empty content. Valid messages are then enriched with Discord-specific metadata—such as channel, guild, and message IDs—and packaged into a `PromptEnvelope`. This envelope is then passed to the `router.Router` for delivery to the assigned agent.

The outbound data flow occurs when the gateway's main loop receives a response from an agent. The gateway identifies the target Discord channel from the original message's metadata and calls the `DeliverResponse` or `SendMessage` methods. These methods handle the complexities of the Discord API, including the 2000-character message limit, by truncating content if necessary before posting it back to the platform.

## Interface Implementations

The `Channel` struct functions as a logical implementation of a gateway channel, providing the standard lifecycle and delivery methods required by the `cmd/server` orchestrator:

- `Start(ctx context.Context) error`: Connects to Discord and initiates the background listener.
- `Stop()`: Gracefully terminates the Discord session.
- `DeliverResponse(discordChannelID, content string) error`: Routes an agent's response back to a specific Discord channel.

While these methods are not currently defined in a formal Go interface, they follow the consistent pattern used by all inbound channels in the gateway ecosystem.

## Public API

### Types

- **`Channel`**: The primary handle for the Discord bot. It is thread-safe, utilizing a `sync.RWMutex` to protect its internal state, including the active session, bot identity, and the map of monitored channels.

### Functions and Methods

The module provides a clean API for managing the bot's lifecycle and interacting with Discord:

- **`New(channelID string, cfg config.ChannelConfig, r *router.Router) *Channel`**: The constructor for the module. It takes a unique gateway-internal channel ID, the channel's configuration (including guild ID and token environment variable name), and a pointer to the system router.
- **`Start(ctx context.Context) error`**: Initiates the connection to Discord. It blocks until the connection is established and the initial channel resolution is complete. It then spawns a goroutine to monitor the provided context for cancellation, ensuring a graceful shutdown.
- **`Stop()`**: Closes the active Discord session and logs the disconnection.
- **`SendMessage(discordChannelID, content string) error`**: A low-level method for sending text to a specific Discord channel. It automatically handles Discord's 2000-character limit by truncating long messages with an ellipsis.
- **`DeliverResponse(discordChannelID, content string) error`**: A high-level wrapper around `SendMessage`, used by the gateway to route agent replies.
- **`SendDM(userID, content string) error`**: Sends a direct message to a specific Discord user. It handles the creation of the DM channel if one does not already exist.
- **`FirstChannelID() string`**: A utility method that returns the ID of the first monitored channel. This is used by the gateway's scheduler for messages that lack a specific channel context.
- **`GetListenChannelIDs() []string`**: Returns a slice of all Discord channel IDs currently being monitored by the bot.

## Implementation Details

### Channel Resolution and Filtering
The `resolveChannels` method is responsible for bridging the gap between the gateway's configuration and Discord's internal ID system. At startup, it fetches all channels for the configured guild. If the configuration specifies a list of `ListenChannels`, the bot only monitors those specific channels. If the list is empty, the bot defaults to monitoring all text channels in the guild. This flexible approach allows for both targeted and broad deployments.

### Message Handling Logic
The `handleMessage` function is the core event processor. It implements a strict filtering pipeline to ensure the bot only responds to relevant stimuli. Beyond basic self-filtering, it distinguishes between guild messages and direct messages. For guild messages, it verifies both the guild ID and the channel ID against its internal whitelist. For direct messages, it bypasses these checks, allowing users to interact with the AI privately. The function also captures the `discord_message_id`, which is essential for future features like message threading or reactions.

### Concurrency and State Safety
Because Discord events are delivered concurrently and agent responses can arrive at any time, the `Channel` struct uses a `sync.RWMutex` to guard its internal state. This ensures that the `session` and `listenChannels` map can be safely accessed across multiple goroutines without data races.

## State Management

The `Channel` maintains the following internal state:
- **Discord Session**: The active `discordgo.Session` representing the bot's connection.
- **Listen Channels**: A `map[string]bool` where keys are Discord channel IDs, providing O(1) lookup during message filtering.
- **Bot Identity**: The `botUserID` is cached after connection to allow for efficient self-filtering of messages.
- **Guild ID**: The specific Discord guild (server) the bot is assigned to.

## Dependencies

- **`github.com/bwmarrin/discordgo`**: The primary library for Discord API interaction.
- **`[pkg/config](../config/design.md)`**: Provides the `ChannelConfig` which dictates the bot's behavior and credentials.
- **`[pkg/router](../router/design.md)`**: The destination for all inbound messages processed by the bot.
- **`[pkg/types](../types/design.md)`**: Defines the `PromptEnvelope` and metadata structures used for message passing.

## Technical Debt and Future Work

- **Rate Limiting**: The module currently relies on the underlying library's basic handling. Implementing more granular, Discord-specific rate limiting would improve reliability during high-traffic periods.
- **Rich Media**: Support for attachments (images, files) is currently missing. Future updates should include the ability to process and forward media to agents.
- **Thread Awareness**: Enhancing the bot to automatically participate in Discord threads where it is mentioned would provide a more natural user experience.
- **Reaction Handling**: Adding support for Discord reactions would allow for simple feedback loops (e.g., using a "thumbs up" to trigger an action).

# Config Module — Architectural Design

The `config` module is the foundational source of truth for the AI Gateway. It defines the entire routing topology, security policies, and operational parameters of the system. In accordance with the project's core philosophy, the configuration file is the single, authoritative declaration of how the gateway behaves; no dynamic registration or agent-initiated routing is permitted.

## Executive Summary

The `config` module provides a structured, type-safe representation of the gateway's configuration. It is responsible for loading the configuration from a JSON file, unmarshaling it into Go structs, and performing deep validation to ensure the system's integrity before startup. By centralizing all routing and policy decisions in a single file, the module ensures that the gateway remains a predictable and secure control plane. It acts as the "data contract" for the entire system, ensuring that all components agree on the identity of agents, the structure of channels, and the rules of engagement.

## File Inventory

- [config.go](config.go): Defines the core configuration structures, the `Load` function for reading the configuration file, and the `Validate` method for ensuring logical consistency across the entire configuration tree.
- [config_test.go](config_test.go): Contains comprehensive tests for loading valid configurations and ensuring that various invalid configurations (e.g., missing required fields, routing to non-existent agents) are correctly rejected.

## Architecture and Data Flow

The `config` module is a passive component that is typically invoked once during the gateway's initialization sequence. It does not maintain any internal state or background processes; instead, it provides the static blueprint that other modules use to build their runtime state.

The data flow begins when the `Load` function is called with a path to a JSON file. The function reads the file from disk and uses the standard `encoding/json` package to unmarshal the content into a `Config` struct. This struct is a hierarchical tree that mirrors the structure of the JSON file, using Go tags to map field names.

Once the struct is populated, the `Validate` method is called to perform a deep semantic check of the configuration. This step is critical because it ensures that the system is in a valid state before any network listeners are started or any agent connections are accepted. The validation logic checks for required global settings, verifies that every agent has a cryptographic identity, and ensures that all routing references (from channels or scheduled jobs) point to valid, defined agents.

After successful validation, the `Config` object is returned to the caller (usually the main server) and subsequently passed to other modules like the Router, Scheduler, and various Channel Managers. These modules treat the configuration as an immutable reference that guides their behavior throughout the lifecycle of the process.

## Interface Implementations

The `config` module does not implement any external interfaces defined in other packages. Instead, it defines the primary data structures that other modules depend on. It serves as the "source" of the system's data model, providing the concrete types that define the boundaries and capabilities of the gateway.

## Public API

The primary entry point for the module is the `Load` function:

`func Load(path string) (*Config, error)`

This function is responsible for the entire lifecycle of configuration ingestion: reading from the filesystem, parsing the JSON format, and executing the validation logic. It returns a pointer to a fully validated `Config` struct or a descriptive error if any stage of the process fails.

The `Config` struct itself is the central data type, containing several exported sub-structs:
- `GatewayConfig`: Global server settings like the listen address and data directory.
- `AgentConfig`: Definitions for AI agents, including their public keys and skills.
- `ChannelConfig`: Definitions for inbound communication paths (Discord, Webhooks, etc.).
- `SchedulerConfig`: Definitions for static, recurring jobs.
- `TwilioConfig`: Optional settings for the Twilio voice integration.
- `TLSConfig`: Settings for the gateway's own TLS termination.

The `Validate` method is also exported, allowing for manual validation of a `Config` struct if it were constructed through means other than the `Load` function:

`func (c *Config) Validate() error`

This method returns a non-nil error if the configuration violates any of the system's logical constraints, such as a channel routing to a non-existent agent.

## Implementation Details

### Configuration Structure and Mapping

The configuration is organized into several top-level maps and structs that represent the different functional areas of the gateway. The `Agents` map uses unique agent identifiers as keys, allowing for O(1) lookups during routing. Similarly, the `Channels` map defines the various entry points into the system. The use of JSON tags allows the external configuration file to use snake_case (e.g., `data_dir`) while the Go code uses idiomatic PascalCase (e.g., `DataDir`).

### Security and Trust Model

A fundamental part of the configuration is the definition of trust levels and security policies. Each `ChannelConfig` includes a `Trust` field (which can be `owner`, `trusted`, or `external`) and a `Policy` struct. The `Policy` defines limits such as `MaxMessageLength` and `AllowedTools`. By baking these into the configuration, the gateway ensures that security is enforced at the very edge of the system. When a message enters through a channel, the router uses this configuration to stamp the message with its immutable trust level before it ever reaches an agent.

### Routing Integrity Enforcement

The `Validate` method implements the "closed-world" routing philosophy of the gateway. It iterates through every defined channel and every scheduled job, checking the `RouteTo` field against the keys in the `Agents` map. If a route points to an agent that is not explicitly defined in the configuration, the gateway will refuse to start. This prevents runtime "404" errors in the routing logic and ensures that the entire communication graph is known and verified at boot time.

### Twilio Integration Logic

The `TwilioConfig` struct demonstrates how the module handles optional, platform-specific settings. It includes fields for environment variable names (like `AccountSIDEnv`) rather than the secrets themselves, following best practices for configuration management. The validation logic ensures that if the `Twilio` section is present in the JSON, all its required fields are populated, preventing partial or broken configurations from being loaded.

## Dependencies

The `config` module is designed to be a leaf node in the dependency graph, depending only on the Go standard library (`encoding/json`, `fmt`, `os`). This ensures it can be imported by any other module without creating circular dependencies.

The following modules depend on `pkg/config` to guide their behavior:
- [pkg/router](../router/design.md): Uses the configuration to determine how to route messages and what trust levels to assign.
- [pkg/scheduler](../scheduler/design.md): Uses the `SchedulerConfig` to initialize static jobs.
- [pkg/noise](../noise/design.md): Uses agent public keys defined in `AgentConfig` for cryptographic authentication.
- [pkg/agent](../agent/design.md): Uses `AgentConfig` to manage connection lifecycles and skills.
- [pkg/discord](../discord/design.md), [pkg/twilio](../twilio/design.md), [pkg/webhook](../webhook/design.md): Use their respective sections of the configuration to initialize their platform-specific listeners.
- [cmd/server](../../cmd/server/design.md): The main entry point that calls `Load` and distributes the configuration to all other components.

## Technical Debt and Future Work

- **JSONC Support**: The current implementation uses standard JSON, which does not allow for comments. Adding support for JSON with comments (JSONC) would allow administrators to document the purpose of specific agents or channels directly within the configuration file.
- **Dynamic Reloading**: Currently, the gateway must be restarted to pick up configuration changes. Implementing a mechanism to reload the configuration (e.g., via a SIGHUP signal) while the system is running would improve operational flexibility, provided that the new configuration passes the same rigorous validation.
- **Environment Variable Substitution**: Adding the ability to substitute environment variables within the JSON file (e.g., `${DATA_DIR}`) would make the configuration more portable across different deployment environments.
- **Secret Management Integration**: While the system currently uses environment variable names for secrets, future iterations could support direct integration with secret managers (like HashiCorp Vault or AWS Secrets Manager) to retrieve sensitive values during the loading process.

# Noise Module Design

## Executive Summary

The `noise` module provides the cryptographic foundation for secure, mutually authenticated communication between the AI Gateway and its connected agents. It implements the **Noise-KK** pattern from the Noise Protocol Framework, ensuring that both the gateway and the agents verify each other's identities using pre-shared Ed25519 public keys. This approach eliminates the need for traditional, vulnerable API tokens or passwords, replacing them with a robust, forward-secure, and integrity-protected transport layer. By handling the complexities of the Noise state machine, this module allows higher-level components to treat secure communication as a simple stream of encrypted messages.

## File Inventory

- [noise_kk.go](noise_kk.go): The core implementation of the Noise-KK protocol handler. This file defines the `NoiseKK` struct and its methods for managing the handshake lifecycle and subsequent encrypted transport.
- [noise_kk_test.go](noise_kk_test.go): A comprehensive test suite that validates the full handshake flow, encryption/decryption reliability, associated data integrity, and various error conditions such as key mismatches and prologue failures.

## Architecture and Data Flow

The module is architected around the `NoiseKK` struct, which serves as a stateful wrapper for the `github.com/flynn/noise` library. The data flow within the module follows a strict transition from a handshake phase to a transport phase.

The process begins with **Initialization**, where both the initiator (typically the agent) and the responder (the gateway) create a `NoiseKK` instance. They must provide their own static keypair, the peer's public key, and a shared `prologue` string that acts as a domain separator.

During the **Handshake Phase**, the parties exchange exactly two messages. The initiator generates the first message using `WriteHandshakeMessage`, which the responder processes via `ReadHandshakeMessage`. The responder then generates the second message, which the initiator processes. This exchange establishes a shared secret and verifies the identities of both parties. The `NoiseKK` struct internally manages the `HandshakeState` provided by the underlying library during this phase.

Once the handshake is finalized, the module enters the **Transport Phase**. The `NoiseKK` struct automatically transitions its internal state, discarding the handshake state and initializing two independent `CipherState` objects—one for sending and one for receiving. All subsequent data is processed through the `Encrypt` and `Decrypt` methods, which utilize AES-GCM for authenticated encryption. These states handle automatic nonce incrementing, ensuring that each message is unique and protected against replay attacks.

## Interface Implementations

The `noise` module is a low-level cryptographic utility and does not implement any external interfaces defined in other packages. Instead, it is a critical dependency for the [agent](../agent/design.md) module, which consumes it to secure its WebSocket-based communication channels.

## Public API

The module exposes a narrative API designed to guide the caller through the Noise lifecycle:

- **`NewNoiseKK(role, localKey, remotePublicKey, prologue)`**: The primary entry point. It initializes a new Noise-KK endpoint. The `role` must be either "initiator" or "responder". It performs strict validation on the provided keys to ensure they are suitable for the KK pattern.
- **`GenerateKeypair()`**: A utility function that generates a new X25519 keypair, ensuring compatibility with the module's chosen cipher suite.
- **`WriteHandshakeMessage(payload)` / `ReadHandshakeMessage(message)`**: These methods advance the handshake state machine. They allow for an optional payload to be sent or received during the handshake, which is encrypted using the keys established up to that point in the exchange.
- **`Encrypt(plaintext, ad)` / `Decrypt(ciphertext, ad)`**: The primary methods for secure communication after the handshake. They provide authenticated encryption with optional associated data (AD), ensuring both confidentiality and integrity.
- **`IsHandshakeComplete()`**: A simple status check that allows callers to verify the session is ready for transport operations.
- **`HandshakeHash()`**: Returns the unique "channel binding" for the session. This hash is a digest of the entire handshake and can be used by higher-level protocols to ensure both parties have an identical view of the secure channel.

## Implementation Details

The module utilizes a fixed, high-security `CipherSuite` consisting of **DH25519** for key exchange, **AESGCM** for encryption, and **SHA256** for hashing. This combination provides a modern, efficient, and widely-vetted cryptographic foundation.

### Mutual Authentication via Noise-KK
The **Noise-KK** pattern was selected because it provides mutual authentication where both parties have pre-existing knowledge of each other's static keys. This aligns perfectly with the gateway's architecture: the gateway knows the public keys of all authorized agents via its configuration, and agents are provisioned with the gateway's public key. This pattern ensures that no unauthorized agent can connect, and no agent will inadvertently connect to a rogue gateway.

### The Role of the Prologue
The `prologue` field is a critical security feature. It is mixed into the handshake hash, ensuring that both parties agree on the context of the communication. In the AI Gateway, this is used to bind the session to a specific protocol version (e.g., `gateway-v1`). If there is a mismatch in the prologue, the handshake will fail, protecting the system against cross-protocol attacks or version incompatibilities.

### State Management and Concurrency
The `NoiseKK` struct is the sole repository of session state, including the handshake machine and the transport ciphers. It is **not thread-safe**. This design choice reflects the intended usage pattern where a single goroutine (such as a WebSocket read/write loop) manages the entire lifecycle of a connection. By avoiding internal mutexes, the module remains lightweight and avoids unnecessary locking overhead. If a caller requires multi-threaded access to a single session, they must provide their own synchronization mechanism.

## Dependencies

- **`github.com/flynn/noise`**: The underlying implementation of the Noise Protocol Framework.
- **[pkg/agent](../agent/design.md)**: The primary consumer of this module, which uses it to secure agent connections.

## Technical Debt and Future Work

- **Key Rotation**: There is currently no mechanism for rotating static keys without a service restart and configuration update. A future improvement could involve a more dynamic key management system.
- **Session Resumption**: Implementing Noise patterns that support session resumption (such as Noise-IK or Noise-XX with PSK) could significantly reduce the handshake overhead for agents that frequently reconnect due to unstable network conditions.
- **Identity Derivation**: While the system currently uses X25519 keys directly, future iterations could derive these from Ed25519 identity keys, aligning with common standards like those used in SSH or Signal.

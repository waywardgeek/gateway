// Package noise provides Noise-KK protocol implementation for the AI Gateway.
// Adapted from OpenADP's Noise-NK implementation. KK provides mutual authentication:
// both parties know each other's static keys before the handshake begins.
package noise

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	noiselib "github.com/flynn/noise"
)

// NoiseKK represents a Noise-KK protocol handler.
// Unlike NK where only the responder has a static key,
// in KK both parties have static keys and know each other's public key.
type NoiseKK struct {
	role              string
	isInitiator       bool
	prologue          []byte
	handshakeComplete bool
	localStaticKey    noiselib.DHKey
	remotePublicKey   []byte
	handshakeState    *noiselib.HandshakeState
	sendCipher        *noiselib.CipherState
	recvCipher        *noiselib.CipherState
	handshakeHash     []byte
}

// CipherSuite is the standard Noise cipher suite used by the gateway.
var CipherSuite = noiselib.NewCipherSuite(noiselib.DH25519, noiselib.CipherAESGCM, noiselib.HashSHA256)

// NewNoiseKK creates a new Noise-KK endpoint. Both parties must provide their
// own static keypair and the other party's public key.
func NewNoiseKK(role string, localKey noiselib.DHKey, remotePublicKey []byte, prologue []byte) (*NoiseKK, error) {
	if role != "initiator" && role != "responder" {
		return nil, errors.New("role must be 'initiator' or 'responder'")
	}
	if remotePublicKey == nil {
		return nil, errors.New("remote public key is required for KK pattern")
	}
	if len(localKey.Public) == 0 || len(localKey.Private) == 0 {
		return nil, errors.New("local static keypair is required for KK pattern")
	}

	kk := &NoiseKK{
		role:            role,
		isInitiator:     role == "initiator",
		prologue:        prologue,
		localStaticKey:  localKey,
		remotePublicKey: remotePublicKey,
	}

	if err := kk.initializeHandshake(rand.Reader); err != nil {
		return nil, err
	}

	return kk, nil
}

// initializeHandshake sets up the Noise handshake state.
func (kk *NoiseKK) initializeHandshake(random io.Reader) error {
	config := noiselib.Config{
		CipherSuite:   CipherSuite,
		Random:        random,
		Pattern:       noiselib.HandshakeKK,
		Initiator:     kk.isInitiator,
		Prologue:      kk.prologue,
		StaticKeypair: kk.localStaticKey,
		PeerStatic:    kk.remotePublicKey,
	}

	hs, err := noiselib.NewHandshakeState(config)
	if err != nil {
		return fmt.Errorf("failed to create handshake state: %v", err)
	}

	kk.handshakeState = hs
	return nil
}

// WriteHandshakeMessage writes the next handshake message with an optional payload.
func (kk *NoiseKK) WriteHandshakeMessage(payload []byte) ([]byte, error) {
	if kk.handshakeComplete {
		return nil, errors.New("handshake is already complete")
	}

	message, cs1, cs2, err := kk.handshakeState.WriteMessage(nil, payload)
	if err != nil {
		return nil, fmt.Errorf("failed to write handshake message: %v", err)
	}

	if cs1 != nil && cs2 != nil {
		kk.finalize(cs1, cs2)
	}

	return message, nil
}

// ReadHandshakeMessage reads and processes a handshake message from the other party.
func (kk *NoiseKK) ReadHandshakeMessage(message []byte) ([]byte, error) {
	if kk.handshakeComplete {
		return nil, errors.New("handshake is already complete")
	}

	payload, cs1, cs2, err := kk.handshakeState.ReadMessage(nil, message)
	if err != nil {
		return nil, fmt.Errorf("failed to read handshake message: %v", err)
	}

	if cs1 != nil && cs2 != nil {
		kk.finalize(cs1, cs2)
	}

	return payload, nil
}

// finalize completes the handshake and sets up transport ciphers.
func (kk *NoiseKK) finalize(cs1, cs2 *noiselib.CipherState) {
	if kk.isInitiator {
		kk.sendCipher = cs1
		kk.recvCipher = cs2
	} else {
		kk.sendCipher = cs2
		kk.recvCipher = cs1
	}
	kk.handshakeComplete = true
	kk.handshakeHash = kk.handshakeState.ChannelBinding()
}

// Encrypt encrypts a plaintext message with optional associated data.
func (kk *NoiseKK) Encrypt(plaintext []byte, ad []byte) ([]byte, error) {
	if !kk.handshakeComplete {
		return nil, errors.New("handshake must be completed before encrypting")
	}
	return kk.sendCipher.Encrypt(nil, ad, plaintext)
}

// Decrypt decrypts a ciphertext message with optional associated data.
func (kk *NoiseKK) Decrypt(ciphertext []byte, ad []byte) ([]byte, error) {
	if !kk.handshakeComplete {
		return nil, errors.New("handshake must be completed before decrypting")
	}
	return kk.recvCipher.Decrypt(nil, ad, ciphertext)
}

// HandshakeHash returns the handshake hash for channel binding verification.
func (kk *NoiseKK) HandshakeHash() []byte {
	return kk.handshakeHash
}

// IsHandshakeComplete returns whether the handshake has finished.
func (kk *NoiseKK) IsHandshakeComplete() bool {
	return kk.handshakeComplete
}

// PublicKey returns this party's static public key.
func (kk *NoiseKK) PublicKey() []byte {
	return kk.localStaticKey.Public
}

// GenerateKeypair generates a new X25519 keypair for Noise-KK.
func GenerateKeypair() (noiselib.DHKey, error) {
	return noiselib.DH25519.GenerateKeypair(rand.Reader)
}

package noise

import (
	"bytes"
	"testing"

	noiselib "github.com/flynn/noise"
)

func TestHandshakeAndEncrypt(t *testing.T) {
	// Generate keypairs for both parties
	aliceKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("failed to generate Alice's key: %v", err)
	}
	bobKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("failed to generate Bob's key: %v", err)
	}

	prologue := []byte("gateway-v1")

	// Create KK endpoints — both know each other's public keys
	alice, err := NewNoiseKK("initiator", aliceKey, bobKey.Public, prologue)
	if err != nil {
		t.Fatalf("failed to create Alice: %v", err)
	}
	bob, err := NewNoiseKK("responder", bobKey, aliceKey.Public, prologue)
	if err != nil {
		t.Fatalf("failed to create Bob: %v", err)
	}

	// KK handshake: 2 messages
	// Message 1: Alice → Bob (with payload)
	msg1, err := alice.WriteHandshakeMessage([]byte("hello bob"))
	if err != nil {
		t.Fatalf("Alice write msg1 failed: %v", err)
	}
	payload1, err := bob.ReadHandshakeMessage(msg1)
	if err != nil {
		t.Fatalf("Bob read msg1 failed: %v", err)
	}
	if string(payload1) != "hello bob" {
		t.Fatalf("expected 'hello bob', got %q", payload1)
	}

	// After msg1: Alice is NOT done, Bob is NOT done
	if alice.IsHandshakeComplete() {
		t.Fatal("Alice should not be complete after msg1")
	}

	// Message 2: Bob → Alice (with payload)
	msg2, err := bob.WriteHandshakeMessage([]byte("hello alice"))
	if err != nil {
		t.Fatalf("Bob write msg2 failed: %v", err)
	}
	payload2, err := alice.ReadHandshakeMessage(msg2)
	if err != nil {
		t.Fatalf("Alice read msg2 failed: %v", err)
	}
	if string(payload2) != "hello alice" {
		t.Fatalf("expected 'hello alice', got %q", payload2)
	}

	// Both should be complete now
	if !alice.IsHandshakeComplete() {
		t.Fatal("Alice should be complete")
	}
	if !bob.IsHandshakeComplete() {
		t.Fatal("Bob should be complete")
	}

	// Channel binding should match
	if !bytes.Equal(alice.HandshakeHash(), bob.HandshakeHash()) {
		t.Fatal("handshake hashes should match")
	}

	// Test encryption: Alice → Bob
	plaintext := []byte("secret message from alice")
	encrypted, err := alice.Encrypt(plaintext, nil)
	if err != nil {
		t.Fatalf("Alice encrypt failed: %v", err)
	}
	decrypted, err := bob.Decrypt(encrypted, nil)
	if err != nil {
		t.Fatalf("Bob decrypt failed: %v", err)
	}
	if !bytes.Equal(plaintext, decrypted) {
		t.Fatalf("decryption mismatch: expected %q, got %q", plaintext, decrypted)
	}

	// Test encryption: Bob → Alice
	plaintext2 := []byte("secret message from bob")
	encrypted2, err := bob.Encrypt(plaintext2, nil)
	if err != nil {
		t.Fatalf("Bob encrypt failed: %v", err)
	}
	decrypted2, err := alice.Decrypt(encrypted2, nil)
	if err != nil {
		t.Fatalf("Alice decrypt failed: %v", err)
	}
	if !bytes.Equal(plaintext2, decrypted2) {
		t.Fatalf("decryption mismatch: expected %q, got %q", plaintext2, decrypted2)
	}
}

func TestAssociatedData(t *testing.T) {
	aliceKey, _ := GenerateKeypair()
	bobKey, _ := GenerateKeypair()

	alice, _ := NewNoiseKK("initiator", aliceKey, bobKey.Public, nil)
	bob, _ := NewNoiseKK("responder", bobKey, aliceKey.Public, nil)

	// Complete handshake
	msg1, _ := alice.WriteHandshakeMessage(nil)
	bob.ReadHandshakeMessage(msg1)
	msg2, _ := bob.WriteHandshakeMessage(nil)
	alice.ReadHandshakeMessage(msg2)

	// Encrypt with AD
	ad := []byte("message-id:123")
	encrypted, err := alice.Encrypt([]byte("data"), ad)
	if err != nil {
		t.Fatalf("encrypt with AD failed: %v", err)
	}

	// Decrypt with correct AD
	decrypted, err := bob.Decrypt(encrypted, ad)
	if err != nil {
		t.Fatalf("decrypt with AD failed: %v", err)
	}
	if string(decrypted) != "data" {
		t.Fatalf("expected 'data', got %q", decrypted)
	}

	// Decrypt with wrong AD should fail
	_, err = bob.Decrypt(encrypted, []byte("wrong-ad"))
	if err == nil {
		t.Fatal("decrypt with wrong AD should fail")
	}
}

func TestWrongRemoteKey(t *testing.T) {
	aliceKey, _ := GenerateKeypair()
	bobKey, _ := GenerateKeypair()
	eveKey, _ := GenerateKeypair()

	// Alice thinks she's talking to Bob, but Eve intercepts
	alice, _ := NewNoiseKK("initiator", aliceKey, bobKey.Public, nil)
	// Eve pretends to be Bob but uses her own key
	eve, _ := NewNoiseKK("responder", eveKey, aliceKey.Public, nil)

	msg1, _ := alice.WriteHandshakeMessage(nil)
	// Eve tries to read Alice's message — should fail because
	// the handshake binds to Bob's static key
	_, err := eve.ReadHandshakeMessage(msg1)
	if err == nil {
		t.Fatal("handshake with wrong responder key should fail")
	}
}

func TestMultipleMessages(t *testing.T) {
	aliceKey, _ := GenerateKeypair()
	bobKey, _ := GenerateKeypair()

	alice, _ := NewNoiseKK("initiator", aliceKey, bobKey.Public, nil)
	bob, _ := NewNoiseKK("responder", bobKey, aliceKey.Public, nil)

	// Complete handshake
	msg1, _ := alice.WriteHandshakeMessage(nil)
	bob.ReadHandshakeMessage(msg1)
	msg2, _ := bob.WriteHandshakeMessage(nil)
	alice.ReadHandshakeMessage(msg2)

	// Send many messages in both directions — Noise nonces increment
	for i := 0; i < 100; i++ {
		plaintext := []byte("message-" + string(rune('A'+i%26)))
		encrypted, err := alice.Encrypt(plaintext, nil)
		if err != nil {
			t.Fatalf("encrypt %d failed: %v", i, err)
		}
		decrypted, err := bob.Decrypt(encrypted, nil)
		if err != nil {
			t.Fatalf("decrypt %d failed: %v", i, err)
		}
		if !bytes.Equal(plaintext, decrypted) {
			t.Fatalf("mismatch at %d", i)
		}
	}
}

func TestEmptyPayloads(t *testing.T) {
	aliceKey, _ := GenerateKeypair()
	bobKey, _ := GenerateKeypair()

	alice, _ := NewNoiseKK("initiator", aliceKey, bobKey.Public, nil)
	bob, _ := NewNoiseKK("responder", bobKey, aliceKey.Public, nil)

	// Handshake with nil payloads
	msg1, _ := alice.WriteHandshakeMessage(nil)
	p1, _ := bob.ReadHandshakeMessage(msg1)
	if len(p1) != 0 {
		t.Fatalf("expected empty payload, got %d bytes", len(p1))
	}

	msg2, _ := bob.WriteHandshakeMessage(nil)
	p2, _ := alice.ReadHandshakeMessage(msg2)
	if len(p2) != 0 {
		t.Fatalf("expected empty payload, got %d bytes", len(p2))
	}

	// Encrypt empty message
	encrypted, err := alice.Encrypt([]byte{}, nil)
	if err != nil {
		t.Fatalf("encrypt empty failed: %v", err)
	}
	decrypted, err := bob.Decrypt(encrypted, nil)
	if err != nil {
		t.Fatalf("decrypt empty failed: %v", err)
	}
	if len(decrypted) != 0 {
		t.Fatalf("expected empty decrypted, got %d bytes", len(decrypted))
	}
}

func TestConstructorValidation(t *testing.T) {
	aliceKey, _ := GenerateKeypair()
	bobKey, _ := GenerateKeypair()

	// Bad role
	_, err := NewNoiseKK("observer", aliceKey, bobKey.Public, nil)
	if err == nil {
		t.Fatal("should reject invalid role")
	}

	// Missing remote key
	_, err = NewNoiseKK("initiator", aliceKey, nil, nil)
	if err == nil {
		t.Fatal("should reject nil remote key")
	}

	// Missing local key
	var emptyKey noiselib.DHKey
	_, err = NewNoiseKK("initiator", emptyKey, bobKey.Public, nil)
	if err == nil {
		t.Fatal("should reject empty local key")
	}
}

func TestPreHandshakeOperations(t *testing.T) {
	aliceKey, _ := GenerateKeypair()
	bobKey, _ := GenerateKeypair()

	alice, _ := NewNoiseKK("initiator", aliceKey, bobKey.Public, nil)

	// Encrypt before handshake should fail
	_, err := alice.Encrypt([]byte("test"), nil)
	if err == nil {
		t.Fatal("encrypt before handshake should fail")
	}

	// Decrypt before handshake should fail
	_, err = alice.Decrypt([]byte("test"), nil)
	if err == nil {
		t.Fatal("decrypt before handshake should fail")
	}
}

func TestPublicKey(t *testing.T) {
	key, _ := GenerateKeypair()
	bobKey, _ := GenerateKeypair()

	kk, _ := NewNoiseKK("initiator", key, bobKey.Public, nil)
	if !bytes.Equal(kk.PublicKey(), key.Public) {
		t.Fatal("PublicKey() should return local public key")
	}
}

func TestPrologueMismatch(t *testing.T) {
	aliceKey, _ := GenerateKeypair()
	bobKey, _ := GenerateKeypair()

	alice, _ := NewNoiseKK("initiator", aliceKey, bobKey.Public, []byte("version-1"))
	bob, _ := NewNoiseKK("responder", bobKey, aliceKey.Public, []byte("version-2"))

	msg1, _ := alice.WriteHandshakeMessage(nil)
	bob.ReadHandshakeMessage(msg1)
	msg2, _ := bob.WriteHandshakeMessage(nil)
	_, err := alice.ReadHandshakeMessage(msg2)

	// Prologue mismatch should cause handshake failure
	if err == nil {
		t.Fatal("prologue mismatch should fail handshake")
	}
}

package service

import (
	"bytes"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCredentialEnvelopeMissingKeysFailClosed(t *testing.T) {
	var envelope *CredentialEnvelope
	require.NotPanics(t, func() {
		_, err := envelope.Seal("openai", 1, 1, []byte("secret"))
		require.ErrorIs(t, err, ErrCredentialEnvelope)
	})
	require.NotPanics(t, func() {
		_, err := envelope.Open("openai", 1, 1, EncryptedCredential{Version: 1})
		require.ErrorIs(t, err, ErrCredentialEnvelope)
	})
}

func TestCredentialEnvelopeAuthenticationAndRotation(t *testing.T) {
	old := bytes.Repeat([]byte{1}, 32)
	current := bytes.Repeat([]byte{2}, 32)
	e, err := NewCredentialEnvelope("old", map[string][]byte{"old": old})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := e.Seal("openai", 10, 20, []byte("secret-token"))
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := NewCredentialEnvelope("new", map[string][]byte{"old": old, "new": current})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := rotated.Open("openai", 10, 20, sealed)
	if err != nil || string(plain) != "secret-token" {
		t.Fatalf("old key decrypt: %q %v", plain, err)
	}
	newEnvelope, err := rotated.Seal("openai", 10, 20, plain)
	if err != nil || newEnvelope.KeyID != "new" {
		t.Fatalf("rotation seal: %v", err)
	}
	newOnly, err := NewCredentialEnvelope("new", map[string][]byte{"new": current})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := newOnly.Open("openai", 10, 20, newEnvelope); err != nil || string(got) != "secret-token" {
		t.Fatalf("rotated decrypt: %v", err)
	}
	wrongKey, err := NewCredentialEnvelope("old", map[string][]byte{"old": current})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongKey.Open("openai", 10, 20, sealed); err == nil {
		t.Fatal("wrong key accepted")
	}
	for _, a := range []struct {
		p     string
		o, id int64
	}{{"other", 10, 20}, {"openai", 11, 20}, {"openai", 10, 21}} {
		if _, err := rotated.Open(a.p, a.o, a.id, sealed); err == nil {
			t.Fatal("wrong AAD accepted")
		}
	}
	sealed.Ciphertext[0] ^= 1
	if _, err := rotated.Open("openai", 10, 20, sealed); err == nil {
		t.Fatal("tampering accepted")
	}
	sealed.Ciphertext[0] ^= 1
	sealed.KeyID = "missing"
	if _, err := rotated.Open("openai", 10, 20, sealed); err == nil {
		t.Fatal("unknown key accepted")
	}
	if _, err := NewCredentialEnvelope("missing", map[string][]byte{"new": current}); err == nil {
		t.Fatal("missing active key accepted")
	}
}

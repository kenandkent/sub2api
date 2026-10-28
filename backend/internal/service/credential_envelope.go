package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
)

// EncryptedCredential is safe to persist, but must not be exposed in DTOs or logs.
type EncryptedCredential struct {
	Version    int    `json:"version"`
	KeyID      string `json:"key_id"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// CredentialEnvelope holds keys supplied by trusted server configuration.
type CredentialEnvelope struct {
	current string
	keys    map[string][]byte
}

var ErrCredentialEnvelope = errors.New("credential envelope invalid or unavailable")

func NewCredentialEnvelope(current string, keys map[string][]byte) (*CredentialEnvelope, error) {
	if current == "" || len(keys[current]) != 32 {
		return nil, ErrCredentialEnvelope
	}
	copied := make(map[string][]byte, len(keys))
	for id, key := range keys {
		if id == "" || len(key) != 32 {
			return nil, ErrCredentialEnvelope
		}
		copied[id] = append([]byte(nil), key...)
	}
	return &CredentialEnvelope{current: current, keys: copied}, nil
}
func credentialAAD(provider string, owner, account int64) ([]byte, error) {
	if provider == "" || owner <= 0 || account <= 0 {
		return nil, ErrCredentialEnvelope
	}
	return json.Marshal([]any{provider, owner, account})
}
func (e *CredentialEnvelope) aead(id string) (cipher.AEAD, error) {
	if e == nil {return nil,ErrCredentialEnvelope}
	key, ok := e.keys[id]
	if !ok {
		return nil, ErrCredentialEnvelope
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrCredentialEnvelope
	}
	return cipher.NewGCM(b)
}
func (e *CredentialEnvelope) Seal(provider string, owner, account int64, plaintext []byte) (EncryptedCredential, error) {
	if e == nil {return EncryptedCredential{},ErrCredentialEnvelope}
	aad, err := credentialAAD(provider, owner, account)
	if err != nil {
		return EncryptedCredential{}, err
	}
	a, err := e.aead(e.current)
	if err != nil {
		return EncryptedCredential{}, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return EncryptedCredential{}, err
	}
	return EncryptedCredential{Version: 1, KeyID: e.current, Nonce: nonce, Ciphertext: a.Seal(nil, nonce, plaintext, aad)}, nil
}
func (e *CredentialEnvelope) Open(provider string, owner, account int64, enc EncryptedCredential) ([]byte, error) {
	aad, err := credentialAAD(provider, owner, account)
	if err != nil || enc.Version != 1 {
		return nil, ErrCredentialEnvelope
	}
	a, err := e.aead(enc.KeyID)
	if err != nil || len(enc.Nonce) != a.NonceSize() {
		return nil, ErrCredentialEnvelope
	}
	p, err := a.Open(nil, enc.Nonce, enc.Ciphertext, aad)
	if err != nil {
		return nil, ErrCredentialEnvelope
	}
	return p, nil
}

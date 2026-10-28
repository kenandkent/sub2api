package service

import (
	"context"
	"errors"
)

var ErrVaultRotationInvalid = errors.New("invalid vault rotation request")
var ErrVaultRotationConflict = errors.New("vault rotation operation conflicts")
var ErrVaultRotationUnavailable = errors.New("vault rotation unavailable")

type VaultRotationResult struct {
	OperationID string `json:"operation_id"`
	Processed   int    `json:"processed"`
	Remaining   int    `json:"remaining"`
	Completed   bool   `json:"completed"`
	TargetKeyID string `json:"target_key_id"`
}

type MarketplaceVaultRotationStore interface {
	Rotate(context.Context, int64, string, string, int) (*VaultRotationResult, error)
}

// CurrentKeyID identifies the configured write key without exposing key material.
func (e *CredentialEnvelope) CurrentKeyID() string {
	if e == nil {
		return ""
	}
	return e.current
}

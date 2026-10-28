//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSupplierRuntimeCASUsesOwnerAndEnvelope(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	old := service.EncryptedCredential{Version: 1, KeyID: "old", Nonce: []byte{1}, Ciphertext: []byte{2}}
	next := service.EncryptedCredential{Version: 1, KeyID: "new", Nonce: []byte{3}, Ciphertext: []byte{4}}
	oldJSON, _ := json.Marshal(old)
	nextJSON, _ := json.Marshal(next)
	mock.ExpectExec(`UPDATE accounts SET credentials`).WithArgs(string(nextJSON), int64(8), int64(7), string(oldJSON)).WillReturnResult(sqlmock.NewResult(0, 0))
	swapped, err := NewSupplierRuntimeCredentialRepository(db).CompareAndSwapSupplierCredential(context.Background(), 8, 7, old, next)
	require.NoError(t, err)
	require.False(t, swapped)
	require.NoError(t, mock.ExpectationsWereMet())
}

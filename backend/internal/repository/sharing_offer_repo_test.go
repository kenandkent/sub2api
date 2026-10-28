//go:build unit

package repository

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"strings"

	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

func TestSupplierAuthorizationWriterRequiresDurableOperation(t *testing.T) {
	r := NewSharingOfferRepository(nil, nil)
	if _, err := r.SaveSupplierAuthorization(context.Background(), 1, "create", 0, "openai", &openai.TokenResponse{AccessToken: "secret", ExpiresIn: 3600}); err == nil {
		t.Fatal("authorization without durable operation must fail")
	}
}

type encryptedCredentialArgument struct {
	t        *testing.T
	envelope *service.CredentialEnvelope
}

func (a encryptedCredentialArgument) Match(value driver.Value) bool {
	raw, ok := value.(string)
	if !ok || strings.Contains(raw, "access-secret") || strings.Contains(raw, "refresh-secret") {
		return false
	}
	var payload struct {
		Envelope service.EncryptedCredential `json:"_supplier_envelope"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return false
	}
	plaintext, err := a.envelope.Open("openai", 7, 42, payload.Envelope)
	if err != nil {
		return false
	}
	var credentials map[string]any
	if json.Unmarshal(plaintext, &credentials) != nil {
		return false
	}
	return credentials["access_token"] == "access-secret" && credentials["refresh_token"] == "refresh-secret"
}

func TestSupplierAuthorizationWriterEncryptsAndCompletesAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	env, err := service.NewCredentialEnvelope("current", map[string][]byte{"current": bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status = 'active'").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectQuery("SELECT metadata FROM supplier_operations").WithArgs(int64(7), "op").WillReturnRows(sqlmock.NewRows([]string{"metadata"}).AddRow([]byte(`{"provider":"openai","purpose":"create","account_id":0}`)))
	mock.ExpectQuery("INSERT INTO accounts").WithArgs(int64(7), "openai").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42)))
	mock.ExpectExec("INSERT INTO sharing_offers").WithArgs(int64(42), int64(7)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE accounts SET credentials=").WithArgs(encryptedCredentialArgument{t, env}, int64(42), int64(7), nil).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE supplier_operations SET status='completed'").WithArgs(int64(7), "op", int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO scheduler_outbox").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	ctx := service.WithSupplierAuthorizationOperation(context.Background(), "op")
	id, err := NewSharingOfferRepository(db, env).SaveSupplierAuthorization(ctx, 7, "create", 0, "openai", &openai.TokenResponse{AccessToken: "access-secret", RefreshToken: "refresh-secret", ExpiresIn: 3600})
	if err != nil || id != 42 {
		t.Fatalf("save: id=%d err=%v", id, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSupplierAuthorizationWriterRejectsOperationBindingMismatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	env, err := service.NewCredentialEnvelope("current", map[string][]byte{"current": bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status = 'active'").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectQuery("SELECT metadata FROM supplier_operations").WithArgs(int64(7), "op").WillReturnRows(sqlmock.NewRows([]string{"metadata"}).AddRow([]byte(`{"provider":"openai","purpose":"reauthorize","account_id":123}`)))
	mock.ExpectRollback()
	ctx := service.WithSupplierAuthorizationOperation(context.Background(), "op")
	if _, err := NewSharingOfferRepository(db, env).SaveSupplierAuthorization(ctx, 7, "create", 0, "openai", &openai.TokenResponse{AccessToken: "access-secret", ExpiresIn: 3600}); err == nil {
		t.Fatal("binding mismatch accepted")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

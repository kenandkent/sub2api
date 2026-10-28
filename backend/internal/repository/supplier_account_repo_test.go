//go:build unit

package repository

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type apiKeyEnvelopeArgument struct {
	envelope *service.CredentialEnvelope
	secret   string
}

func (a apiKeyEnvelopeArgument) Match(value driver.Value) bool {
	raw, ok := value.(string)
	if !ok || strings.Contains(raw, a.secret) {
		return false
	}
	var payload struct {
		Envelope service.EncryptedCredential `json:"_supplier_envelope"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return false
	}
	plain, err := a.envelope.Open("openai", 7, 42, payload.Envelope)
	if err != nil {
		return false
	}
	var v map[string]string
	return json.Unmarshal(plain, &v) == nil && v["api_key"] == a.secret
}
func TestSupplierAccountCreateEncryptsCredentialAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	env, err := service.NewCredentialEnvelope("current", map[string][]byte{"current": bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	secret := "sk-example-high-entropy-credential"
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status='active'").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectQuery("INSERT INTO supplier_operations").WithArgs(int64(7), "op", "digest").WillReturnRows(sqlmock.NewRows([]string{"operation_key", "kind", "status", "account_id", "error_code"}).AddRow("op", "account_create", "processing", nil, ""))
	mock.ExpectQuery("INSERT INTO accounts").WithArgs(int64(7), "example", "openai", "apikey", 2, nil).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42)))
	mock.ExpectExec("UPDATE accounts SET credentials").WithArgs(apiKeyEnvelopeArgument{env, secret}, int64(42), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO marketplace_credential_fingerprints").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO sharing_offers").WithArgs(int64(42), int64(7), `["gpt-4o"]`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE supplier_operations").WithArgs(int64(7), "op", int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO scheduler_outbox").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	op, err := NewSupplierAccountRepository(db, env).Create(context.Background(), 7, "op", "digest", service.SupplierAccountCreate{Name: "example", Platform: "openai", Type: "apikey", APIKey: secret, Concurrency: 2, AllowedModels: []string{"gpt-4o"}}, config.MarketplaceProviderConfig{BaseURL: "https://api.openai.com/v1"})
	if err != nil || op.AccountID == nil || *op.AccountID != 42 || op.Status != "completed" {
		t.Fatalf("op=%+v err=%v", op, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestSupplierAccountCreateRequiresEnvelope(t *testing.T) {
	if _, err := NewSupplierAccountRepository(nil, nil).Create(context.Background(), 7, "op", "digest", service.SupplierAccountCreate{}, config.MarketplaceProviderConfig{}); err == nil {
		t.Fatal("missing envelope accepted")
	}
}
func TestSupplierAccountPatchVersionConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status='active'").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectQuery("SELECT a.id,a.name").WithArgs(int64(42), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "platform", "type", "status", "concurrency", "credential", "offer", "review", "enabled", "models", "version"}).AddRow(42, "name", "openai", "apikey", "active", 2, true, 9, "approved", true, []byte(`["gpt-4o"]`), 3))
	mock.ExpectRollback()
	_, err = NewSupplierAccountRepository(db, nil).Patch(context.Background(), 7, 42, service.SupplierAccountPatch{ExpectedVersion: 2})
	if err == nil {
		t.Fatal("stale version accepted")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

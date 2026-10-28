//go:build unit

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestSupplierRepositoryScopesAccountLookup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT id,name,platform,type,status FROM accounts WHERE id=\$1 AND owner_user_id=\$2 AND deleted_at IS NULL`).WithArgs(int64(7), int64(43)).WillReturnError(sql.ErrNoRows)
	_, err = NewSupplierRepository(db).GetAccount(context.Background(), 43, 7)
	if !apperrors.IsNotFound(err) {
		t.Fatalf("other owner: %v", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestSupplierRepositoryReviewAuditAtomic(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM supplier_profiles WHERE user_id=\$1 FOR UPDATE`).WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("pending"))
	cols := []string{"user_id", "status", "review_reason", "agreement_version", "agreement_accepted_at", "application_fields", "payout_frozen", "version"}
	mock.ExpectQuery(`UPDATE supplier_profiles SET status=`).WithArgs(int64(42), service.SupplierStatusActive, "approved").WillReturnRows(sqlmock.NewRows(cols).AddRow(42, "active", "approved", 3, time.Now(), []byte(`{}`), false, 2))
	mock.ExpectExec(`INSERT INTO supplier_review_audits`).WithArgs(int64(42), int64(9), "pending", service.SupplierStatusActive, "approved").WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()
	_, err = NewSupplierRepository(db).Review(context.Background(), 42, 9, service.SupplierStatusActive, "approved")
	if err == nil {
		t.Fatal("audit failure must roll back review")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSupplierRepositoryListsRequestedPage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM supplier_profiles`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(41))
	mock.ExpectQuery(`FROM supplier_profiles ORDER BY user_id LIMIT \$1 OFFSET \$2`).WithArgs(20, 20).WillReturnRows(sqlmock.NewRows([]string{"user_id", "status", "review_reason", "agreement_version", "agreement_accepted_at", "application_fields", "payout_frozen", "version"}).AddRow(21, "pending", "", 1, nil, []byte(`{}`), false, 1))
	rows, total, err := NewSupplierRepository(db).ListProfiles(context.Background(), 2, 20)
	if err != nil || total != 41 || len(rows) != 1 || rows[0].UserID != 21 {
		t.Fatalf("%v %d %v", rows, total, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

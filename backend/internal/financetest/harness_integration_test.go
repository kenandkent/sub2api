//go:build integration

package financetest

import (
	"context"
	"database/sql"
	"log"
	"os"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	_ "github.com/lib/pq"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

var integrationDB *sql.DB

func TestMain(m *testing.M) {
	ctx := context.Background()
	p, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("finance_test"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
	dsn, err := p.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
	integrationDB, err = sql.Open("postgres", dsn)
	if err == nil {
		err = repository.ApplyMigrations(ctx, integrationDB)
	}
	if err != nil {
		log.Print(err)
		_ = p.Terminate(ctx)
		os.Exit(1)
	}
	code := m.Run()
	_ = integrationDB.Close()
	_ = p.Terminate(ctx)
	os.Exit(code)
}

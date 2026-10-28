//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeFreeSettlementIsAtomicAndIdempotent(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("free-%s@example.com", uuid.NewString()), PasswordHash: "hash", Balance: 10})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-free-" + uuid.NewString(), Name: "free"})
	account := mustCreateAccount(t, client, &service.Account{Name: "free-" + uuid.NewString(), Type: service.AccountTypeAPIKey})
	r := NewUsageBillingRepository(client, integrationDB).(service.OpenCodeFreeUsageRepository)
	meta := &service.OpenCodeFreeUsageMeta{RequestID: "ocfree_rollback_" + uuid.NewString(), LeaseToken: uuid.NewString(), Complete: true, BillingSource: "upstream", RawUsage: json.RawMessage(`{"prompt_tokens":10,"completion_tokens":2}`)}
	require.NoError(t, r.BeginOpenCodeFreeAttempt(ctx, meta, key.ID, account.ID, "profile", "payload", time.Minute))
	cmd := &service.UsageBillingCommand{RequestID: meta.RequestID, APIKeyID: key.ID, UserID: user.ID, AccountID: account.ID, AccountType: service.AccountTypeAPIKey, RequestPayloadHash: "payload", BalanceCost: 1.25}
	cmd.Normalize()
	log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: meta.RequestID, Model: "big-pickle", CreatedAt: time.Now()}
	require.NoError(t, r.StageOpenCodeFreeUsage(ctx, meta, cmd, log, &service.OpenCodeFreeBillingContext{Platform: "openai"}))
	// Retries preserve the first price, while changed raw usage is rejected.
	repriced := *cmd
	repriced.BalanceCost = 8
	require.NoError(t, r.StageOpenCodeFreeUsage(ctx, meta, &repriced, log, &service.OpenCodeFreeBillingContext{Platform: "different"}))
	changed := *meta
	changed.RawUsage = json.RawMessage(`{"prompt_tokens":11,"completion_tokens":2}`)
	require.Error(t, r.StageOpenCodeFreeUsage(ctx, &changed, cmd, log, &service.OpenCodeFreeBillingContext{Platform: "openai"}))
	// A failed log insert must roll back the deduction and leave the snapshot recoverable.
	_, err := integrationDB.ExecContext(ctx, `CREATE FUNCTION opencode_free_test_reject_log() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.request_id LIKE 'ocfree_rollback_%' THEN RAISE EXCEPTION 'test insert failure'; END IF; RETURN NEW; END $$`)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `CREATE TRIGGER opencode_free_test_reject BEFORE INSERT ON usage_logs FOR EACH ROW EXECUTE FUNCTION opencode_free_test_reject_log()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS opencode_free_test_reject ON usage_logs`)
		_, _ = integrationDB.ExecContext(context.Background(), `DROP FUNCTION IF EXISTS opencode_free_test_reject_log()`)
	})
	_, err = r.SettleOpenCodeFreeUsage(ctx, meta.RequestID)
	require.Error(t, err)
	var unchanged float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", user.ID).Scan(&unchanged))
	require.InDelta(t, 10, unchanged, 0.0000001)
	var dedup int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1", meta.RequestID).Scan(&dedup))
	require.Zero(t, dedup)
	_, err = integrationDB.ExecContext(ctx, `DROP TRIGGER opencode_free_test_reject ON usage_logs`)
	require.NoError(t, err)
	// Concurrent settlement attempts must serialize around the frozen row.
	type settlement struct {
		result *service.OpenCodeFreeSettlement
		err    error
	}
	results := make(chan settlement, 8)
	for i := 0; i < cap(results); i++ {
		go func() {
			result, err := r.SettleOpenCodeFreeUsage(ctx, meta.RequestID)
			results <- settlement{result, err}
		}()
	}
	applied := 0
	for i := 0; i < cap(results); i++ {
		out := <-results
		require.NoError(t, out.err)
		if out.result.Result.Applied {
			require.InDelta(t, 1.25, out.result.Command.BalanceCost, 0.0000001)
			require.Equal(t, "openai", out.result.BillingContext.Platform)
			applied++
		}
	}
	require.Equal(t, 1, applied)
	result, err := r.SettleOpenCodeFreeUsage(ctx, meta.RequestID)
	require.NoError(t, err)
	require.False(t, result.Result.Applied)
	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", user.ID).Scan(&balance))
	require.InDelta(t, 8.75, balance, 0.0000001)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM usage_logs WHERE request_id=$1", meta.RequestID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestOpenCodeFreeSessionKeepsFirstCandidateUntilExpiry(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	r := NewUsageBillingRepository(testEntClient(t), integrationDB).(service.OpenCodeFreeUsageRepository)
	hash := uuid.NewString()
	first, err := r.ResolveOpenCodeFreeSession(ctx, hash, "ses_first", time.Hour)
	require.NoError(t, err)
	require.Equal(t, "ses_first", first)
	second, err := r.ResolveOpenCodeFreeSession(ctx, hash, "ses_second", time.Hour)
	require.NoError(t, err)
	require.Equal(t, first, second)
	_, err = integrationDB.ExecContext(ctx, "UPDATE opencode_free_sessions SET expires_at=now()-interval '1 second' WHERE key_hash=$1", hash)
	require.NoError(t, err)
	third, err := r.ResolveOpenCodeFreeSession(ctx, hash, "ses_third", time.Hour)
	require.NoError(t, err)
	require.Equal(t, "ses_third", third)
}

func TestOpenCodeFreeRecoverySettlesOnlyFrozenUsageAndExpiresPending(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("free-recovery-%s@example.com", uuid.NewString()), PasswordHash: "hash", Balance: 10})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-recover-" + uuid.NewString(), Name: "recover"})
	account := mustCreateAccount(t, client, &service.Account{Name: "free-recovery-" + uuid.NewString(), Type: service.AccountTypeAPIKey})
	r := NewUsageBillingRepository(client, integrationDB).(service.OpenCodeFreeUsageRepository)
	ready := &service.OpenCodeFreeUsageMeta{RequestID: "ocfree_" + uuid.NewString(), LeaseToken: uuid.NewString(), Complete: true, BillingSource: "free", RawUsage: json.RawMessage(`{"prompt_tokens":5,"completion_tokens":1}`)}
	pending := &service.OpenCodeFreeUsageMeta{RequestID: "ocfree_" + uuid.NewString(), LeaseToken: uuid.NewString()}
	require.NoError(t, r.BeginOpenCodeFreeAttempt(ctx, ready, key.ID, account.ID, "profile", "payload", time.Minute))
	require.NoError(t, r.BeginOpenCodeFreeAttempt(ctx, pending, key.ID, account.ID, "profile", "payload", time.Minute))
	cmd := &service.UsageBillingCommand{RequestID: ready.RequestID, APIKeyID: key.ID, UserID: user.ID, AccountID: account.ID, AccountType: service.AccountTypeAPIKey, RequestPayloadHash: "payload", BalanceCost: 2}
	cmd.Normalize()
	log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: ready.RequestID, Model: "big-pickle", CreatedAt: time.Now()}
	require.NoError(t, r.StageOpenCodeFreeUsage(ctx, ready, cmd, log, &service.OpenCodeFreeBillingContext{Platform: "openai"}))
	_, err := integrationDB.ExecContext(ctx, `UPDATE opencode_free_attempts SET updated_at=now()-interval '61 seconds' WHERE request_id=$1`, ready.RequestID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE opencode_free_attempts SET lease_until=now()-interval '1 second' WHERE request_id=$1`, pending.RequestID)
	require.NoError(t, err)
	recovered, err := r.RecoverOpenCodeFreeUsage(ctx)
	require.NoError(t, err)
	require.Len(t, recovered, 1)
	require.Equal(t, ready.RequestID, recovered[0].Command.RequestID)
	var state string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state FROM opencode_free_attempts WHERE request_id=$1`, pending.RequestID).Scan(&state))
	require.Equal(t, "unknown", state)
	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, user.ID).Scan(&balance))
	require.InDelta(t, 8, balance, 0.0000001)
	recovered, err = r.RecoverOpenCodeFreeUsage(ctx)
	require.NoError(t, err)
	require.Empty(t, recovered)
}

func TestOpenCodeFreeRecoveryRotatesFailedRows(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("free-rotate-%s@example.com", uuid.NewString()), PasswordHash: "hash", Balance: 10})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-rotate-" + uuid.NewString(), Name: "rotate"})
	account := mustCreateAccount(t, client, &service.Account{Name: "free-rotate-" + uuid.NewString(), Type: service.AccountTypeAPIKey})
	r := NewUsageBillingRepository(client, integrationDB).(service.OpenCodeFreeUsageRepository)
	prefix := "ocfree_poison_" + uuid.NewString() + "_"
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO opencode_free_attempts(request_id,lease_token,api_key_id,user_id,account_id,profile_hash,payload_hash,state,lease_until,billing_source,raw_usage,billing_command,usage_log,updated_at) SELECT $1||n,'lease',$2,$3,$4,'profile','payload','ready',now(),'free','{}'::jsonb,'{}'::jsonb,'{}'::jsonb,now()-interval '2 minutes' FROM generate_series(1,100) n`, prefix, key.ID, user.ID, account.ID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM opencode_free_attempts WHERE request_id LIKE $1`, prefix+"%")
	})
	meta := &service.OpenCodeFreeUsageMeta{RequestID: "ocfree_" + uuid.NewString(), LeaseToken: uuid.NewString(), Complete: true, BillingSource: "free", RawUsage: json.RawMessage(`{"prompt_tokens":5,"completion_tokens":1}`)}
	require.NoError(t, r.BeginOpenCodeFreeAttempt(ctx, meta, key.ID, account.ID, "profile", "payload", time.Minute))
	cmd := &service.UsageBillingCommand{RequestID: meta.RequestID, APIKeyID: key.ID, UserID: user.ID, AccountID: account.ID, AccountType: service.AccountTypeAPIKey, RequestPayloadHash: "payload", BalanceCost: 2}
	cmd.Normalize()
	log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: meta.RequestID, Model: "big-pickle", CreatedAt: time.Now()}
	require.NoError(t, r.StageOpenCodeFreeUsage(ctx, meta, cmd, log, &service.OpenCodeFreeBillingContext{Platform: "openai"}))
	_, err = integrationDB.ExecContext(ctx, `UPDATE opencode_free_attempts SET updated_at=now()-interval '61 seconds' WHERE request_id=$1`, meta.RequestID)
	require.NoError(t, err)
	_, err = r.RecoverOpenCodeFreeUsage(ctx)
	require.Error(t, err)
	recovered, _ := r.RecoverOpenCodeFreeUsage(ctx)
	require.Len(t, recovered, 1)
	require.Equal(t, meta.RequestID, recovered[0].Command.RequestID)
}

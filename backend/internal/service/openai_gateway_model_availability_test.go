//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 事故场景回归：opencode_go 分组全部实例被上游免费配额 429 冷却后，
// OpenAI 兼容诊断必须给出完整的限流普查（而不是在第一个可服务账号就提前
// 返回），分类器才能把 pool=0 的选号失败判成 429 + Retry-After。
func TestOpenAIDiagnose_OpenCodeGoGroupFullyRateLimited_ReportsFullCensus(t *testing.T) {
	groupID := int64(3)
	early := time.Now().Add(90 * time.Minute)
	late := time.Now().Add(12 * time.Hour)
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:               2,
				Platform:         PlatformOpenCodeGo,
				Status:           StatusActive,
				Schedulable:      true,
				RateLimitResetAt: &early,
				AccountGroups:    []AccountGroup{{GroupID: groupID}},
				Credentials: map[string]any{
					"model_mapping": map[string]any{"deepseek-v4.1-flash-free": "deepseek-v4.1-flash-free"},
				},
			},
			{
				ID:               3,
				Platform:         PlatformOpenCodeGo,
				Status:           StatusActive,
				Schedulable:      true,
				RateLimitResetAt: &late,
				AccountGroups:    []AccountGroup{{GroupID: groupID}},
				Credentials: map[string]any{
					"model_mapping": map[string]any{"deepseek-v4.1-flash-free": "deepseek-v4.1-flash-free"},
				},
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	svc := &OpenAIGatewayService{
		accountRepo:       repo,
		cfg:               testConfig(),
		schedulerSnapshot: &SchedulerSnapshotService{},
	}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "deepseek-v4.1-flash-free", PlatformOpenCodeGo)

	require.True(t, diag.HasAccountsInPool)
	require.True(t, diag.HasModelSupport)
	require.Equal(t, 2, diag.ModelCapableAccounts)
	require.Equal(t, 2, diag.ModelCapableRateLimited, "整组冷却时普查必须覆盖全部可服务账号")
	require.WithinDuration(t, early, diag.EarliestRateLimitResetAt, time.Second)
}

func TestOpenAIDiagnose_OpenCodeGoGroupHealthy_ReportsZeroCensus(t *testing.T) {
	groupID := int64(3)
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:            2,
				Platform:      PlatformOpenCodeGo,
				Status:        StatusActive,
				Schedulable:   true,
				AccountGroups: []AccountGroup{{GroupID: groupID}},
				Credentials: map[string]any{
					"model_mapping": map[string]any{"deepseek-v4.1-flash-free": "deepseek-v4.1-flash-free"},
				},
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	svc := &OpenAIGatewayService{
		accountRepo:       repo,
		cfg:               testConfig(),
		schedulerSnapshot: &SchedulerSnapshotService{},
	}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "deepseek-v4.1-flash-free", PlatformOpenCodeGo)

	require.True(t, diag.HasModelSupport)
	require.Equal(t, 1, diag.ModelCapableAccounts)
	require.Equal(t, 0, diag.ModelCapableRateLimited, "健康账号不得被计入限流普查")
	require.True(t, diag.EarliestRateLimitResetAt.IsZero())
}

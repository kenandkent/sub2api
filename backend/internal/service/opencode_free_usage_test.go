package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"testing"
	"time"
)

func TestOpenCodeFreeAttemptFailsClosedWithoutRepository(t *testing.T) {
	s := &OpenAIGatewayService{}
	if _, err := s.BeginOpenCodeFreeAttempt(context.Background(), 1, 2, "profile", "payload", time.Minute); err == nil {
		t.Fatal("attempt must fail without persistent repository")
	}
}

func TestOpenCodeFreeUsageRejectsMismatchBetweenRawAndBilledTokens(t *testing.T) {
	meta := &OpenCodeFreeUsageMeta{RequestID: "ocfree_test", LeaseToken: "lease", BillingSource: "free", Complete: true, RawUsage: []byte(`{"prompt_tokens":10,"completion_tokens":2,"billing_source":"free"}`)}
	result := &OpenAIForwardResult{OpenCodeFree: meta, Usage: OpenAIUsage{InputTokens: 11, OutputTokens: 2}}
	if err := validateOpenCodeFreeResult(result); err == nil {
		t.Fatal("mismatched raw and billed usage must be rejected")
	}
}

func TestOpenCodeFreeUsageRejectsNonFreeBillingSource(t *testing.T) {
	result := &OpenAIForwardResult{OpenCodeFree: &OpenCodeFreeUsageMeta{RequestID: "ocfree_test", LeaseToken: "lease", BillingSource: "paid", Complete: true, RawUsage: []byte(`{"prompt_tokens":10,"completion_tokens":2,"billing_source":"paid"}`)}, Usage: OpenAIUsage{InputTokens: 10, OutputTokens: 2}}
	if err := validateOpenCodeFreeResult(result); err == nil {
		t.Fatal("non-free billing source must be rejected")
	}
}

func TestOpenCodeFreeUsageRejectsOverflowingCacheTokens(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	raw := fmt.Sprintf(`{"prompt_tokens":10,"completion_tokens":1,"billing_source":"free","cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}`, maxInt, maxInt)
	result := &OpenAIForwardResult{OpenCodeFree: &OpenCodeFreeUsageMeta{RequestID: "ocfree_test", LeaseToken: "lease", BillingSource: "free", Complete: true, RawUsage: []byte(raw)}, Usage: OpenAIUsage{InputTokens: 10, OutputTokens: 1, CacheReadInputTokens: maxInt, CacheCreationInputTokens: maxInt}}
	if err := validateOpenCodeFreeResult(result); err == nil {
		t.Fatal("overflowing cache counts must be rejected")
	}
}

type freeRetryRepo struct {
	UsageBillingRepository
	snapshot *OpenCodeFreeSettlement
	fail     bool
}

func (r *freeRetryRepo) BeginOpenCodeFreeAttempt(context.Context, *OpenCodeFreeUsageMeta, int64, int64, string, string, time.Duration) error {
	return nil
}
func (r *freeRetryRepo) MarkOpenCodeFreeUnknown(context.Context, *OpenCodeFreeUsageMeta, string) error {
	return nil
}
func (r *freeRetryRepo) ResolveOpenCodeFreeSession(context.Context, string, string, time.Duration) (string, error) {
	return "", nil
}
func (r *freeRetryRepo) StageOpenCodeFreeUsage(_ context.Context, _ *OpenCodeFreeUsageMeta, cmd *UsageBillingCommand, log *UsageLog, b *OpenCodeFreeBillingContext) error {
	if r.snapshot == nil {
		r.snapshot = &OpenCodeFreeSettlement{Command: *cmd, Log: *log, BillingContext: *b}
	}
	return nil
}
func (r *freeRetryRepo) SettleOpenCodeFreeUsage(context.Context, string) (*OpenCodeFreeSettlement, error) {
	if r.fail {
		r.fail = false
		return nil, errors.New("temporary failure")
	}
	r.snapshot.Result.Applied = true
	return r.snapshot, nil
}
func (r *freeRetryRepo) RecoverOpenCodeFreeUsage(context.Context) ([]OpenCodeFreeRecoveredUsage, error) {
	return nil, nil
}

type freeSideEffectCache struct {
	BillingCache
	balance, platform float64
	platformName      string
}

func (c *freeSideEffectCache) DeductUserBalance(_ context.Context, _ int64, amount float64) error {
	c.balance = amount
	return nil
}
func (c *freeSideEffectCache) GetUserPlatformQuotaCache(context.Context, int64, string) (*UserPlatformQuotaCacheEntry, bool, error) {
	return nil, false, nil
}
func (c *freeSideEffectCache) IncrUserPlatformQuotaUsageCache(_ context.Context, _ int64, platform string, cost float64, _ time.Duration, _ bool) error {
	c.platformName = platform
	c.platform = cost
	return nil
}

type freeQuotaRepo struct{ UserPlatformQuotaRepository }

func (*freeQuotaRepo) IncrementUsageWithReset(context.Context, int64, string, float64, time.Time) error {
	return nil
}

func TestOpenCodeFreeRetryUsesFrozenPriceForCacheAndPlatformQuota(t *testing.T) {
	repo := &freeRetryRepo{fail: true}
	cache := &freeSideEffectCache{}
	cfg := &config.Config{}
	svc := &OpenAIGatewayService{usageBillingRepo: repo, billingCacheService: &BillingCacheService{cache: cache, cfg: cfg}, deferredService: &DeferredService{}, userPlatformQuotaRepo: &freeQuotaRepo{}}
	meta := &OpenCodeFreeUsageMeta{RequestID: "ocfree_retry", LeaseToken: "lease", BillingSource: "free", Complete: true, RawUsage: []byte(`{"prompt_tokens":10,"completion_tokens":2,"billing_source":"free"}`)}
	p := &postUsageBillingParams{User: &User{ID: 1}, APIKey: &APIKey{ID: 2}, Account: &Account{ID: 3}, Cost: &CostBreakdown{ActualCost: 1.25, TotalCost: 1.25}, Platform: "openai"}
	log := &UsageLog{RequestID: meta.RequestID, UserID: 1, APIKeyID: 2, AccountID: 3, ActualCost: 1.25, TotalCost: 1.25}
	if err := svc.settleOpenCodeFreeUsage(context.Background(), meta, log, p, false); err == nil {
		t.Fatal("first settlement must fail")
	}
	p.Cost = &CostBreakdown{ActualCost: 8, TotalCost: 8}
	p.Platform = "different"
	log.ActualCost = 8
	log.TotalCost = 8
	if err := svc.settleOpenCodeFreeUsage(context.Background(), meta, log, p, false); err != nil {
		t.Fatal(err)
	}
	if cache.balance != 1.25 || cache.platform != 1.25 || cache.platformName != "openai" {
		t.Fatalf("side effects used retry price/platform: balance=%v platform=%v name=%q", cache.balance, cache.platform, cache.platformName)
	}
}

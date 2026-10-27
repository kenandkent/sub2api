package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// OpenCodeFreeUsageMeta is created internally before dispatch; callers cannot supply it.
type OpenCodeFreeUsageMeta struct {
	RequestID     string
	LeaseToken    string
	BillingSource string
	RawUsage      json.RawMessage
	Complete      bool
}

// OpenCodeFreeUsageRepository is intentionally separate from ordinary billing.
type OpenCodeFreeUsageRepository interface {
	BeginOpenCodeFreeAttempt(context.Context, *OpenCodeFreeUsageMeta, int64, int64, string, string, time.Duration) error
	MarkOpenCodeFreeUnknown(context.Context, *OpenCodeFreeUsageMeta, string) error
	ResolveOpenCodeFreeSession(context.Context, string, string, time.Duration) (string, error)
	StageOpenCodeFreeUsage(context.Context, *OpenCodeFreeUsageMeta, *UsageBillingCommand, *UsageLog, *OpenCodeFreeBillingContext) error
	SettleOpenCodeFreeUsage(context.Context, string) (*OpenCodeFreeSettlement, error)
	RecoverOpenCodeFreeUsage(context.Context) ([]OpenCodeFreeRecoveredUsage, error)
}

func freeRandomID(prefix string) (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func (s *OpenAIGatewayService) freeUsageRepo() (OpenCodeFreeUsageRepository, error) {
	if s == nil {
		return nil, errors.New("OpenCode free billing unavailable")
	}
	r, ok := s.usageBillingRepo.(OpenCodeFreeUsageRepository)
	if !ok || r == nil {
		return nil, errors.New("OpenCode free billing unavailable")
	}
	return r, nil
}

func (s *OpenAIGatewayService) BeginOpenCodeFreeAttempt(ctx context.Context, apiKeyID, accountID int64, profileHash, payloadHash string, timeout time.Duration) (*OpenCodeFreeUsageMeta, error) {
	r, err := s.freeUsageRepo()
	if err != nil {
		return nil, err
	}
	if apiKeyID <= 0 || accountID <= 0 || strings.TrimSpace(profileHash) == "" || strings.TrimSpace(payloadHash) == "" || timeout <= 0 {
		return nil, errors.New("invalid OpenCode free attempt")
	}
	id, err := freeRandomID("ocfree_")
	if err != nil {
		return nil, err
	}
	token, err := freeRandomID("")
	if err != nil {
		return nil, err
	}
	meta := &OpenCodeFreeUsageMeta{RequestID: id, LeaseToken: token}
	if err = r.BeginOpenCodeFreeAttempt(ctx, meta, apiKeyID, accountID, profileHash, payloadHash, timeout); err != nil {
		return nil, err
	}
	return meta, nil
}

func (s *OpenAIGatewayService) MarkOpenCodeFreeUnknown(ctx context.Context, meta *OpenCodeFreeUsageMeta, reason string) error {
	r, err := s.freeUsageRepo()
	if err != nil {
		return err
	}
	if meta == nil || meta.RequestID == "" || meta.LeaseToken == "" {
		return errors.New("invalid OpenCode free attempt")
	}
	return r.MarkOpenCodeFreeUnknown(ctx, meta, reason)
}
func (s *OpenAIGatewayService) ResolveOpenCodeFreeSession(ctx context.Context, keyHash, candidate string, ttl time.Duration) (string, error) {
	r, err := s.freeUsageRepo()
	if err != nil {
		return "", err
	}
	if keyHash == "" || candidate == "" || ttl <= 0 {
		return "", errors.New("invalid OpenCode free session")
	}
	return r.ResolveOpenCodeFreeSession(ctx, keyHash, candidate, ttl)
}

// OpenCodeFreeBillingContext freezes the mode and platform used by post-commit effects.
type OpenCodeFreeBillingContext struct {
	Simple                     bool
	SimpleModeKeyRateLimitOnly bool
	IsSubscriptionBill         bool
	Platform                   string
}

// OpenCodeFreeSettlement carries the exact snapshot applied by the transaction.
type OpenCodeFreeSettlement struct {
	Result         UsageBillingApplyResult
	Command        UsageBillingCommand
	Log            UsageLog
	BillingContext OpenCodeFreeBillingContext
}

type OpenCodeFreeRecoveredUsage struct {
	Command UsageBillingCommand
	Log     UsageLog
}

func (s *OpenAIGatewayService) RecoverOpenCodeFreeUsage(ctx context.Context) error {
	r, err := s.freeUsageRepo()
	if err != nil {
		return err
	}
	recovered, recoverErr := r.RecoverOpenCodeFreeUsage(ctx)
	// Invalidate rather than replay a cache delta: the database is authoritative.
	if s.billingCacheService != nil {
		for _, entry := range recovered {
			cmd := entry.Command
			if cmd.BalanceCost > 0 {
				_ = s.billingCacheService.InvalidateUserBalance(ctx, cmd.UserID)
			}
			if cmd.SubscriptionCost > 0 && entry.Log.GroupID != nil {
				_ = s.billingCacheService.InvalidateSubscription(ctx, cmd.UserID, *entry.Log.GroupID)
			}
			if cmd.APIKeyRateLimitCost > 0 {
				_ = s.billingCacheService.InvalidateAPIKeyRateLimit(ctx, cmd.APIKeyID)
			}
		}
	}
	return recoverErr
}

func validateOpenCodeFreeMeta(meta *OpenCodeFreeUsageMeta) error {
	if meta == nil || !meta.Complete || !strings.HasPrefix(meta.RequestID, "ocfree_") || meta.LeaseToken == "" || meta.BillingSource != "free" || !json.Valid(meta.RawUsage) {
		return fmt.Errorf("incomplete OpenCode free usage")
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(meta.RawUsage, &raw) != nil || raw == nil {
		return errors.New("invalid OpenCode free raw usage")
	}
	return nil
}

func (s *OpenAIGatewayService) settleOpenCodeFreeUsage(ctx context.Context, meta *OpenCodeFreeUsageMeta, log *UsageLog, p *postUsageBillingParams, simple bool) error {
	repo, err := s.freeUsageRepo()
	if err != nil {
		return err
	}
	cmd := buildUsageBillingCommand(meta.RequestID, log, p)
	if cmd == nil {
		return errors.New("OpenCode free billing command unavailable")
	}
	if simple {
		cmd.BalanceCost = 0
		cmd.SubscriptionCost = 0
		cmd.APIKeyQuotaCost = 0
		cmd.APIKeyRateLimitCost = 0
		cmd.AccountQuotaCost = 0
		cmd.RequestFingerprint = ""
		cmd.Normalize()
	}
	billingCtx, cancel := detachedBillingContext(ctx)
	defer cancel()
	frozenContext := &OpenCodeFreeBillingContext{Simple: simple, SimpleModeKeyRateLimitOnly: p.SimpleModeKeyRateLimitOnly, IsSubscriptionBill: p.IsSubscriptionBill, Platform: p.Platform}
	if err = repo.StageOpenCodeFreeUsage(billingCtx, meta, cmd, log, frozenContext); err != nil {
		return err
	}
	result, err := repo.SettleOpenCodeFreeUsage(billingCtx, meta.RequestID)
	if err != nil {
		return err
	}
	if result == nil || !result.Result.Applied {
		return nil
	}
	if result.BillingContext.Simple {
		if s.deferredService != nil {
			s.deferredService.ScheduleLastUsedUpdate(p.Account.ID)
		}
		return nil
	}
	if result.Result.APIKeyQuotaExhausted {
		if invalidator, ok := p.APIKeyService.(apiKeyAuthCacheInvalidator); ok && p.APIKey != nil && p.APIKey.Key != "" {
			invalidator.InvalidateAuthCacheByKey(billingCtx, p.APIKey.Key)
		}
	}
	frozen := openCodeFreeFrozenBillingParams(p, result)
	finalizePostUsageBilling(billingCtx, frozen, s.billingDeps(), &result.Result)
	return nil
}

func validateOpenCodeFreeResult(result *OpenAIForwardResult) error {
	if result == nil {
		return errors.New("missing OpenCode free result")
	}
	if err := validateOpenCodeFreeMeta(result.OpenCodeFree); err != nil {
		return err
	}
	var raw struct {
		Prompt        *int   `json:"prompt_tokens"`
		Completion    *int   `json:"completion_tokens"`
		BillingSource string `json:"billing_source"`
		CacheRead     *int   `json:"cache_read_input_tokens"`
		CacheCreation *int   `json:"cache_creation_input_tokens"`
		Details       struct {
			Cached   *int `json:"cached_tokens"`
			Creation *int `json:"cache_creation_tokens"`
		} `json:"prompt_tokens_details"`
	}
	if err := json.Unmarshal(result.OpenCodeFree.RawUsage, &raw); err != nil || raw.Prompt == nil || raw.Completion == nil || *raw.Prompt < 0 || *raw.Completion < 0 || raw.BillingSource != result.OpenCodeFree.BillingSource {
		return errors.New("invalid OpenCode free raw usage")
	}
	read, creation := 0, 0
	if raw.Details.Cached != nil {
		read = *raw.Details.Cached
	}
	if raw.Details.Creation != nil {
		creation = *raw.Details.Creation
	}
	if raw.CacheRead != nil {
		if raw.Details.Cached != nil && read != *raw.CacheRead {
			return errors.New("conflicting OpenCode free cache usage")
		}
		read = *raw.CacheRead
	}
	if raw.CacheCreation != nil {
		if raw.Details.Creation != nil && creation != *raw.CacheCreation {
			return errors.New("conflicting OpenCode free cache usage")
		}
		creation = *raw.CacheCreation
	}
	if read < 0 || creation < 0 || read > *raw.Prompt || creation > *raw.Prompt-read || result.Usage.InputTokens != *raw.Prompt || result.Usage.OutputTokens != *raw.Completion || result.Usage.CacheReadInputTokens != read || result.Usage.CacheCreationInputTokens != creation {
		return errors.New("OpenCode free raw usage mismatch")
	}
	return nil
}

// Rebuild post-commit amounts and routing from the committed snapshot, never a retry's new prices.
func openCodeFreeFrozenBillingParams(current *postUsageBillingParams, settlement *OpenCodeFreeSettlement) *postUsageBillingParams {
	p := *current
	log := settlement.Log
	p.Cost = &CostBreakdown{InputCost: log.InputCost, ImageInputCost: log.ImageInputCost, OutputCost: log.OutputCost, ImageOutputCost: log.ImageOutputCost, CacheCreationCost: log.CacheCreationCost, CacheReadCost: log.CacheReadCost, TotalCost: log.TotalCost, ActualCost: log.ActualCost, LongContextBillingApplied: log.LongContextBillingApplied}
	p.IsSubscriptionBill = settlement.BillingContext.IsSubscriptionBill
	p.SimpleModeKeyRateLimitOnly = settlement.BillingContext.SimpleModeKeyRateLimitOnly
	p.Platform = settlement.BillingContext.Platform
	user := *current.User
	user.ID = settlement.Command.UserID
	p.User = &user
	account := *current.Account
	account.ID = settlement.Command.AccountID
	p.Account = &account
	key := *current.APIKey
	key.ID = settlement.Command.APIKeyID
	key.GroupID = log.GroupID
	// Match the frozen decision to update rate-limit usage even if configuration changed.
	key.RateLimit5h = 0
	key.RateLimit1d = 0
	key.RateLimit7d = 0
	if settlement.Command.APIKeyRateLimitCost > 0 {
		key.RateLimit5h = 1
	}
	p.APIKey = &key
	return &p
}

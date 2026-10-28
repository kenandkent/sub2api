package service

import (
	"context"
	"database/sql"
	"errors"
)

var (
	ErrMarketplaceIdempotencyConflict = errors.New("marketplace idempotency key reused with different request")
	ErrMarketplaceRequestState        = errors.New("invalid marketplace request state")
	ErrMarketplaceRequestInvalid      = errors.New("invalid marketplace request")
)

const (
	MarketplaceDefinitelyNotDispatched = "definitely_not_dispatched"
	MarketplaceRejectedBeforeExecution = "rejected_before_execution"
	MarketplacePossiblyExecuted        = "possibly_executed"
)

type MarketplacePrepareCommand struct {
	BuyerUserID    int64
	APIKeyID       int64
	GroupID        int64
	RequestDigest  string
	IdempotencyKey *string
}

// MarketplaceRequestRepository persists the receipt and serializes attempt
// transitions. All transactional operations must share the caller's funding
// transaction; no operation here creates or verifies a funding hold.
type MarketplaceRequestRepository interface {
	Prepare(context.Context, MarketplacePrepareCommand) (*MarketplaceReceipt, bool, error)
	GetOwned(context.Context, string, int64, int64) (*MarketplaceReceipt, error)
	Reserve(context.Context, *sql.Tx, string, Money) error
	CreateAttempt(context.Context, *sql.Tx, string, QuoteSnapshot) (*MarketplaceAttempt, error)
	MarkDispatched(context.Context, *sql.Tx, string, int64) error
	ClassifyFailure(context.Context, *sql.Tx, string, int64, string) error
	RecordUsage(context.Context, *sql.Tx, string, int64, UsageSnapshot) error
	MarkUncertain(context.Context, *sql.Tx, string, int64, string) error
	MarkSettled(context.Context, *sql.Tx, string, int64, Money) error
	MarkNeedsReview(context.Context, *sql.Tx, string, int64, string) error
	MarkVoided(context.Context, *sql.Tx, string) error
}

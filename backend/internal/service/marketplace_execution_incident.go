package service

import "time"

// MarketplaceExecutionIncident is an operator-reported association. It is not
// proof of usage and creates no financial effect.
type MarketplaceExecutionIncident struct {
	OperationID        string    `json:"operation_id"`
	ReceiptID          string    `json:"receipt_id"`
	AttemptID          int64     `json:"attempt_id"`
	ReleaseOperationID string    `json:"release_operation_id"`
	ActorUserID        int64     `json:"actor_user_id"`
	QuoteDigest        string    `json:"quote_digest"`
	Evidence           string    `json:"evidence"`
	CreatedAt          time.Time `json:"created_at"`
	Status             string    `json:"status"`
}

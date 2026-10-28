package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type marketplaceTestStore struct {
	completed, reviewed, voided int
	fail                        bool
	replay                      *service.MarketplaceReceipt
}

func (s *marketplaceTestStore) PrepareAndReserve(context.Context, service.MarketplacePrepareCommand, service.Money) (*service.MarketplaceReceipt, bool, error) {
	return s.replay, false, nil
}
func (s *marketplaceTestStore) Qualify(context.Context, int64, int64, int64, string) (int64, error) {
	return 1, nil
}
func (s *marketplaceTestStore) Dispatch(context.Context, string, service.QuoteSnapshot) (*service.MarketplaceAttempt, error) {
	return nil, nil
}
func (s *marketplaceTestStore) Complete(context.Context, string, int64, service.QuoteSnapshot, service.UsageSnapshot, service.UsageBillingCommand) (*service.MarketplaceBillingResult, error) {
	s.completed++
	if s.fail {
		return nil, errors.New("db down")
	}
	return &service.MarketplaceBillingResult{FirstApplied: true}, nil
}
func (s *marketplaceTestStore) MarkReview(context.Context, string, int64, string) error {
	s.reviewed++
	return nil
}
func (s *marketplaceTestStore) VoidUndispatched(context.Context, string) error {
	s.voided++
	return nil
}
func TestMarketplaceGatewaySettlementControlsResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name                        string
		reported, fail              bool
		wantCompleted, wantReviewed int
		wantBody                    bool
	}{
		{"known", true, false, 1, 0, true}, {"missing", false, false, 0, 1, false}, {"database failure", true, true, 1, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			writer := newMarketplaceResponseWriter(c.Writer, false)
			c.Writer = writer
			_, err := writer.Write([]byte("response"))
			require.NoError(t, err)
			store := &marketplaceTestStore{fail: tc.fail}
			state := &marketplaceRequestState{gateway: service.NewMarketplaceGateway(nil, store, nil), receipt: &service.MarketplaceReceipt{ID: "r"}, attempt: &service.MarketplaceAttempt{ID: 1}, writer: writer}
			state.finish(c, &service.OpenAIForwardResult{UsageReported: tc.reported}, nil)
			require.Equal(t, tc.wantCompleted, store.completed)
			require.Equal(t, tc.wantReviewed, store.reviewed)
			if tc.wantBody {
				require.Equal(t, "response", rec.Body.String())
			} else {
				require.NotContains(t, rec.Body.String(), "response")
			}
		})
	}
}
func TestMarketplaceGatewaySupportedRoutes(t *testing.T) {
	require.True(t, marketplaceSupportedRoute("POST", "/v1/responses"))
	require.True(t, marketplaceSupportedRoute("POST", "/v1/chat/completions"))
	require.False(t, marketplaceSupportedRoute("GET", "/v1/responses"))
	require.False(t, marketplaceSupportedRoute("POST", "/v1/images/generations"))
	require.False(t, marketplaceSupportedRoute("POST", "/v1/responses/compact"))
	require.False(t, marketplaceSupportedRoute("POST", "/backend-api/codex/realtime/calls"))
}

func TestMarketplaceReplayReportsReceiptAndStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		state  string
		status int
		code   string
	}{
		{"budget_reserved", 409, "operation_in_progress"},
		{"dispatched", 409, "operation_in_progress"},
		{"needs_review", 409, "operation_needs_review"},
		{"usage_pending", 409, "operation_needs_review"},
		{"settled", 200, "operation_completed"},
		{"voided", 200, "operation_completed"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			store := &marketplaceTestStore{replay: &service.MarketplaceReceipt{ID: "receipt-123", Status: tc.state}}
			state := &marketplaceRequestState{gateway: service.NewMarketplaceGateway(nil, store, nil)}
			require.False(t, state.reserve(c, &OpenAIGatewayHandler{}))
			require.Equal(t, tc.status, rec.Code)
			require.Contains(t, rec.Body.String(), `"receipt_id":"receipt-123"`)
			require.Contains(t, rec.Body.String(), `"status":"`+tc.state+`"`)
			require.Contains(t, rec.Body.String(), `"code":"`+tc.code+`"`)
		})
	}
}

type replayLookupStore struct {
	marketplaceTestStore
	seen          service.MarketplacePrepareCommand
	lookupReceipt *service.MarketplaceReceipt
	lookupErr     error
}

func (s *replayLookupStore) LookupReplay(_ context.Context, command service.MarketplacePrepareCommand) (*service.MarketplaceReceipt, error) {
	s.seen = command
	return s.lookupReceipt, s.lookupErr
}

type sharedReplayCatalog struct{}

func (sharedReplayCatalog) IsSharedGroup(context.Context, int64) (bool, error) { return true, nil }
func (sharedReplayCatalog) QuoteTemplate(context.Context, int64, string, string) (service.QuoteSnapshot, int64, int64, error) {
	panic("replay must precede quote")
}
func TestMarketplaceReplayLookupBeforeCatalogAndDispatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request.Header.Set("Idempotency-Key", "stable-key")
	group := int64(3)
	key := &service.APIKey{ID: 2, UserID: 1, GroupID: &group}
	store := &replayLookupStore{lookupReceipt: &service.MarketplaceReceipt{ID: "receipt-123", Status: "settled"}}
	h := &OpenAIGatewayHandler{marketplaceGateway: service.NewMarketplaceGateway(sharedReplayCatalog{}, store, nil)}
	_, _, ok := h.marketplaceRequest(c, key, []byte(`{"model":"public","input":"hi"}`), "public", "responses")
	require.False(t, ok)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), `"receipt_id":"receipt-123"`)
	require.EqualValues(t, 1, store.seen.BuyerUserID)
	require.EqualValues(t, 2, store.seen.APIKeyID)
	require.EqualValues(t, 3, store.seen.GroupID)
	require.Equal(t, "stable-key", *store.seen.IdempotencyKey)
	require.Equal(t, service.HashUsageRequestPayload([]byte("responses\x00"+`{"model":"public","input":"hi"}`)), store.seen.RequestDigest)
}

func TestMarketplaceReplayOnlyNeverDispatchesOnLookupMiss(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, receipt := range []*service.MarketplaceReceipt{nil, {ID: "receipt-123", Status: "settled"}} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		c.Request.Header.Set("Idempotency-Key", "stable-key")
		c.Set("marketplace_replay_only", true)
		group := int64(3)
		key := &service.APIKey{ID: 2, UserID: 1, GroupID: &group}
		store := &replayLookupStore{lookupReceipt: receipt}
		h := &OpenAIGatewayHandler{marketplaceGateway: service.NewMarketplaceGateway(nil, store, nil)}
		_, _, ok := h.marketplaceRequest(c, key, []byte(`{"model":"public","input":"hi"}`), "public", "responses")
		require.False(t, ok)
		if receipt == nil {
			require.Equal(t, 409, rec.Code)
		} else {
			require.Equal(t, 200, rec.Code)
		}
		require.Zero(t, store.completed)
	}
}

func TestMarketplaceClientWriteDeadlineFailsClosedWhenUnsupported(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := marketplaceBoundClientWrites(c, ctx)
	require.Error(t, err)
	require.Nil(t, release)
}

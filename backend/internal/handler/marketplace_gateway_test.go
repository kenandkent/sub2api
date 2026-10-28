package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type marketplaceTestStore struct {
	completed, reviewed, voided int
	fail                        bool
}

func (s *marketplaceTestStore) PrepareAndReserve(context.Context, service.MarketplacePrepareCommand, service.Money) (*service.MarketplaceReceipt, bool, error) {
	return nil, false, nil
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

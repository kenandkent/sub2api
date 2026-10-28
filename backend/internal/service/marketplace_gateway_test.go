package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMarketplaceGatewayValidateTextAndEnforceCap(t *testing.T) {
	body := []byte(`{"model":"public","messages":[{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","content":"ok"}],"tools":[{"type":"function","function":{"name":"f"}}]}`)
	actual, err := MarketplaceEnforceTextRequest(body, "chat_completions", 100)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(actual, &decoded))
	require.Equal(t, float64(100), decoded["max_completion_tokens"])
	_, err = MarketplaceEnforceTextRequest([]byte(`{"model":"m","messages":[{"content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`), "chat_completions", 100)
	require.Error(t, err)
	_, err = MarketplaceEnforceTextRequest([]byte(`{"model":"m","tools":[{"type":"web_search_preview"}]}`), "responses", 100)
	require.Error(t, err)
	_, err = MarketplaceEnforceTextRequest([]byte(`{"model":"m","max_output_tokens":101}`), "responses", 100)
	require.Error(t, err)
}
func TestMarketplaceGatewayRejectsUnpricedToolAndTier(t *testing.T) {
	_, err := MarketplaceEnforceTextRequest([]byte(`{"model":"m","tools":[{"type":"mcp","server_label":"x"}]}`), "responses", 100)
	require.Error(t, err)
	_, err = MarketplaceEnforceTextRequest([]byte(`{"model":"m","service_tier":"priority"}`), "responses", 100)
	require.Error(t, err)
}
func TestMarketplaceGatewayOAuthTransformKeepsOutputLimit(t *testing.T) {
	req := map[string]any{"model": "gpt-5.4", "input": "hello", "max_output_tokens": 42}
	applyCodexOAuthTransformWithOptions(req, codexOAuthTransformOptions{PreserveMaxOutputTokens: true})
	require.Equal(t, 42, req["max_output_tokens"])
}
func TestMarketplaceGatewayRejectsUnscopedConversationReferences(t *testing.T) {
	_, err := MarketplaceEnforceTextRequest([]byte(`{"model":"m","conversation":"conv_someone_else"}`), "responses", 100)
	require.Error(t, err)
	_, err = MarketplaceEnforceTextRequest([]byte(`{"model":"m","client_metadata":{"session_id":"unscoped"}}`), "responses", 100)
	require.Error(t, err)
}

func TestMarketplaceOutboundMustMatchQuoteAndCap(t *testing.T) {
	ctx := WithMarketplaceOutputLimit(WithMarketplaceExpectedModel(WithMarketplaceBuyer(context.Background(), 15), "actual"), 100)
	for _, body := range []string{
		`{"model":"wrong","max_output_tokens":100}`,
		`{"model":"actual"}`,
		`{"model":"actual","max_output_tokens":101}`,
		`{"model":"actual","max_output_tokens":0}`,
	} {
		require.Error(t, marketplaceValidateOutbound(ctx, []byte(body)), body)
	}
	require.NoError(t, marketplaceValidateOutbound(ctx, []byte(`{"model":"actual","max_output_tokens":100}`)))
	require.NoError(t, marketplaceValidateOutbound(ctx, []byte(`{"model":"actual","max_completion_tokens":42}`)))
}

func TestMarketplaceRejectsOpaqueReferences(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","input":"hello","previous_response_id":"resp_123"}`,
		`{"model":"m","input":[{"type":"item_reference","id":"msg_123"}]}`,
		`{"model":"m","input":[{"type":"reasoning","encrypted_content":"opaque"}]}`,
		`{"model":"m","input":[{"type":"message","id":"msg_stored","role":"user","content":"hi"}]}`,
	} {
		_, err := MarketplaceEnforceTextRequest([]byte(body), "responses", 100)
		require.Error(t, err, body)
	}
}

func TestMarketplaceExecutionSurvivesClientCancelButRetainsDeadline(t *testing.T) {
	ctx, cancelClient := context.WithCancel(WithMarketplaceBuyer(context.Background(), 1))
	wrapped, cancelExecution := WithMarketplaceExecution(ctx, 0)
	defer cancelExecution()
	detached, release := detachUpstreamContext(wrapped)
	defer release()
	_, hasDeadline := detached.Deadline()
	require.True(t, hasDeadline)
	cancelClient()
	require.NoError(t, detached.Err())
	cancelExecution()
	require.ErrorIs(t, detached.Err(), context.Canceled)
}

func TestMarketplaceDetachedExecutionExpires(t *testing.T) {
	wrapped, cancel := marketplaceWithExecutionTimeout(WithMarketplaceBuyer(context.Background(), 1), 20*time.Millisecond)
	defer cancel()
	detached, release := detachUpstreamContext(wrapped)
	defer release()
	select {
	case <-detached.Done():
		require.ErrorIs(t, detached.Err(), context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("detached upstream ignored execution deadline")
	}
}

func TestMarketplaceRejectsStoredPromptAndUnknownTopLevelFields(t *testing.T) {
	for _, raw := range []string{
		`{"model":"m","prompt":{"id":"pmpt_supplier","variables":{"x":"hello"}}}`,
		`{"model":"m","input":"hello","future_remote_context":{"id":"foreign"}}`,
		`{"model":"m","input":[{"type":"additional_tools","tools":[{"type":"mcp","server_label":"x"}]}]}`,
	} {
		_, err := MarketplaceEnforceTextRequest([]byte(raw), "responses", 100)
		require.Error(t, err, raw)
	}
	legacy, err := MarketplaceEnforceTextRequest([]byte(`{"model":"m","prompt":"hello"}`), "responses", 100)
	require.NoError(t, err)
	normalized, changed, err := normalizeOpenAIResponsesLegacyIngressWithConfig(nil, legacy)
	require.NoError(t, err)
	require.True(t, changed)
	var got map[string]any
	require.NoError(t, json.Unmarshal(normalized, &got))
	require.Equal(t, "hello", got["input"])
}

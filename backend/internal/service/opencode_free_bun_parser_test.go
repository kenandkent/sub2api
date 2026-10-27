package service

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenCodeFreeSSERejectsMixedToolFrameBeforeWriting(t *testing.T) {
	var out bytes.Buffer
	fixture := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"secret\",\"tool_calls\":[{\"id\":\"tool\"}]},\"finish_reason\":null}]}\n\n"
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "")
	require.Error(t, err)
	require.NotContains(t, out.String(), "secret")
	require.NotContains(t, out.String(), "tool")
}

func TestOpenCodeFreeSSERequiresStopDoneAndUsage(t *testing.T) {
	fixture := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"prompt_tokens_details\":{\"cached_tokens\":3},\"billing_source\":\"free\"}}\n\n" +
		"data: [DONE]\n\n"
	var out bytes.Buffer
	result, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "")
	require.NoError(t, err)
	require.Equal(t, "hi", result.Text)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.CacheReadInputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Contains(t, out.String(), "[DONE]")
	for _, bad := range []string{strings.Replace(fixture, "data: [DONE]\n\n", "", 1), strings.Replace(fixture, "\"completion_tokens\":2,", "", 1), strings.Replace(fixture, "\"finish_reason\":\"stop\"", "\"finish_reason\":null", 1)} {
		out.Reset()
		_, err = parseOpenCodeFreeSSE(context.Background(), strings.NewReader(bad), &out, 1<<20, 1<<16, "")
		require.Error(t, err)
		require.NotContains(t, out.String(), "[DONE]")
	}
}

func TestOpenCodeFreeSSERejectsUnexpectedChoicePayloadBeforeWriting(t *testing.T) {
	var out bytes.Buffer
	fixture := `data: {"choices":[{"index":0,"delta":{"content":"visible"},"message":{"tool_calls":[{"id":"hidden"}]},"finish_reason":null}]}` + "\n\n"
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "")
	require.Error(t, err)
	require.Empty(t, out.String())
}

func TestOpenCodeFreeSSEAcceptsCapturedProviderUsage(t *testing.T) {
	// Shape and counts from published-balanced-proof.json; response text is synthetic.
	fixture := `data: {"id":"answer","object":"chat.completion.chunk","created":1793084105,"model":"big-pickle","choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":"ok","reasoning":null,"tool_calls":null}}],"usage":null}` + "\n\n" +
		`data: {"id":"answer","object":"chat.completion.chunk","created":1793084105,"model":"big-pickle","choices":[],"usage":{"prompt_tokens":181,"completion_tokens":35,"total_tokens":216,"prompt_tokens_details":{"cached_tokens":69},"completion_tokens_details":{"reasoning_tokens":22},"cost":0,"opencode":{"billing_source":"free","list_cost":0,"tokens":{"input":112,"output":35,"reasoning":22,"cache_read":69,"cache_write_5m":0,"cache_write_1h":0}}}}` + "\n\n" + "data: [DONE]\n\n"
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), nil, 1<<20, 1<<16, "")
	require.NoError(t, err)
	require.Equal(t, "ok", parsed.Text)
	require.Equal(t, "free", parsed.BillingSource)
	require.Equal(t, 181, parsed.Usage.InputTokens)
	require.Equal(t, 69, parsed.Usage.CacheReadInputTokens)
	require.Equal(t, 35, parsed.Usage.OutputTokens)
}

func TestOpenCodeFreeSSEAcceptsNamedMessageAndID(t *testing.T) {
	fixture := "event: message\nid: 7\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"billing_source\":\"free\"}}\n\ndata: [DONE]\n\n"
	var out bytes.Buffer
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "my-alias")
	require.NoError(t, err)
	require.Equal(t, "hi", parsed.Text)
	require.Contains(t, out.String(), "event: message\n")
	require.Contains(t, out.String(), "id: 7\n")
}

func TestOpenCodeFreeSSERejectsDuplicateDeltaBeforeWriting(t *testing.T) {
	fixture := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"id":"hidden"}],"tool_calls":null,"content":"visible"},"finish_reason":null}]}` + "\n\n"
	var out bytes.Buffer
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "")
	require.Error(t, err)
	require.Empty(t, out.String())
}

func TestOpenCodeFreeSSERejectsConflictingCapturedUsage(t *testing.T) {
	valid := `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":181,"completion_tokens":35,"total_tokens":216,"prompt_tokens_details":{"cached_tokens":69},"completion_tokens_details":{"reasoning_tokens":22},"opencode":{"billing_source":"free","tokens":{"input":112,"output":35,"reasoning":22,"cache_read":69,"cache_write_5m":0,"cache_write_1h":0}}}}` + "\n\n" + "data: [DONE]\n\n"
	for _, bad := range []string{strings.Replace(valid, `"cache_read":69`, `"cache_read":70`, 1), strings.Replace(valid, `"input":112`, `"input":113`, 1), strings.Replace(valid, `"reasoning":22`, `"reasoning":23`, 1), strings.Replace(valid, `"output":35`, `"output":36`, 1)} {
		var out bytes.Buffer
		_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(bad), &out, 1<<20, 1<<16, "")
		require.Error(t, err)
		require.NotContains(t, out.String(), "[DONE]")
	}
}

package service

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenCodeFreeSSERejectsMixedToolFrameBeforeWriting(t *testing.T) {
	var out bytes.Buffer
	fixture := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"secret\",\"tool_calls\":[{\"id\":\"tool\"}]},\"finish_reason\":null}]}\n\n"
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "", nil)
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
	result, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "", nil)
	require.NoError(t, err)
	require.Equal(t, "hi", result.Text)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.CacheReadInputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Contains(t, out.String(), "[DONE]")
	for _, line := range strings.Split(out.String(), "\n") {
		if line == "" {
			continue
		}
		require.True(t, strings.HasPrefix(line, "data: "), "client line missing data prefix: %q", line)
	}
	for _, bad := range []string{strings.Replace(fixture, "data: [DONE]\n\n", "", 1), strings.Replace(fixture, "\"completion_tokens\":2,", "", 1), strings.Replace(fixture, "\"finish_reason\":\"stop\"", "\"finish_reason\":null", 1)} {
		out.Reset()
		_, err = parseOpenCodeFreeSSE(context.Background(), strings.NewReader(bad), &out, 1<<20, 1<<16, "", nil)
		require.Error(t, err)
		require.NotContains(t, out.String(), "[DONE]")
	}
}

func TestOpenCodeFreeSSERejectsUnexpectedChoicePayloadBeforeWriting(t *testing.T) {
	var out bytes.Buffer
	fixture := `data: {"choices":[{"index":0,"delta":{"content":"visible"},"message":{"tool_calls":[{"id":"hidden"}]},"finish_reason":null}]}` + "\n\n"
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "", nil)
	require.Error(t, err)
	require.Empty(t, out.String())
}

func TestOpenCodeFreeSSEAcceptsCapturedProviderUsage(t *testing.T) {
	// Shape and counts from published-balanced-proof.json; response text is synthetic.
	fixture := `data: {"id":"answer","object":"chat.completion.chunk","created":1793084105,"model":"big-pickle","choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":"ok","reasoning":null,"tool_calls":null}}],"usage":null}` + "\n\n" +
		`data: {"id":"answer","object":"chat.completion.chunk","created":1793084105,"model":"big-pickle","choices":[],"usage":{"prompt_tokens":181,"completion_tokens":35,"total_tokens":216,"prompt_tokens_details":{"cached_tokens":69},"completion_tokens_details":{"reasoning_tokens":22},"cost":0,"opencode":{"billing_source":"free","list_cost":0,"tokens":{"input":112,"output":35,"reasoning":22,"cache_read":69,"cache_write_5m":0,"cache_write_1h":0}}}}` + "\n\n" + "data: [DONE]\n\n"
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), nil, 1<<20, 1<<16, "", nil)
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
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "my-alias", nil)
	require.NoError(t, err)
	require.Equal(t, "hi", parsed.Text)
	require.Contains(t, out.String(), "event: message\n")
	require.Contains(t, out.String(), "id: 7\n")
}

func TestOpenCodeFreeSSERejectsDuplicateDeltaBeforeWriting(t *testing.T) {
	fixture := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"id":"hidden"}],"tool_calls":null,"content":"visible"},"finish_reason":null}]}` + "\n\n"
	var out bytes.Buffer
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "", nil)
	require.Error(t, err)
	require.Empty(t, out.String())
}

func TestOpenCodeFreeSSERejectsConflictingCapturedUsage(t *testing.T) {
	valid := `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":181,"completion_tokens":35,"total_tokens":216,"prompt_tokens_details":{"cached_tokens":69},"completion_tokens_details":{"reasoning_tokens":22},"opencode":{"billing_source":"free","tokens":{"input":112,"output":35,"reasoning":22,"cache_read":69,"cache_write_5m":0,"cache_write_1h":0}}}}` + "\n\n" + "data: [DONE]\n\n"
	for _, bad := range []string{strings.Replace(valid, `"cache_read":69`, `"cache_read":70`, 1), strings.Replace(valid, `"input":112`, `"input":113`, 1), strings.Replace(valid, `"reasoning":22`, `"reasoning":23`, 1), strings.Replace(valid, `"output":35`, `"output":36`, 1)} {
		var out bytes.Buffer
		_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(bad), &out, 1<<20, 1<<16, "", nil)
		require.Error(t, err)
		require.NotContains(t, out.String(), "[DONE]")
	}
}

func openCodeFreeAgentFixture() string {
	return "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_\",\"type\":\"function\",\"function\":{\"name\":\"look\",\"arguments\":\"{\\\"q\\\"\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"up\",\"function\":{\"name\":\"up\",\"arguments\":\":1}\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"total_tokens\":6,\"billing_source\":\"free\",\"opencode\":{\"billing_source\":\"free\",\"tokens\":{\"input\":5,\"output\":1,\"reasoning\":0,\"cache_read\":0,\"cache_write_5m\":0,\"cache_write_1h\":0}}}}\n\n" +
		"data: [DONE]\n\n"
}

func TestOpenCodeFreeSSEWithholdsToolsUntilFinalEvents(t *testing.T) {
	opts := &openCodeFreeSSEOptions{ToolNames: []string{"lookup"}, ToolChoice: openCodeFreeToolChoice{Kind: "auto"}}
	var out bytes.Buffer
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(openCodeFreeAgentFixture()), &out, 1<<20, 1<<16, "alias", opts)
	require.NoError(t, err)
	require.Len(t, parsed.ToolCalls, 1)
	require.Equal(t, "call_up", parsed.ToolCalls[0].ID)
	require.Equal(t, "lookup", parsed.ToolCalls[0].Function.Name)
	require.Equal(t, `{"q":1}`, parsed.ToolCalls[0].Function.Arguments)
	require.Equal(t, "tool_calls", parsed.Finish)
	stream := out.String()
	require.NotContains(t, stream, "tool_calls\")],")
	require.Contains(t, stream, `"tool_calls":[{"id":"call_up","type":"function","function":{"name":"lookup","arguments":"{\"q\":1}"}}]`)
	require.Contains(t, stream, `"finish_reason":"tool_calls"`)
	require.Contains(t, stream, "[DONE]")
	for _, line := range strings.Split(stream, "\n") {
		if line == "" {
			continue
		}
		require.True(t, strings.HasPrefix(line, "data: "), "client line missing data prefix: %q", line)
	}
}

func TestOpenCodeFreeSSERejectsUndeclaredToolStreamBeforeWriting(t *testing.T) {
	opts := &openCodeFreeSSEOptions{ToolNames: []string{"lookup"}, ToolChoice: openCodeFreeToolChoice{Kind: "auto"}}
	fixture := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\",\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"type\":\"function\",\"function\":{\"name\":\"secret\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n"
	var out bytes.Buffer
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "", opts)
	require.Error(t, err)
	require.NotContains(t, out.String(), "secret")
	require.NotContains(t, out.String(), "partial")
}

func TestOpenCodeFreeSSERejectsToolChoiceNoneWithTools(t *testing.T) {
	fixture := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n"
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), nil, 1<<20, 1<<16, "", nil)
	require.Error(t, err)
}

func TestOpenCodeFreeSSERejectsFinishToolCallsWithoutCalls(t *testing.T) {
	fixture := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"billing_source\":\"free\"}}\n\n" + "data: [DONE]\n\n"
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), nil, 1<<20, 1<<16, "", nil)
	require.Error(t, err)
}

func TestOpenCodeFreeSSERejectsRequiredChoiceWithoutCalls(t *testing.T) {
	opts := &openCodeFreeSSEOptions{ToolNames: []string{"lookup"}, ToolChoice: openCodeFreeToolChoice{Kind: "required"}}
	fixture := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"billing_source\":\"free\"}}\n\n" + "data: [DONE]\n\n"
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), nil, 1<<20, 1<<16, "", opts)
	require.Error(t, err)
}

func TestOpenCodeFreeSSERejectsForcedToolMismatch(t *testing.T) {
	opts := &openCodeFreeSSEOptions{ToolNames: []string{"lookup", "other"}, ToolChoice: openCodeFreeToolChoice{Kind: "function", Name: "lookup"}}
	fixture := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"type\":\"function\",\"function\":{\"name\":\"other\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"billing_source\":\"free\"}}\n\n" + "data: [DONE]\n\n"
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), nil, 1<<20, 1<<16, "", opts)
	require.Error(t, err)
}

func TestOpenCodeFreeSSEAcceptsCompleteToolCallTerminalFrame(t *testing.T) {
	opts := &openCodeFreeSSEOptions{ToolNames: []string{"lookup"}, ToolChoice: openCodeFreeToolChoice{Kind: "auto"}}
	fixture := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"billing_source\":\"free\"}}\n\n" + "data: [DONE]\n\n"
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), nil, 1<<20, 1<<16, "", opts)
	require.NoError(t, err)
	require.Len(t, parsed.ToolCalls, 1)
	require.Equal(t, "call_1", parsed.ToolCalls[0].ID)
}

func TestOpenCodeFreeSSEAcceptsRepeatedFinishWithUsageOnlyFrame(t *testing.T) {
	fixture := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"billing_source\":\"free\"}}\n\n" +
		"data: [DONE]\n\n"
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), nil, 1<<20, 1<<16, "", nil)
	require.NoError(t, err)
	require.Equal(t, "hi", parsed.Text)
	require.Equal(t, "free", parsed.BillingSource)
}

func TestOpenCodeFreeSSEAcceptsNullRoleDeltas(t *testing.T) {
	fixture := "data: {\"id\":\"chatcmpl-a\",\"object\":\"chat.completion.chunk\",\"created\":1793348711,\"model\":\"deepseek-v4.1-flash-free\",\"choices\":[{\"index\":0,\"finish_reason\":null,\"logprobs\":null,\"delta\":{\"role\":null,\"content\":null,\"reasoning_content\":null,\"tool_calls\":null}}],\"usage\":null}\n\n" +
		"data: {\"id\":\"chatcmpl-a\",\"object\":\"chat.completion.chunk\",\"created\":1793348711,\"model\":\"deepseek-v4.1-flash-free\",\"choices\":[{\"index\":0,\"finish_reason\":null,\"logprobs\":null,\"delta\":{\"role\":null,\"content\":\"Hello\"}}],\"usage\":null}\n\n" +
		"data: {\"id\":\"chatcmpl-a\",\"object\":\"chat.completion.chunk\",\"created\":1793348711,\"model\":\"deepseek-v4.1-flash-free\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"logprobs\":null,\"delta\":{\"role\":null,\"content\":\" OK\",\"reasoning_content\":null,\"tool_calls\":null}}],\"usage\":null}\n\n" +
		"data: {\"id\":\"chatcmpl-a\",\"object\":\"chat.completion.chunk\",\"created\":1793348711,\"model\":\"deepseek-v4.1-flash-free\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5,\"billing_source\":\"free\"}}\n\n" +
		"data: [DONE]\n\n"
	var out bytes.Buffer
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "", nil)
	require.NoError(t, err)
	require.Equal(t, "Hello OK", parsed.Text)
	require.Equal(t, "stop", parsed.Finish)
	require.Equal(t, "free", parsed.BillingSource)
	require.Contains(t, out.String(), "Hello")
	require.Contains(t, out.String(), " OK")
}

func openCodeFreeUsageTail() string {
	return "data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"billing_source\":\"free\"}}\n\n" +
		"data: [DONE]\n\n"
}

// Mixed stream: the worker injected placeholder `read` at index 0 (name split
// across frames) and the model called the caller-declared `lookup` at index 1.
func openCodeFreeInjectedMixedFixture() string {
	return "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_r\",\"type\":\"function\",\"function\":{\"name\":\"rea\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"d\",\"arguments\":\"{\\\"p\\\":1}\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call_l\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		openCodeFreeUsageTail()
}

func openCodeFreeInjectedOnlyFixture() string {
	return "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_r\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		openCodeFreeUsageTail()
}

func openCodeFreeInjectedOpts(choice openCodeFreeToolChoice) *openCodeFreeSSEOptions {
	return &openCodeFreeSSEOptions{ToolNames: []string{"lookup"}, ToolChoice: choice, InjectedTools: []string{"read"}}
}

func TestOpenCodeFreeSSEStripsInjectedPlaceholderAndKeepsClientCall(t *testing.T) {
	opts := openCodeFreeInjectedOpts(openCodeFreeToolChoice{Kind: "auto"})
	var out bytes.Buffer
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(openCodeFreeInjectedMixedFixture()), &out, 1<<20, 1<<16, "alias", opts)
	require.NoError(t, err)
	require.Equal(t, "tool_calls", parsed.Finish)
	require.Len(t, parsed.ToolCalls, 1)
	require.Equal(t, "call_l", parsed.ToolCalls[0].ID)
	require.Equal(t, "lookup", parsed.ToolCalls[0].Function.Name)
	stream := out.String()
	require.Contains(t, stream, `"tool_calls":[{"id":"call_l","type":"function","function":{"name":"lookup","arguments":"{}"}}]`)
	require.Contains(t, stream, `"finish_reason":"tool_calls"`)
	require.NotContains(t, stream, "call_r")
	require.NotContains(t, stream, `"read"`)
	require.Contains(t, stream, "[DONE]")
}

func TestOpenCodeFreeSSEInjectedOnlyCallDegradesToStop(t *testing.T) {
	opts := openCodeFreeInjectedOpts(openCodeFreeToolChoice{Kind: "auto"})
	var out bytes.Buffer
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(openCodeFreeInjectedOnlyFixture()), &out, 1<<20, 1<<16, "alias", opts)
	require.NoError(t, err)
	require.Equal(t, "stop", parsed.Finish)
	require.Empty(t, parsed.ToolCalls)
	stream := out.String()
	require.Contains(t, stream, `"finish_reason":"stop"`)
	require.NotContains(t, stream, `"finish_reason":"tool_calls"`)
	require.NotContains(t, stream, `"tool_calls"`)
	require.NotContains(t, stream, "call_r")
	require.Contains(t, stream, "[DONE]")
}

func TestOpenCodeFreeSSECompleteTerminalFrameStripsInjectedPlaceholder(t *testing.T) {
	opts := openCodeFreeInjectedOpts(openCodeFreeToolChoice{Kind: "auto"})
	fixture := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"id\":\"call_r\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{}\"}},{\"id\":\"call_l\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		openCodeFreeUsageTail()
	var out bytes.Buffer
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "alias", opts)
	require.NoError(t, err)
	require.Equal(t, "tool_calls", parsed.Finish)
	require.Len(t, parsed.ToolCalls, 1)
	require.Equal(t, "call_l", parsed.ToolCalls[0].ID)
	require.NotContains(t, out.String(), "call_r")
}

func TestOpenCodeFreeSSERejectsInjectedNameWithoutInjectionFlag(t *testing.T) {
	opts := &openCodeFreeSSEOptions{ToolNames: []string{"lookup"}, ToolChoice: openCodeFreeToolChoice{Kind: "auto"}}
	fixture := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\",\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n"
	var out bytes.Buffer
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "", opts)
	require.Error(t, err)
	require.NotContains(t, out.String(), "partial")
	require.NotContains(t, out.String(), "call")
}

func TestOpenCodeFreeSSERejectsInjectedCallWhenToolChoiceNone(t *testing.T) {
	opts := openCodeFreeInjectedOpts(openCodeFreeToolChoice{Kind: "none"})
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(openCodeFreeInjectedOnlyFixture()), nil, 1<<20, 1<<16, "", opts)
	require.Error(t, err)
}

func TestOpenCodeFreeSSEForcedChoiceStripsExtraInjectedCall(t *testing.T) {
	opts := &openCodeFreeSSEOptions{ToolNames: []string{"lookup", "other"}, ToolChoice: openCodeFreeToolChoice{Kind: "function", Name: "lookup"}, InjectedTools: []string{"read"}}
	var out bytes.Buffer
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(openCodeFreeInjectedMixedFixture()), &out, 1<<20, 1<<16, "", opts)
	require.NoError(t, err)
	require.Equal(t, "tool_calls", parsed.Finish)
	require.Len(t, parsed.ToolCalls, 1)
	require.Equal(t, "lookup", parsed.ToolCalls[0].Function.Name)
	require.NotContains(t, out.String(), "call_r")
}

func TestOpenCodeFreeSSERejectsInjectedOnlyCallUnderRequiredChoice(t *testing.T) {
	opts := openCodeFreeInjectedOpts(openCodeFreeToolChoice{Kind: "required"})
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(openCodeFreeInjectedOnlyFixture()), nil, 1<<20, 1<<16, "", opts)
	require.Error(t, err)
}

func TestOpenCodeFreeSSEInjectedOnlyCallAcceptsRepeatedUpstreamFinish(t *testing.T) {
	opts := openCodeFreeInjectedOpts(openCodeFreeToolChoice{Kind: "auto"})
	fixture := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_r\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"billing_source\":\"free\"}}\n\n" +
		"data: [DONE]\n\n"
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), nil, 1<<20, 1<<16, "", opts)
	require.NoError(t, err)
	require.Equal(t, "stop", parsed.Finish)
	require.Empty(t, parsed.ToolCalls)
	require.Equal(t, "free", parsed.BillingSource)
}

func TestOpenCodeFreeSSEValidatorRejectsInjectedToolMisuse(t *testing.T) {
	_, err := newOpenCodeFreeSSEValidator("", &openCodeFreeSSEOptions{ToolNames: []string{"read"}, InjectedTools: []string{"read"}})
	require.Error(t, err)
	_, err = newOpenCodeFreeSSEValidator("", &openCodeFreeSSEOptions{InjectedTools: []string{"read", "read"}})
	require.Error(t, err)
	_, err = newOpenCodeFreeSSEValidator("", &openCodeFreeSSEOptions{InjectedTools: []string{"bad name"}})
	require.Error(t, err)
}

// Shape of the production 2026-11-03 capture (inst18/mimo-v2.6-flash-free):
// reasoning_content, finish=stop, free usage and [DONE] — no answer at all.
func openCodeFreeEmptyAnswerFixture() string {
	return "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\",\"reasoning_content\":null},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":null,\"content\":null,\"reasoning_content\":\"Simple question\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":null,\"content\":null,\"reasoning_content\":\".\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":133,\"completion_tokens\":6,\"total_tokens\":139,\"billing_source\":\"free\",\"opencode\":{\"billing_source\":\"free\",\"list_cost\":0,\"tokens\":{\"input\":5,\"output\":6,\"reasoning\":4,\"cache_read\":128,\"cache_write_5m\":0,\"cache_write_1h\":0}}}}\n\n" +
		"data: [DONE]\n\n"
}

func openCodeFreeEmptyAnswerWithContentFixture() string {
	return "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\",\"reasoning_content\":null},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":null,\"content\":null,\"reasoning_content\":\"Simple question\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":133,\"completion_tokens\":6,\"total_tokens\":139,\"billing_source\":\"free\"}}\n\n" +
		"data: [DONE]\n\n"
}

// While the writer is still untouched, an answer-less stream must stay
// unwritten so the caller can still fail over to another instance.
func TestOpenCodeFreeSSEHoldUntilContentKeepsEmptyAnswerUnwritten(t *testing.T) {
	var out bytes.Buffer
	opts := &openCodeFreeSSEOptions{HoldUntilContent: true, ToolChoice: openCodeFreeToolChoice{Kind: "auto"}}
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(openCodeFreeEmptyAnswerFixture()), &out, 1<<20, 1<<16, "alias", opts)
	require.NoError(t, err)
	require.Equal(t, "", parsed.Text)
	require.Empty(t, parsed.ToolCalls)
	require.Equal(t, "stop", parsed.Finish)
	require.Equal(t, "free", parsed.BillingSource)
	require.Contains(t, parsed.Reasoning, "Simple question")
	require.Zero(t, out.Len(), "an empty answer must not commit the response writer")
}

// Holding must be transparent once visible content arrives: the buffered
// reasoning stays ahead of the content frame and the terminal frames follow.
func TestOpenCodeFreeSSEHoldUntilContentReleasesOnVisibleContent(t *testing.T) {
	var out bytes.Buffer
	opts := &openCodeFreeSSEOptions{HoldUntilContent: true, ToolChoice: openCodeFreeToolChoice{Kind: "auto"}}
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(openCodeFreeEmptyAnswerWithContentFixture()), &out, 1<<20, 1<<16, "alias", opts)
	require.NoError(t, err)
	require.Equal(t, "hello", parsed.Text)
	stream := out.String()
	require.Contains(t, stream, "Simple question")
	require.Contains(t, stream, "hello")
	require.Contains(t, stream, "[DONE]")
	require.Less(t, strings.Index(stream, "Simple question"), strings.Index(stream, "hello"), "held reasoning must be flushed before the content frame")
}

// An answer-less stream must not commit the writer even when its reasoning
// exceeds the bounded hold buffer. Failing it preserves the caller's failover.
func TestOpenCodeFreeSSEHoldUntilContentRejectsOversizedStreamBeforeWriting(t *testing.T) {
	reasoning := strings.Repeat("x", openCodeFreeHoldBackMaxBytes+1024)
	fixture := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\",\"reasoning_content\":\"" + reasoning + "\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":133,\"completion_tokens\":6,\"billing_source\":\"free\"}}\n\n" +
		"data: [DONE]\n\n"
	var out bytes.Buffer
	opts := &openCodeFreeSSEOptions{HoldUntilContent: true, ToolChoice: openCodeFreeToolChoice{Kind: "auto"}}
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<24, 1<<20, "alias", opts)
	require.Error(t, err)
	require.Zero(t, out.Len(), "an oversized reasoning-only stream must remain safe to fail over")
}

func TestOpenCodeFreeSSEHoldUntilContentKeepsWhitespaceOnlyAnswerUnwritten(t *testing.T) {
	fixture := `data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":" \t\r\n\u2003"},"finish_reason":"stop"}]}` + "\n\n" + openCodeFreeUsageTail()
	var out bytes.Buffer
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "alias", &openCodeFreeSSEOptions{HoldUntilContent: true, ToolChoice: openCodeFreeToolChoice{Kind: "none"}})
	require.NoError(t, err)
	require.Equal(t, " \t\r\n\u2003", parsed.Text, "classification must not alter the upstream text")
	require.Zero(t, out.Len(), "whitespace alone is not a visible answer")
}

func TestOpenCodeFreeSSEHoldUntilContentKeepsInvalidToolAnswerUnwritten(t *testing.T) {
	opts := &openCodeFreeSSEOptions{HoldUntilContent: true, ToolNames: []string{"lookup"}, ToolChoice: openCodeFreeToolChoice{Kind: "auto"}}
	for _, tc := range []struct {
		name    string
		fixture string
	}{
		{"truncated arguments", strings.Replace(openCodeFreeAgentFixture(), `"arguments":":1}"`, `"arguments":":1"`, 1)},
		{"missing done", strings.TrimSuffix(openCodeFreeAgentFixture(), "data: [DONE]\n\n")},
		{"paid usage", strings.ReplaceAll(openCodeFreeAgentFixture(), `"billing_source":"free"`, `"billing_source":"paid"`)},
		{"event after done", openCodeFreeAgentFixture() + "data: {}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(tc.fixture), &out, 1<<20, 1<<16, "alias", opts)
			require.Error(t, err)
			require.Zero(t, out.Len(), "tool fragments are withheld, so they must not commit the response before the whole answer validates")
		})
	}
}

func TestOpenCodeFreeSSEHoldUntilContentKeepsInjectedOnlyAnswerUnwritten(t *testing.T) {
	opts := openCodeFreeInjectedOpts(openCodeFreeToolChoice{Kind: "auto"})
	opts.HoldUntilContent = true
	var out bytes.Buffer
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(openCodeFreeInjectedOnlyFixture()), &out, 1<<20, 1<<16, "alias", opts)
	require.NoError(t, err)
	require.Empty(t, parsed.ToolCalls)
	require.Zero(t, out.Len(), "placeholder tool calls carry no client-visible answer")
}

func TestOpenCodeFreeSSEExpectedModelRejectsWrongModelOnAnyEvent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture string
	}{
		{"answer model", `data: {"model":"wrong","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n" + openCodeFreeUsageTail()},
		{"usage model", `data: {"model":"big-pickle","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n" + strings.Replace(openCodeFreeUsageTail(), `"id":"x"`, `"id":"x","model":"wrong"`, 1)},
		{"null model", `data: {"model":null,"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n" + openCodeFreeUsageTail()},
		{"non-string model", `data: {"model":7,"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n" + openCodeFreeUsageTail()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(tc.fixture), nil, 1<<20, 1<<16, "client-alias", &openCodeFreeSSEOptions{ExpectedModel: "big-pickle", ToolChoice: openCodeFreeToolChoice{Kind: "none"}})
			require.Error(t, err)
		})
	}
}

func TestOpenCodeFreeSSERejectsInvalidCompleteTerminalToolCallsBeforeWriting(t *testing.T) {
	tooMany := make([]string, 129)
	for index := range tooMany {
		tooMany[index] = `{"id":"call_` + strconv.Itoa(index) + `","type":"function","function":{"name":"lookup","arguments":"{}"}}`
	}
	for _, tc := range []struct {
		name  string
		calls string
	}{
		{"unsupported call field", `[{"id":"call_1","type":"function","extra":true,"function":{"name":"lookup","arguments":"{}"}}]`},
		{"unsupported function field", `[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}","extra":true}}]`},
		{"arguments too large", `[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"\"` + strings.Repeat("x", 256*1024) + `\""}}]`},
		{"too many calls", "[" + strings.Join(tooMany, ",") + "]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := `data: {"choices":[{"index":0,"delta":{"tool_calls":` + tc.calls + `},"finish_reason":"tool_calls"}]}` + "\n\n" + openCodeFreeUsageTail()
			var out bytes.Buffer
			_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<20, "alias", &openCodeFreeSSEOptions{HoldUntilContent: true, ToolNames: []string{"lookup"}, ToolChoice: openCodeFreeToolChoice{Kind: "auto"}})
			require.Error(t, err)
			require.Zero(t, out.Len())
		})
	}
}

func TestOpenCodeFreeSSEHoldUntilContentPreservesReasoningAndToolArguments(t *testing.T) {
	fixture := `data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":" \n","reasoning_content":"  Keep\n\tunicode 雪  "},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_exact","type":"function","function":{"name":"lookup","arguments":" { \"q\" : \"雪\" } "}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" + openCodeFreeUsageTail()
	var out bytes.Buffer
	opts := &openCodeFreeSSEOptions{HoldUntilContent: true, ToolNames: []string{"lookup"}, ToolChoice: openCodeFreeToolChoice{Kind: "auto"}}
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), &out, 1<<20, 1<<16, "alias", opts)
	require.NoError(t, err)
	require.Equal(t, " \n", parsed.Text)
	require.Equal(t, "  Keep\n\tunicode 雪  ", parsed.Reasoning)
	require.Len(t, parsed.ToolCalls, 1)
	require.Equal(t, "call_exact", parsed.ToolCalls[0].ID)
	require.Equal(t, "lookup", parsed.ToolCalls[0].Function.Name)
	require.Equal(t, ` { "q" : "雪" } `, parsed.ToolCalls[0].Function.Arguments)
	stream := out.String()
	require.Contains(t, stream, `"reasoning_content":"  Keep\n\tunicode 雪  "`)
	require.Contains(t, stream, `"arguments":" { \"q\" : \"雪\" } "`)
	require.Less(t, strings.Index(stream, "Keep"), strings.Index(stream, "call_exact"))
	require.Less(t, strings.Index(stream, "call_exact"), strings.Index(stream, "[DONE]"))
}

func TestOpenCodeFreeSSEExpectedModelPreservesOptionalModelAndAliasBehavior(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fixture  string
		expected string
	}{
		{"omitted upstream model", strings.ReplaceAll(openCodeFreeEmptyAnswerWithContentFixture(), `"id":"x",`, ""), "big-pickle"},
		{"matching upstream model", strings.Replace(openCodeFreeEmptyAnswerWithContentFixture(), `"id":"x",`, `"id":"x","model":"big-pickle",`, 1), "big-pickle"},
		{"unset expectation", strings.Replace(openCodeFreeEmptyAnswerWithContentFixture(), `"id":"x",`, `"id":"x","model":"provider-model",`, 1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(tc.fixture), &out, 1<<20, 1<<16, "client-alias", &openCodeFreeSSEOptions{ExpectedModel: tc.expected, ToolChoice: openCodeFreeToolChoice{Kind: "none"}})
			require.NoError(t, err)
			require.Equal(t, "hello", parsed.Text)
			require.Contains(t, out.String(), `"model":"client-alias"`)
			require.Contains(t, out.String(), "[DONE]")
		})
	}
}

func TestOpenCodeFreeSSERejectsRawByteOverflowWithCRLF(t *testing.T) {
	fixture := strings.ReplaceAll(openCodeFreeEmptyAnswerWithContentFixture(), "\n", "\r\n")
	// Cutting the last CRLF short used to look like a clean EOF because
	// Scanner strips CR from every line and the parser counted only LF.
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture+"data: invalid\r\n\r\n"), nil, int64(len(fixture)-1), 1<<16, "alias", nil)
	require.Error(t, err, "the response-byte cap must not turn a hidden invalid tail into success")
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), nil, int64(len(fixture)), 1<<16, "alias", nil)
	require.NoError(t, err, "a complete stream exactly at the raw-byte limit remains valid")
	require.Equal(t, "hello", parsed.Text)
}

func TestOpenCodeFreeSSERejectsRawEventOverflowWithCRLF(t *testing.T) {
	// Multiple short lines fit Scanner's per-line limit while their raw CRLF
	// bytes together exceed the configured SSE event size.
	event := "event: message\r\nid: 12345678\r\ndata: " + `{"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}` + "\r\n"
	fixture := event + "\r\n" + strings.ReplaceAll(openCodeFreeUsageTail(), "\n", "\r\n")
	_, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), nil, 1<<20, len(event)-1, "alias", nil)
	require.Error(t, err, "CR bytes count toward the event limit")
	parsed, err := parseOpenCodeFreeSSE(context.Background(), strings.NewReader(fixture), nil, 1<<20, len(event), "alias", nil)
	require.NoError(t, err, "the raw event exactly at its limit remains valid")
	require.Equal(t, "hi", parsed.Text)
}

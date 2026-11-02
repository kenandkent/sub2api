package service

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOpenCodeFreeRequestCapabilities(t *testing.T) {
	for _, body := range []string{
		`{"model":"alias","messages":[{"role":"user","content":"hello"}],"temperature":0,"tools":[],"tool_choice":"none","n":1}`,
		`{"model":"alias","messages":[{"role":"system","content":"rules"},{"role":"assistant","content":"ok"},{"role":"user","content":"next"}]}`,
	} {
		req, err := normalizeOpenCodeFreeRequest([]byte(body), "big-pickle")
		require.NoError(t, err, body)
		require.Equal(t, openCodeFreeModeText, req.Mode, body)
		var got map[string]any
		require.NoError(t, json.Unmarshal(req.Body, &got))
		require.Equal(t, "big-pickle", got["model"])
		require.Empty(t, req.ToolNames)
		require.Empty(t, req.InjectedTools, body)
	}
	contentParts := `{"model":"alias","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	req, err := normalizeOpenCodeFreeRequest([]byte(contentParts), "big-pickle")
	require.NoError(t, err)
	require.Equal(t, openCodeFreeModeAgent, req.Mode)
	require.Empty(t, req.InjectedTools)
	for _, body := range []string{
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"temperature":0.1}`,
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{}]}`,
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"n":2}`,
		`{"model":"alias","messages":[{"role":"tool","content":"x"}]}`,
		`{"model":"alias","messages":[{"role":"user","content":[{"type":"image_url"}]}]}`,
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"functions":[]}`,
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"unknown":true}`,
	} {
		_, err = normalizeOpenCodeFreeRequest([]byte(body), "big-pickle")
		require.Error(t, err, body)
	}
}

func TestOpenCodeFreeRequestAgentMode(t *testing.T) {
	toolBody := `{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"lookup","description":"d","parameters":{"type":"object"}}}]}`
	req, err := normalizeOpenCodeFreeRequest([]byte(toolBody), "big-pickle")
	require.NoError(t, err)
	require.Equal(t, openCodeFreeModeAgent, req.Mode)
	require.Equal(t, []string{"lookup"}, req.ToolNames)
	require.Equal(t, openCodeFreeToolChoice{Kind: "auto"}, req.ToolChoice)
	require.Equal(t, []string{"read"}, req.InjectedTools)

	readDeclared := `{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"read"}}]}`
	req, err = normalizeOpenCodeFreeRequest([]byte(readDeclared), "big-pickle")
	require.NoError(t, err)
	require.Equal(t, []string{"read"}, req.ToolNames)
	require.Empty(t, req.InjectedTools)

	forced := `{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":{"type":"function","function":{"name":"lookup"}}}`
	req, err = normalizeOpenCodeFreeRequest([]byte(forced), "big-pickle")
	require.NoError(t, err)
	require.Equal(t, openCodeFreeToolChoice{Kind: "function", Name: "lookup"}, req.ToolChoice)

	history := `{"model":"alias","messages":[{"role":"user","content":"x"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"result"}],"tools":[{"type":"function","function":{"name":"lookup"}}],"parallel_tool_calls":false}`
	req, err = normalizeOpenCodeFreeRequest([]byte(history), "big-pickle")
	require.NoError(t, err)
	require.Equal(t, openCodeFreeModeAgent, req.Mode)

	for _, body := range []string{
		// unmatched tool result
		`{"model":"alias","messages":[{"role":"user","content":"x"},{"role":"tool","tool_call_id":"call_9","content":"r"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`,
		// unfinished tool calls before the next non-tool message
		`{"model":"alias","messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"a","arguments":"{}"}}]},{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"a"}}]}`,
		// duplicate tool call id
		`{"model":"alias","messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"a","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"r"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"a","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"r"}],"tools":[{"type":"function","function":{"name":"a"}}]}`,
		// forced tool_choice not declared
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"a"}}],"tool_choice":{"type":"function","function":{"name":"b"}}}`,
		// required choice without tools
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"tool_choice":"required"}`,
		// empty assistant message
		`{"model":"alias","messages":[{"role":"assistant"}],"tools":[{"type":"function","function":{"name":"a"}}]}`,
		// invalid message role
		`{"model":"alias","messages":[{"role":"nope","content":"x"}],"tools":[{"type":"function","function":{"name":"a"}}]}`,
		// parameters must be an absent or object-valued JSON Schema (chatSchema record)
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"a","parameters":null}}]}`,
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"a","parameters":[]}}]}`,
		// description must be a string when present (chatSchema has no nullable)
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"a","description":null}}]}`,
	} {
		_, err = normalizeOpenCodeFreeRequest([]byte(body), "big-pickle")
		require.Error(t, err, body)
	}
}

func TestOpenCodeFreeRequestStripsSessionStatus(t *testing.T) {
	toolNames := func(req *openCodeFreeRequest) []string {
		var got struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		require.NoError(t, json.Unmarshal(req.Body, &got))
		names := make([]string, 0, len(got.Tools))
		for _, tool := range got.Tools {
			names = append(names, tool.Function.Name)
		}
		return names
	}

	// The upstream rejects declared sets that pair `session_status` with other
	// caller tools, so it is dropped from the dispatched body and from the
	// caller-facing contract in the same step.
	body := `{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"gateway"}},{"type":"function","function":{"name":"session_status"}}],"tool_choice":"auto"}`
	req, err := normalizeOpenCodeFreeRequest([]byte(body), "big-pickle")
	require.NoError(t, err)
	require.Equal(t, openCodeFreeModeAgent, req.Mode)
	require.Equal(t, []string{"read", "gateway"}, req.ToolNames)
	require.Equal(t, openCodeFreeToolChoice{Kind: "auto"}, req.ToolChoice)
	require.Empty(t, req.InjectedTools)
	require.Equal(t, []string{"read", "gateway"}, toolNames(req))

	// A forced tool_choice that targets the stripped tool falls back to auto
	// because the declared set no longer contains it.
	forced := `{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"session_status"}}],"tool_choice":{"type":"function","function":{"name":"session_status"}}}`
	req, err = normalizeOpenCodeFreeRequest([]byte(forced), "big-pickle")
	require.NoError(t, err)
	require.Equal(t, []string{"read"}, req.ToolNames)
	require.Equal(t, openCodeFreeToolChoice{Kind: "auto"}, req.ToolChoice)
	require.Equal(t, []string{"read"}, toolNames(req))

	// Declaring only the stripped tool collapses the set: the caller keeps a
	// valid agent body with an empty tool list pinned to tool_choice none.
	only := `{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"session_status"}}]}`
	req, err = normalizeOpenCodeFreeRequest([]byte(only), "big-pickle")
	require.NoError(t, err)
	require.Equal(t, openCodeFreeModeAgent, req.Mode)
	require.Empty(t, req.ToolNames)
	require.Equal(t, openCodeFreeToolChoice{Kind: "none"}, req.ToolChoice)
	require.Empty(t, toolNames(req))

	// Tool sets without the stripped tool are dispatched unchanged.
	unchanged := `{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"gateway"}}]}`
	req, err = normalizeOpenCodeFreeRequest([]byte(unchanged), "big-pickle")
	require.NoError(t, err)
	require.Equal(t, []string{"read", "gateway"}, req.ToolNames)
	require.Equal(t, []string{"read", "gateway"}, toolNames(req))
}

func TestOpenCodeFreeRequestAcceptsStoreFlag(t *testing.T) {
	// DeepSeek Harness (pi-ai) sends OpenAI's eval-store flag alongside
	// max_completion_tokens and stream_options on plain text turns.
	harness := `{"model":"alias","messages":[{"role":"system","content":"rules"},{"role":"user","content":"hi"}],"max_completion_tokens":4096,"stream":true,"stream_options":{"include_usage":true},"store":false}`
	req, err := normalizeOpenCodeFreeRequest([]byte(harness), "big-pickle")
	require.NoError(t, err)
	require.Equal(t, openCodeFreeModeText, req.Mode)
	var got map[string]any
	require.NoError(t, json.Unmarshal(req.Body, &got))
	_, present := got["store"]
	require.False(t, present, "store must be stripped before dispatch")
	require.Equal(t, float64(4096), got["max_completion_tokens"])

	agent := `{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"read"}}],"store":true}`
	req, err = normalizeOpenCodeFreeRequest([]byte(agent), "big-pickle")
	require.NoError(t, err)
	require.Equal(t, openCodeFreeModeAgent, req.Mode)
	require.NoError(t, json.Unmarshal(req.Body, &got))
	_, present = got["store"]
	require.False(t, present)

	for _, body := range []string{
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"store":"yes"}`,
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"store":1}`,
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"store":null}`,
	} {
		_, err = normalizeOpenCodeFreeRequest([]byte(body), "big-pickle")
		require.Error(t, err, body)
	}
}

func TestOpenCodeFreeRequestStripsSubagentFork(t *testing.T) {
	// DeepSeek Harness declares `subagent_fork` in its 25-tool agent set;
	// live ablation pinned that name alone as the FreeUsageLimitError trigger
	// (`[subagent]` alone succeeds, `[subagent_fork]` alone fails).
	body := `{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"subagent"}},{"type":"function","function":{"name":"subagent_fork"}}],"tool_choice":"auto"}`
	req, err := normalizeOpenCodeFreeRequest([]byte(body), "big-pickle")
	require.NoError(t, err)
	require.Equal(t, openCodeFreeModeAgent, req.Mode)
	require.Equal(t, []string{"read", "subagent"}, req.ToolNames)
	require.Equal(t, openCodeFreeToolChoice{Kind: "auto"}, req.ToolChoice)
	var got struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		ToolChoice any `json:"tool_choice"`
	}
	require.NoError(t, json.Unmarshal(req.Body, &got))
	require.Len(t, got.Tools, 2)
	require.Equal(t, "read", got.Tools[0].Function.Name)
	require.Equal(t, "subagent", got.Tools[1].Function.Name)

	forced := `{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"subagent_fork"}}],"tool_choice":{"type":"function","function":{"name":"subagent_fork"}}}`
	req, err = normalizeOpenCodeFreeRequest([]byte(forced), "big-pickle")
	require.NoError(t, err)
	require.Equal(t, []string{"read"}, req.ToolNames)
	require.Equal(t, openCodeFreeToolChoice{Kind: "auto"}, req.ToolChoice)

	// Both rejected names in one set are stripped together.
	both := `{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"session_status"}},{"type":"function","function":{"name":"subagent_fork"}},{"type":"function","function":{"name":"read"}}]}`
	req, err = normalizeOpenCodeFreeRequest([]byte(both), "big-pickle")
	require.NoError(t, err)
	require.Equal(t, []string{"read"}, req.ToolNames)
	require.Empty(t, req.InjectedTools)

	// working_directory poisons the set only together with read_image;
	// the bookkeeping tool is stripped and the caller capability kept.
	pair := `{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"read_image"}},{"type":"function","function":{"name":"working_directory"}},{"type":"function","function":{"name":"read"}}]}`
	req, err = normalizeOpenCodeFreeRequest([]byte(pair), "big-pickle")
	require.NoError(t, err)
	require.Equal(t, []string{"read_image", "read"}, req.ToolNames)
	var gotPair struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(req.Body, &gotPair))
	require.Len(t, gotPair.Tools, 2)
	require.Equal(t, "read_image", gotPair.Tools[0].Function.Name)
	require.Equal(t, "read", gotPair.Tools[1].Function.Name)
}

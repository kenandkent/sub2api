package service

import (
	"encoding/json"
	"errors"
	"fmt"
)

const openCodeFreeProfile = "opencode-big-pickle-text-balanced-20261027-v2"
const openCodeFreeProfileHash = "da80fdbfd8112be5433856044945bbf09f926d06c1a4dc59b70624ab8bd9a0a9"

// normalizeOpenCodeFreeRequest accepts only the worker's audited text subset.
func normalizeOpenCodeFreeRequest(body []byte, mapped string) ([]byte, bool, error) {
	if err := validateOpenCodeFreeJSON(string(body)); err != nil {
		return nil, false, errors.New("invalid chat JSON")
	}
	if mapped != "big-pickle" {
		return nil, false, errors.New("unsupported model")
	}
	var v map[string]json.RawMessage
	if json.Unmarshal(body, &v) != nil || v == nil {
		return nil, false, errors.New("invalid chat body")
	}
	for k := range v {
		switch k {
		case "model", "messages", "temperature", "tools", "tool_choice", "n", "stream", "stream_options":
		default:
			return nil, false, fmt.Errorf("unsupported parameter: %s", k)
		}
	}
	var model string
	if json.Unmarshal(v["model"], &model) != nil || model == "" {
		return nil, false, errors.New("invalid model")
	}
	var msgs []map[string]json.RawMessage
	if json.Unmarshal(v["messages"], &msgs) != nil || len(msgs) == 0 {
		return nil, false, errors.New("messages must contain text")
	}
	for _, m := range msgs {
		if len(m) != 2 {
			return nil, false, errors.New("unsupported message fields")
		}
		var role, content string
		if json.Unmarshal(m["role"], &role) != nil || (role != "user" && role != "assistant" && role != "system") || json.Unmarshal(m["content"], &content) != nil || string(m["content"]) == "null" {
			return nil, false, errors.New("only text messages are supported")
		}
	}
	if val, ok := v["temperature"]; ok {
		var n float64
		if json.Unmarshal(val, &n) != nil || string(val) == "null" || n != 0 {
			return nil, false, errors.New("unsupported temperature")
		}
	}
	if val, ok := v["tools"]; ok {
		var arr []json.RawMessage
		if json.Unmarshal(val, &arr) != nil || string(val) == "null" || len(arr) != 0 {
			return nil, false, errors.New("unsupported tools")
		}
	}
	if val, ok := v["tool_choice"]; ok {
		var x string
		if json.Unmarshal(val, &x) != nil || x != "none" {
			return nil, false, errors.New("unsupported tool choice")
		}
	}
	if val, ok := v["n"]; ok {
		var n int
		if json.Unmarshal(val, &n) != nil || n != 1 {
			return nil, false, errors.New("unsupported n")
		}
	}
	stream := false
	if val, ok := v["stream"]; ok {
		if json.Unmarshal(val, &stream) != nil || string(val) == "null" {
			return nil, false, errors.New("invalid stream")
		}
	}
	if val, ok := v["stream_options"]; ok {
		var opts map[string]json.RawMessage
		if json.Unmarshal(val, &opts) != nil || opts == nil || len(opts) != 1 {
			return nil, false, errors.New("invalid stream options")
		}
		for k, v := range opts {
			var b bool
			if k != "include_usage" || json.Unmarshal(v, &b) != nil || string(v) == "null" {
				return nil, false, errors.New("unsupported stream option")
			}
		}
	}
	v["model"] = json.RawMessage(`"big-pickle"`)
	b, err := json.Marshal(v)
	return b, stream, err
}

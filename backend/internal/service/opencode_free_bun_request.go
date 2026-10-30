package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// openCodeFreeProfileRef and friends live in opencode_free_bun_models.go.
// This file ports the audited chatSchema.ts text/agent request contract: the
// smallest valid profile is selected per request, caller tools and tool history
// are preserved exactly, and every other field is rejected before dispatch.

const (
	openCodeFreeMaxTools       = 128
	openCodeFreeMaxToolDepth   = 64
	openCodeFreeMaxToolNodes   = 100000
	openCodeFreeMaxToolNameLen = 64
)

var openCodeFreeToolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type openCodeFreeToolChoice struct {
	Kind string // "auto", "none", "required" or "function"
	Name string // function name when Kind == "function"
}

type openCodeFreeRequest struct {
	Body       []byte
	Stream     bool
	Mode       string
	Profile    openCodeFreeProfileRef
	ToolNames  []string
	ToolChoice openCodeFreeToolChoice
	// InjectedTools mirrors the placeholder tool the worker merges into the
	// upstream body; the caller never declared them, so the SSE validator
	// allows and strips their calls instead of rejecting the stream.
	InjectedTools []string
}

var (
	errOpenCodeFreeText  = errors.New("text profile mismatch")
	errOpenCodeFreeAgent = errors.New("agent profile mismatch")
)

// normalizeOpenCodeFreeRequest validates the caller body against the pinned
// text or agent contract, selects the matching profile for the mapped model and
// rewrites the model to the mapped upstream value.
func normalizeOpenCodeFreeRequest(body []byte, mapped string) (*openCodeFreeRequest, error) {
	if err := validateOpenCodeFreeJSON(string(body)); err != nil {
		return nil, errors.New("invalid chat JSON")
	}
	if !openCodeFreeAdmissible(mapped) {
		return nil, errors.New("unsupported model")
	}
	var v map[string]json.RawMessage
	if json.Unmarshal(body, &v) != nil || v == nil {
		return nil, errors.New("invalid chat body")
	}
	if err := validateOpenCodeFreeToolParameters(v); err != nil {
		return nil, err
	}
	mode := openCodeFreeModeText
	if err := validateOpenCodeFreeTextBody(v); err != nil {
		if agentErr := validateOpenCodeFreeAgentBody(v); agentErr != nil {
			return nil, errors.New("unsupported chat request")
		}
		mode = openCodeFreeModeAgent
	}
	profile, ok := openCodeFreeProfileFor(mapped, mode)
	if !ok {
		return nil, errors.New("unsupported model")
	}
	stream, err := validateOpenCodeFreeCommon(v)
	if err != nil {
		return nil, err
	}
	toolNames, toolChoice := openCodeFreeCallerTools(v, mode)
	model, err := json.Marshal(mapped)
	if err != nil {
		return nil, errors.New("invalid model")
	}
	v["model"] = model
	out, err := json.Marshal(v)
	if err != nil {
		return nil, errors.New("invalid chat body")
	}
	return &openCodeFreeRequest{
		Body:          out,
		Stream:        stream,
		Mode:          mode,
		Profile:       profile,
		ToolNames:     toolNames,
		ToolChoice:    toolChoice,
		InjectedTools: openCodeFreeInjectedTools(toolNames),
	}, nil
}

// openCodeFreeInjectedTools mirrors the worker placeholder merge: the free
// upstream refuses any agent request whose declared tool set does not include
// the official placeholder tool `read`, so the worker appends it whenever the
// caller's tools lack it. The two rules must stay identical: injection only
// happens for non-empty caller tool sets without a declared `read`.
func openCodeFreeInjectedTools(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	for _, name := range names {
		if name == "read" {
			return nil
		}
	}
	return []string{"read"}
}

// openCodeFreeCallerTools mirrors the adapter: the SSE validator only accepts
// tool calls the caller actually declared, and a request without declared tools
// is pinned to tool_choice none even when the profile carries its own tool list.
func openCodeFreeCallerTools(v map[string]json.RawMessage, mode string) ([]string, openCodeFreeToolChoice) {
	names := []string{}
	choice := openCodeFreeToolChoice{Kind: "none"}
	if mode != openCodeFreeModeAgent {
		return names, choice
	}
	if raw, ok := v["tools"]; ok {
		var tools []map[string]json.RawMessage
		if json.Unmarshal(raw, &tools) == nil {
			for _, tool := range tools {
				var fn map[string]json.RawMessage
				if json.Unmarshal(tool["function"], &fn) == nil {
					var name string
					if json.Unmarshal(fn["name"], &name) == nil {
						names = append(names, name)
					}
				}
			}
		}
	}
	if len(names) == 0 {
		return names, choice
	}
	choice = openCodeFreeToolChoice{Kind: "auto"}
	if raw, ok := v["tool_choice"]; ok {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			choice = openCodeFreeToolChoice{Kind: text}
		} else {
			var forced struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			if json.Unmarshal(raw, &forced) == nil && forced.Function.Name != "" {
				choice = openCodeFreeToolChoice{Kind: "function", Name: forced.Function.Name}
			}
		}
	}
	return names, choice
}

// validateOpenCodeFreeToolParameters enforces the bounded, acyclic JSON shape
// audit for caller tool schemas before any mode is selected. JSON payloads
// cannot express cycles or prototypes; depth and node budgets bound parsing work.
func validateOpenCodeFreeToolParameters(v map[string]json.RawMessage) error {
	raw, ok := v["tools"]
	if !ok || string(raw) == "null" {
		return nil
	}
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return nil // the mode schemas reject non-arrays with their own errors
	}
	if len(tools) > openCodeFreeMaxTools {
		return errors.New("unsupported tools")
	}
	nodes := 0
	for _, tool := range tools {
		var item map[string]json.RawMessage
		if json.Unmarshal(tool, &item) != nil || item == nil {
			continue
		}
		params, ok := item["function"]
		if !ok {
			continue
		}
		var fn map[string]json.RawMessage
		if json.Unmarshal(params, &fn) != nil || fn == nil {
			continue
		}
		schema, ok := fn["parameters"]
		if !ok {
			continue
		}
		var value any
		if json.Unmarshal(schema, &value) != nil {
			return errors.New("unsupported tool parameters")
		}
		if err := openCodeFreeWalkToolValue(value, 1, &nodes); err != nil {
			return err
		}
	}
	return nil
}

func openCodeFreeWalkToolValue(value any, depth int, nodes *int) error {
	*nodes++
	if *nodes > openCodeFreeMaxToolNodes || depth > openCodeFreeMaxToolDepth {
		return errors.New("unsupported tool parameters")
	}
	switch typed := value.(type) {
	case nil, string, bool:
		return nil
	case float64:
		return nil
	case []any:
		for _, item := range typed {
			if err := openCodeFreeWalkToolValue(item, depth+1, nodes); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for _, item := range typed {
			if err := openCodeFreeWalkToolValue(item, depth+1, nodes); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("unsupported tool parameters")
	}
}

func openCodeFreeKeys(v map[string]json.RawMessage, allowed ...string) error {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key := range v {
		if _, ok := set[key]; !ok {
			return fmt.Errorf("unsupported parameter: %s", key)
		}
	}
	return nil
}

func openCodeFreeString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", errors.New("expected string")
	}
	var out string
	if json.Unmarshal(raw, &out) != nil {
		return "", errors.New("expected string")
	}
	return out, nil
}

func openCodeFreeInt(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, errors.New("expected integer")
	}
	var out int
	if json.Unmarshal(raw, &out) != nil {
		return 0, errors.New("expected integer")
	}
	return out, nil
}

func openCodeFreeBool(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, errors.New("expected boolean")
	}
	var out bool
	if json.Unmarshal(raw, &out) != nil {
		return false, errors.New("expected boolean")
	}
	return out, nil
}

// validateOpenCodeFreeCommon checks the fields shared by both profiles. It is
// called once after mode selection and returns the caller's stream flag.
func validateOpenCodeFreeCommon(v map[string]json.RawMessage) (bool, error) {
	if raw, ok := v["temperature"]; ok {
		if string(raw) == "null" {
			return false, errors.New("unsupported temperature")
		}
		var n float64
		if json.Unmarshal(raw, &n) != nil || n != 0 {
			return false, errors.New("unsupported temperature")
		}
	}
	if raw, ok := v["n"]; ok {
		n, err := openCodeFreeInt(raw)
		if err != nil || n != 1 {
			return false, errors.New("unsupported n")
		}
	}
	if raw, ok := v["max_tokens"]; ok {
		n, err := openCodeFreeInt(raw)
		if err != nil || n <= 0 {
			return false, errors.New("unsupported max_tokens")
		}
	}
	if raw, ok := v["max_completion_tokens"]; ok {
		n, err := openCodeFreeInt(raw)
		if err != nil || n <= 0 {
			return false, errors.New("unsupported max_completion_tokens")
		}
	}
	if raw, ok := v["stop"]; ok {
		if err := validateOpenCodeFreeStop(raw); err != nil {
			return false, err
		}
	}
	stream := false
	if raw, ok := v["stream"]; ok {
		parsed, err := openCodeFreeBool(raw)
		if err != nil {
			return false, errors.New("invalid stream")
		}
		stream = parsed
	}
	if raw, ok := v["stream_options"]; ok {
		if string(raw) == "null" {
			return false, errors.New("invalid stream options")
		}
		var opts map[string]json.RawMessage
		if json.Unmarshal(raw, &opts) != nil || opts == nil || len(opts) != 1 {
			return false, errors.New("invalid stream options")
		}
		for key, value := range opts {
			if key != "include_usage" {
				return false, errors.New("unsupported stream option")
			}
			if _, err := openCodeFreeBool(value); err != nil {
				return false, errors.New("unsupported stream option")
			}
		}
	}
	return stream, nil
}

func validateOpenCodeFreeStop(raw json.RawMessage) error {
	if string(raw) == "null" {
		return errors.New("unsupported stop")
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return nil
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return errors.New("unsupported stop")
	}
	for _, item := range list {
		if _, err := openCodeFreeString(item); err != nil {
			return errors.New("unsupported stop")
		}
	}
	return nil
}

// validateOpenCodeFreeTextBody accepts only string content in system/user/
// assistant messages with no caller tools.
func validateOpenCodeFreeTextBody(v map[string]json.RawMessage) error {
	if err := openCodeFreeKeys(v,
		"model", "messages", "temperature", "n", "stream", "stream_options",
		"max_tokens", "max_completion_tokens", "stop", "tools", "tool_choice",
	); err != nil {
		return errOpenCodeFreeText
	}
	if err := validateOpenCodeFreeTextMessages(v); err != nil {
		return errOpenCodeFreeText
	}
	if raw, ok := v["tools"]; ok {
		if string(raw) == "null" {
			return errOpenCodeFreeText
		}
		var tools []json.RawMessage
		if json.Unmarshal(raw, &tools) != nil || len(tools) != 0 {
			return errOpenCodeFreeText
		}
	}
	if raw, ok := v["tool_choice"]; ok {
		text, err := openCodeFreeString(raw)
		if err != nil || text != "none" {
			return errOpenCodeFreeText
		}
	}
	return nil
}

func validateOpenCodeFreeTextMessages(v map[string]json.RawMessage) error {
	raw, ok := v["messages"]
	if !ok || string(raw) == "null" {
		return errOpenCodeFreeText
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(raw, &messages) != nil || len(messages) == 0 {
		return errOpenCodeFreeText
	}
	for _, message := range messages {
		if len(message) != 2 {
			return errOpenCodeFreeText
		}
		role, err := openCodeFreeString(message["role"])
		if err != nil || (role != "system" && role != "user" && role != "assistant") {
			return errOpenCodeFreeText
		}
		if _, err := openCodeFreeString(message["content"]); err != nil {
			return errOpenCodeFreeText
		}
	}
	return nil
}

// validateOpenCodeFreeAgentBody ports the agent profile contract: caller tools,
// tool_choice, tool results continuation and reasoning fields are preserved
// exactly; unmatched or duplicated tool call history is rejected.
func validateOpenCodeFreeAgentBody(v map[string]json.RawMessage) error {
	if err := openCodeFreeKeys(v,
		"model", "messages", "temperature", "n", "stream", "stream_options",
		"max_tokens", "max_completion_tokens", "stop", "tools", "tool_choice",
		"parallel_tool_calls",
	); err != nil {
		return errOpenCodeFreeAgent
	}
	if err := validateOpenCodeFreeAgentMessages(v); err != nil {
		return errOpenCodeFreeAgent
	}
	declared := map[string]struct{}{}
	if raw, ok := v["tools"]; ok {
		if string(raw) == "null" {
			return errOpenCodeFreeAgent
		}
		var tools []json.RawMessage
		if json.Unmarshal(raw, &tools) != nil || len(tools) > openCodeFreeMaxTools {
			return errOpenCodeFreeAgent
		}
		for _, tool := range tools {
			name, err := validateOpenCodeFreeToolDefinition(tool)
			if err != nil {
				return errOpenCodeFreeAgent
			}
			if _, exists := declared[name]; exists {
				return errOpenCodeFreeAgent // duplicate tool name
			}
			declared[name] = struct{}{}
		}
	}
	if raw, ok := v["tool_choice"]; ok {
		choice, err := openCodeFreeParseToolChoice(raw)
		if err != nil {
			return errOpenCodeFreeAgent
		}
		if choice.Kind == "function" {
			if _, ok := declared[choice.Name]; !ok {
				return errOpenCodeFreeAgent // forced tool not declared
			}
		}
		if choice.Kind == "required" && len(declared) == 0 {
			return errOpenCodeFreeAgent // required choice needs tools
		}
	}
	if raw, ok := v["parallel_tool_calls"]; ok {
		if _, err := openCodeFreeBool(raw); err != nil {
			return errOpenCodeFreeAgent
		}
	}
	return nil
}

func openCodeFreeParseToolChoice(raw json.RawMessage) (openCodeFreeToolChoice, error) {
	if string(raw) == "null" {
		return openCodeFreeToolChoice{}, errors.New("invalid tool choice")
	}
	if text, err := openCodeFreeString(raw); err == nil {
		switch text {
		case "auto", "none", "required":
			return openCodeFreeToolChoice{Kind: text}, nil
		default:
			return openCodeFreeToolChoice{}, errors.New("invalid tool choice")
		}
	}
	var forced map[string]json.RawMessage
	if json.Unmarshal(raw, &forced) != nil || len(forced) != 2 {
		return openCodeFreeToolChoice{}, errors.New("invalid tool choice")
	}
	kind, err := openCodeFreeString(forced["type"])
	if err != nil || kind != "function" {
		return openCodeFreeToolChoice{}, errors.New("invalid tool choice")
	}
	var fn map[string]json.RawMessage
	if json.Unmarshal(forced["function"], &fn) != nil || len(fn) != 1 {
		return openCodeFreeToolChoice{}, errors.New("invalid tool choice")
	}
	name, err := openCodeFreeString(fn["name"])
	if err != nil || !openCodeFreeValidToolName(name) {
		return openCodeFreeToolChoice{}, errors.New("invalid tool choice")
	}
	return openCodeFreeToolChoice{Kind: "function", Name: name}, nil
}

func openCodeFreeValidToolName(name string) bool {
	return len(name) > 0 && len(name) <= openCodeFreeMaxToolNameLen && openCodeFreeToolNamePattern.MatchString(name)
}

func validateOpenCodeFreeToolDefinition(raw json.RawMessage) (string, error) {
	var tool map[string]json.RawMessage
	if json.Unmarshal(raw, &tool) != nil || tool == nil || len(tool) != 2 {
		return "", errors.New("invalid tool")
	}
	kind, err := openCodeFreeString(tool["type"])
	if err != nil || kind != "function" {
		return "", errors.New("invalid tool")
	}
	var fn map[string]json.RawMessage
	if json.Unmarshal(tool["function"], &fn) != nil || fn == nil {
		return "", errors.New("invalid tool")
	}
	for key := range fn {
		switch key {
		case "name", "description", "parameters", "strict":
		default:
			return "", errors.New("invalid tool")
		}
	}
	name, err := openCodeFreeString(fn["name"])
	if err != nil || !openCodeFreeValidToolName(name) {
		return "", errors.New("invalid tool")
	}
	if rawDescription, ok := fn["description"]; ok {
		if _, err := openCodeFreeString(rawDescription); err != nil {
			return "", errors.New("invalid tool")
		}
	}
	if rawStrict, ok := fn["strict"]; ok && string(rawStrict) != "null" {
		if _, err := openCodeFreeBool(rawStrict); err != nil {
			return "", errors.New("invalid tool")
		}
	}
	// The audited chatSchema accepts an absent or object-valued JSON Schema
	// only; null or non-object parameters are rejected before dispatch.
	if rawParams, ok := fn["parameters"]; ok {
		var schema map[string]any
		if json.Unmarshal(rawParams, &schema) != nil || schema == nil {
			return "", errors.New("invalid tool")
		}
	}
	return name, nil
}

// validateOpenCodeFreeAgentMessages verifies the tool result continuation
// rules over the whole history.
func validateOpenCodeFreeAgentMessages(v map[string]json.RawMessage) error {
	raw, ok := v["messages"]
	if !ok || string(raw) == "null" {
		return errors.New("invalid messages")
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(raw, &messages) != nil || len(messages) == 0 {
		return errors.New("invalid messages")
	}
	pending := map[string]struct{}{}
	seen := map[string]struct{}{}
	for _, message := range messages {
		role, err := openCodeFreeString(message["role"])
		if err != nil {
			return errors.New("invalid message role")
		}
		if role == "tool" {
			for key := range message {
				switch key {
				case "role", "content", "tool_call_id", "name":
				default:
					return errors.New("unsupported message fields")
				}
			}
			if err := validateOpenCodeFreeAgentContent(message["content"]); err != nil {
				return err
			}
			id, err := openCodeFreeString(message["tool_call_id"])
			if err != nil || id == "" {
				return errors.New("invalid tool result")
			}
			if rawName, ok := message["name"]; ok {
				if name, err := openCodeFreeString(rawName); err != nil || !openCodeFreeValidToolName(name) {
					return errors.New("invalid tool result")
				}
			}
			if _, ok := pending[id]; !ok {
				return errors.New("unmatched tool result")
			}
			delete(pending, id)
			continue
		}
		if len(pending) > 0 {
			return errors.New("unfinished tool calls")
		}
		switch role {
		case "system", "user":
			for key := range message {
				switch key {
				case "role", "content", "name":
				default:
					return errors.New("unsupported message fields")
				}
			}
			if err := validateOpenCodeFreeAgentContent(message["content"]); err != nil {
				return err
			}
			if rawName, ok := message["name"]; ok {
				if name, err := openCodeFreeString(rawName); err != nil || !openCodeFreeValidToolName(name) {
					return errors.New("invalid message name")
				}
			}
		case "assistant":
			for key := range message {
				switch key {
				case "role", "content", "name", "reasoning_content", "reasoning", "tool_calls":
				default:
					return errors.New("unsupported message fields")
				}
			}
			if rawName, ok := message["name"]; ok {
				if name, err := openCodeFreeString(rawName); err != nil || !openCodeFreeValidToolName(name) {
					return errors.New("invalid message name")
				}
			}
			for _, key := range []string{"reasoning_content", "reasoning"} {
				if rawValue, ok := message[key]; ok {
					if _, err := openCodeFreeString(rawValue); err != nil {
						return errors.New("invalid reasoning content")
					}
				}
			}
			hasContent := false
			if rawContent, ok := message["content"]; ok && string(rawContent) != "null" {
				if err := validateOpenCodeFreeAgentContent(rawContent); err != nil {
					return err
				}
				hasContent = true
			}
			var calls []map[string]json.RawMessage
			if rawCalls, ok := message["tool_calls"]; ok {
				if string(rawCalls) == "null" || json.Unmarshal(rawCalls, &calls) != nil || len(calls) == 0 {
					return errors.New("invalid tool calls")
				}
			}
			if !hasContent && len(calls) == 0 {
				return errors.New("empty assistant message")
			}
			for _, call := range calls {
				id, name, arguments, err := openCodeFreeParseToolCall(call)
				if err != nil {
					return err
				}
				if _, exists := seen[id]; exists {
					return errors.New("duplicate tool call id")
				}
				seen[id] = struct{}{}
				pending[id] = struct{}{}
				_ = name
				_ = arguments
			}
		default:
			return errors.New("invalid message role")
		}
	}
	if len(pending) > 0 {
		return errors.New("unfinished tool calls")
	}
	return nil
}

func openCodeFreeParseToolCall(call map[string]json.RawMessage) (string, string, string, error) {
	if len(call) != 3 {
		return "", "", "", errors.New("invalid tool call")
	}
	id, err := openCodeFreeString(call["id"])
	if err != nil || id == "" {
		return "", "", "", errors.New("invalid tool call")
	}
	kind, err := openCodeFreeString(call["type"])
	if err != nil || kind != "function" {
		return "", "", "", errors.New("invalid tool call")
	}
	var fn map[string]json.RawMessage
	if json.Unmarshal(call["function"], &fn) != nil || len(fn) != 2 {
		return "", "", "", errors.New("invalid tool call")
	}
	name, err := openCodeFreeString(fn["name"])
	if err != nil || !openCodeFreeValidToolName(name) {
		return "", "", "", errors.New("invalid tool call")
	}
	arguments, err := openCodeFreeString(fn["arguments"])
	if err != nil {
		return "", "", "", errors.New("invalid tool call")
	}
	return id, name, arguments, nil
}

// validateOpenCodeFreeAgentContent accepts a plain string or an array of
// {"type":"text","text":string} parts.
func validateOpenCodeFreeAgentContent(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return errors.New("invalid message content")
	}
	if _, err := openCodeFreeString(raw); err == nil {
		return nil
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return errors.New("invalid message content")
	}
	for _, part := range parts {
		if len(part) != 2 {
			return errors.New("invalid message content")
		}
		kind, err := openCodeFreeString(part["type"])
		if err != nil || kind != "text" {
			return errors.New("invalid message content")
		}
		if _, err := openCodeFreeString(part["text"]); err != nil {
			return errors.New("invalid message content")
		}
	}
	return nil
}

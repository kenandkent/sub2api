package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

type openCodeFreeSSEOptions struct {
	ToolNames  []string
	ToolChoice openCodeFreeToolChoice
	// InjectedTools are placeholder tool names the worker merged into the
	// upstream body (the free tier refuses tool sets without the official
	// `read` placeholder). The caller never declared them, so their calls are
	// allowed through validation and stripped from the client stream.
	InjectedTools []string
	// QuietErrors keeps upstream frames and parser errors out of admin logs.
	QuietErrors bool
}

type openCodeFreeToolCallResult struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openCodeFreeParsed struct {
	ID            string
	Text          string
	Reasoning     string
	Finish        string
	ToolCalls     []openCodeFreeToolCallResult
	Usage         OpenAIUsage
	RawUsage      json.RawMessage
	BillingSource string
}

var (
	openCodeFreeToolCallIDPattern       = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	openCodeFreeToolCallFragmentPattern = regexp.MustCompile(`^[A-Za-z0-9_-]*$`)
)

type openCodeFreeSSEValidator struct {
	clientModel string
	model       string
	names       map[string]struct{}
	injected    map[string]struct{}
	forcedName  string
	forced      bool
	required    bool
	none        bool

	calls          map[int]*openCodeFreeToolCallResult
	text           string
	reasoning      string
	finished       bool
	finishReason   string
	upstreamFinish string
	usage          json.RawMessage
	rawUsageResult OpenAIUsage
	billingResult  string
	haveUsage      bool
	id             string
	created        int64
	started        int64
}

func newOpenCodeFreeSSEValidator(clientModel string, opts *openCodeFreeSSEOptions) (*openCodeFreeSSEValidator, error) {
	v := &openCodeFreeSSEValidator{
		clientModel: clientModel,
		model:       clientModel,
		names:       map[string]struct{}{},
		injected:    map[string]struct{}{},
		calls:       map[int]*openCodeFreeToolCallResult{},
		started:     time.Now().Unix(),
	}
	choice := openCodeFreeToolChoice{Kind: "none"}
	if opts != nil {
		choice = opts.ToolChoice
		seen := map[string]struct{}{}
		for _, name := range opts.ToolNames {
			if !openCodeFreeValidToolName(name) {
				return nil, errors.New("invalid tool names")
			}
			if _, dup := seen[name]; dup {
				return nil, errors.New("duplicate tool names")
			}
			seen[name] = struct{}{}
			v.names[name] = struct{}{}
		}
		for _, name := range opts.InjectedTools {
			if !openCodeFreeValidToolName(name) {
				return nil, errors.New("invalid injected tool")
			}
			if _, dup := v.injected[name]; dup {
				return nil, errors.New("duplicate injected tools")
			}
			if _, clash := v.names[name]; clash {
				return nil, errors.New("injected tool already declared")
			}
			v.injected[name] = struct{}{}
		}
	}
	switch choice.Kind {
	case "auto", "none", "required":
		v.none = choice.Kind == "none"
		v.required = choice.Kind == "required"
	case "function":
		if !openCodeFreeValidToolName(choice.Name) {
			return nil, errors.New("invalid forced tool")
		}
		if _, ok := v.names[choice.Name]; !ok {
			return nil, errors.New("forced tool not declared")
		}
		v.forced = true
		v.forcedName = choice.Name
	default:
		return nil, errors.New("invalid tool choice")
	}
	return v, nil
}

// isInjected reports whether the name belongs to a placeholder tool the worker
// merged upstream. Those calls are valid to receive but never reach the caller.
func (v *openCodeFreeSSEValidator) isInjected(name string) bool {
	_, ok := v.injected[name]
	return ok
}

// inspectTools ports the audited OpenCode free SSE tool delta rules: declared
// tools only, bounded ids/names/arguments, prefix accumulation and the
// complete-call terminal fallback.
func (v *openCodeFreeSSEValidator) inspectTools(raw json.RawMessage, calls map[int]*openCodeFreeToolCallResult, finishReason string) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if v.none {
		return errors.New("unexpected tool calls")
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		return errors.New("invalid tool calls")
	}
	needsIndex := false
	for _, item := range items {
		var probe map[string]json.RawMessage
		if json.Unmarshal(item, &probe) == nil {
			if _, ok := probe["index"]; !ok {
				needsIndex = true
			}
		}
	}
	if needsIndex {
		// Some upstreams return complete tool calls in one terminal frame without
		// indices. Array order is unambiguous only when there was no prior tool
		// state and every call is complete and independently verifiable.
		if finishReason != "tool_calls" || len(calls) != 0 {
			return errors.New("invalid tool calls")
		}
		for index, item := range items {
			var call struct {
				ID       string  `json:"id"`
				Type     string  `json:"type"`
				Index    *string `json:"index"`
				Function struct {
					Name      string  `json:"name"`
					Arguments *string `json:"arguments"`
				} `json:"function"`
			}
			if json.Unmarshal(item, &call) != nil || call.Index != nil || call.ID == "" || !openCodeFreeToolCallIDPattern.MatchString(call.ID) || call.Type != "function" || call.Function.Arguments == nil || !openCodeFreeValidToolName(call.Function.Name) {
				return errors.New("invalid tool calls")
			}
			if _, ok := v.names[call.Function.Name]; !ok && !v.isInjected(call.Function.Name) {
				return errors.New("undeclared tool")
			}
			if v.forced && !v.isInjected(call.Function.Name) && call.Function.Name != v.forcedName {
				return errors.New("forced tool mismatch")
			}
			if !json.Valid([]byte(*call.Function.Arguments)) {
				return errors.New("invalid tool arguments")
			}
			entry := &openCodeFreeToolCallResult{ID: call.ID, Type: "function"}
			entry.Function.Name = call.Function.Name
			entry.Function.Arguments = *call.Function.Arguments
			calls[index] = entry
		}
		return nil
	}
	seen := map[int]struct{}{}
	for _, item := range items {
		var fields map[string]json.RawMessage
		if json.Unmarshal(item, &fields) != nil || fields == nil {
			return errors.New("invalid tool call")
		}
		for key := range fields {
			switch key {
			case "index", "id", "type", "function":
			default:
				return errors.New("unsupported tool call field")
			}
		}
		index, err := openCodeFreeInt(fields["index"])
		if err != nil || index < 0 || index > 127 {
			return errors.New("invalid tool call index")
		}
		if _, dup := seen[index]; dup {
			return errors.New("duplicate tool call index")
		}
		seen[index] = struct{}{}
		var fn map[string]json.RawMessage
		if rawFn, ok := fields["function"]; ok && string(rawFn) != "null" {
			if json.Unmarshal(rawFn, &fn) != nil || fn == nil {
				return errors.New("invalid tool function")
			}
			for key := range fn {
				switch key {
				case "name", "arguments":
				default:
					return errors.New("unsupported tool function field")
				}
			}
		}
		call, ok := calls[index]
		if !ok {
			call = &openCodeFreeToolCallResult{Type: "function"}
			calls[index] = call
		}
		if rawID, ok := fields["id"]; ok && string(rawID) != "null" {
			id, err := openCodeFreeString(rawID)
			if err != nil || !openCodeFreeToolCallFragmentPattern.MatchString(id) {
				return errors.New("invalid tool call id")
			}
			// Some upstreams repeat the complete ID; otherwise append ID fragments.
			switch {
			case id == call.ID:
			case call.ID == "":
				call.ID = id
			default:
				call.ID += id
			}
			if len(call.ID) > 256 || !openCodeFreeToolCallIDPattern.MatchString(call.ID) {
				return errors.New("invalid tool call id")
			}
		}
		if rawType, ok := fields["type"]; ok && string(rawType) != "null" {
			kind, err := openCodeFreeString(rawType)
			if err != nil || kind != "function" {
				return errors.New("invalid tool call type")
			}
			call.Type = kind
		}
		if rawName, ok := fn["name"]; ok && string(rawName) != "null" {
			fragment, err := openCodeFreeString(rawName)
			if err != nil || !openCodeFreeToolCallFragmentPattern.MatchString(fragment) {
				return errors.New("invalid tool call name")
			}
			name := call.Function.Name + fragment
			if len(name) > openCodeFreeMaxToolNameLen || !openCodeFreeToolNameAllowed(name, v) {
				return errors.New("undeclared tool")
			}
			call.Function.Name = name
		}
		if rawArgs, ok := fn["arguments"]; ok && string(rawArgs) != "null" {
			arguments, err := openCodeFreeString(rawArgs)
			if err != nil {
				return errors.New("invalid tool arguments")
			}
			if len(call.Function.Arguments)+len(arguments) > 256*1024 {
				return errors.New("tool arguments too large")
			}
			call.Function.Arguments += arguments
		}
	}
	return nil
}

func openCodeFreeToolNameAllowed(name string, v *openCodeFreeSSEValidator) bool {
	if v.forced {
		if strings.HasPrefix(v.forcedName, name) {
			return true
		}
	} else {
		for candidate := range v.names {
			if strings.HasPrefix(candidate, name) {
				return true
			}
		}
	}
	for candidate := range v.injected {
		if strings.HasPrefix(candidate, name) {
			return true
		}
	}
	return false
}

// inspectFinish enforces the audited terminal invariants: declared finish
// reasons only, tool_calls finish iff calls exist, required/forced choice needs
// calls, and every finished call is complete, contiguous, unique and declared.
// It returns the finish reason the client should observe: when the upstream
// only called injected placeholder tools, the tool_calls finish degrades to
// stop and the placeholder calls are removed (and the remaining client calls
// reindexed) so the caller never sees a tool it never declared.
func (v *openCodeFreeSSEValidator) inspectFinish(reason string, calls map[int]*openCodeFreeToolCallResult) (string, error) {
	switch reason {
	case "":
		return "", nil
	case "stop", "length", "content_filter", "tool_calls":
	default:
		return "", errors.New("unsupported finish reason")
	}
	clientCalls, injectedCalls := 0, 0
	for _, call := range calls {
		if call != nil && v.isInjected(call.Function.Name) {
			injectedCalls++
			continue
		}
		clientCalls++
	}
	if reason == "tool_calls" && clientCalls == 0 && injectedCalls > 0 {
		// Only placeholder calls: the caller declared no tool call at all, so
		// present the turn as an ordinary text stop.
		reason = "stop"
	}
	if (reason == "tool_calls") != (clientCalls > 0) {
		return "", errors.New("finish reason mismatch")
	}
	if (v.required || v.forced) && clientCalls == 0 {
		return "", errors.New("required tool calls missing")
	}
	ids := map[string]struct{}{}
	for index := 0; index < len(calls); index++ {
		call, ok := calls[index]
		if !ok || !openCodeFreeToolCallIDPattern.MatchString(call.ID) {
			return "", errors.New("incomplete tool call")
		}
		if _, dup := ids[call.ID]; dup {
			return "", errors.New("duplicate tool call id")
		}
		ids[call.ID] = struct{}{}
		if v.isInjected(call.Function.Name) {
			if !json.Valid([]byte(call.Function.Arguments)) {
				return "", errors.New("invalid tool arguments")
			}
			continue
		}
		if _, ok := v.names[call.Function.Name]; !ok {
			return "", errors.New("undeclared tool")
		}
		if v.forced && call.Function.Name != v.forcedName {
			return "", errors.New("forced tool mismatch")
		}
		if !json.Valid([]byte(call.Function.Arguments)) {
			return "", errors.New("invalid tool arguments")
		}
	}
	if injectedCalls > 0 {
		compacted := make(map[int]*openCodeFreeToolCallResult, clientCalls)
		next := 0
		for index := 0; index < len(calls); index++ {
			call := calls[index]
			if v.isInjected(call.Function.Name) {
				continue
			}
			compacted[next] = call
			next++
		}
		for index := range calls {
			delete(calls, index)
		}
		for index, call := range compacted {
			calls[index] = call
		}
	}
	return reason, nil
}

func (v *openCodeFreeSSEValidator) validateUsage(raw json.RawMessage) error {
	if v.haveUsage {
		return errors.New("duplicate usage")
	}
	var usage struct {
		Prompt        *int   `json:"prompt_tokens"`
		Completion    *int   `json:"completion_tokens"`
		BillingSource string `json:"billing_source"`
		Details       struct {
			Cached   *int `json:"cached_tokens"`
			Creation *int `json:"cache_creation_tokens"`
		} `json:"prompt_tokens_details"`
		CacheRead         *int `json:"cache_read_input_tokens"`
		CacheCreation     *int `json:"cache_creation_input_tokens"`
		Total             *int `json:"total_tokens"`
		CompletionDetails struct {
			Reasoning *int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		OpenCode *struct {
			BillingSource string `json:"billing_source"`
			Tokens        *struct {
				Input        *int `json:"input"`
				Output       *int `json:"output"`
				Reasoning    *int `json:"reasoning"`
				CacheRead    *int `json:"cache_read"`
				CacheWrite5m *int `json:"cache_write_5m"`
				CacheWrite1h *int `json:"cache_write_1h"`
			} `json:"tokens"`
		} `json:"opencode"`
	}
	if json.Unmarshal(raw, &usage) != nil || usage.Prompt == nil || usage.Completion == nil || *usage.Prompt < 0 || *usage.Completion < 0 {
		return errors.New("invalid usage")
	}
	read, create := 0, 0
	if usage.Details.Cached != nil {
		read = *usage.Details.Cached
	}
	if usage.Details.Creation != nil {
		create = *usage.Details.Creation
	}
	if usage.CacheRead != nil {
		if usage.Details.Cached != nil && read != *usage.CacheRead {
			return errors.New("conflicting cache usage")
		}
		read = *usage.CacheRead
	}
	if usage.CacheCreation != nil {
		if usage.Details.Creation != nil && create != *usage.CacheCreation {
			return errors.New("conflicting cache usage")
		}
		create = *usage.CacheCreation
	}
	if usage.Total != nil && (*usage.Total < 0 || *usage.Total-*usage.Prompt != *usage.Completion) {
		return errors.New("invalid total usage")
	}
	if usage.CompletionDetails.Reasoning != nil && (*usage.CompletionDetails.Reasoning < 0 || *usage.CompletionDetails.Reasoning > *usage.Completion) {
		return errors.New("invalid reasoning usage")
	}
	var billing string
	if usage.OpenCode != nil {
		t := usage.OpenCode.Tokens
		if t == nil || t.Input == nil || t.Output == nil || t.Reasoning == nil || t.CacheRead == nil || t.CacheWrite5m == nil || t.CacheWrite1h == nil || *t.Input < 0 || *t.Output < 0 || *t.Reasoning < 0 || *t.CacheRead < 0 || *t.CacheWrite5m < 0 || *t.CacheWrite1h < 0 || *t.Output != *usage.Completion || *t.Reasoning > *t.Output {
			return errors.New("invalid OpenCode usage")
		}
		if usage.BillingSource != "" && usage.BillingSource != usage.OpenCode.BillingSource {
			return errors.New("conflicting billing source")
		}
		billing = usage.OpenCode.BillingSource
		if (usage.Details.Cached != nil || usage.CacheRead != nil) && read != *t.CacheRead {
			return errors.New("conflicting cache read")
		}
		read = *t.CacheRead
		if *t.CacheWrite5m > *usage.Prompt || *t.CacheWrite1h > *usage.Prompt-*t.CacheWrite5m {
			return errors.New("invalid cache creation")
		}
		combined := *t.CacheWrite5m + *t.CacheWrite1h
		if (usage.Details.Creation != nil || usage.CacheCreation != nil) && create != combined {
			return errors.New("conflicting cache creation")
		}
		create = combined
		if read > *usage.Prompt || create > *usage.Prompt-read || *t.Input != *usage.Prompt-read-create {
			return errors.New("inconsistent prompt usage")
		}
		if usage.CompletionDetails.Reasoning != nil && *usage.CompletionDetails.Reasoning != *t.Reasoning {
			return errors.New("conflicting reasoning usage")
		}
	} else {
		billing = usage.BillingSource
	}
	if read < 0 || create < 0 || read > *usage.Prompt || create > *usage.Prompt-read {
		return errors.New("invalid cache usage")
	}
	v.usage = append(json.RawMessage(nil), raw...)
	v.haveUsage = true
	v.rawUsageResult = OpenAIUsage{InputTokens: *usage.Prompt, OutputTokens: *usage.Completion, CacheReadInputTokens: read, CacheCreationInputTokens: create}
	v.billingResult = billing
	return nil
}

// parse validates one complete SSE data payload. It returns the frame to write
// (empty when the frame is withheld), or an error for any invalid event. Tools,
// finish and usage stay private until the whole stream is valid.
func (v *openCodeFreeSSEValidator) parse(payload string) (string, bool, error) {
	if payload == "[DONE]" {
		if !v.finished || !v.haveUsage {
			return "", false, errors.New("incomplete stream")
		}
		return "", true, nil
	}
	var top map[string]json.RawMessage
	if json.Unmarshal([]byte(payload), &top) != nil || top == nil {
		return "", false, errors.New("invalid upstream event")
	}
	for key := range top {
		switch key {
		case "id", "object", "created", "model", "choices", "usage", "system_fingerprint", "service_tier", "error":
		default:
			return "", false, errors.New("unsupported event field")
		}
	}
	if rawError, ok := top["error"]; ok && string(rawError) != "null" {
		return "", false, errors.New("upstream error event")
	}
	if rawObject, ok := top["object"]; ok && string(rawObject) != "null" {
		object, err := openCodeFreeString(rawObject)
		if err != nil || object != "chat.completion.chunk" {
			return "", false, errors.New("invalid event object")
		}
	}
	if rawUsage, ok := top["usage"]; ok && string(rawUsage) != "null" {
		if err := v.validateUsage(rawUsage); err != nil {
			return "", false, err
		}
	}
	rawChoices, ok := top["choices"]
	if !ok || string(rawChoices) == "null" {
		return "", false, errors.New("missing choices")
	}
	var choiceFields []map[string]json.RawMessage
	if json.Unmarshal(rawChoices, &choiceFields) != nil {
		return "", false, errors.New("invalid choices")
	}
	if len(choiceFields) == 0 && !v.haveUsage {
		return "", false, errors.New("missing choices")
	}
	if len(choiceFields) > 1 {
		return "", false, errors.New("unexpected choices")
	}
	calls := make(map[int]*openCodeFreeToolCallResult, len(v.calls))
	for index, call := range v.calls {
		clone := *call
		calls[index] = &clone
	}
	finish := ""
	upstreamFinish := ""
	sawFinish := false
	var deltas []map[string]json.RawMessage
	for _, fields := range choiceFields {
		for key := range fields {
			switch key {
			case "index", "delta", "finish_reason", "logprobs":
			default:
				return "", false, errors.New("unsupported choice field")
			}
		}
		if rawLogprobs, ok := fields["logprobs"]; ok && string(rawLogprobs) != "null" {
			return "", false, errors.New("unsupported logprobs")
		}
		index, err := openCodeFreeInt(fields["index"])
		if err != nil || index != 0 {
			return "", false, errors.New("invalid choice index")
		}
		var delta map[string]json.RawMessage
		if json.Unmarshal(fields["delta"], &delta) != nil || delta == nil {
			return "", false, errors.New("invalid delta")
		}
		var reason string
		if rawFinish, ok := fields["finish_reason"]; ok && string(rawFinish) != "null" {
			if reason, err = openCodeFreeString(rawFinish); err != nil {
				return "", false, errors.New("invalid finish reason")
			}
		}
		if v.finished {
			// Mimo may repeat the same finish alongside its usage-only frame.
			// Accept it once only when it contains no new output or tool semantics.
			if !v.haveUsage || reason != v.upstreamFinish || len(delta) > 2 {
				return "", false, errors.New("unexpected event after finish")
			}
			for key, val := range delta {
				switch key {
				case "role":
					if string(val) != "null" {
						role, err := openCodeFreeString(val)
						if err != nil || role != "assistant" {
							return "", false, errors.New("invalid role")
						}
					}
				case "content":
					content, err := openCodeFreeString(val)
					if err != nil || content != "" {
						return "", false, errors.New("unexpected content after finish")
					}
				default:
					return "", false, errors.New("unexpected event after finish")
				}
			}
			continue
		}
		if rawFn, ok := delta["function_call"]; ok && string(rawFn) != "null" {
			return "", false, errors.New("legacy function_call unsupported")
		}
		if reason == "function_call" {
			return "", false, errors.New("legacy function_call unsupported")
		}
		for key, val := range delta {
			switch key {
			case "role":
				if string(val) != "null" {
					role, err := openCodeFreeString(val)
					if err != nil || role != "assistant" {
						return "", false, errors.New("invalid role")
					}
				}
			case "content", "reasoning_content", "reasoning":
				if string(val) != "null" {
					if _, err := openCodeFreeString(val); err != nil {
						return "", false, errors.New("invalid delta text")
					}
				}
			case "reasoning_details":
				if err := validateOpenCodeFreeReasoningDetails(val, delta); err != nil {
					return "", false, err
				}
			case "tool_calls":
				if err := v.inspectTools(val, calls, reason); err != nil {
					return "", false, err
				}
			case "function_call":
			default:
				return "", false, fmt.Errorf("unsupported delta field %q", key)
			}
		}
		finalReason, err := v.inspectFinish(reason, calls)
		if err != nil {
			return "", false, err
		}
		if reason != "" {
			finish = finalReason
			upstreamFinish = reason
			sawFinish = true
		}
		deltas = append(deltas, delta)
	}
	if len(choiceFields) == 0 {
		return "", false, nil
	}
	v.calls = calls
	if rawID, ok := top["id"]; ok && string(rawID) != "null" {
		id, err := openCodeFreeString(rawID)
		if err != nil {
			return "", false, errors.New("invalid response id")
		}
		v.id = id
	}
	if rawCreated, ok := top["created"]; ok && string(rawCreated) != "null" {
		created, err := openCodeFreeInt(rawCreated)
		if err != nil || created < 0 {
			return "", false, errors.New("invalid created")
		}
		v.created = int64(created)
	}
	if rawModel, ok := top["model"]; ok && string(rawModel) != "null" && v.clientModel == "" {
		if observed, err := openCodeFreeString(rawModel); err == nil {
			v.model = observed
		}
	}
	delta := map[string]json.RawMessage{}
	if len(deltas) > 0 {
		delta = deltas[0]
	}
	if rawContent, ok := delta["content"]; ok && string(rawContent) != "null" {
		content, _ := openCodeFreeString(rawContent)
		v.text += content
	}
	if rawReasoning, ok := delta["reasoning_content"]; ok && string(rawReasoning) != "null" {
		content, _ := openCodeFreeString(rawReasoning)
		v.reasoning += content
	} else if rawReasoning, ok := delta["reasoning"]; ok && string(rawReasoning) != "null" {
		content, _ := openCodeFreeString(rawReasoning)
		v.reasoning += content
	}
	if sawFinish {
		v.finished = true
		v.finishReason = finish
		v.upstreamFinish = upstreamFinish
	}
	frame, err := v.rewrite(payload, delta)
	if err != nil {
		return "", false, err
	}
	if frame == "" {
		return "", false, nil
	}
	return frame, false, nil
}

func validateOpenCodeFreeReasoningDetails(raw json.RawMessage, delta map[string]json.RawMessage) error {
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) != 1 || parts[0] == nil {
		return errors.New("invalid reasoning details")
	}
	detail := parts[0]
	for key := range detail {
		switch key {
		case "type", "text", "format", "index":
		default:
			return errors.New("unsupported reasoning detail field")
		}
	}
	if kind, err := openCodeFreeString(detail["type"]); err != nil || kind != "reasoning.text" {
		return errors.New("invalid reasoning details")
	}
	if format, err := openCodeFreeString(detail["format"]); err != nil || format != "unknown" {
		return errors.New("invalid reasoning details")
	}
	if index, err := openCodeFreeInt(detail["index"]); err != nil || index != 0 {
		return errors.New("invalid reasoning details")
	}
	text, err := openCodeFreeString(detail["text"])
	if err != nil {
		return errors.New("invalid reasoning details")
	}
	reasoning, err := openCodeFreeString(delta["reasoning"])
	if err != nil || text != reasoning {
		return errors.New("conflicting reasoning details")
	}
	if rawContent, ok := delta["reasoning_content"]; ok && string(rawContent) != "null" {
		content, err := openCodeFreeString(rawContent)
		if err != nil || content != reasoning {
			return errors.New("conflicting reasoning details")
		}
	}
	return nil
}

// rewrite produces the withheld passthrough frame: tools, finish and usage stay
// private, model is rewritten to the client alias and duplicate reasoning
// details are stripped.
func (v *openCodeFreeSSEValidator) rewrite(payload string, delta map[string]json.RawMessage) (string, error) {
	frame := payload
	if v.clientModel != "" {
		rewritten, err := sjson.Set(frame, "model", v.clientModel)
		if err != nil {
			return "", errors.New("invalid response model")
		}
		frame = rewritten
	}
	if _, ok := delta["reasoning_details"]; ok {
		cleaned, err := sjson.Delete(frame, "choices.0.delta.reasoning_details")
		if err != nil {
			return "", errors.New("invalid delta")
		}
		frame = cleaned
	}
	if _, ok := delta["reasoning_content"]; ok {
		if _, has := delta["reasoning"]; has {
			cleaned, err := sjson.Delete(frame, "choices.0.delta.reasoning")
			if err != nil {
				return "", errors.New("invalid delta")
			}
			frame = cleaned
		}
	}
	if _, ok := delta["tool_calls"]; ok {
		cleaned, err := sjson.Delete(frame, "choices.0.delta.tool_calls")
		if err != nil {
			return "", errors.New("invalid delta")
		}
		frame = cleaned
	}
	frame, err := sjson.Set(frame, "choices.0.finish_reason", nil)
	if err != nil {
		return "", errors.New("invalid finish reason")
	}
	frame, err = sjson.Delete(frame, "usage")
	if err != nil {
		return "", errors.New("invalid usage")
	}
	hasDelta := false
	for _, val := range delta {
		if string(val) == "null" || string(val) == `""` {
			continue
		}
		hasDelta = true
	}
	callsReady := len(v.calls) > 0
	if callsReady {
		for index := 0; index < len(v.calls); index++ {
			call, ok := v.calls[index]
			if !ok || call.ID == "" {
				callsReady = false
				break
			}
			if _, declared := v.names[call.Function.Name]; !declared && !v.isInjected(call.Function.Name) {
				callsReady = false
				break
			}
		}
	}
	if !hasDelta && !callsReady {
		return "", nil
	}
	if !utf8.ValidString(frame) {
		return "", errors.New("invalid UTF-8")
	}
	return frame, nil
}

// finalEvents emits the withheld terminal records: tool calls, the real finish
// reason and the usage frame, followed by [DONE]. Only called after the whole
// stream, including EOF, is valid.
func (v *openCodeFreeSSEValidator) finalEvents(out io.Writer) error {
	id := v.id
	if id == "" {
		id = "chatcmpl-opencode-free"
	}
	created := v.created
	if created == 0 {
		created = v.started
	}
	base := fmt.Sprintf(`data: {"id":%s,"object":"chat.completion.chunk","created":%d,"model":%s`, openCodeFreeJSONString(id), created, openCodeFreeJSONString(v.model))
	var frames bytes.Buffer
	if len(v.calls) > 0 {
		calls := make([]openCodeFreeToolCallResult, 0, len(v.calls))
		for index := 0; index < len(v.calls); index++ {
			calls = append(calls, *v.calls[index])
		}
		encoded, err := json.Marshal(calls)
		if err != nil {
			return errors.New("cannot encode tool calls")
		}
		frames.WriteString(base)
		frames.WriteString(`,"choices":[{"index":0,"delta":{"tool_calls":`)
		frames.Write(encoded)
		frames.WriteString(`},"finish_reason":null}]}` + "\n\n")
	}
	frames.WriteString(base)
	finish, err := json.Marshal(v.finishReason)
	if err != nil {
		return errors.New("cannot encode finish reason")
	}
	frames.WriteString(`,"choices":[{"index":0,"delta":{},"finish_reason":`)
	frames.Write(finish)
	frames.WriteString(`}]}` + "\n\n")
	if v.haveUsage {
		frames.WriteString(base)
		frames.WriteString(`,"choices":[],"usage":`)
		frames.Write(v.usage)
		frames.WriteString("}\n\n")
	}
	frames.WriteString("data: [DONE]\n\n")
	if _, err := io.WriteString(out, frames.String()); err != nil {
		return err
	}
	if f, ok := out.(interface{ Flush() }); ok {
		f.Flush()
	}
	return nil
}

func (v *openCodeFreeSSEValidator) result() *openCodeFreeParsed {
	parsed := &openCodeFreeParsed{
		ID:            v.id,
		Text:          v.text,
		Reasoning:     v.reasoning,
		Finish:        v.finishReason,
		Usage:         v.rawUsageResult,
		RawUsage:      append(json.RawMessage(nil), v.usage...),
		BillingSource: v.billingResult,
	}
	for index := 0; index < len(v.calls); index++ {
		parsed.ToolCalls = append(parsed.ToolCalls, *v.calls[index])
	}
	return parsed
}

func openCodeFreeJSONString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// parseOpenCodeFreeSSE buffers each complete event before inspecting every
// choice. The terminal marker is emitted only after the stream, including EOF,
// is valid. Caller tools and tool history are preserved; tool calls are
// withheld until the whole stream validates.
func parseOpenCodeFreeSSE(ctx context.Context, src io.Reader, out io.Writer, maxResponse int64, maxEvent int, clientModel string, opts *openCodeFreeSSEOptions) (*openCodeFreeParsed, error) {
	if maxResponse <= 0 || maxEvent <= 0 {
		return nil, errors.New("invalid stream limits")
	}
	validator, err := newOpenCodeFreeSSEValidator(clientModel, opts)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(io.LimitReader(src, maxResponse+1))
	scanner.Buffer(make([]byte, 4096), maxEvent+1)
	var event bytes.Buffer
	var total int64
	var done bool
	emit := func() error {
		raw := event.String()
		event.Reset()
		var data []string
		var metadata strings.Builder
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSuffix(line, "\r")
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}
			key, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch key {
			case "data":
				data = append(data, value)
			case "event":
				if value != "" && value != "message" {
					return errors.New("unsupported SSE event")
				}
				_, _ = metadata.WriteString(line)
				_ = metadata.WriteByte('\n')
			case "id":
				if strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
					return errors.New("invalid SSE id")
				}
				_, _ = metadata.WriteString(line)
				_ = metadata.WriteByte('\n')
			case "retry":
			default:
				return fmt.Errorf("unsupported SSE field %q", key)
			}
		}
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		if done {
			return errors.New("event after done")
		}
		if payload == "[DONE]" {
			frame, finished, err := validator.parse(payload)
			if err != nil {
				return err
			}
			_ = frame
			done = finished
			return nil
		}
		if err := validateOpenCodeFreeJSON(payload); err != nil {
			return errors.New("invalid upstream JSON")
		}
		if !utf8.ValidString(payload) {
			return errors.New("invalid UTF-8")
		}
		frame, finished, err := validator.parse(payload)
		if err != nil {
			if opts == nil || !opts.QuietErrors {
				sample := payload
				if len(sample) > 500 {
					sample = sample[:500] + "..."
				}
				logger.L().Warn("opencode free: upstream frame rejected",
					zap.Error(err),
					zap.String("payload", sample),
				)
			}
			return err
		}
		if finished {
			done = true
			return nil
		}
		if frame == "" || out == nil {
			return nil
		}
		if _, err := io.WriteString(out, metadata.String()+"data: "+frame+"\n\n"); err != nil {
			return err
		}
		if f, ok := out.(interface{ Flush() }); ok {
			f.Flush()
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		total += int64(len(line) + 1)
		if total > maxResponse {
			return nil, errors.New("response limit exceeded")
		}
		if line == "" || line == "\r" {
			if err := emit(); err != nil {
				return nil, err
			}
			continue
		}
		if event.Len()+len(line)+1 > maxEvent {
			return nil, errors.New("event limit exceeded")
		}
		_, _ = event.WriteString(line)
		_ = event.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if event.Len() != 0 {
		return nil, errors.New("truncated event")
	}
	if !done || !validator.finished || !validator.haveUsage || validator.billingResult != "free" || ctx.Err() != nil {
		return nil, errors.New("incomplete stream")
	}
	if out != nil {
		if err := validator.finalEvents(out); err != nil {
			return nil, err
		}
	}
	return validator.result(), nil
}

package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/sjson"
)

type openCodeFreeParsed struct {
	ID            string
	Text          string
	Finish        string
	Usage         OpenAIUsage
	RawUsage      json.RawMessage
	BillingSource string
}

// parseOpenCodeFreeSSE buffers each complete event before inspecting every choice.
// The terminal marker is emitted only after the stream, including EOF, is valid.
func parseOpenCodeFreeSSE(ctx context.Context, src io.Reader, out io.Writer, maxResponse int64, maxEvent int, clientModel string) (*openCodeFreeParsed, error) {
	if maxResponse <= 0 || maxEvent <= 0 {
		return nil, errors.New("invalid stream limits")
	}
	scanner := bufio.NewScanner(io.LimitReader(src, maxResponse+1))
	scanner.Buffer(make([]byte, 4096), maxEvent+1)
	var event bytes.Buffer
	var total int64
	var result openCodeFreeParsed
	var done, stop, haveUsage bool
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
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
			switch key {
			case "data":
				data = append(data, value)
			case "event":
				if value != "" && value != "message" {
					return errors.New("unsupported SSE event")
				}
				metadata.WriteString(line)
				metadata.WriteByte('\n')
			case "id":
				if strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
					return errors.New("invalid SSE id")
				}
				metadata.WriteString(line)
				metadata.WriteByte('\n')
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
			done = true
			return nil
		}
		if err := validateOpenCodeFreeJSON(payload); err != nil {
			return errors.New("invalid upstream JSON")
		}
		if !utf8.ValidString(payload) {
			return errors.New("invalid UTF-8")
		}
		var v struct {
			ID      string          `json:"id"`
			Choices json.RawMessage `json:"choices"`
			Usage   json.RawMessage `json:"usage"`
			Error   json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &v); err != nil || len(v.Error) > 0 && string(v.Error) != "null" {
			return errors.New("invalid upstream event")
		}
		if len(v.Choices) == 0 || string(v.Choices) == "null" {
			return errors.New("missing choices")
		}
		var choiceFields []map[string]json.RawMessage
		if json.Unmarshal(v.Choices, &choiceFields) != nil {
			return errors.New("invalid choices")
		}
		for _, fields := range choiceFields {
			for k := range fields {
				if k != "index" && k != "delta" && k != "finish_reason" {
					return errors.New("unsupported choice field")
				}
			}
		}
		var choices []struct {
			Index  *int                       `json:"index"`
			Delta  map[string]json.RawMessage `json:"delta"`
			Finish *string                    `json:"finish_reason"`
		}
		if json.Unmarshal(v.Choices, &choices) != nil {
			return errors.New("invalid choices")
		}
		if len(choices) > 1 {
			return errors.New("unexpected choices")
		}
		for _, choice := range choices {
			if choice.Index == nil || *choice.Index != 0 || choice.Delta == nil || stop {
				return errors.New("invalid choice")
			}
			for k, val := range choice.Delta {
				switch k {
				case "role":
					var role string
					if json.Unmarshal(val, &role) != nil || role != "assistant" {
						return errors.New("invalid role")
					}
				case "reasoning", "reasoning_content":
					if string(val) != "null" {
						var reasoning string
						if json.Unmarshal(val, &reasoning) != nil {
							return errors.New("invalid reasoning")
						}
					}
				case "tool_calls", "function_call":
					if string(val) != "null" {
						return errors.New("unsupported tool call")
					}
				case "content":
					var text string
					if string(val) == "null" {
						continue
					}
					if json.Unmarshal(val, &text) != nil {
						return errors.New("invalid content")
					}
					result.Text += text
				default:
					return fmt.Errorf("unsupported delta field %q", k)
				}
			}
			if choice.Finish != nil {
				if *choice.Finish != "stop" {
					return errors.New("unsupported finish")
				}
				stop = true
				result.Finish = *choice.Finish
			}
		}
		if len(v.Usage) > 0 && string(v.Usage) != "null" {
			if haveUsage {
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
			if json.Unmarshal(v.Usage, &usage) != nil || usage.Prompt == nil || usage.Completion == nil || *usage.Prompt < 0 || *usage.Completion < 0 {
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
			if usage.OpenCode != nil {
				t := usage.OpenCode.Tokens
				if t == nil || t.Input == nil || t.Output == nil || t.Reasoning == nil || t.CacheRead == nil || t.CacheWrite5m == nil || t.CacheWrite1h == nil || *t.Input < 0 || *t.Output < 0 || *t.Reasoning < 0 || *t.CacheRead < 0 || *t.CacheWrite5m < 0 || *t.CacheWrite1h < 0 || *t.Output != *usage.Completion || *t.Reasoning > *t.Output {
					return errors.New("invalid OpenCode usage")
				}
				if usage.BillingSource != "" && usage.BillingSource != usage.OpenCode.BillingSource {
					return errors.New("conflicting billing source")
				}
				usage.BillingSource = usage.OpenCode.BillingSource
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
			}
			if read < 0 || create < 0 || read > *usage.Prompt || create > *usage.Prompt-read {
				return errors.New("invalid cache usage")
			}
			result.Usage = OpenAIUsage{InputTokens: *usage.Prompt, OutputTokens: *usage.Completion, CacheReadInputTokens: read, CacheCreationInputTokens: create}
			result.RawUsage = append(json.RawMessage(nil), v.Usage...)
			result.BillingSource = usage.BillingSource
			haveUsage = true
		}
		if v.ID != "" {
			if result.ID != "" && result.ID != v.ID {
				return errors.New("response ID changed")
			}
			result.ID = v.ID
		}
		if out != nil {
			if clientModel != "" {
				rewritten, err := sjson.Set(payload, "model", clientModel)
				if err != nil {
					return errors.New("invalid response model")
				}
				raw = metadata.String() + "data: " + rewritten + "\n"
			}
			if _, err := io.WriteString(out, raw+"\n"); err != nil {
				return err
			}
			if f, ok := out.(interface{ Flush() }); ok {
				f.Flush()
			}
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
		event.WriteString(line)
		event.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if event.Len() != 0 {
		return nil, errors.New("truncated event")
	}
	if !done || !stop || !haveUsage || result.BillingSource != "free" || ctx.Err() != nil {
		return nil, errors.New("incomplete stream")
	}
	if out != nil {
		if _, err := io.WriteString(out, "data: [DONE]\n\n"); err != nil {
			return nil, err
		}
		if f, ok := out.(interface{ Flush() }); ok {
			f.Flush()
		}
	}
	return &result, nil
}

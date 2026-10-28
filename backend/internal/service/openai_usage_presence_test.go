//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func usageReportedForTest(usage OpenAIUsage) bool {
	field := reflect.ValueOf(usage).FieldByName("Reported")
	return field.IsValid() && field.Bool()
}

func TestRawOpenAIUsagePresence(t *testing.T) {
	cases := []struct {
		name, body string
		want       bool
	}{
		{"integral exponent", `{"usage":{"input_tokens":0e0,"output_tokens":1.0}}`, true},
		{"responses zero", `{"usage":{"input_tokens":0,"output_tokens":0}}`, true},
		{"chat zero", `{"usage":{"prompt_tokens":0,"completion_tokens":0}}`, true},
		{"nested response", `{"response":{"usage":{"input_tokens":3,"output_tokens":1}}}`, true},
		{"absent", `{"output":[]}`, false},
		{"empty", `{"usage":{}}`, false},
		{"partial", `{"usage":{"input_tokens":1}}`, false},
		{"mixed", `{"usage":{"input_tokens":1,"completion_tokens":1}}`, false},
		{"negative", `{"usage":{"input_tokens":-1,"output_tokens":1}}`, false},
		{"fraction", `{"usage":{"input_tokens":1.5,"output_tokens":1}}`, false},
		{"string", `{"usage":{"input_tokens":"1","output_tokens":1}}`, false},
		{"conflicting objects", `{"usage":{"input_tokens":0,"output_tokens":0},"response":{"usage":{"input_tokens":1,"output_tokens":1}}}`, false},
		{"duplicate", `{"usage":{"input_tokens":0,"input_tokens":-1,"output_tokens":0}}`, false},
		{"text", `{"output":[{"text":"usage: input_tokens 2 output_tokens 1"}]}`, false},
		{"invalid json", `{"usage":{"input_tokens":0,"output_tokens":0}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usage, _ := extractOpenAIUsageFromJSONBytes([]byte(tc.body))
			if got := usageReportedForTest(usage); got != tc.want {
				t.Fatalf("reported = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOpenAIUsagePresenceStreamMerge(t *testing.T) {
	svc := &OpenAIGatewayService{}
	var usage OpenAIUsage
	svc.parseSSEUsage(`{"type":"response.output_text.delta","delta":"usage"}`, &usage)
	svc.parseSSEUsage(`{"type":"response.completed","response":{"usage":{"input_tokens":0,"output_tokens":0}}}`, &usage)
	if !usageReportedForTest(usage) {
		t.Fatal("zero usage on terminal frame must be reported")
	}
	svc.parseSSEUsage(`{"type":"response.completed","response":{"usage":{}}}`, &usage)
	if !usageReportedForTest(usage) {
		t.Fatal("malformed later usage must not erase reported usage")
	}
}

func TestRawChatForwardReportsUpstreamUsagePresence(t *testing.T) {
	cases := []struct {
		name, response string
		stream, want   bool
	}{
		{"json zero", `{"id":"chat1","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0}}`, false, true},
		{"json missing", `{"id":"chat1","choices":[]}`, false, false},
		{"json partial", `{"id":"chat1","choices":[],"usage":{"prompt_tokens":3}}`, false, false},
		{"stream zero", "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0}}\n\ndata: [DONE]\n\n", true, true},
		{"stream absent", "data: {\"choices\":[{\"delta\":{\"content\":\"usage\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", true, false},
	}
	gin.SetMode(gin.TestMode)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reqBody := []byte(fmt.Sprintf(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":%v}`, tc.stream))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(reqBody))
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.response))}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			result, err := svc.forwardAsRawChatCompletions(context.Background(), c, rawChatCompletionsTestAccount(), reqBody, "")
			if err != nil {
				t.Fatal(err)
			}
			field := reflect.ValueOf(result).Elem().FieldByName("UsageReported")
			got := field.IsValid() && field.Bool()
			if got != tc.want {
				t.Fatalf("UsageReported = %v, want %v", got, tc.want)
			}
		})
	}
}

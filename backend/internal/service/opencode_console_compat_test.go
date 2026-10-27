//go:build unit

package service

import (
	"errors"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestLegacyOpenCodeZenBaseRoutesAllProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, base := range []string{"https://opencode.ai/zen/v1", "https://opencode.ai/zen"} {
		for _, tc := range []struct{ ingress, model, want string }{
			{"chat", "glm-5.3", "https://opencode.ai/zen/v1/chat/completions"},
			{"responses", "gpt-5.6-luna", "https://opencode.ai/zen/v1/responses"},
			{"messages", "claude-sonnet-4", "https://opencode.ai/zen/v1/messages"},
		} {
			t.Run(base+"/"+tc.ingress, func(t *testing.T) {
				ingress := routingMatrixIngresses()
				for _, in := range ingress {
					if in.name != tc.ingress {
						continue
					}
					upstream := &httpUpstreamRecorder{err: errors.New("capture upstream")}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
					route := routingMatrixCase{ingress: in, model: tc.model}
					body := route.body()
					c := adaptiveProtocolTestContext(in.path, body)
					account := &Account{ID: 801, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{
						"api_key": "key", "account_mode": AccountModeZen, "api_protocol": APIProtocolAdaptive, "base_url": base,
					}}
					_ = in.forward(svc, c, account, body)
					require.NotNil(t, upstream.lastReq)
					require.Equal(t, tc.want, upstream.lastReq.URL.String())
				}
			})
		}
	}
}

func TestMuseWonderZenRoutesAllIngressesToResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, in := range routingMatrixIngresses() {
		if in.name == "chat_responses_shape" {
			continue
		}
		t.Run(in.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{err: errors.New("capture upstream")}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			route := routingMatrixCase{ingress: in, model: "muse-wonder-1.0"}
			body := route.body()
			c := adaptiveProtocolTestContext(in.path, body)
			account := &Account{ID: 801, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "key", "account_mode": AccountModeZen, "api_protocol": APIProtocolAdaptive,
			}}
			_ = in.forward(svc, c, account, body)
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, "https://opencode.ai/inference/openai/v1/responses", upstream.lastReq.URL.String())
		})
	}
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"account_mode": AccountModeZen}}
	require.Equal(t, APIProtocolChatCompletions, account.resolveModelRoutedProtocol("muse-wonder-future"))
}

func TestLegacyZenPartialProtocolURLsPreserveExplicitAndMissingTargets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name      string
		addresses map[string]any
		chat      string
		responses string
		anthropic string
	}{
		{"chat only and empty anthropic", map[string]any{APIProtocolChatCompletions: "https://opencode.ai/zen/v1", APIProtocolAnthropic: ""}, "https://opencode.ai/zen/v1/chat/completions", "https://opencode.ai/zen/v1/responses", "https://opencode.ai/zen/v1/messages"},
		{"explicit anthropic", map[string]any{APIProtocolAnthropic: "https://relay.example.com/anthropic", APIProtocolResponses: " "}, "https://opencode.ai/zen/v1/chat/completions", "https://opencode.ai/zen/v1/responses", "https://relay.example.com/anthropic/v1/messages"},
		{"explicit responses", map[string]any{APIProtocolResponses: "https://relay.example.com/v1"}, "https://opencode.ai/zen/v1/chat/completions", "https://relay.example.com/v1/responses", "https://opencode.ai/zen/v1/messages"},
	}
	for _, tc := range cases {
		for _, test := range []struct{ ingress, model, want string }{
			{"chat", "glm-5.3", tc.chat}, {"responses", "gpt-5.6-luna", tc.responses}, {"messages", "claude-sonnet-4", tc.anthropic},
		} {
			t.Run(tc.name+"/"+test.ingress, func(t *testing.T) {
				for _, in := range routingMatrixIngresses() {
					if in.name != test.ingress {
						continue
					}
					upstream := &httpUpstreamRecorder{err: errors.New("capture upstream")}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
					body := (routingMatrixCase{ingress: in, model: test.model}).body()
					c := adaptiveProtocolTestContext(in.path, body)
					account := &Account{ID: 801, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{
						"api_key": "key", "account_mode": AccountModeZen, "api_protocol": APIProtocolAdaptive,
						"base_url": "https://opencode.ai/zen/v1", "api_base_urls": tc.addresses,
					}}
					_ = in.forward(svc, c, account, body)
					require.NotNil(t, upstream.lastReq)
					require.Equal(t, test.want, upstream.lastReq.URL.String())
				}
			})
		}
	}
}

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const openCodeFreeRetryStateKey = "opencode_free_runtime_retry_state"
const openCodeFreeRuntimeRetryDelay = 500 * time.Millisecond

// This budget belongs to the client request, not to an account. After a
// runtime failure is retried on A, the handler may select B for one final
// attempt. B cannot reset the budget, even if it fails with quota or empty output.
type openCodeFreeRetryState struct {
	finalReplacementPending bool
	exhausted               bool
}

// Runtime errors are held before writing a client response so a safe retry can
// occur. Pre-dispatch validation/accounting errors still fail closed directly.
type openCodeFreeRuntimeError struct {
	status  int
	message string
	headers http.Header
	body    []byte
}

func (e *openCodeFreeRuntimeError) Error() string { return e.message }

func (e *openCodeFreeRuntimeError) write(c *gin.Context) error {
	if len(e.body) == 0 {
		return freeReject(c, e.status, e.message)
	}
	if c.Writer.Written() {
		var compact bytes.Buffer
		if err := json.Compact(&compact, e.body); err != nil {
			return freeReject(c, http.StatusBadGateway, "OpenCode free upstream failed")
		}
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", compact.Bytes())
		c.Writer.Flush()
	} else {
		c.Header("Content-Type", "application/json; charset=utf-8")
		if retry := e.headers.Get("Retry-After"); retry != "" {
			c.Header("Retry-After", openCodeFreeClientRetryAfter(retry))
		}
		c.Data(e.status, "application/json", e.body)
	}
	MarkResponseCommitted(c)
	return e
}

func (e *openCodeFreeRuntimeError) failover(c *gin.Context) *UpstreamFailoverError {
	body := e.body
	if len(body) == 0 {
		body, _ = json.Marshal(gin.H{"error": gin.H{"type": "upstream_error", "message": e.message}})
	}
	setOpsUpstreamError(c, e.status, e.message, "")
	return &UpstreamFailoverError{StatusCode: e.status, ResponseBody: body, ResponseHeaders: e.headers, Reason: "opencode_free_runtime_retry_exhausted"}
}

func (s *OpenAIGatewayService) forwardOpenCodeFree(ctx context.Context, c *gin.Context, a *Account, body []byte, defaultModel string) (*OpenAIForwardResult, error) {
	var state *openCodeFreeRetryState
	if value, ok := c.Get(openCodeFreeRetryStateKey); ok {
		state, _ = value.(*openCodeFreeRetryState)
	}
	if state == nil {
		state = &openCodeFreeRetryState{}
		c.Set(openCodeFreeRetryStateKey, state)
	}
	if state.exhausted {
		return nil, freeReject(c, http.StatusBadGateway, "OpenCode free retry budget exhausted")
	}
	finalReplacement := state.finalReplacementPending
	if finalReplacement {
		state.finalReplacementPending = false
		state.exhausted = true
	}
	clientCtx := ctx
	if c.Request != nil {
		clientCtx = c.Request.Context()
	}
	for retry := 0; ; retry++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := clientCtx.Err(); err != nil {
			return nil, err
		}
		result, err := s.forwardOpenCodeFreeAttempt(ctx, c, a, body, defaultModel)
		if err == nil {
			return result, nil
		}
		if !c.Writer.Written() {
			// Let the next attempt or terminal handler choose its MIME. A
			// held parser can fail after staging text/event-stream headers.
			c.Writer.Header().Del("Content-Type")
		}
		var failover *UpstreamFailoverError
		if errors.As(err, &failover) {
			if finalReplacement {
				failover.NextAccountAction = NextAccountStop
				failover.RetryableOnSameAccount = false
			} else if retry > 0 {
				// The same-account retry failed with quota/empty output: the
				// replacement is still the last attempt, not a fresh chain.
				state.finalReplacementPending = true
			}
			return nil, err
		}
		var failure *openCodeFreeRuntimeError
		if !errors.As(err, &failure) {
			return nil, err
		}
		if c.Writer.Written() || IsResponseCommitted(c) || finalReplacement {
			return nil, failure.write(c)
		}
		if ctx.Err() != nil || clientCtx.Err() != nil {
			return nil, err
		}
		if retry > 0 {
			state.finalReplacementPending = true
			return nil, failure.failover(c)
		}
		logger.L().Warn("opencode free: retrying same instance once",
			zap.Int64("account_id", a.ID), zap.Int("status", failure.status))
		timer := time.NewTimer(openCodeFreeRuntimeRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-clientCtx.Done():
			timer.Stop()
			return nil, clientCtx.Err()
		case <-timer.C:
		}
	}
}

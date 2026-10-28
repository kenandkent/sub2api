package handler

import (
	"bytes"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const marketplaceMaxBufferedResponse = 8 << 20

var errMarketplaceResponseTooLarge = errors.New("shared response buffer limit exceeded")

// marketplaceResponseWriter holds unary output and the terminal streaming event
// until settlement. Complete nonterminal SSE events are forwarded immediately.
type marketplaceResponseWriter struct {
	gin.ResponseWriter
	streaming bool
	pending   []byte
	terminal  bool
	failed    bool
	status    int
	size      int
}

func newMarketplaceResponseWriter(w gin.ResponseWriter, streaming bool) *marketplaceResponseWriter {
	return &marketplaceResponseWriter{ResponseWriter: w, streaming: streaming, status: 200, size: -1}
}
func (w *marketplaceResponseWriter) Status() int   { return w.status }
func (w *marketplaceResponseWriter) Size() int     { return w.size }
func (w *marketplaceResponseWriter) Written() bool { return w.size >= 0 }
func (w *marketplaceResponseWriter) WriteHeader(code int) {
	if !w.Written() && code > 0 {
		w.status = code
	}
}
func (w *marketplaceResponseWriter) WriteHeaderNow() {
	if w.size < 0 {
		w.size = 0
	}
}
func (w *marketplaceResponseWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *marketplaceResponseWriter) Write(p []byte) (int, error) {
	if w.failed {
		return 0, errMarketplaceResponseTooLarge
	}
	if len(p) > marketplaceMaxBufferedResponse-len(w.pending) {
		w.failed = true
		return 0, errMarketplaceResponseTooLarge
	}
	w.WriteHeaderNow()
	w.size += len(p)
	w.pending = append(w.pending, p...)
	if !w.streaming || w.terminal {
		return len(p), nil
	}
	for {
		end := bytes.Index(w.pending, []byte("\n\n"))
		separator := 2
		crlf := bytes.Index(w.pending, []byte("\r\n\r\n"))
		if crlf >= 0 && (end < 0 || crlf < end) {
			end = crlf
			separator = 4
		}
		if end < 0 {
			break
		}
		end += separator
		event := w.pending[:end]
		if marketplaceTerminalSSE(event) {
			w.terminal = true
			break
		}
		w.ResponseWriter.WriteHeader(w.status)
		if _, err := w.ResponseWriter.Write(event); err != nil {
			return 0, err
		}
		w.pending = w.pending[end:]
	}
	return len(p), nil
}
func marketplaceTerminalSSE(event []byte) bool {
	for _, line := range strings.Split(string(event), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "event:") {
			t := strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			if t == "response.completed" || t == "response.failed" || t == "response.incomplete" || t == "response.done" {
				return true
			}
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return true
		}
		t := gjson.Get(data, "type").String()
		if t == "response.completed" || t == "response.failed" || t == "response.incomplete" || t == "response.done" {
			return true
		}
		for _, choice := range gjson.Get(data, "choices").Array() {
			finish := choice.Get("finish_reason")
			if finish.Exists() && finish.Type != gjson.Null {
				return true
			}
		}
	}
	return false
}
func (w *marketplaceResponseWriter) Flush() {
	if w.streaming && w.ResponseWriter.Written() {
		w.ResponseWriter.Flush()
	}
}
func (w *marketplaceResponseWriter) Finish(commit bool) error {
	if !commit || w.failed {
		w.pending = nil
		if w.failed {
			return errMarketplaceResponseTooLarge
		}
		return nil
	}
	w.ResponseWriter.WriteHeader(w.status)
	if len(w.pending) > 0 {
		if _, err := w.ResponseWriter.Write(w.pending); err != nil {
			return err
		}
	}
	w.pending = nil
	if w.streaming {
		w.ResponseWriter.Flush()
	}
	return nil
}

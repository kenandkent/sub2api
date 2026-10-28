package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsDeadlinePool struct{ value any }

func (p *opsDeadlinePool) Get() any  { v := p.value; p.value = nil; return v }
func (p *opsDeadlinePool) Put(v any) { p.value = v }

type opsDeadlineSpy struct {
	gin.ResponseWriter
	mu        sync.Mutex
	deadlines []time.Time
	entered   chan struct{}
	release   chan struct{}
}

func (w *opsDeadlineSpy) SetWriteDeadline(d time.Time) error {
	if w.entered != nil {
		close(w.entered)
		<-w.release
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadlines = append(w.deadlines, d)
	return nil
}
func TestOpsWriterWriteDeadlineUsesActiveLease(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	pool := &opsDeadlinePool{}
	firstTarget := &opsDeadlineSpy{ResponseWriter: c.Writer}
	old := acquireOpsCaptureWriterFromPool(pool, firstTarget)
	deadline := time.Now().Add(time.Second)
	require.NoError(t, http.NewResponseController(old).SetWriteDeadline(deadline))
	releaseOpsCaptureWriter(old)
	secondTarget := &opsDeadlineSpy{ResponseWriter: c.Writer}
	current := acquireOpsCaptureWriterFromPool(pool, secondTarget)
	defer releaseOpsCaptureWriter(current)
	require.Error(t, http.NewResponseController(old).SetWriteDeadline(time.Time{}), "stale handle must not affect reused writer")
	require.Empty(t, secondTarget.deadlines)
	require.NoError(t, http.NewResponseController(current).SetWriteDeadline(deadline))
	require.Len(t, secondTarget.deadlines, 1)
}
func TestOpsWriterReleaseWaitsForDeadlineDelegation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	target := &opsDeadlineSpy{ResponseWriter: c.Writer, entered: make(chan struct{}), release: make(chan struct{})}
	w := acquireOpsCaptureWriter(target)
	completed := make(chan error, 1)
	go func() { completed <- http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second)) }()
	select {
	case <-target.entered:
	case <-time.After(time.Second):
		t.Fatal("deadline delegation was not invoked")
	}
	released := make(chan struct{})
	go func() { releaseOpsCaptureWriter(w); close(released) }()
	select {
	case <-released:
		t.Fatal("writer released during delegated deadline operation")
	case <-time.After(50 * time.Millisecond):
	}
	close(target.release)
	require.NoError(t, <-completed)
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("release did not finish")
	}
}

func TestMarketplaceDeadlineClearsBeforeOpsWriterRelease(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	target := &opsDeadlineSpy{ResponseWriter: c.Writer}
	w := acquireOpsCaptureWriter(target)
	c.Writer = w
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := marketplaceBoundClientWrites(c, ctx)
	require.NoError(t, err)
	release()
	require.Len(t, target.deadlines, 2)
	require.False(t, target.deadlines[0].IsZero())
	require.True(t, target.deadlines[1].IsZero())
	releaseOpsCaptureWriter(w)
	require.Error(t, http.NewResponseController(w).SetWriteDeadline(time.Time{}))
}

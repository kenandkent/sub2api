package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMarketplaceGatewayWriterHoldsUntilSettlement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Run("non-stream", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		w := newMarketplaceResponseWriter(c.Writer, false)
		_, err := w.Write([]byte(`{"ok":true}`))
		require.NoError(t, err)
		require.Empty(t, rec.Body.String())
		require.NoError(t, w.Finish(true))
		require.Equal(t, `{"ok":true}`, rec.Body.String())
	})
	t.Run("stream terminal", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		w := newMarketplaceResponseWriter(c.Writer, true)
		_, err := w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\n"))
		require.NoError(t, err)
		require.Contains(t, rec.Body.String(), "content")
		_, err = w.Write([]byte("data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n"))
		require.NoError(t, err)
		_, err = w.Write([]byte("\ndata: [DONE]\n\n"))
		require.NoError(t, err)
		require.NotContains(t, rec.Body.String(), "finish_reason\":\"stop")
		require.NoError(t, w.Finish(true))
		require.Contains(t, rec.Body.String(), "finish_reason\":\"stop")
		require.Contains(t, rec.Body.String(), "[DONE]")
	})
	t.Run("failure", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		w := newMarketplaceResponseWriter(c.Writer, false)
		_, err := w.Write([]byte("secret response"))
		require.NoError(t, err)
		require.NoError(t, w.Finish(false))
		require.Empty(t, rec.Body.String())
	})
}

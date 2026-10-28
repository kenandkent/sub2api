//go:build unit && integration

package handler

import (
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"time"
)

// In-memory recorders cannot set socket deadlines; network test servers still
// delegate to their real response controller.
type marketplaceDeadlineTestWriter struct{ gin.ResponseWriter }

func (w *marketplaceDeadlineTestWriter) SetWriteDeadline(deadline time.Time) error {
	err := http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline)
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

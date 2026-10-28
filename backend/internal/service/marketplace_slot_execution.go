package service

import (
	"context"
)

// WithMarketplaceSlotBoundExecution must be called before selecting or
// acquiring a shared-account slot. Redis timestamps slots in whole seconds;
// reserve 31 seconds for that rounding, settlement and slot release. The
// detached upstream execution can outlive a disconnected client but cannot
// outlive the slot-safe deadline or a preexisting execution deadline.
func WithMarketplaceSlotBoundExecution(ctx context.Context, requestedSeconds, slotTTLMinutes int) (context.Context, context.CancelFunc) {
	if slotTTLMinutes <= 0 {
		failed, cancel := context.WithCancel(ctx)
		cancel()
		return failed, func() {}
	}
	seconds := 300
	if requestedSeconds > 0 && requestedSeconds <= 3600 {
		seconds = requestedSeconds
	}
	// Avoid overflowing an int or time.Duration for extreme configuration.
	if slotTTLMinutes <= 60 {
		safeSeconds := slotTTLMinutes*60 - 31
		if safeSeconds <= 0 {
			failed, cancel := context.WithCancel(ctx)
			cancel()
			return failed, func() {}
		}
		if safeSeconds < seconds {
			seconds = safeSeconds
		}
	}
	previousExecution := marketplaceExecutionContext(ctx)
	wrapped, cancelExecution := WithMarketplaceExecution(ctx, seconds)
	execution := marketplaceExecutionContext(wrapped)
	deadline, _ := execution.Deadline()
	if previous, ok := ctx.Deadline(); ok && previous.Before(deadline) {
		deadline = previous
	}
	if previousExecution != nil {
		if previous, ok := previousExecution.Deadline(); ok && previous.Before(deadline) {
			deadline = previous
		}
	}
	boundedExecution, cancelBounded := context.WithDeadline(execution, deadline)
	var stopPrevious func() bool
	if previousExecution != nil {
		stopPrevious = context.AfterFunc(previousExecution, cancelBounded)
	}
	boundedRequest, cancelRequest := context.WithDeadline(context.WithValue(wrapped, marketplaceExecutionContextKey{}, boundedExecution), deadline)
	return boundedRequest, func() {
		if stopPrevious != nil {
			stopPrevious()
		}
		cancelRequest()
		cancelBounded()
		cancelExecution()
	}
}

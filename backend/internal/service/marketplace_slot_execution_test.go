package service

import (
	"context"
	"testing"
	"time"
)

func TestMarketplaceSlotBoundExecutionDeadline(t *testing.T) {
	for _, tc := range []struct {
		name           string
		requested, ttl int
		upper          time.Duration
	}{
		{"one-minute-slot", 3600, 1, 29 * time.Second},
		{"default-timeout", 0, 30, 5 * time.Minute},
		{"long-timeout", 3600, 30, 1769 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			ctx, cancel := WithMarketplaceSlotBoundExecution(context.Background(), tc.requested, tc.ttl)
			defer cancel()
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("missing request deadline")
			}
			execution := marketplaceExecutionContext(ctx)
			if execution == nil {
				t.Fatal("missing detached execution context")
			}
			ed, ok := execution.Deadline()
			if !ok {
				t.Fatal("missing execution deadline")
			}
			if deadline.After(start.Add(tc.upper+20*time.Millisecond)) || ed.After(start.Add(tc.upper+20*time.Millisecond)) {
				t.Fatalf("deadline exceeds slot-safe limit: request=%s execution=%s", deadline.Sub(start), ed.Sub(start))
			}
			if deadline.Before(start.Add(tc.upper-time.Second)) || ed.Before(start.Add(tc.upper-time.Second)) {
				t.Fatalf("deadline unexpectedly early")
			}
		})
	}
}

func TestMarketplaceSlotBoundExecutionPreservesEarlierDeadlines(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancelParent()
	parentDeadline, _ := parent.Deadline()
	ctx, cancel := WithMarketplaceSlotBoundExecution(parent, 3600, 30)
	defer cancel()
	ed, _ := marketplaceExecutionContext(ctx).Deadline()
	if ed.After(parentDeadline) {
		t.Fatal("detached context extended parent deadline")
	}
	nested, cancelNested := WithMarketplaceSlotBoundExecution(ctx, 3600, 30)
	defer cancelNested()
	nd, _ := marketplaceExecutionContext(nested).Deadline()
	if nd.After(ed) {
		t.Fatal("nested execution extended the original deadline")
	}
}

func TestMarketplaceSlotBoundExecutionNestedCancellationAndValues(t *testing.T) {
	type valueKey struct{}
	parent := context.WithValue(context.Background(), valueKey{}, "kept")
	first, cancelFirst := WithMarketplaceSlotBoundExecution(parent, 30, 2)
	second, cancelSecond := WithMarketplaceSlotBoundExecution(first, 60, 2)
	defer cancelSecond()
	execution := marketplaceExecutionContext(second)
	if execution.Value(valueKey{}) != "kept" {
		t.Fatal("execution lost request values")
	}
	cancelFirst()
	select {
	case <-execution.Done():
	case <-time.After(time.Second):
		t.Fatal("nested execution outlived prior execution cancellation")
	}
}

func TestMarketplaceSlotBoundExecutionRejectsInvalidTTL(t *testing.T) {
	for _, ttl := range []int{0, -1} {
		ctx, cancel := WithMarketplaceSlotBoundExecution(context.Background(), 300, ttl)
		if ctx.Err() == nil {
			t.Fatalf("ttl %d left request active", ttl)
		}
		cancel()
	}
}

func TestMarketplaceSlotBoundExecutionDetachesClientButCancellationEndsExecution(t *testing.T) {
	parent, cancelClient := context.WithCancel(context.Background())
	ctx, cancel := WithMarketplaceSlotBoundExecution(parent, 60, 2)
	execution := marketplaceExecutionContext(ctx)
	cancelClient()
	if execution.Err() != nil {
		t.Fatal("client cancellation stopped detached execution")
	}
	cancel()
	if execution.Err() == nil {
		t.Fatal("execution cancel did not end detached execution")
	}
}

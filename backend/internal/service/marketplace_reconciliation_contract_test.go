package service

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReconciliationPublishesBacklogAndBuyerLedgerMetrics(t *testing.T) {
	data, err := json.Marshal(ReconciliationTotals{})
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(data, &fields))
	for _, key := range []string{"buyer_ledger_differences", "cache_backlog", "cache_oldest_age_seconds", "maturity_backlog", "maturity_oldest_age_seconds"} {
		require.Contains(t, fields, key)
	}
}

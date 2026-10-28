package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSupplierStatementPublishesDebtSnapshot(t *testing.T) {
	data, err := json.Marshal(SupplierStatement{})
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(data, &fields))
	for _, key := range []string{"opening_debt", "closing_debt", "debt_offset"} {
		require.Contains(t, fields, key)
	}
	data, err = json.Marshal(SupplierStatementEntry{})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &fields))
	require.Contains(t, fields, "supplier_debt_offset")
}

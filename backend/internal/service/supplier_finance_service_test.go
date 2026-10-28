package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSupplierCSVCellEscapesFormula(t *testing.T) {
	for _, s := range []string{"=1+1", "+1", "-1", "@cmd", "  =1", "\t@cmd"} {
		require.Equal(t, "'"+s, SupplierCSVCell(s))
	}
	require.Equal(t, "normal", SupplierCSVCell("normal"))
}

func TestStatementMonthValidation(t *testing.T) {
	_, err := StatementMonth("2026-02", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	_, err = StatementMonth("2026-03", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	require.Error(t, err)
	_, err = StatementMonth("2026-2", time.Now())
	require.Error(t, err)
}

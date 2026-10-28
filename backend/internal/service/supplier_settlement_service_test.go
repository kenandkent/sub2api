package service

import (
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMaturityAmountRespectsDebtOffsetAndRefunds(t *testing.T) {
	d := decimal.RequireFromString
	require.True(t, MaturityAmount(d("10"), d("3"), d("2")).Equal(d("5")))
	require.True(t, MaturityAmount(d("10"), d("3"), d("8")).IsZero())
}

func TestCumulativeRefundRoundsOnceAndClosesTail(t *testing.T) {
	d := decimal.RequireFromString
	net, fee, err := CumulativeRefund(d("3"), d("2"), d("2"))
	require.NoError(t, err)
	require.True(t, net.Equal(d("1.33333333")))
	require.True(t, fee.Equal(d("0.66666667")))
	net, fee, err = CumulativeRefund(d("3"), d("2"), d("3"))
	require.NoError(t, err)
	require.True(t, net.Equal(d("2")))
	require.True(t, fee.Equal(d("1")))
	_, _, err = CumulativeRefund(d("0"), d("0"), d("1"))
	require.Error(t, err)
}

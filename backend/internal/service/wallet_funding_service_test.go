package service

import (
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWalletFundingPlanEligibleFIFO(t *testing.T) {
	d := decimal.RequireFromString
	lots := []FundingLot{{ID: 3, Kind: FundingCashPaid, Remaining: d("2.00000001")}, {ID: 1, Kind: FundingPromotional, Remaining: d("7")}, {ID: 2, Kind: FundingSubsidy, Remaining: d("1"), Reserved: d("0.25")}}
	got, err := PlanFundingConsumption(lots, d("1.25000001"), true)
	require.NoError(t, err)
	require.Equal(t, []FundingPortion{{LotID: 2, Amount: d("0.75")}, {LotID: 3, Amount: d("0.50000001")}}, got)
	_, err = PlanFundingConsumption(lots, d("2.75000002"), true)
	require.ErrorIs(t, err, ErrInsufficientFunding)
}

func TestWalletFundingPlanSelfOwnedPriorityAndReserved(t *testing.T) {
	d := decimal.RequireFromString
	lots := []FundingLot{{ID: 1, Kind: FundingCashPaid, Remaining: d("3")}, {ID: 2, Kind: FundingPromotional, Remaining: d("2"), Reserved: d("1")}, {ID: 3, Kind: FundingLegacy, Remaining: d("1")}}
	got, err := PlanFundingConsumption(lots, d("2.5"), false)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, []int64{2, 3, 1}, []int64{got[0].LotID, got[1].LotID, got[2].LotID})
	require.True(t, got[0].Amount.Equal(d("1")))
	require.True(t, got[1].Amount.Equal(d("1")))
	require.True(t, got[2].Amount.Equal(d("0.5")))
}

func TestWalletFundingDebtPaymentAndPrecision(t *testing.T) {
	d := decimal.RequireFromString
	paid, available := FundingApplyDebt(d("1.00000001"), d("0.600000005"))
	require.True(t, paid.Equal(d("0.60000001")))
	require.True(t, available.Equal(d("0.4")))
}

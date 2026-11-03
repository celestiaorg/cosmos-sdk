package gov_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	sdklog "cosmossdk.io/log"
	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktestutil "github.com/cosmos/cosmos-sdk/x/bank/testutil"
	"github.com/cosmos/cosmos-sdk/x/gov"
	"github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

func TestImportExportQueues_ErrorUnconsistentState(t *testing.T) {
	suite := createTestSuite(t)
	app := suite.App
	ctx := app.BaseApp.NewContext(false)
	require.Panics(t, func() {
		gov.InitGenesis(ctx, suite.AccountKeeper, suite.BankKeeper, suite.GovKeeper, &v1.GenesisState{
			Deposits: v1.Deposits{
				{
					ProposalId: 1234,
					Depositor:  "me",
					Amount: sdk.Coins{
						sdk.NewCoin(
							"stake",
							sdkmath.NewInt(1234),
						),
					},
				},
			},
		})
	})
	gov.InitGenesis(ctx, suite.AccountKeeper, suite.BankKeeper, suite.GovKeeper, v1.DefaultGenesisState())
	genState, err := gov.ExportGenesis(ctx, suite.GovKeeper)
	require.NoError(t, err)
	require.Equal(t, genState, v1.DefaultGenesisState())
}

func genesisWithDeposit(amount sdk.Coins) *v1.GenesisState {
	genState := v1.DefaultGenesisState()
	genState.Deposits = v1.Deposits{
		{ProposalId: 1, Depositor: sdk.AccAddress(pubkeys[0].Address()).String(), Amount: amount},
	}
	return genState
}

func TestInitGenesis_BalanceSurplus(t *testing.T) {
	suite := createTestSuite(t)
	var logs bytes.Buffer
	ctx := suite.App.BaseApp.NewContext(false).WithLogger(sdklog.NewLogger(&logs, sdklog.OutputJSONOption()))

	balance := sdk.NewCoins(sdk.NewInt64Coin("stake", 1000))
	require.NoError(t, banktestutil.FundModuleAccount(ctx, suite.BankKeeper, types.ModuleName, balance))

	require.NotPanics(t, func() {
		gov.InitGenesis(ctx, suite.AccountKeeper, suite.BankKeeper, suite.GovKeeper,
			genesisWithDeposit(sdk.NewCoins(sdk.NewInt64Coin("stake", 400))))
	})
	require.Contains(t, logs.String(), `"surplus":"600stake"`)
}

func TestInitGenesis_BalanceDeficit(t *testing.T) {
	suite := createTestSuite(t)
	ctx := suite.App.BaseApp.NewContext(false)

	balance := sdk.NewCoins(sdk.NewInt64Coin("stake", 100))
	require.NoError(t, banktestutil.FundModuleAccount(ctx, suite.BankKeeper, types.ModuleName, balance))

	require.PanicsWithValue(t, "expected module account was 100stake but we got 400stake", func() {
		gov.InitGenesis(ctx, suite.AccountKeeper, suite.BankKeeper, suite.GovKeeper,
			genesisWithDeposit(sdk.NewCoins(sdk.NewInt64Coin("stake", 400))))
	})
}

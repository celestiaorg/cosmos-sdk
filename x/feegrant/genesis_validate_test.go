package feegrant_test

import (
	"testing"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	storetypes "cosmossdk.io/store/types"
	"cosmossdk.io/x/feegrant"

	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// TestValidateGenesisAcceptsSpentPeriodicDenom spends a denom of a periodic
// allowance to zero with Accept and checks that the stored allowance, which
// ValidateBasic rejects, still passes genesis validation.
func TestValidateGenesisAcceptsSpentPeriodicDenom(t *testing.T) {
	key := storetypes.NewKVStoreKey(feegrant.StoreKey)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	now := time.Now()
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Time: now})

	allowance := &feegrant.PeriodicAllowance{
		Basic: feegrant.BasicAllowance{
			SpendLimit: sdk.NewCoins(sdk.NewInt64Coin("utia", 10), sdk.NewInt64Coin("uother", 5)),
		},
		Period:           time.Hour,
		PeriodSpendLimit: sdk.NewCoins(sdk.NewInt64Coin("utia", 10)),
		PeriodCanSpend:   sdk.NewCoins(sdk.NewInt64Coin("utia", 10)),
		PeriodReset:      now.Add(time.Hour),
	}
	require.NoError(t, allowance.ValidateBasic())

	remove, err := allowance.Accept(ctx, sdk.NewCoins(sdk.NewInt64Coin("utia", 10)), nil)
	require.NoError(t, err)
	require.False(t, remove, "the allowance is stored again, not removed")
	require.Equal(t, sdk.NewCoins(sdk.NewInt64Coin("uother", 5)), allowance.Basic.SpendLimit)
	require.Error(t, allowance.ValidateBasic(), "a grant message with this allowance stays invalid")

	granter, grantee := sdk.AccAddress("granter_____________"), sdk.AccAddress("grantee_____________")
	filtered := &feegrant.AllowedMsgAllowance{AllowedMessages: []string{"/cosmos.bank.v1beta1.MsgSend"}}
	require.NoError(t, filtered.SetAllowance(allowance))

	for name, a := range map[string]feegrant.FeeAllowanceI{"periodic": allowance, "filtered periodic": filtered} {
		t.Run(name, func(t *testing.T) {
			grant, err := feegrant.NewGrant(granter, grantee, a)
			require.NoError(t, err)
			require.NoError(t, feegrant.ValidateGenesis(feegrant.GenesisState{Allowances: []feegrant.Grant{grant}}))
		})
	}

	// Every other ValidateBasic rule still applies in genesis.
	invalid := *allowance
	invalid.PeriodSpendLimit = sdk.Coins{}
	grant, err := feegrant.NewGrant(granter, grantee, &invalid)
	require.NoError(t, err)
	require.Error(t, feegrant.ValidateGenesis(feegrant.GenesisState{Allowances: []feegrant.Grant{grant}}))

	emptyFilter := &feegrant.AllowedMsgAllowance{}
	require.NoError(t, emptyFilter.SetAllowance(allowance))
	grant, err = feegrant.NewGrant(granter, grantee, emptyFilter)
	require.NoError(t, err)
	require.Error(t, feegrant.ValidateGenesis(feegrant.GenesisState{Allowances: []feegrant.Grant{grant}}))
}

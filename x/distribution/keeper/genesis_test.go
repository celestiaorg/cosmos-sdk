package keeper_test

import (
	"strings"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/distribution"
	"github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	distrtestutil "github.com/cosmos/cosmos-sdk/x/distribution/testutil"
	"github.com/cosmos/cosmos-sdk/x/distribution/types"
)

// setupGenesisKeeper returns a keeper whose module account holds balance.
func setupGenesisKeeper(t *testing.T, balance sdk.Coins) (sdk.Context, keeper.Keeper) {
	t.Helper()
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(types.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec(sdk.Bech32MainPrefix)).AnyTimes()
	stakingKeeper.EXPECT().ValidatorAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixValAddr)).AnyTimes()
	stakingKeeper.EXPECT().ConsensusAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixConsAddr)).AnyTimes()
	accountKeeper.EXPECT().GetModuleAccount(gomock.Any(), types.ModuleName).Return(distrAcc).AnyTimes()
	accountKeeper.EXPECT().SetModuleAccount(gomock.Any(), distrAcc).AnyTimes()
	bankKeeper.EXPECT().GetAllBalances(gomock.Any(), distrAcc.GetAddress()).Return(balance).AnyTimes()

	distrKeeper := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		stakingKeeper,
		"fee_collector",
		authtypes.NewModuleAddress("gov").String(),
	)
	return testCtx.Ctx, distrKeeper
}

func TestGenesisUserOutstandingRewardsRoundTrip(t *testing.T) {
	addrs := simtestutil.CreateIncrementalAccounts(3)
	valAddr := sdk.ValAddress(addrs[0])
	rewards1 := sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 100))
	rewards2 := sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 5), sdk.NewInt64Coin("utia", 7))

	ctx, distrKeeper := setupGenesisKeeper(t, sdk.Coins{})
	require.NoError(t, distrKeeper.FeePool.Set(ctx, types.InitialFeePool()))
	require.NoError(t, distrKeeper.Params.Set(ctx, types.DefaultParams()))
	require.NoError(t, distrKeeper.SetPreviousProposerConsAddr(ctx, valConsAddr0))
	// Insert out of key order; export must follow key order.
	require.NoError(t, distrKeeper.UserOutstandingRewards.Set(ctx, collections.Join(addrs[2], valAddr), types.UserOutstandingRewards{Rewards: rewards2}))
	require.NoError(t, distrKeeper.UserOutstandingRewards.Set(ctx, collections.Join(addrs[1], valAddr), types.UserOutstandingRewards{Rewards: rewards1}))

	gs := distrKeeper.ExportGenesis(ctx)
	require.Equal(t, []types.UserOutstandingRewardsRecord{
		{DelegatorAddress: addrs[1].String(), ValidatorAddress: valAddr.String(), Rewards: rewards1},
		{DelegatorAddress: addrs[2].String(), ValidatorAddress: valAddr.String(), Rewards: rewards2},
	}, gs.UserOutstandingRewards)
	require.NoError(t, types.ValidateGenesis(gs))

	cdc := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{}).Codec
	var imported types.GenesisState
	require.NoError(t, cdc.UnmarshalJSON(cdc.MustMarshalJSON(gs), &imported))
	require.Equal(t, gs.UserOutstandingRewards, imported.UserOutstandingRewards)

	// The module account holds only the parked rewards; no delegation or validator exists.
	ctx2, distrKeeper2 := setupGenesisKeeper(t, rewards1.Add(rewards2...))
	require.NotPanics(t, func() { distrKeeper2.InitGenesis(ctx2, imported) })

	got, err := distrKeeper2.UserOutstandingRewards.Get(ctx2, collections.Join(addrs[2], valAddr))
	require.NoError(t, err)
	require.Equal(t, rewards2, got.Rewards)
	require.Equal(t, gs, distrKeeper2.ExportGenesis(ctx2))

	msg, broken := keeper.ModuleAccountInvariant(distrKeeper2)(ctx2)
	require.False(t, broken, msg)
}

func TestInitGenesisInvalidUserOutstandingRewards(t *testing.T) {
	addrs := simtestutil.CreateIncrementalAccounts(2)
	delAddr := addrs[0].String()
	valAddr := sdk.ValAddress(addrs[1]).String()
	rewards := sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 100))

	testCases := []struct {
		name    string
		records []types.UserOutstandingRewardsRecord
		expErr  string
	}{
		{
			name:    "invalid delegator address",
			records: []types.UserOutstandingRewardsRecord{{DelegatorAddress: "", ValidatorAddress: valAddr, Rewards: rewards}},
			expErr:  "record 0: invalid delegator address",
		},
		{
			name:    "invalid validator address",
			records: []types.UserOutstandingRewardsRecord{{DelegatorAddress: delAddr, ValidatorAddress: delAddr, Rewards: rewards}},
			expErr:  "record 0: invalid validator address",
		},
		{
			name: "duplicate pair",
			records: []types.UserOutstandingRewardsRecord{
				{DelegatorAddress: delAddr, ValidatorAddress: valAddr, Rewards: rewards},
				{DelegatorAddress: delAddr, ValidatorAddress: valAddr, Rewards: rewards},
			},
			expErr: "record 1: duplicate delegator",
		},
		{
			name: "duplicate pair with uppercase delegator",
			records: []types.UserOutstandingRewardsRecord{
				{DelegatorAddress: delAddr, ValidatorAddress: valAddr, Rewards: rewards},
				{DelegatorAddress: strings.ToUpper(delAddr), ValidatorAddress: valAddr, Rewards: rewards},
			},
			expErr: "record 1: duplicate delegator",
		},
		{
			name: "nil amount",
			records: []types.UserOutstandingRewardsRecord{{DelegatorAddress: delAddr, ValidatorAddress: valAddr, Rewards: sdk.Coins{
				sdk.NewInt64Coin("aaa", 1), {Denom: sdk.DefaultBondDenom},
			}}},
			expErr: "record 0: invalid reward coin: amount is nil",
		},
		{
			name:    "invalid denom",
			records: []types.UserOutstandingRewardsRecord{{DelegatorAddress: delAddr, ValidatorAddress: valAddr, Rewards: sdk.Coins{{Denom: "1", Amount: math.NewInt(1)}}}},
			expErr:  "record 0: invalid reward coin: invalid denom: 1",
		},
		{
			name:    "zero amount",
			records: []types.UserOutstandingRewardsRecord{{DelegatorAddress: delAddr, ValidatorAddress: valAddr, Rewards: sdk.Coins{{Denom: sdk.DefaultBondDenom, Amount: math.ZeroInt()}}}},
			expErr:  "record 0: invalid rewards: coin 0stake amount is not positive",
		},
		{
			name: "unsorted",
			records: []types.UserOutstandingRewardsRecord{{DelegatorAddress: delAddr, ValidatorAddress: valAddr, Rewards: sdk.Coins{
				sdk.NewInt64Coin("bbb", 1), sdk.NewInt64Coin("aaa", 1),
			}}},
			expErr: "record 0: invalid rewards: denomination aaa is not sorted",
		},
		{
			name:    "empty rewards",
			records: []types.UserOutstandingRewardsRecord{{DelegatorAddress: delAddr, ValidatorAddress: valAddr}},
			expErr:  "record 0: empty rewards",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gs := types.DefaultGenesisState()
			gs.UserOutstandingRewards = tc.records

			err := types.ValidateGenesis(gs)
			require.ErrorContains(t, err, "distribution genesis: user outstanding rewards "+tc.expErr)

			ctx, distrKeeper := setupGenesisKeeper(t, sdk.Coins{})
			require.PanicsWithError(t, err.Error(), func() { distrKeeper.InitGenesis(ctx, *gs) })
		})
	}
}

package keeper_test

import (
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"

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

// TestGenesisExportImportRoundTrip checks that a genesis state containing
// user outstanding rewards (CIP-30) survives an import -> export -> fresh
// import -> export round trip, and that the rewards are counted in the module
// holdings during InitGenesis.
func TestGenesisExportImportRoundTrip(t *testing.T) {
	ctrl := gomock.NewController(t)

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress()).AnyTimes()
	accountKeeper.EXPECT().GetModuleAccount(gomock.Any(), "distribution").Return(distrAcc).AnyTimes()
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec(sdk.Bech32MainPrefix)).AnyTimes()
	stakingKeeper.EXPECT().ValidatorAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixValAddr)).AnyTimes()
	stakingKeeper.EXPECT().ConsensusAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixConsAddr)).AnyTimes()

	addrs := simtestutil.CreateIncrementalAccounts(2)
	valAddr := sdk.ValAddress(valConsAddr0)

	communityPool := sdk.DecCoins{sdk.NewDecCoin("stake", math.NewInt(100))}
	valOutstanding := sdk.DecCoins{sdk.NewDecCoin("stake", math.NewInt(500))}
	userRewards0 := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(123)))
	userRewards1 := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(77)))

	// module balance = community pool + validator outstanding + user outstanding rewards
	moduleBalance := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(100+500+123+77)))
	bankKeeper.EXPECT().GetAllBalances(gomock.Any(), distrAcc.GetAddress()).Return(moduleBalance).AnyTimes()

	genesisState := types.GenesisState{
		Params:                          types.DefaultParams(),
		FeePool:                         types.FeePool{CommunityPool: communityPool},
		DelegatorWithdrawInfos:          []types.DelegatorWithdrawInfo{},
		PreviousProposer:                valConsAddr0.String(),
		OutstandingRewards:              []types.ValidatorOutstandingRewardsRecord{{ValidatorAddress: valAddr.String(), OutstandingRewards: valOutstanding}},
		ValidatorAccumulatedCommissions: []types.ValidatorAccumulatedCommissionRecord{},
		ValidatorHistoricalRewards:      []types.ValidatorHistoricalRewardsRecord{},
		ValidatorCurrentRewards:         []types.ValidatorCurrentRewardsRecord{},
		DelegatorStartingInfos:          []types.DelegatorStartingInfoRecord{},
		ValidatorSlashEvents:            []types.ValidatorSlashEventRecord{},
		// sorted by (delegator, validator) so the order matches the export walk order
		UserOutstandingRewards: []types.UserOutstandingRewardsRecord{
			{DelegatorAddress: addrs[0].String(), ValidatorAddress: valAddr.String(), Rewards: userRewards0},
			{DelegatorAddress: addrs[1].String(), ValidatorAddress: valAddr.String(), Rewards: userRewards1},
		},
	}

	newKeeper := func() (keeper.Keeper, sdk.Context) {
		key := storetypes.NewKVStoreKey(types.StoreKey)
		testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
		encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
		k := keeper.NewKeeper(
			encCfg.Codec,
			runtime.NewKVStoreService(key),
			accountKeeper,
			bankKeeper,
			stakingKeeper,
			"fee_collector",
			authtypes.NewModuleAddress("gov").String(),
		)
		return k, testCtx.Ctx
	}

	// import the genesis state and check it round-trips through export
	distrKeeper, ctx := newKeeper()
	require.NotPanics(t, func() { distrKeeper.InitGenesis(ctx, genesisState) })
	exported := distrKeeper.ExportGenesis(ctx)
	require.Equal(t, genesisState, *exported)

	// import the exported state into a fresh keeper and check the re-export matches
	freshKeeper, freshCtx := newKeeper()
	require.NotPanics(t, func() { freshKeeper.InitGenesis(freshCtx, *exported) })
	reExported := freshKeeper.ExportGenesis(freshCtx)
	require.Equal(t, exported, reExported)
}

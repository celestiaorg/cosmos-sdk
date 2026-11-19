package keeper_test

import (
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/distribution"
	"github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	distrtestutil "github.com/cosmos/cosmos-sdk/x/distribution/testutil"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestBeginBlockerFirstBlockAboveOne checks a chain that starts at height
// I > 1, as after a restart from an export: block I has no previous commit and
// allocates nothing, so the fees carried in the fee collector are not sent to
// the community pool; block I+1 allocates them to the validators that signed.
// The bank keeper mock fails the test on any unexpected call.
func TestBeginBlockerFirstBlockAboveOne(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(disttypes.StoreKey)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	const initialHeight = 5562

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)
	feeCollectorAcc := authtypes.NewEmptyModuleAccount("fee_collector")
	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())
	stakingKeeper.EXPECT().ValidatorAddressCodec().Return(address.NewBech32Codec("cosmosvaloper")).AnyTimes()

	k := keeper.NewKeeper(encCfg.Codec, runtime.NewKVStoreService(key), accountKeeper, bankKeeper, stakingKeeper,
		"fee_collector", authtypes.NewModuleAddress("gov").String())
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Height: initialHeight, Time: time.Now(), ProposerAddress: valConsAddr0})
	require.NoError(t, k.Params.Set(ctx, disttypes.DefaultParams()))
	require.NoError(t, k.FeePool.Set(ctx, disttypes.InitialFeePool()))

	// Block I: no votes, nothing allocated, the proposer is recorded.
	require.NoError(t, distribution.BeginBlocker(ctx, k))
	feePool, err := k.FeePool.Get(ctx)
	require.NoError(t, err)
	require.True(t, feePool.CommunityPool.IsZero(), "block I sent fees to the community pool")
	proposer, err := k.GetPreviousProposerConsAddr(ctx)
	require.NoError(t, err)
	require.Equal(t, valConsAddr0, proposer)

	// Block I+1: two validators with powers 100 and 300 signed block I.
	val0, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
	require.NoError(t, err)
	val0.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDec(0))
	val1, err := distrtestutil.CreateValidator(valConsPk1, math.NewInt(300))
	require.NoError(t, err)
	val1.Commission = stakingtypes.NewCommission(math.LegacyNewDec(0), math.LegacyNewDec(0), math.LegacyNewDec(0))
	stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), valConsAddr0).Return(val0, nil).AnyTimes()
	stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), valConsAddr1).Return(val1, nil).AnyTimes()

	fees := sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(100)))
	accountKeeper.EXPECT().GetModuleAccount(gomock.Any(), "fee_collector").Return(feeCollectorAcc).Times(1)
	bankKeeper.EXPECT().GetAllBalances(gomock.Any(), feeCollectorAcc.GetAddress()).Return(fees).Times(1)
	bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "fee_collector", disttypes.ModuleName, fees).Times(1)

	ctx = ctx.WithBlockHeader(cmtproto.Header{Height: initialHeight + 1, Time: time.Now(), ProposerAddress: valConsAddr1}).
		WithVoteInfos([]abci.VoteInfo{
			{Validator: abci.Validator{Address: valConsPk0.Address(), Power: 100}, BlockIdFlag: cmtproto.BlockIDFlagCommit},
			{Validator: abci.Validator{Address: valConsPk1.Address(), Power: 300}, BlockIdFlag: cmtproto.BlockIDFlagCommit},
		})
	require.NoError(t, distribution.BeginBlocker(ctx, k))

	// 2% community tax, then 98 split by power: 24.5 and 73.5.
	feePool, err = k.FeePool.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(2)}}, feePool.CommunityPool)
	for addr, want := range map[string]math.LegacyDec{
		sdk.ValAddress(valConsAddr0).String(): math.LegacyNewDecWithPrec(245, 1),
		sdk.ValAddress(valConsAddr1).String(): math.LegacyNewDecWithPrec(735, 1),
	} {
		valAddr, err := sdk.ValAddressFromBech32(addr)
		require.NoError(t, err)
		rewards, err := k.GetValidatorOutstandingRewards(ctx, valAddr)
		require.NoError(t, err)
		require.Equal(t, sdk.DecCoins{{Denom: sdk.DefaultBondDenom, Amount: want}}, rewards.Rewards, addr)
	}
	// 50% commission of val0's 24.5.
	commission, err := k.GetValidatorAccumulatedCommission(ctx, sdk.ValAddress(valConsAddr0))
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDecWithPrec(1225, 2)}}, commission.Commission)
	proposer, err = k.GetPreviousProposerConsAddr(ctx)
	require.NoError(t, err)
	require.Equal(t, valConsAddr1, proposer)
}

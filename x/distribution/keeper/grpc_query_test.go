package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/distribution"
	"github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	distrtestutil "github.com/cosmos/cosmos-sdk/x/distribution/testutil"
	"github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDelegationRewards(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(types.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Height: 1})

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())
	stakingKeeper.EXPECT().ValidatorAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixValAddr)).AnyTimes()
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec(sdk.Bech32MainPrefix)).AnyTimes()

	distrKeeper := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		stakingKeeper,
		"fee_collector",
		authtypes.NewModuleAddress("gov").String(),
	)

	querier := keeper.NewQuerier(distrKeeper)

	// reset fee pool
	require.NoError(t, distrKeeper.FeePool.Set(ctx, types.InitialFeePool()))
	require.NoError(t, distrKeeper.Params.Set(ctx, types.DefaultParams()))

	// create addresses
	valAddr := sdk.ValAddress(valConsAddr0)
	delegatorAddr := sdk.AccAddress(valConsAddr1)

	t.Run("nil request", func(t *testing.T) {
		resp, err := querier.DelegationRewards(ctx, nil)
		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "invalid request")
		require.Nil(t, resp)
	})

	t.Run("empty delegator address", func(t *testing.T) {
		req := &types.QueryDelegationRewardsRequest{
			DelegatorAddress: "",
			ValidatorAddress: valAddr.String(),
		}
		resp, err := querier.DelegationRewards(ctx, req)
		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "empty delegator address")
		require.Nil(t, resp)
	})

	t.Run("empty validator address", func(t *testing.T) {
		req := &types.QueryDelegationRewardsRequest{
			DelegatorAddress: delegatorAddr.String(),
			ValidatorAddress: "",
		}
		resp, err := querier.DelegationRewards(ctx, req)
		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "empty validator address")
		require.Nil(t, resp)
	})

	t.Run("invalid validator address", func(t *testing.T) {
		req := &types.QueryDelegationRewardsRequest{
			DelegatorAddress: delegatorAddr.String(),
			ValidatorAddress: "invalid-address",
		}
		resp, err := querier.DelegationRewards(ctx, req)
		require.Error(t, err)
		require.Nil(t, resp)
	})

	t.Run("validator does not exist", func(t *testing.T) {
		req := &types.QueryDelegationRewardsRequest{
			DelegatorAddress: delegatorAddr.String(),
			ValidatorAddress: valAddr.String(),
		}

		// Mock validator does not exist
		stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(nil, nil)

		resp, err := querier.DelegationRewards(ctx, req)
		require.Error(t, err)
		require.ErrorIs(t, err, types.ErrNoValidatorExists)
		require.Nil(t, resp)
	})

	t.Run("delegation does not exist", func(t *testing.T) {
		val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
		require.NoError(t, err)

		req := &types.QueryDelegationRewardsRequest{
			DelegatorAddress: delegatorAddr.String(),
			ValidatorAddress: valAddr.String(),
		}

		// Mock validator exists but delegation does not exist
		stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil)
		stakingKeeper.EXPECT().Delegation(gomock.Any(), delegatorAddr, valAddr).Return(nil, nil)

		resp, err := querier.DelegationRewards(ctx, req)
		require.Error(t, err)
		require.Contains(t, err.Error(), "delegation does not exist")
		require.Nil(t, resp)
	})

	t.Run("successful query with no rewards", func(t *testing.T) {
		val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
		require.NoError(t, err)
		val.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDec(0))

		del := stakingtypes.NewDelegation(delegatorAddr.String(), valAddr.String(), val.DelegatorShares)

		req := &types.QueryDelegationRewardsRequest{
			DelegatorAddress: delegatorAddr.String(),
			ValidatorAddress: valAddr.String(),
		}

		// Mock expectations
		stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).AnyTimes()
		stakingKeeper.EXPECT().Delegation(gomock.Any(), delegatorAddr, valAddr).Return(del, nil).AnyTimes()

		// Set up validator hooks to create historical records
		err = distrtestutil.CallCreateValidatorHooks(ctx, distrKeeper, delegatorAddr, valAddr)
		require.NoError(t, err)

		resp, err := querier.DelegationRewards(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.True(t, resp.Rewards.IsZero())
	})

	t.Run("successful query with rewards", func(t *testing.T) {
		val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
		require.NoError(t, err)
		val.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDec(0))

		del := stakingtypes.NewDelegation(delegatorAddr.String(), valAddr.String(), val.DelegatorShares)

		req := &types.QueryDelegationRewardsRequest{
			DelegatorAddress: delegatorAddr.String(),
			ValidatorAddress: valAddr.String(),
		}

		stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).AnyTimes()
		stakingKeeper.EXPECT().Delegation(gomock.Any(), delegatorAddr, valAddr).Return(del, nil).AnyTimes()

		err = distrtestutil.CallCreateValidatorHooks(ctx, distrKeeper, delegatorAddr, valAddr)
		require.NoError(t, err)

		// Delegate and set up delegation hooks
		err = distrKeeper.Hooks().AfterDelegationModified(ctx, delegatorAddr, valAddr)
		require.NoError(t, err)

		// Allocate some rewards
		ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
		initial := int64(20)
		tokens := sdk.DecCoins{{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(initial)}}
		require.NoError(t, distrKeeper.AllocateTokensToValidator(ctx, val, tokens))

		resp, err := querier.DelegationRewards(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.False(t, resp.Rewards.IsZero())
		expectedRewards := sdk.DecCoins{{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(initial / 2)}}
		require.Equal(t, expectedRewards, resp.Rewards)
	})

	t.Run("query with outstanding rewards only and (nil, nil) delegation response", func(t *testing.T) {
		val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
		require.NoError(t, err)

		// Create new delegator and validator addresses to avoid conflicts
		newValAddr := sdk.ValAddress(valConsAddr2)
		newDelegatorAddr := sdk.AccAddress(valConsAddr2)

		req := &types.QueryDelegationRewardsRequest{
			DelegatorAddress: newDelegatorAddr.String(),
			ValidatorAddress: newValAddr.String(),
		}

		// Set up outstanding rewards without delegation
		outstandingRewards := sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(50)))
		err = distrKeeper.UserOutstandingRewards.Set(ctx, collections.Join(newDelegatorAddr, newValAddr), types.UserOutstandingRewards{
			Rewards: outstandingRewards,
		})
		require.NoError(t, err)

		stakingKeeper.EXPECT().Validator(gomock.Any(), newValAddr).Return(val, nil).AnyTimes()
		stakingKeeper.EXPECT().Delegation(gomock.Any(), newDelegatorAddr, newValAddr).Return(nil, nil).AnyTimes()

		resp, err := querier.DelegationRewards(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, sdk.NewDecCoinsFromCoins(outstandingRewards...), resp.Rewards)
	})

	t.Run("query with outstanding rewards only and (nil, ErrNoDelegation) delegation response", func(t *testing.T) {
		val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
		require.NoError(t, err)

		newValAddr := sdk.ValAddress(valConsAddr2)
		newDelegatorAddr := sdk.AccAddress(valConsAddr2)

		req := &types.QueryDelegationRewardsRequest{
			DelegatorAddress: newDelegatorAddr.String(),
			ValidatorAddress: newValAddr.String(),
		}

		// Set up outstanding rewards without delegation
		outstandingRewards := sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(50)))
		err = distrKeeper.UserOutstandingRewards.Set(ctx, collections.Join(newDelegatorAddr, newValAddr), types.UserOutstandingRewards{
			Rewards: outstandingRewards,
		})
		require.NoError(t, err)

		stakingKeeper.EXPECT().Validator(gomock.Any(), newValAddr).Return(val, nil).AnyTimes()
		stakingKeeper.EXPECT().Delegation(gomock.Any(), newDelegatorAddr, newValAddr).Return(nil, stakingtypes.ErrNoDelegation).AnyTimes()

		resp, err := querier.DelegationRewards(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, sdk.NewDecCoinsFromCoins(outstandingRewards...), resp.Rewards)
	})

	t.Run("query for rewards from delegation + outstanding rewards", func(t *testing.T) {
		val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
		require.NoError(t, err)

		req := &types.QueryDelegationRewardsRequest{
			DelegatorAddress: delegatorAddr.String(),
			ValidatorAddress: valAddr.String(),
		}
		del := stakingtypes.NewDelegation(delegatorAddr.String(), valAddr.String(), val.DelegatorShares)

		stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).AnyTimes()
		stakingKeeper.EXPECT().Delegation(gomock.Any(), delegatorAddr, valAddr).Return(del, nil).AnyTimes()

		err = distrtestutil.CallCreateValidatorHooks(ctx, distrKeeper, delegatorAddr, valAddr)
		require.NoError(t, err)

		// Delegate and set up delegation hooks
		err = distrKeeper.Hooks().AfterDelegationModified(ctx, delegatorAddr, valAddr)
		require.NoError(t, err)

		// Allocate some rewards
		ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
		rewards := int64(20)
		tokens := sdk.DecCoins{{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(rewards)}}
		require.NoError(t, distrKeeper.AllocateTokensToValidator(ctx, val, tokens))

		outstanding := int64(50)
		outstandingRewards := sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(outstanding)))
		err = distrKeeper.UserOutstandingRewards.Set(ctx, collections.Join(delegatorAddr, valAddr), types.UserOutstandingRewards{
			Rewards: outstandingRewards,
		})
		require.NoError(t, err)

		resp, err := querier.DelegationRewards(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		expectedRewards := sdk.DecCoins{{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(rewards + outstanding)}}
		require.Equal(t, expectedRewards, resp.Rewards)
	})
}

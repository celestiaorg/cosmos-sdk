package keeper_test

import (
	"testing"

	"cosmossdk.io/core/store"
	storetypes "cosmossdk.io/store/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttime "github.com/cometbft/cometbft/types/time"
	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtestutil "github.com/cosmos/cosmos-sdk/x/staking/testutil"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
)

func TestBondDenom(t *testing.T) {
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, _, storeService := setupTestContext(t, "")
	accountKeeper, bankKeeper := setupMockKeepers(ctrl)

	keeper := setupKeeper(encCfg, storeService, accountKeeper, bankKeeper)

	t.Run("returns bond denom from params when set", func(t *testing.T) {
		customDenom := "custom-denom"
		params := stakingtypes.DefaultParams()
		params.BondDenom = customDenom

		err := keeper.SetParams(ctx, params)
		require.NoError(t, err)

		bondDenom, err := keeper.BondDenom(ctx)
		require.NoError(t, err)
		require.Equal(t, customDenom, bondDenom)
	})

	t.Run("returns default bond denom when params.BondDenom is empty", func(t *testing.T) {
		params := stakingtypes.DefaultParams()
		params.BondDenom = "" // Set empty bond denom

		err := keeper.SetParams(ctx, params)
		require.NoError(t, err)

		bondDenom, err := keeper.BondDenom(ctx)
		require.NoError(t, err)
		require.Equal(t, sdk.DefaultBondDenom, bondDenom)
		require.Equal(t, "stake", bondDenom)
	})
}

// setupTestContext creates a test context with a new store key
func setupTestContext(t *testing.T, storeKeySuffix string) (sdk.Context, storetypes.StoreKey, store.KVStoreService) {
	t.Helper()
	key := storetypes.NewKVStoreKey(stakingtypes.StoreKey + storeKeySuffix)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"+storeKeySuffix))
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Time: cmttime.Now()})
	return ctx, key, storeService
}

// setupMockKeepers creates and configures mock account and bank keepers
func setupMockKeepers(ctrl *gomock.Controller) (*stakingtestutil.MockAccountKeeper, *stakingtestutil.MockBankKeeper) {
	accountKeeper := stakingtestutil.NewMockAccountKeeper(ctrl)
	bondedAcc := authtypes.NewEmptyModuleAccount(stakingtypes.BondedPoolName)
	notBondedAcc := authtypes.NewEmptyModuleAccount(stakingtypes.NotBondedPoolName)
	accountKeeper.EXPECT().GetModuleAddress(stakingtypes.BondedPoolName).Return(bondedAcc.GetAddress()).AnyTimes()
	accountKeeper.EXPECT().GetModuleAddress(stakingtypes.NotBondedPoolName).Return(notBondedAcc.GetAddress()).AnyTimes()
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec("cosmos")).AnyTimes()

	bankKeeper := stakingtestutil.NewMockBankKeeper(ctrl)
	return accountKeeper, bankKeeper
}

// setupKeeper creates a new staking keeper with the provided dependencies
func setupKeeper(
	encCfg moduletestutil.TestEncodingConfig,
	storeService store.KVStoreService,
	accountKeeper *stakingtestutil.MockAccountKeeper,
	bankKeeper *stakingtestutil.MockBankKeeper,
) *stakingkeeper.Keeper {
	return stakingkeeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		authtypes.NewModuleAddress(govtypes.ModuleName).String(),
		address.NewBech32Codec("cosmosvaloper"),
		address.NewBech32Codec("cosmosvalcons"),
	)
}

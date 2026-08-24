package keeper_test

import (
	"math/big"
	"testing"

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
)

// TestIncrementValidatorPeriodOverflow verifies that an overflow of the
// cumulative reward ratio returns an error instead of panicking, so the fold
// cannot halt the chain from a slashing BeginBlocker. It also verifies that no
// state is mutated when the overflow is detected.
func TestIncrementValidatorPeriodOverflow(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(disttypes.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx

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

	// A validator with a single token, so the ratio increment equals the whole
	// reward.
	val, err := distrtestutil.CreateValidator(valConsPk0, math.OneInt())
	require.NoError(t, err)
	valAddr := sdk.ValAddress(valConsAddr0)

	denom := sdk.DefaultBondDenom

	// Park the cumulative reward ratio one whole token below the LegacyDec
	// ceiling: internal value (2^256-1)*10^18, leaving 10^18-1 of headroom.
	maxInt := new(big.Int).Sub(new(big.Int).Exp(big.NewInt(2), big.NewInt(256), nil), big.NewInt(1))
	nearCeiling := sdk.DecCoins{{Denom: denom, Amount: math.LegacyNewDecFromBigInt(maxInt)}}
	require.NoError(t, distrKeeper.SetValidatorHistoricalRewards(ctx, valAddr, 0,
		disttypes.NewValidatorHistoricalRewards(nearCeiling, 1)))

	// One whole token of pending reward; current = reward/tokens = 1.0, which
	// pushes the ratio over the ceiling.
	reward := sdk.DecCoins{{Denom: denom, Amount: math.LegacyOneDec()}}
	require.NoError(t, distrKeeper.SetValidatorCurrentRewards(ctx, valAddr,
		disttypes.NewValidatorCurrentRewards(reward, 1)))

	var incErr error
	require.NotPanics(t, func() {
		_, incErr = distrKeeper.IncrementValidatorPeriod(ctx, val)
	}, "the overflow must not panic")
	require.Error(t, incErr, "the overflow must be returned as an error")
	require.Contains(t, incErr.Error(), "overflow")

	// The failure must leave state untouched: the reference count on the
	// previous period is unchanged (decrementReferenceCount never ran).
	historical, err := distrKeeper.GetValidatorHistoricalRewards(ctx, valAddr, 0)
	require.NoError(t, err)
	require.Equal(t, uint32(1), historical.ReferenceCount)
}

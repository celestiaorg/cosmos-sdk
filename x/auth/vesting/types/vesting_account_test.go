package types_test

import (
	"testing"
	"time"

	tmtime "github.com/cometbft/cometbft/types/time"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"cosmossdk.io/core/header"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authcodec "github.com/cosmos/cosmos-sdk/x/auth/codec"
	"github.com/cosmos/cosmos-sdk/x/auth/keeper"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/auth/vesting"
	"github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
)

var (
	stakeDenom = "stake"
	feeDenom   = "fee"
	emptyCoins = sdk.Coins{}
)

type VestingAccountTestSuite struct {
	suite.Suite

	ctx           sdk.Context
	accountKeeper keeper.AccountKeeper
}

func (s *VestingAccountTestSuite) SetupTest() {
	encCfg := moduletestutil.MakeTestEncodingConfig(vesting.AppModuleBasic{})

	key := storetypes.NewKVStoreKey(authtypes.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(s.T(), key, storetypes.NewTransientStoreKey("transient_test"))
	s.ctx = testCtx.Ctx.WithHeaderInfo(header.Info{})

	maccPerms := map[string][]string{
		"fee_collector":          nil,
		"mint":                   {"minter"},
		"bonded_tokens_pool":     {"burner", "staking"},
		"not_bonded_tokens_pool": {"burner", "staking"},
		"multiPerm":              {"burner", "minter", "staking"},
		"random":                 {"random"},
	}

	s.accountKeeper = keeper.NewAccountKeeper(
		encCfg.Codec,
		storeService,
		authtypes.ProtoBaseAccount,
		maccPerms,
		authcodec.NewBech32Codec("cosmos"),
		"cosmos",
		authtypes.NewModuleAddress("gov").String(),
	)
}

func TestGetVestedCoinsContVestingAcc(t *testing.T) {
	now := tmtime.Now()
	startTime := now.Add(24 * time.Hour)
	endTime := startTime.Add(24 * time.Hour)

	bacc, origCoins := initBaseAccount()
	cva, err := types.NewContinuousVestingAccount(bacc, origCoins, startTime.Unix(), endTime.Unix())
	require.NoError(t, err)

	// require no coins vested _before_ the start time of the vesting schedule
	vestedCoins := cva.GetVestedCoins(now)
	require.Nil(t, vestedCoins)

	// require no coins vested _before_ the very beginning of the vesting schedule
	vestedCoins = cva.GetVestedCoins(startTime.Add(-1))
	require.Nil(t, vestedCoins)

	// require all coins vested at the end of the vesting schedule
	vestedCoins = cva.GetVestedCoins(endTime)
	require.Equal(t, origCoins, vestedCoins)

	// require 50% of coins vested
	vestedCoins = cva.GetVestedCoins(startTime.Add(12 * time.Hour))
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}, vestedCoins)

	// require 75% of coins vested
	vestedCoins = cva.GetVestedCoins(startTime.Add(18 * time.Hour))
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(feeDenom, 750), sdk.NewInt64Coin(stakeDenom, 75)}, vestedCoins)

	// require 100% of coins vested
	vestedCoins = cva.GetVestedCoins(endTime)
	require.Equal(t, origCoins, vestedCoins)
}

func TestGetVestingCoinsContVestingAcc(t *testing.T) {
	now := tmtime.Now()
	startTime := now.Add(24 * time.Hour)
	endTime := startTime.Add(24 * time.Hour)

	bacc, origCoins := initBaseAccount()
	cva, err := types.NewContinuousVestingAccount(bacc, origCoins, startTime.Unix(), endTime.Unix())
	require.NoError(t, err)

	// require all coins vesting before the start time of the vesting schedule
	vestingCoins := cva.GetVestingCoins(now)
	require.Equal(t, origCoins, vestingCoins)

	// require all coins vesting right before the start time of the vesting schedule
	vestingCoins = cva.GetVestingCoins(startTime.Add(-1))
	require.Equal(t, origCoins, vestingCoins)

	// require no coins vesting at the end of the vesting schedule
	vestingCoins = cva.GetVestingCoins(endTime)
	require.Equal(t, emptyCoins, vestingCoins)

	// require 50% of coins vesting in the middle between start and end time
	vestingCoins = cva.GetVestingCoins(startTime.Add(12 * time.Hour))
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}, vestingCoins)

	// require 25% of coins vesting after 3/4 of the time between start and end time has passed
	vestingCoins = cva.GetVestingCoins(startTime.Add(18 * time.Hour))
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}, vestingCoins)
}

func TestSpendableCoinsContVestingAcc(t *testing.T) {
	now := tmtime.Now()
	startTime := now.Add(24 * time.Hour)
	endTime := startTime.Add(24 * time.Hour)

	bacc, origCoins := initBaseAccount()
	cva, err := types.NewContinuousVestingAccount(bacc, origCoins, startTime.Unix(), endTime.Unix())
	require.NoError(t, err)

	// require that all original coins are locked before the beginning of the vesting
	// schedule
	lockedCoins := cva.LockedCoins(now)
	require.Equal(t, origCoins, lockedCoins)

	// require that all original coins are locked at the beginning of the vesting
	// schedule
	lockedCoins = cva.LockedCoins(startTime)
	require.Equal(t, origCoins, lockedCoins)

	// require that there exist no locked coins in the end of the vesting schedule
	lockedCoins = cva.LockedCoins(endTime)
	require.Equal(t, sdk.NewCoins(), lockedCoins)

	// require that all vested coins (50%) are spendable
	lockedCoins = cva.LockedCoins(startTime.Add(12 * time.Hour))
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}, lockedCoins)

	// require 25% of coins vesting after 3/4 of the time between start and end time has passed
	lockedCoins = cva.LockedCoins(startTime.Add(18 * time.Hour))
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}, lockedCoins)
}

func TestTrackDelegationContVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(24 * time.Hour)

	bacc, origCoins := initBaseAccount()

	// require the ability to delegate all vesting coins
	cva, err := types.NewContinuousVestingAccount(bacc, origCoins, now.Unix(), endTime.Unix())
	require.NoError(t, err)
	cva.TrackDelegation(now, origCoins, origCoins)
	require.Equal(t, origCoins, cva.DelegatedVesting)
	require.Nil(t, cva.DelegatedFree)

	// require the ability to delegate all vested coins
	cva, err = types.NewContinuousVestingAccount(bacc, origCoins, now.Unix(), endTime.Unix())
	require.NoError(t, err)
	cva.TrackDelegation(endTime, origCoins, origCoins)
	require.Nil(t, cva.DelegatedVesting)
	require.Equal(t, origCoins, cva.DelegatedFree)

	// require the ability to delegate all vesting coins (50%) and all vested coins (50%)
	cva, err = types.NewContinuousVestingAccount(bacc, origCoins, now.Unix(), endTime.Unix())
	require.NoError(t, err)
	cva.TrackDelegation(now.Add(12*time.Hour), origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)}, cva.DelegatedVesting)
	require.Nil(t, cva.DelegatedFree)

	cva.TrackDelegation(now.Add(12*time.Hour), origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)}, cva.DelegatedVesting)
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)}, cva.DelegatedFree)

	// require no modifications when delegation amount is zero or not enough funds
	cva, err = types.NewContinuousVestingAccount(bacc, origCoins, now.Unix(), endTime.Unix())
	require.NoError(t, err)
	require.Panics(t, func() {
		cva.TrackDelegation(endTime, origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 1000000)})
	})
	require.Nil(t, cva.DelegatedVesting)
	require.Nil(t, cva.DelegatedFree)
}

func TestTrackUndelegationContVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(24 * time.Hour)

	bacc, origCoins := initBaseAccount()

	// require the ability to undelegate all vesting coins
	cva, err := types.NewContinuousVestingAccount(bacc, origCoins, now.Unix(), endTime.Unix())
	require.NoError(t, err)
	cva.TrackDelegation(now, origCoins, origCoins)
	cva.TrackUndelegation(origCoins)
	require.Nil(t, cva.DelegatedFree)
	require.Equal(t, emptyCoins, cva.DelegatedVesting)

	// require the ability to undelegate all vested coins
	cva, err = types.NewContinuousVestingAccount(bacc, origCoins, now.Unix(), endTime.Unix())
	require.NoError(t, err)
	cva.TrackDelegation(endTime, origCoins, origCoins)
	cva.TrackUndelegation(origCoins)
	require.Equal(t, emptyCoins, cva.DelegatedFree)
	require.Nil(t, cva.DelegatedVesting)

	// require no modifications when the undelegation amount is zero
	cva, err = types.NewContinuousVestingAccount(bacc, origCoins, now.Unix(), endTime.Unix())
	require.NoError(t, err)
	require.Panics(t, func() {
		cva.TrackUndelegation(sdk.Coins{sdk.NewInt64Coin(stakeDenom, 0)})
	})
	require.Nil(t, cva.DelegatedFree)
	require.Nil(t, cva.DelegatedVesting)

	// vest 50% and delegate to two validators
	cva, err = types.NewContinuousVestingAccount(bacc, origCoins, now.Unix(), endTime.Unix())
	require.NoError(t, err)
	cva.TrackDelegation(now.Add(12*time.Hour), origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})
	cva.TrackDelegation(now.Add(12*time.Hour), origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})

	// undelegate from one validator that got slashed 50%
	cva.TrackUndelegation(sdk.Coins{sdk.NewInt64Coin(stakeDenom, 25)})
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 25)}, cva.DelegatedFree)
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)}, cva.DelegatedVesting)

	// undelegate from the other validator that did not get slashed
	cva.TrackUndelegation(sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})
	require.Equal(t, emptyCoins, cva.DelegatedFree)
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 25)}, cva.DelegatedVesting)
}

func TestGetVestedCoinsDelVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(24 * time.Hour)

	bacc, origCoins := initBaseAccount()

	// require no coins are vested until schedule maturation
	dva, err := types.NewDelayedVestingAccount(bacc, origCoins, endTime.Unix())
	require.NoError(t, err)
	vestedCoins := dva.GetVestedCoins(now)
	require.Nil(t, vestedCoins)

	// require all coins be vested at schedule maturation
	vestedCoins = dva.GetVestedCoins(endTime)
	require.Equal(t, origCoins, vestedCoins)
}

func TestGetVestingCoinsDelVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(24 * time.Hour)

	bacc, origCoins := initBaseAccount()

	// require all coins vesting at the beginning of the schedule
	dva, err := types.NewDelayedVestingAccount(bacc, origCoins, endTime.Unix())
	require.NoError(t, err)
	vestingCoins := dva.GetVestingCoins(now)
	require.Equal(t, origCoins, vestingCoins)

	// require no coins vesting at schedule maturation
	vestingCoins = dva.GetVestingCoins(endTime)
	require.Equal(t, emptyCoins, vestingCoins)
}

func TestSpendableCoinsDelVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(24 * time.Hour)

	bacc, origCoins := initBaseAccount()

	// require that all coins are locked in the beginning of the vesting
	// schedule
	dva, err := types.NewDelayedVestingAccount(bacc, origCoins, endTime.Unix())
	require.NoError(t, err)
	lockedCoins := dva.LockedCoins(now)
	require.True(t, lockedCoins.Equal(origCoins))

	// require that all coins are spendable after the maturation of the vesting
	// schedule
	lockedCoins = dva.LockedCoins(endTime)
	require.Equal(t, sdk.NewCoins(), lockedCoins)

	// require that all coins are still vesting after some time
	lockedCoins = dva.LockedCoins(now.Add(12 * time.Hour))
	require.True(t, lockedCoins.Equal(origCoins))

	// delegate some locked coins
	// require that locked is reduced
	delegatedAmount := sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50))
	dva.TrackDelegation(now.Add(12*time.Hour), origCoins, delegatedAmount)
	lockedCoins = dva.LockedCoins(now.Add(12 * time.Hour))
	require.True(t, lockedCoins.Equal(origCoins.Sub(delegatedAmount...)))
}

func TestTrackDelegationDelVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(24 * time.Hour)

	bacc, origCoins := initBaseAccount()

	// require the ability to delegate all vesting coins
	dva, err := types.NewDelayedVestingAccount(bacc, origCoins, endTime.Unix())
	require.NoError(t, err)
	dva.TrackDelegation(now, origCoins, origCoins)
	require.Equal(t, origCoins, dva.DelegatedVesting)
	require.Nil(t, dva.DelegatedFree)

	// require the ability to delegate all vested coins
	dva, err = types.NewDelayedVestingAccount(bacc, origCoins, endTime.Unix())
	require.NoError(t, err)
	dva.TrackDelegation(endTime, origCoins, origCoins)
	require.Nil(t, dva.DelegatedVesting)
	require.Equal(t, origCoins, dva.DelegatedFree)

	// require the ability to delegate all coins half way through the vesting
	// schedule
	dva, err = types.NewDelayedVestingAccount(bacc, origCoins, endTime.Unix())
	require.NoError(t, err)
	dva.TrackDelegation(now.Add(12*time.Hour), origCoins, origCoins)
	require.Equal(t, origCoins, dva.DelegatedVesting)
	require.Nil(t, dva.DelegatedFree)

	// require no modifications when delegation amount is zero or not enough funds
	dva, err = types.NewDelayedVestingAccount(bacc, origCoins, endTime.Unix())
	require.NoError(t, err)
	require.Panics(t, func() {
		dva.TrackDelegation(endTime, origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 1000000)})
	})
	require.Nil(t, dva.DelegatedVesting)
	require.Nil(t, dva.DelegatedFree)
}

func TestTrackUndelegationDelVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(24 * time.Hour)

	bacc, origCoins := initBaseAccount()

	// require the ability to undelegate all vesting coins
	dva, err := types.NewDelayedVestingAccount(bacc, origCoins, endTime.Unix())
	require.NoError(t, err)
	dva.TrackDelegation(now, origCoins, origCoins)
	dva.TrackUndelegation(origCoins)
	require.Nil(t, dva.DelegatedFree)
	require.Equal(t, emptyCoins, dva.DelegatedVesting)

	// require the ability to undelegate all vested coins
	dva, err = types.NewDelayedVestingAccount(bacc, origCoins, endTime.Unix())
	require.NoError(t, err)
	dva.TrackDelegation(endTime, origCoins, origCoins)
	dva.TrackUndelegation(origCoins)
	require.Equal(t, emptyCoins, dva.DelegatedFree)
	require.Nil(t, dva.DelegatedVesting)

	// require no modifications when the undelegation amount is zero
	dva, err = types.NewDelayedVestingAccount(bacc, origCoins, endTime.Unix())
	require.NoError(t, err)
	require.Panics(t, func() {
		dva.TrackUndelegation(sdk.Coins{sdk.NewInt64Coin(stakeDenom, 0)})
	})
	require.Nil(t, dva.DelegatedFree)
	require.Nil(t, dva.DelegatedVesting)

	// vest 50% and delegate to two validators
	dva, err = types.NewDelayedVestingAccount(bacc, origCoins, endTime.Unix())
	require.NoError(t, err)
	dva.TrackDelegation(now.Add(12*time.Hour), origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})
	dva.TrackDelegation(now.Add(12*time.Hour), origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})

	// undelegate from one validator that got slashed 50%
	dva.TrackUndelegation(sdk.Coins{sdk.NewInt64Coin(stakeDenom, 25)})

	require.Nil(t, dva.DelegatedFree)
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 75)}, dva.DelegatedVesting)

	// undelegate from the other validator that did not get slashed
	dva.TrackUndelegation(sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})
	require.Nil(t, dva.DelegatedFree)
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 25)}, dva.DelegatedVesting)
}

func TestGetVestedCoinsPeriodicVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(24 * time.Hour)
	periods := types.Periods{
		types.Period{Length: int64(12 * 60 * 60), Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}},
		types.Period{Length: int64(6 * 60 * 60), Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}},
		types.Period{Length: int64(6 * 60 * 60), Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}},
	}

	bacc, origCoins := initBaseAccount()
	pva, err := types.NewPeriodicVestingAccount(bacc, origCoins, now.Unix(), periods)
	require.NoError(t, err)

	// require no coins vested at the beginning of the vesting schedule
	vestedCoins := pva.GetVestedCoins(now)
	require.Nil(t, vestedCoins)

	// require all coins vested at the end of the vesting schedule
	vestedCoins = pva.GetVestedCoins(endTime)
	require.Equal(t, origCoins, vestedCoins)

	// require no coins vested during first vesting period
	vestedCoins = pva.GetVestedCoins(now.Add(6 * time.Hour))
	require.Nil(t, vestedCoins)

	// require 50% of coins vested after period 1
	vestedCoins = pva.GetVestedCoins(now.Add(12 * time.Hour))
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}, vestedCoins)

	// require period 2 coins don't vest until period is over
	vestedCoins = pva.GetVestedCoins(now.Add(15 * time.Hour))
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}, vestedCoins)

	// require 75% of coins vested after period 2
	vestedCoins = pva.GetVestedCoins(now.Add(18 * time.Hour))
	require.Equal(t,
		sdk.Coins{
			sdk.NewInt64Coin(feeDenom, 750), sdk.NewInt64Coin(stakeDenom, 75),
		}, vestedCoins)

	// require 100% of coins vested
	vestedCoins = pva.GetVestedCoins(now.Add(48 * time.Hour))
	require.Equal(t, origCoins, vestedCoins)
}

func TestOverflowAndNegativeVestedCoinsPeriods(t *testing.T) {
	now := tmtime.Now()
	tests := []struct {
		name    string
		periods []types.Period
		wantErr string
	}{
		{
			"negative .Length",
			types.Periods{
				types.Period{Length: -1, Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}},
				types.Period{Length: 6 * 60 * 60, Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}},
			},
			"period #0 has a negative length: -1",
		},
		{
			"overflow after .Length additions",
			types.Periods{
				types.Period{Length: 9223372036854775108, Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}},
				types.Period{Length: 6 * 60 * 60, Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}},
			},
			"vesting start-time cannot be before end-time", // it overflow to a negative number, making start-time > end-time
		},
		{
			"good periods that are not negative nor overflow",
			types.Periods{
				types.Period{Length: now.Unix() - 1000, Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}},
				types.Period{Length: 60, Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}},
				types.Period{Length: 30, Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}},
			},
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bacc, origCoins := initBaseAccount()
			pva, err := types.NewPeriodicVestingAccount(bacc, origCoins, now.Unix(), tt.periods)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			if pbva := pva.BaseVestingAccount; pbva.EndTime < 0 {
				t.Fatalf("Unfortunately we still have negative .EndTime :-(: %d", pbva.EndTime)
			}
		})
	}
}

func TestGetVestingCoinsPeriodicVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(24 * time.Hour)
	periods := types.Periods{
		types.Period{Length: int64(12 * 60 * 60), Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}},
		types.Period{Length: int64(6 * 60 * 60), Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}},
		types.Period{Length: int64(6 * 60 * 60), Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}},
	}

	bacc, origCoins := initBaseAccount()
	pva, err := types.NewPeriodicVestingAccount(bacc, origCoins, now.Unix(), periods)
	require.NoError(t, err)

	// require all coins vesting at the beginning of the vesting schedule
	vestingCoins := pva.GetVestingCoins(now)
	require.Equal(t, origCoins, vestingCoins)

	// require no coins vesting at the end of the vesting schedule
	vestingCoins = pva.GetVestingCoins(endTime)
	require.Equal(t, emptyCoins, vestingCoins)

	// require 50% of coins vesting
	vestingCoins = pva.GetVestingCoins(now.Add(12 * time.Hour))
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}, vestingCoins)

	// require 50% of coins vesting after period 1, but before period 2 completes.
	vestingCoins = pva.GetVestingCoins(now.Add(15 * time.Hour))
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}, vestingCoins)

	// require 25% of coins vesting after period 2
	vestingCoins = pva.GetVestingCoins(now.Add(18 * time.Hour))
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}, vestingCoins)

	// require 0% of coins vesting after vesting complete
	vestingCoins = pva.GetVestingCoins(now.Add(48 * time.Hour))
	require.Equal(t, emptyCoins, vestingCoins)
}

func TestSpendableCoinsPeriodicVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(24 * time.Hour)
	periods := types.Periods{
		types.Period{Length: int64(12 * 60 * 60), Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}},
		types.Period{Length: int64(6 * 60 * 60), Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}},
		types.Period{Length: int64(6 * 60 * 60), Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}},
	}

	bacc, origCoins := initBaseAccount()
	pva, err := types.NewPeriodicVestingAccount(bacc, origCoins, now.Unix(), periods)
	require.NoError(t, err)

	// require that there exist no spendable coins at the beginning of the
	// vesting schedule
	lockedCoins := pva.LockedCoins(now)
	require.Equal(t, origCoins, lockedCoins)

	// require that all original coins are spendable at the end of the vesting
	// schedule
	lockedCoins = pva.LockedCoins(endTime)
	require.Equal(t, sdk.NewCoins(), lockedCoins)

	// require that all still vesting coins (50%) are locked
	lockedCoins = pva.LockedCoins(now.Add(12 * time.Hour))
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}, lockedCoins)
}

func TestTrackDelegationPeriodicVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(24 * time.Hour)
	periods := types.Periods{
		types.Period{Length: int64(12 * 60 * 60), Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}},
		types.Period{Length: int64(6 * 60 * 60), Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}},
		types.Period{Length: int64(6 * 60 * 60), Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}},
	}

	bacc, origCoins := initBaseAccount()

	// require the ability to delegate all vesting coins
	pva, err := types.NewPeriodicVestingAccount(bacc, origCoins, now.Unix(), periods)
	require.NoError(t, err)
	pva.TrackDelegation(now, origCoins, origCoins)
	require.Equal(t, origCoins, pva.DelegatedVesting)
	require.Nil(t, pva.DelegatedFree)

	// require the ability to delegate all vested coins
	pva, err = types.NewPeriodicVestingAccount(bacc, origCoins, now.Unix(), periods)
	require.NoError(t, err)
	pva.TrackDelegation(endTime, origCoins, origCoins)
	require.Nil(t, pva.DelegatedVesting)
	require.Equal(t, origCoins, pva.DelegatedFree)

	// delegate half of vesting coins
	pva, err = types.NewPeriodicVestingAccount(bacc, origCoins, now.Unix(), periods)
	require.NoError(t, err)
	pva.TrackDelegation(now, origCoins, periods[0].Amount)
	// require that all delegated coins are delegated vesting
	require.Equal(t, pva.DelegatedVesting, periods[0].Amount)
	require.Nil(t, pva.DelegatedFree)

	// delegate 75% of coins, split between vested and vesting
	pva, err = types.NewPeriodicVestingAccount(bacc, origCoins, now.Unix(), periods)
	require.NoError(t, err)
	pva.TrackDelegation(now.Add(12*time.Hour), origCoins, periods[0].Amount.Add(periods[1].Amount...))
	// require that the maximum possible amount of vesting coins are chosen for delegation.
	require.Equal(t, pva.DelegatedFree, periods[1].Amount)
	require.Equal(t, pva.DelegatedVesting, periods[0].Amount)

	// require the ability to delegate all vesting coins (50%) and all vested coins (50%)
	pva, err = types.NewPeriodicVestingAccount(bacc, origCoins, now.Unix(), periods)
	require.NoError(t, err)
	pva.TrackDelegation(now.Add(12*time.Hour), origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)}, pva.DelegatedVesting)
	require.Nil(t, pva.DelegatedFree)

	pva.TrackDelegation(now.Add(12*time.Hour), origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)}, pva.DelegatedVesting)
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)}, pva.DelegatedFree)

	// require no modifications when delegation amount is zero or not enough funds
	pva, err = types.NewPeriodicVestingAccount(bacc, origCoins, now.Unix(), periods)
	require.NoError(t, err)
	require.Panics(t, func() {
		pva.TrackDelegation(endTime, origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 1000000)})
	})
	require.Nil(t, pva.DelegatedVesting)
	require.Nil(t, pva.DelegatedFree)
}

func TestTrackUndelegationPeriodicVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(24 * time.Hour)
	periods := types.Periods{
		types.Period{Length: int64(12 * 60 * 60), Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 500), sdk.NewInt64Coin(stakeDenom, 50)}},
		types.Period{Length: int64(6 * 60 * 60), Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}},
		types.Period{Length: int64(6 * 60 * 60), Amount: sdk.Coins{sdk.NewInt64Coin(feeDenom, 250), sdk.NewInt64Coin(stakeDenom, 25)}},
	}

	bacc, origCoins := initBaseAccount()

	// require the ability to undelegate all vesting coins at the beginning of vesting
	pva, err := types.NewPeriodicVestingAccount(bacc, origCoins, now.Unix(), periods)
	require.NoError(t, err)
	pva.TrackDelegation(now, origCoins, origCoins)
	pva.TrackUndelegation(origCoins)
	require.Nil(t, pva.DelegatedFree)
	require.Equal(t, emptyCoins, pva.DelegatedVesting)

	// require the ability to undelegate all vested coins at the end of vesting
	pva, err = types.NewPeriodicVestingAccount(bacc, origCoins, now.Unix(), periods)
	require.NoError(t, err)
	pva.TrackDelegation(endTime, origCoins, origCoins)
	pva.TrackUndelegation(origCoins)
	require.Equal(t, emptyCoins, pva.DelegatedFree)
	require.Nil(t, pva.DelegatedVesting)

	// require the ability to undelegate half of coins
	pva, err = types.NewPeriodicVestingAccount(bacc, origCoins, now.Unix(), periods)
	require.NoError(t, err)
	pva.TrackDelegation(endTime, origCoins, periods[0].Amount)
	pva.TrackUndelegation(periods[0].Amount)
	require.Equal(t, emptyCoins, pva.DelegatedFree)
	require.Nil(t, pva.DelegatedVesting)

	// require no modifications when the undelegation amount is zero
	pva, err = types.NewPeriodicVestingAccount(bacc, origCoins, now.Unix(), periods)
	require.NoError(t, err)
	require.Panics(t, func() {
		pva.TrackUndelegation(sdk.Coins{sdk.NewInt64Coin(stakeDenom, 0)})
	})
	require.Nil(t, pva.DelegatedFree)
	require.Nil(t, pva.DelegatedVesting)

	// vest 50% and delegate to two validators
	pva, err = types.NewPeriodicVestingAccount(bacc, origCoins, now.Unix(), periods)
	require.NoError(t, err)
	pva.TrackDelegation(now.Add(12*time.Hour), origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})
	pva.TrackDelegation(now.Add(12*time.Hour), origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})

	// undelegate from one validator that got slashed 50%
	pva.TrackUndelegation(sdk.Coins{sdk.NewInt64Coin(stakeDenom, 25)})
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 25)}, pva.DelegatedFree)
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)}, pva.DelegatedVesting)

	// undelegate from the other validator that did not get slashed
	pva.TrackUndelegation(sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})
	require.Equal(t, emptyCoins, pva.DelegatedFree)
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 25)}, pva.DelegatedVesting)
}

func TestGetVestedCoinsPermLockedVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(1000 * 24 * time.Hour)

	bacc, origCoins := initBaseAccount()

	// require no coins are vested
	plva, err := types.NewPermanentLockedAccount(bacc, origCoins)
	require.NoError(t, err)
	vestedCoins := plva.GetVestedCoins(now)
	require.Nil(t, vestedCoins)

	// require no coins be vested at end time
	vestedCoins = plva.GetVestedCoins(endTime)
	require.Nil(t, vestedCoins)
}

func TestGetVestingCoinsPermLockedVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(1000 * 24 * time.Hour)

	bacc, origCoins := initBaseAccount()

	// require all coins vesting at the beginning of the schedule
	plva, err := types.NewPermanentLockedAccount(bacc, origCoins)
	require.NoError(t, err)
	vestingCoins := plva.GetVestingCoins(now)
	require.Equal(t, origCoins, vestingCoins)

	// require all coins vesting at the end time
	vestingCoins = plva.GetVestingCoins(endTime)
	require.Equal(t, origCoins, vestingCoins)
}

func TestSpendableCoinsPermLockedVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(1000 * 24 * time.Hour)

	bacc, origCoins := initBaseAccount()

	// require that all coins are locked in the beginning of the vesting
	// schedule
	plva, err := types.NewPermanentLockedAccount(bacc, origCoins)
	require.NoError(t, err)
	lockedCoins := plva.LockedCoins(now)
	require.True(t, lockedCoins.Equal(origCoins))

	// require that all coins are still locked at end time
	lockedCoins = plva.LockedCoins(endTime)
	require.True(t, lockedCoins.Equal(origCoins))

	// delegate some locked coins
	// require that locked is reduced
	delegatedAmount := sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50))
	plva.TrackDelegation(now.Add(12*time.Hour), origCoins, delegatedAmount)
	lockedCoins = plva.LockedCoins(now.Add(12 * time.Hour))
	require.True(t, lockedCoins.Equal(origCoins.Sub(delegatedAmount...)))
}

func TestTrackDelegationPermLockedVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(1000 * 24 * time.Hour)

	bacc, origCoins := initBaseAccount()

	// require the ability to delegate all vesting coins
	plva, err := types.NewPermanentLockedAccount(bacc, origCoins)
	require.NoError(t, err)
	plva.TrackDelegation(now, origCoins, origCoins)
	require.Equal(t, origCoins, plva.DelegatedVesting)
	require.Nil(t, plva.DelegatedFree)

	// require the ability to delegate all vested coins at endTime
	plva, err = types.NewPermanentLockedAccount(bacc, origCoins)
	require.NoError(t, err)
	plva.TrackDelegation(endTime, origCoins, origCoins)
	require.Equal(t, origCoins, plva.DelegatedVesting)
	require.Nil(t, plva.DelegatedFree)

	// require no modifications when delegation amount is zero or not enough funds
	plva, err = types.NewPermanentLockedAccount(bacc, origCoins)
	require.NoError(t, err)
	require.Panics(t, func() {
		plva.TrackDelegation(endTime, origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 1000000)})
	})
	require.Nil(t, plva.DelegatedVesting)
	require.Nil(t, plva.DelegatedFree)
}

func TestTrackUndelegationPermLockedVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(1000 * 24 * time.Hour)

	bacc, origCoins := initBaseAccount()

	// require the ability to undelegate all vesting coins
	plva, err := types.NewPermanentLockedAccount(bacc, origCoins)
	require.NoError(t, err)
	plva.TrackDelegation(now, origCoins, origCoins)
	plva.TrackUndelegation(origCoins)
	require.Nil(t, plva.DelegatedFree)
	require.Equal(t, emptyCoins, plva.DelegatedVesting)

	// require the ability to undelegate all vesting coins at endTime
	plva, err = types.NewPermanentLockedAccount(bacc, origCoins)
	require.NoError(t, err)
	plva.TrackDelegation(endTime, origCoins, origCoins)
	plva.TrackUndelegation(origCoins)
	require.Nil(t, plva.DelegatedFree)
	require.Equal(t, emptyCoins, plva.DelegatedVesting)

	// require no modifications when the undelegation amount is zero
	plva, err = types.NewPermanentLockedAccount(bacc, origCoins)
	require.NoError(t, err)
	require.Panics(t, func() {
		plva.TrackUndelegation(sdk.Coins{sdk.NewInt64Coin(stakeDenom, 0)})
	})
	require.Nil(t, plva.DelegatedFree)
	require.Nil(t, plva.DelegatedVesting)

	// delegate to two validators
	plva, err = types.NewPermanentLockedAccount(bacc, origCoins)
	require.NoError(t, err)
	plva.TrackDelegation(now, origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})
	plva.TrackDelegation(now, origCoins, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})

	// undelegate from one validator that got slashed 50%
	plva.TrackUndelegation(sdk.Coins{sdk.NewInt64Coin(stakeDenom, 25)})

	require.Nil(t, plva.DelegatedFree)
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 75)}, plva.DelegatedVesting)

	// undelegate from the other validator that did not get slashed
	plva.TrackUndelegation(sdk.Coins{sdk.NewInt64Coin(stakeDenom, 50)})
	require.Nil(t, plva.DelegatedFree)
	require.Equal(t, sdk.Coins{sdk.NewInt64Coin(stakeDenom, 25)}, plva.DelegatedVesting)
}

func TestGenesisAccountValidate(t *testing.T) {
	pubkey := secp256k1.GenPrivKey().PubKey()
	addr := sdk.AccAddress(pubkey.Address())
	baseAcc := authtypes.NewBaseAccount(addr, pubkey, 0, 0)
	initialVesting := sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 50))
	baseVestingWithCoins, err := types.NewBaseVestingAccount(baseAcc, initialVesting, 100)
	require.NoError(t, err)
	tests := []struct {
		name   string
		acc    authtypes.GenesisAccount
		expErr bool
	}{
		{
			"valid base account",
			baseAcc,
			false,
		},
		{
			"invalid base valid account",
			authtypes.NewBaseAccount(addr, secp256k1.GenPrivKey().PubKey(), 0, 0),
			true,
		},
		{
			"valid base vesting account",
			baseVestingWithCoins,
			false,
		},
		{
			"valid continuous vesting account",
			func() authtypes.GenesisAccount {
				acc, _ := types.NewContinuousVestingAccount(baseAcc, initialVesting, 100, 200)
				return acc
			}(),
			false,
		},
		{
			"invalid vesting times",
			func() authtypes.GenesisAccount {
				acc, _ := types.NewContinuousVestingAccount(baseAcc, initialVesting, 1654668078, 1554668078)
				return acc
			}(),
			true,
		},
		{
			"valid periodic vesting account",
			func() authtypes.GenesisAccount {
				acc, _ := types.NewPeriodicVestingAccount(baseAcc, initialVesting, 0, types.Periods{types.Period{Length: int64(100), Amount: sdk.Coins{sdk.NewInt64Coin(sdk.DefaultBondDenom, 50)}}})
				return acc
			}(),
			false,
		},
		{
			"invalid vesting period lengths",
			types.NewPeriodicVestingAccountRaw(
				baseVestingWithCoins,
				0, types.Periods{types.Period{Length: int64(50), Amount: sdk.Coins{sdk.NewInt64Coin(sdk.DefaultBondDenom, 50)}}}),
			true,
		},
		{
			"invalid vesting period amounts",
			types.NewPeriodicVestingAccountRaw(
				baseVestingWithCoins,
				0, types.Periods{types.Period{Length: int64(100), Amount: sdk.Coins{sdk.NewInt64Coin(sdk.DefaultBondDenom, 25)}}}),
			true,
		},
		{
			"valid permanent locked vesting account",
			func() authtypes.GenesisAccount {
				acc, _ := types.NewPermanentLockedAccount(baseAcc, initialVesting)
				return acc
			}(),
			false,
		},
		{
			"invalid positive end time for permanently locked vest account",
			&types.PermanentLockedAccount{BaseVestingAccount: baseVestingWithCoins},
			true,
		},
	}

	for _, tt := range tests {
		tt := tt

		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expErr, tt.acc.Validate() != nil)
		})
	}
}

func initBaseAccount() (*authtypes.BaseAccount, sdk.Coins) {
	_, _, addr := testdata.KeyTestPubAddr()
	origCoins := sdk.Coins{sdk.NewInt64Coin(feeDenom, 1000), sdk.NewInt64Coin(stakeDenom, 100)}
	bacc := authtypes.NewBaseAccountWithAddress(addr)

	return bacc, origCoins
}

func TestVestingAccountTestSuite(t *testing.T) {
	suite.Run(t, new(VestingAccountTestSuite))
}

func TestUpdateScheduleBaseVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(24 * time.Hour)
	bacc, initialOrigCoins := initBaseAccount() // Use a distinct name for clarity

	testCases := []struct {
		name             string
		originalVesting  sdk.Coins
		delegatedVesting sdk.Coins
		delegatedFree    sdk.Coins
		rewardCoins      sdk.Coins
		expectedVesting  sdk.Coins
		expectError      bool
	}{
		{
			name:             "no delegations",
			originalVesting:  initialOrigCoins,
			delegatedVesting: sdk.NewCoins(),
			delegatedFree:    sdk.NewCoins(),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			expectedVesting:  initialOrigCoins, // No change expected
			expectError:      false,
		},
		{
			name:             "50% vesting, 50% free delegation",
			originalVesting:  initialOrigCoins,
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			expectedVesting: sdk.NewCoins( // fee: 1000, stake: 100 + 50 = 150
				sdk.NewInt64Coin(feeDenom, 1000),
				sdk.NewInt64Coin(stakeDenom, 150),
			),
			expectError: false,
		},
		{
			name:             "100% delegated vesting",
			originalVesting:  initialOrigCoins,
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			delegatedFree:    sdk.NewCoins(),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			expectedVesting: sdk.NewCoins( // fee: 1000, stake: 100 + 100 = 200
				sdk.NewInt64Coin(feeDenom, 1000),
				sdk.NewInt64Coin(stakeDenom, 200),
			),
			expectError: false,
		},
		{
			name:             "100% delegated free",
			originalVesting:  initialOrigCoins,
			delegatedVesting: sdk.NewCoins(),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			expectedVesting:  initialOrigCoins, // No change expected
			expectError:      false,
		},
		{
			name:             "zero rewards",
			originalVesting:  initialOrigCoins,
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			rewardCoins:      sdk.NewCoins(),
			expectedVesting:  initialOrigCoins, // No change expected
			expectError:      false,
		},
		{
			name:             "delegation exceeds original vesting (should not happen in practice, but test)",
			originalVesting:  initialOrigCoins,
			delegatedVesting: initialOrigCoins.Add(sdk.NewInt64Coin(stakeDenom, 1)), // More than available
			delegatedFree:    sdk.NewCoins(),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			// Expecting update to proceed based on provided delegations, even if inconsistent
			expectedVesting: initialOrigCoins.Add(sdk.NewInt64Coin(stakeDenom, 100)), // All rewards go to vesting
			expectError:     false,                                                   // The UpdateSchedule function itself might not error here
		},
	}

	for _, tc := range testCases {
		tc := tc // Capture range variable
		t.Run(tc.name, func(t *testing.T) {
			// Create a new base vesting account for each test case
			bva, err := types.NewBaseVestingAccount(bacc, tc.originalVesting, endTime.Unix())
			require.NoError(t, err)

			// Set delegations for the test case
			bva.DelegatedVesting = tc.delegatedVesting
			bva.DelegatedFree = tc.delegatedFree

			// Update the schedule
			err = bva.UpdateSchedule(tc.rewardCoins)

			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				// Verify the original vesting amount is updated as expected
				require.Equal(t, tc.expectedVesting, bva.OriginalVesting, "OriginalVesting mismatch")
				// EndTime should not change for BaseVestingAccount during update
				require.Equal(t, endTime.Unix(), bva.EndTime, "EndTime mismatch")
			}
		})
	}
}

func TestUpdateScheduleContinuousVestingAcc(t *testing.T) {
	now := tmtime.Now()

	testCases := []struct {
		name             string
		startTime        int64
		endTime          int64
		originalVesting  sdk.Coins
		delegatedVesting sdk.Coins
		delegatedFree    sdk.Coins
		rewardCoins      sdk.Coins
		expectedVesting  sdk.Coins
		expectedEndTime  int64
		testTime         int64 // Time at which test is run (for time-dependent tests)
	}{
		{
			name:             "basic 50-50 delegation split",
			startTime:        now.Unix(),
			endTime:          now.Add(24 * time.Hour).Unix(),
			originalVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			expectedVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 150)), // Original 100 + 50 new
			expectedEndTime:  now.Add(24 * time.Hour).Unix(),
			testTime:         now.Add(12 * time.Hour).Unix(),
		},
		{
			name:             "100% delegated vesting",
			startTime:        now.Unix(),
			endTime:          now.Add(24 * time.Hour).Unix(),
			originalVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			delegatedFree:    sdk.NewCoins(),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			expectedVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 200)), // Original 100 + 100 new
			expectedEndTime:  now.Add(24 * time.Hour).Unix(),
			testTime:         now.Add(12 * time.Hour).Unix(),
		},
		{
			name:             "100% delegated free",
			startTime:        now.Unix(),
			endTime:          now.Add(24 * time.Hour).Unix(),
			originalVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			delegatedVesting: sdk.NewCoins(),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			expectedVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)), // No change
			expectedEndTime:  now.Add(24 * time.Hour).Unix(),
			testTime:         now.Add(12 * time.Hour).Unix(),
		},
		{
			name:             "uneven delegation split (75% vesting, 25% free)",
			startTime:        now.Unix(),
			endTime:          now.Add(24 * time.Hour).Unix(),
			originalVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 75)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 25)),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			expectedVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 175)), // Original 100 + 75 new
			expectedEndTime:  now.Add(24 * time.Hour).Unix(),
			testTime:         now.Add(12 * time.Hour).Unix(),
		},
		{
			name:             "large reward amount",
			startTime:        now.Unix(),
			endTime:          now.Add(24 * time.Hour).Unix(),
			originalVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 10000)),
			expectedVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 5100)), // Original 100 + 5000 new
			expectedEndTime:  now.Add(24 * time.Hour).Unix(),
			testTime:         now.Add(12 * time.Hour).Unix(),
		},
		{
			name:             "partial delegation (50% of vesting delegated)",
			startTime:        now.Unix(),
			endTime:          now.Add(24 * time.Hour).Unix(),
			originalVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			expectedVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 200)), // Original 100 + 100 new
			expectedEndTime:  now.Add(24 * time.Hour).Unix(),
			testTime:         now.Add(12 * time.Hour).Unix(),
		},
		{
			name:             "update after vesting period completed",
			startTime:        now.Add(-48 * time.Hour).Unix(), // Start time in the past
			endTime:          now.Add(-24 * time.Hour).Unix(), // End time in the past
			originalVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			expectedVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 150)), // Original 100 + 50 new
			expectedEndTime:  now.Add(-24 * time.Hour).Unix(),                 // End time should remain unchanged
			testTime:         now.Unix(),
		},
		{
			name:             "update at exactly the vesting end time",
			startTime:        now.Add(-24 * time.Hour).Unix(),
			endTime:          now.Unix(), // End time is now
			originalVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			expectedVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 150)), // Original 100 + 50 new
			expectedEndTime:  now.Unix(),
			testTime:         now.Unix(),
		},
		{
			name:             "multiple denominations in original vesting",
			startTime:        now.Unix(),
			endTime:          now.Add(24 * time.Hour).Unix(),
			originalVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			expectedVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 150)),
			expectedEndTime:  now.Add(24 * time.Hour).Unix(),
			testTime:         now.Add(12 * time.Hour).Unix(),
		},
		{
			name:             "zero rewards",
			startTime:        now.Unix(),
			endTime:          now.Add(24 * time.Hour).Unix(),
			originalVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 0)),
			expectedVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)), // No change
			expectedEndTime:  now.Add(24 * time.Hour).Unix(),
			testTime:         now.Add(12 * time.Hour).Unix(),
		},
		{
			name:             "nil rewards",
			startTime:        now.Unix(),
			endTime:          now.Add(24 * time.Hour).Unix(),
			originalVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			rewardCoins:      nil,
			expectedVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)), // No change
			expectedEndTime:  now.Add(24 * time.Hour).Unix(),
			testTime:         now.Add(12 * time.Hour).Unix(),
		},
		{
			name:             "start time equals end time (zero duration)",
			startTime:        now.Unix(),
			endTime:          now.Unix(), // Zero duration
			originalVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			expectedVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 150)), // Should still update vesting amount
			expectedEndTime:  now.Unix(),                                      // End time remains unchanged
			testTime:         now.Unix(),
		},
		{
			name:             "update before start time",
			startTime:        now.Add(1 * time.Hour).Unix(), // Starts in the future
			endTime:          now.Add(25 * time.Hour).Unix(),
			originalVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100)),
			expectedVesting:  sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 150)),
			expectedEndTime:  now.Add(25 * time.Hour).Unix(),
			testTime:         now.Unix(), // Test time is before start time
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			bacc, _ := initBaseAccount()
			cva, err := types.NewContinuousVestingAccount(bacc, tc.originalVesting, tc.startTime, tc.endTime)
			require.NoError(t, err)

			// Setup delegations
			cva.DelegatedVesting = tc.delegatedVesting
			cva.DelegatedFree = tc.delegatedFree

			// Only test future vesting if the vesting period hasn't completed yet and has a non-zero duration
			if tc.endTime > tc.testTime && tc.endTime > tc.startTime {
				// Check vested coins at future time points BEFORE update
				// Calculate several future checkpoints to verify vesting progression
				duration := tc.endTime - tc.startTime
				checkpoints := []float64{0.25, 0.5, 0.75, 1.0} // 25%, 50%, 75%, 100% of vesting period

				// Store pre-update vested amounts for each checkpoint
				preUpdateVestedCoins := make([]sdk.Coins, len(checkpoints))
				for i, fraction := range checkpoints {
					// Calculate the checkpoint time
					checkpointTime := tc.startTime + int64(float64(duration)*fraction)
					if checkpointTime > tc.testTime {
						// Only track future checkpoints (after our test time)
						preUpdateVestedCoins[i] = cva.GetVestedCoins(time.Unix(checkpointTime, 0))
					}
				}

				// Update the vesting schedule
				err = cva.UpdateSchedule(tc.rewardCoins)
				require.NoError(t, err)

				// Verify results
				require.Equal(t, tc.expectedVesting, cva.OriginalVesting)
				require.Equal(t, tc.expectedEndTime, cva.EndTime)

				// Check vested coins at the same future time points AFTER update
				for i, fraction := range checkpoints {
					checkpointTime := tc.startTime + int64(float64(duration)*fraction)
					if checkpointTime > tc.testTime {
						postUpdateVestedCoins := cva.GetVestedCoins(time.Unix(checkpointTime, 0))

						// For cases where rewards are added (expectedVesting > originalVesting)
						if !tc.expectedVesting.Equal(tc.originalVesting) && fraction > 0 {
							// Verify more coins vest at each checkpoint after the update
							for _, coin := range postUpdateVestedCoins {
								// Find matching coin in pre-update vested coins
								preUpdateAmt := math.ZeroInt()
								for _, preCoin := range preUpdateVestedCoins[i] {
									if preCoin.Denom == coin.Denom {
										preUpdateAmt = preCoin.Amount
										break
									}
								}

								// If this denomination had vesting coins before the update
								if !preUpdateAmt.IsZero() {
									// Verify more coins vest at this checkpoint after the update
									require.True(t, coin.Amount.GT(preUpdateAmt),
										"Checkpoint %v (%v%%): Expected more vested coins after update. Got %v, expected more than %v",
										checkpointTime, fraction*100, coin, preUpdateAmt)
								}
							}
						}
					}
				}
			} else {
				// For cases where vesting has already completed or zero duration
				err = cva.UpdateSchedule(tc.rewardCoins)
				require.NoError(t, err)

				// Verify results
				require.Equal(t, tc.expectedVesting, cva.OriginalVesting)
				require.Equal(t, tc.expectedEndTime, cva.EndTime)
			}

			// Verify vesting calculations still work correctly
			if tc.testTime > tc.startTime && tc.testTime < tc.endTime {
				// If we're in the middle of vesting, check that GetVestedCoins returns the expected amount
				elapsed := tc.testTime - tc.startTime
				duration := tc.endTime - tc.startTime

				// Calculate expected vested coins based on linear vesting
				expectedVestedRatio := math.LegacyNewDec(elapsed).Quo(math.LegacyNewDec(duration))
				expectedVestedCoins := sdk.NewCoins()

				for _, coin := range tc.expectedVesting {
					vestedAmt := math.LegacyNewDec(coin.Amount.Int64()).Mul(expectedVestedRatio).RoundInt64()
					expectedVestedCoins = expectedVestedCoins.Add(sdk.NewInt64Coin(coin.Denom, vestedAmt))
				}

				vestedCoins := cva.GetVestedCoins(time.Unix(tc.testTime, 0))
				require.Equal(t, expectedVestedCoins, vestedCoins)
			}
		})
	}
}

func TestUpdateScheduleDelayedVestingAcc(t *testing.T) {
	now := tmtime.Now()
	endTime := now.Add(24 * time.Hour).Unix()
	bacc, initialOrigCoins := initBaseAccount()
	rewardCoins := sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100))

	testCases := []struct {
		name             string
		endTime          int64
		originalVesting  sdk.Coins
		delegatedVesting sdk.Coins
		delegatedFree    sdk.Coins
		rewardCoins      sdk.Coins
		expectedVesting  sdk.Coins
		expectedEndTime  int64 // Delayed vesting EndTime should not change
		expectError      bool
	}{
		{
			name:             "50% vesting, 50% free delegation",
			endTime:          endTime,
			originalVesting:  initialOrigCoins,
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			rewardCoins:      rewardCoins,
			expectedVesting:  initialOrigCoins.Add(sdk.NewInt64Coin(stakeDenom, 50)), // 50% rewards added
			expectedEndTime:  endTime,
			expectError:      false,
		},
		{
			name:             "100% vesting delegation",
			endTime:          endTime,
			originalVesting:  initialOrigCoins,
			delegatedVesting: initialOrigCoins,
			delegatedFree:    sdk.NewCoins(),
			rewardCoins:      rewardCoins,
			expectedVesting:  initialOrigCoins.Add(rewardCoins...),
			expectedEndTime:  endTime,
			expectError:      false,
		},
		{
			name:             "100% free delegation",
			endTime:          endTime,
			originalVesting:  initialOrigCoins,
			delegatedVesting: sdk.NewCoins(),
			delegatedFree:    initialOrigCoins,
			rewardCoins:      rewardCoins,
			expectedVesting:  initialOrigCoins, // No rewards added
			expectedEndTime:  endTime,
			expectError:      false,
		},
		{
			name:             "zero rewards",
			endTime:          endTime,
			originalVesting:  initialOrigCoins,
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			rewardCoins:      sdk.NewCoins(),
			expectedVesting:  initialOrigCoins,
			expectedEndTime:  endTime,
			expectError:      false,
		},
		{
			name:             "nil rewards",
			endTime:          endTime,
			originalVesting:  initialOrigCoins,
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			rewardCoins:      nil,
			expectedVesting:  initialOrigCoins,
			expectedEndTime:  endTime,
			expectError:      false,
		},
		{
			name:             "update after vesting end time",
			endTime:          now.Add(-1 * time.Hour).Unix(), // End time already passed
			originalVesting:  initialOrigCoins,
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			rewardCoins:      rewardCoins,
			expectedVesting:  initialOrigCoins.Add(sdk.NewInt64Coin(stakeDenom, 50)),
			expectedEndTime:  now.Add(-1 * time.Hour).Unix(), // End time does not change
			expectError:      false,
		},
	}

	for _, tc := range testCases {
		tc := tc // capture range variable
		t.Run(tc.name, func(t *testing.T) {
			dva, err := types.NewDelayedVestingAccount(bacc, tc.originalVesting, tc.endTime)
			require.NoError(t, err)

			// Setup delegations
			dva.DelegatedVesting = tc.delegatedVesting
			dva.DelegatedFree = tc.delegatedFree

			// Update schedule
			err = dva.UpdateSchedule(tc.rewardCoins)

			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)

				// Verify results
				require.Equal(t, tc.expectedVesting, dva.OriginalVesting, "OriginalVesting mismatch")
				require.Equal(t, tc.expectedEndTime, dva.EndTime, "EndTime mismatch")
			}
		})
	}
}

func TestUpdateSchedulePermanentLockedAcc(t *testing.T) {
	bacc, initialOrigCoins := initBaseAccount()
	rewardCoins := sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 100))

	testCases := []struct {
		name             string
		originalVesting  sdk.Coins
		delegatedVesting sdk.Coins
		delegatedFree    sdk.Coins // DelegatedFree is not applicable but test its handling
		rewardCoins      sdk.Coins
		expectedVesting  sdk.Coins
		expectError      bool
	}{
		{
			name:             "50% vesting delegation (free ignored)",
			originalVesting:  initialOrigCoins,
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)), // Should be ignored by logic
			rewardCoins:      rewardCoins,
			expectedVesting:  initialOrigCoins.Add(sdk.NewInt64Coin(stakeDenom, 50)), // 50% rewards added
			expectError:      false,
		},
		{
			name:             "100% vesting delegation",
			originalVesting:  initialOrigCoins,
			delegatedVesting: initialOrigCoins,
			delegatedFree:    sdk.NewCoins(),
			rewardCoins:      rewardCoins,
			expectedVesting:  initialOrigCoins.Add(rewardCoins...),
			expectError:      false,
		},
		{
			name:             "0% vesting delegation (all free delegation - ignored)",
			originalVesting:  initialOrigCoins,
			delegatedVesting: sdk.NewCoins(),
			delegatedFree:    initialOrigCoins, // Should be ignored
			rewardCoins:      rewardCoins,
			expectedVesting:  initialOrigCoins, // 0% rewards added
			expectError:      false,
		},
		{
			name:             "zero rewards",
			originalVesting:  initialOrigCoins,
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(),
			rewardCoins:      sdk.NewCoins(),
			expectedVesting:  initialOrigCoins,
			expectError:      false,
		},
		{
			name:             "nil rewards",
			originalVesting:  initialOrigCoins,
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(),
			rewardCoins:      nil,
			expectedVesting:  initialOrigCoins,
			expectError:      false,
		},
		{
			name:             "rewards in new denom",
			originalVesting:  initialOrigCoins,
			delegatedVesting: sdk.NewCoins(sdk.NewInt64Coin(stakeDenom, 50)),
			delegatedFree:    sdk.NewCoins(),
			rewardCoins:      sdk.NewCoins(sdk.NewInt64Coin("newdenom", 100)),
			expectedVesting:  initialOrigCoins,
			expectError:      false,
		},
	}

	for _, tc := range testCases {
		tc := tc // capture range variable
		t.Run(tc.name, func(t *testing.T) {
			plva, err := types.NewPermanentLockedAccount(bacc, tc.originalVesting)
			require.NoError(t, err)

			// Setup delegations
			plva.DelegatedVesting = tc.delegatedVesting
			plva.DelegatedFree = tc.delegatedFree // Although not used, set for completeness

			// Update schedule
			err = plva.UpdateSchedule(tc.rewardCoins)

			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)

				// Verify results
				require.Equal(t, tc.expectedVesting, plva.OriginalVesting, "OriginalVesting mismatch")
				// End time should always be 0 for permanent locked accounts
				require.Equal(t, int64(0), plva.EndTime, "EndTime mismatch")
			}
		})
	}
}

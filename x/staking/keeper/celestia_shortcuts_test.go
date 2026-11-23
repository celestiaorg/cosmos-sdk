package keeper_test

import (
	"bytes"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/golang/mock/gomock"

	"cosmossdk.io/math"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/cosmos/cosmos-sdk/x/staking/testutil"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestCelestiaHistoricalSortMatchesStock compares the memoized voting-power
// sort with the stock comparator, including power ties and unbonded validators.
func (s *KeeperTestSuite) TestCelestiaHistoricalSortMatchesStock() {
	ctx, keeper := s.ctx, s.stakingKeeper
	require := s.Require()
	r := keeper.PowerReduction(ctx)
	for seed := 0; seed < 50; seed++ {
		n := 1 + seed*3
		vals := make([]stakingtypes.Validator, n)
		for i := range vals {
			v := testutil.NewValidator(s.T(), sdk.ValAddress(PKs[i].Address()), PKs[i])
			v.Tokens = keeper.TokensFromConsensusPower(ctx, int64((i*7+seed)%5))
			if i%6 == 5 {
				// GetConsAddr error (no cached pubkey): the comparator's error=>false branch
				v.ConsensusPubkey = &codectypes.Any{}
			}
			if (i+seed)%4 == 0 {
				v.Status = stakingtypes.Unbonded
			} else {
				v.Status = stakingtypes.Bonded
			}
			vals[i] = v
		}
		a := append([]stakingtypes.Validator(nil), vals...)
		b := append([]stakingtypes.Validator(nil), vals...)
		restore := setExecutionCaches(false)
		ha := stakingtypes.NewHistoricalInfo(ctx.BlockHeader(), stakingtypes.Validators{Validators: a}, r)
		restore()
		restore = setExecutionCaches(true)
		hb := stakingtypes.NewHistoricalInfo(ctx.BlockHeader(), stakingtypes.Validators{Validators: b}, r)
		restore()
		require.Equal(len(ha.Valset), len(hb.Valset))
		for i := range ha.Valset {
			require.Equal(ha.Valset[i].OperatorAddress, hb.Valset[i].OperatorAddress, "seed %d idx %d", seed, i)
			require.Equal(a[i].OperatorAddress, b[i].OperatorAddress)
		}
	}
}

// TestCelestiaTrackHistoricalInfoShortcut runs TrackHistoricalInfo over many
// heights with a small HistoricalEntries, flags off and on, and compares the
// stored historical infos.
func (s *KeeperTestSuite) TestCelestiaTrackHistoricalInfoShortcut() {
	require := s.Require()
	run := func(on bool) map[int64][]byte {
		defer setExecutionCaches(on)()
		s.SetupTest()
		ctx, keeper := s.ctx, s.stakingKeeper
		p, err := keeper.GetParams(ctx)
		require.NoError(err)
		p.HistoricalEntries = 3
		require.NoError(keeper.SetParams(ctx, p))
		for i := 0; i < 4; i++ {
			v := testutil.NewValidator(s.T(), sdk.ValAddress(PKs[i].Address().Bytes()), PKs[i])
			v.Status = stakingtypes.Unbonded
			v.Tokens = math.ZeroInt()
			v, _ = v.AddTokensFromDel(keeper.TokensFromConsensusPower(ctx, int64(1+i%2)))
			s.bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), stakingtypes.NotBondedPoolName, stakingtypes.BondedPoolName, gomock.Any())
			stakingkeeper.TestingUpdateValidator(keeper, ctx, v, true)
		}
		for h := int64(1); h <= 12; h++ {
			hctx := ctx.WithBlockHeader(cmtproto.Header{Height: h, Time: time.Unix(1_700_000_000+h, 0).UTC()})
			require.NoError(keeper.TrackHistoricalInfo(hctx))
		}
		out := map[int64][]byte{}
		store := s.storeService.OpenKVStore(ctx)
		for h := int64(0); h <= 12; h++ {
			bz, err := store.Get(stakingtypes.GetHistoricalInfoKey(h))
			require.NoError(err)
			if bz != nil {
				out[h] = bz
			}
		}
		return out
	}
	off, on := run(false), run(true)
	require.Len(off, 3)
	require.Len(on, 3)
	for h, bz := range off {
		require.True(bytes.Equal(bz, on[h]), "historical info %d differs", h)
	}
}

// TestCelestiaTrackHistoricalInfoCorrupt checks that undecodable stored bytes
// make TrackHistoricalInfo fail with the caches on, exactly as with them off.
func (s *KeeperTestSuite) TestCelestiaTrackHistoricalInfoCorrupt() {
	require := s.Require()
	run := func(on bool) error {
		defer setExecutionCaches(on)()
		s.SetupTest()
		ctx, keeper := s.ctx, s.stakingKeeper
		p, err := keeper.GetParams(ctx)
		require.NoError(err)
		p.HistoricalEntries = 2
		require.NoError(keeper.SetParams(ctx, p))
		var last error
		for h := int64(1); h <= 4; h++ {
			hctx := ctx.WithBlockHeader(cmtproto.Header{Height: h, Time: time.Unix(1_700_000_000+h, 0).UTC()})
			if h == 3 {
				// overwrite the entry the next call prunes with bytes that do not decode
				store := s.storeService.OpenKVStore(ctx)
				require.NoError(store.Set(stakingtypes.GetHistoricalInfoKey(1), []byte{0xff, 0xff, 0xff}))
			}
			last = keeper.TrackHistoricalInfo(hctx)
			if last != nil {
				return last
			}
		}
		return nil
	}
	off, on := run(false), run(true)
	require.Error(off)
	require.Error(on)
	require.Equal(off.Error(), on.Error())
}

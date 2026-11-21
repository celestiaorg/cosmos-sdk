package keeper_test

import (
	"bytes"
	"fmt"
	"reflect"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/golang/mock/gomock"

	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/cosmos/cosmos-sdk/x/staking/testutil"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// celestiaPointers collects the addresses of all mutable memory reachable from
// v (pointers, slice backing arrays, maps), except the intentionally shared
// unpacked Any value and immutable time.Location / strings.
func celestiaPointers(v reflect.Value, path string, out map[uintptr]string) {
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() || v.Type() == reflect.TypeOf((*time.Location)(nil)) {
			return
		}
		out[v.Pointer()] = path
		celestiaPointers(v.Elem(), path+".*", out)
	case reflect.Interface:
		if !v.IsNil() {
			celestiaPointers(v.Elem(), path, out)
		}
	case reflect.Slice:
		if v.IsNil() {
			return
		}
		if v.Cap() > 0 {
			out[v.Pointer()] = path
		}
		for i := 0; i < v.Len(); i++ {
			celestiaPointers(v.Index(i), fmt.Sprintf("%s[%d]", path, i), out)
		}
	case reflect.Map:
		if !v.IsNil() {
			out[v.Pointer()] = path
		}
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			return
		}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if f.Name == "cachedValue" {
				continue // unpacked pubkey: shared by design (immutable)
			}
			celestiaPointers(v.Field(i), path+"."+f.Name, out)
		}
	}
}

func moduleCodec() codec.Codec {
	enc := moduletestutil.MakeTestEncodingConfig()
	stakingtypes.RegisterInterfaces(enc.InterfaceRegistry)
	return enc.Codec
}

func (s *KeeperTestSuite) requireNoSharedMemory(a, b interface{}) {
	pa, pb := map[uintptr]string{}, map[uintptr]string{}
	celestiaPointers(reflect.ValueOf(a), "v", pa)
	celestiaPointers(reflect.ValueOf(b), "v", pb)
	s.Require().NotEmpty(pa)
	for p, path := range pa {
		if q, ok := pb[p]; ok {
			s.T().Fatalf("shared memory: %s and %s", path, q)
		}
	}
}

func (s *KeeperTestSuite) TestCelestiaValidatorCache() {
	defer sdk.CelestiaSetExecFlags(true)()
	ctx, keeper := s.ctx, s.stakingKeeper
	require := s.Require()
	cdc := moduleCodec()
	store := s.storeService.OpenKVStore(ctx)

	for i, power := range []int64{9, 8, 7} {
		v := testutil.NewValidator(s.T(), sdk.ValAddress(PKs[i].Address().Bytes()), PKs[i])
		v.Status = stakingtypes.Unbonded
		v.Tokens = math.ZeroInt()
		v, _ = v.AddTokensFromDel(keeper.TokensFromConsensusPower(ctx, power))
		v.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(1, 1), math.LegacyNewDecWithPrec(2, 1), math.LegacyNewDecWithPrec(1, 2))
		v.MinSelfDelegation = math.NewInt(7)
		v.UnbondingIds = []uint64{3, 1, 4}
		v.UnbondingOnHoldRefCount = 1
		s.bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), stakingtypes.NotBondedPoolName, stakingtypes.BondedPoolName, gomock.Any())
		stakingkeeper.TestingUpdateValidator(keeper, ctx, v, true)
		require.NoError(keeper.SetValidatorByConsAddr(ctx, v))
	}

	addr0 := sdk.ValAddress(PKs[0].Address().Bytes())
	key := stakingtypes.GetValidatorKey(addr0)
	raw, err := store.Get(key)
	require.NoError(err)
	fresh := stakingtypes.MustUnmarshalValidator(cdc, raw)

	check := func(v stakingtypes.Validator, raw []byte) {
		require.Equal(raw, stakingtypes.MustMarshalValidator(cdc, &v))
		f := stakingtypes.MustUnmarshalValidator(cdc, raw)
		require.True(reflect.DeepEqual(f, v), "differs from a fresh decode")
		ca, err := v.GetConsAddr()
		require.NoError(err)
		fa, err := f.GetConsAddr()
		require.NoError(err)
		require.Equal(fa, ca)
	}

	v1, err := keeper.GetValidator(ctx, addr0)
	require.NoError(err)
	v2, err := keeper.GetValidator(ctx, addr0) // hit
	require.NoError(err)
	require.Greater(keeper.CelestiaValidatorCacheLen(), 0)
	master, ok := keeper.CelestiaValidatorCacheMaster(key)
	require.True(ok)
	check(v1, raw)
	check(v2, raw)
	check(master, raw)
	s.requireNoSharedMemory(v1, v2)
	s.requireNoSharedMemory(v2, master)
	s.requireNoSharedMemory(v1, master)
	s.requireNoSharedMemory(fresh, v2)

	// in-place mutation of a returned validator does not leak into the cache
	v2.DelegatorShares.AddMut(math.LegacyNewDec(1000))
	v2.Commission.Rate.AddMut(math.LegacyOneDec())
	v2.Commission.MaxRate.AddMut(math.LegacyOneDec())
	v2.Commission.MaxChangeRate.AddMut(math.LegacyOneDec())
	v2.Tokens.BigIntMut().SetInt64(1)
	v2.MinSelfDelegation.BigIntMut().SetInt64(1)
	v2.ConsensusPubkey.Value[0] ^= 0xff
	v2.UnbondingIds[0] = 99
	v2.Description.Moniker = "mutated"
	v2.Jailed = !v2.Jailed
	v3, err := keeper.GetValidator(ctx, addr0)
	require.NoError(err)
	check(v3, raw)

	// a write invalidates (raw bytes differ)
	upd := v3
	upd.Tokens = upd.Tokens.AddRaw(5)
	upd.Description.Moniker = "updated"
	require.NoError(keeper.SetValidator(ctx, upd))
	raw2, err := store.Get(key)
	require.NoError(err)
	require.NotEqual(raw, raw2)
	v4, err := keeper.GetValidator(ctx, addr0)
	require.NoError(err)
	check(v4, raw2)
	require.Equal("updated", v4.Description.Moniker)

	// a write in a discarded cache context is seen there, not in the parent
	cctx, _ := ctx.CacheContext()
	upd2 := v4
	upd2.Jailed = true
	require.NoError(keeper.SetValidator(cctx, upd2))
	vc, err := keeper.GetValidator(cctx, addr0)
	require.NoError(err)
	require.True(vc.Jailed)
	vp, err := keeper.GetValidator(ctx, addr0)
	require.NoError(err)
	require.False(vp.Jailed)
	jailed, err := keeper.IsValidatorJailed(ctx, sdk.ConsAddress(PKs[0].Address()))
	require.NoError(err)
	require.False(jailed)
	jailed, err = keeper.IsValidatorJailed(cctx, sdk.ConsAddress(PKs[0].Address()))
	require.NoError(err)
	require.True(jailed)

	// results with the cache on equal results with it off
	take := func() [][]byte {
		var out [][]byte
		last, err := keeper.GetLastValidators(ctx)
		require.NoError(err)
		require.NotEmpty(last)
		bonded, err := keeper.GetBondedValidatorsByPower(ctx)
		require.NoError(err)
		bc, err := keeper.GetValidatorByConsAddr(ctx, sdk.GetConsAddress(PKs[1]))
		require.NoError(err)
		for _, v := range append(append(last, bonded...), bc) {
			out = append(out, stakingtypes.MustMarshalValidator(cdc, &v))
		}
		return out
	}
	on1, on2 := take(), take()
	sdk.CelestiaValidatorCache = false
	off := take()
	sdk.CelestiaValidatorCache = true
	require.Equal(off, on1)
	require.Equal(off, on2)
}

func (s *KeeperTestSuite) TestCelestiaParamsCache() {
	defer sdk.CelestiaSetExecFlags(true)()
	ctx, keeper := s.ctx, s.stakingKeeper
	require := s.Require()

	p := stakingtypes.DefaultParams()
	p.MinCommissionRate = math.LegacyNewDecWithPrec(5, 2)
	p.HistoricalEntries = 3
	require.NoError(keeper.SetParams(ctx, p))
	for i := 0; i < 3; i++ {
		got, err := keeper.GetParams(ctx)
		require.NoError(err)
		require.True(reflect.DeepEqual(p, got))
		got.MinCommissionRate.AddMut(math.LegacyOneDec()) // must not leak
	}
	p.MaxValidators = 7
	require.NoError(keeper.SetParams(ctx, p))
	got, err := keeper.GetParams(ctx)
	require.NoError(err)
	require.Equal(uint32(7), got.MaxValidators)
	require.True(reflect.DeepEqual(p, got))
}

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
		restore := sdk.CelestiaSetExecFlags(false)
		ha := stakingtypes.NewHistoricalInfo(ctx.BlockHeader(), stakingtypes.Validators{Validators: a}, r)
		restore()
		restore = sdk.CelestiaSetExecFlags(true)
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
		defer sdk.CelestiaSetExecFlags(on)()
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

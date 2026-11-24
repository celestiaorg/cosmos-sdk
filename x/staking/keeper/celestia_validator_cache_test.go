package keeper_test

import (
	"fmt"
	"reflect"
	"time"

	"github.com/golang/mock/gomock"

	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256r1"
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
	defer setExecutionCaches(true)()
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
	sdk.SetExecutionCaches(false)
	off := take()
	sdk.SetExecutionCaches(true)
	require.Equal(off, on1)
	require.Equal(off, on2)
}

func (s *KeeperTestSuite) TestCelestiaParamsCache() {
	defer setExecutionCaches(true)()
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

// setExecutionCaches sets the execution-caches switch and returns a restore func.
func setExecutionCaches(on bool) func() {
	prev := sdk.ExecutionCachesEnabled()
	sdk.SetExecutionCaches(on)
	return func() { sdk.SetExecutionCaches(prev) }
}

// TestCelestiaValidatorCacheKeyTypes checks that validators whose consensus
// key type is not known to be immutable are decoded but never cached.
func (s *KeeperTestSuite) TestCelestiaValidatorCacheKeyTypes() {
	defer setExecutionCaches(true)()
	ctx, keeper := s.ctx, s.stakingKeeper
	require := s.Require()

	pk, err := secp256r1.GenPrivKey()
	require.NoError(err)
	addr := sdk.ValAddress(pk.PubKey().Address())
	v := testutil.NewValidator(s.T(), addr, pk.PubKey())
	require.NoError(keeper.SetValidator(ctx, v))

	for i := 0; i < 2; i++ {
		got, err := keeper.GetValidator(ctx, addr)
		require.NoError(err)
		require.Equal(v.OperatorAddress, got.OperatorAddress)
	}
	_, ok := keeper.CelestiaValidatorCacheMaster(stakingtypes.GetValidatorKey(addr))
	require.False(ok, "validator with a secp256r1 consensus key was cached")
}

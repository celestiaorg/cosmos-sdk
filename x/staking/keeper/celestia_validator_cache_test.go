package keeper_test

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/cosmos/cosmos-sdk/x/staking/teststaking"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
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

func requireNoSharedMemory(t *testing.T, a, b types.Validator) {
	t.Helper()
	pa, pb := map[uintptr]string{}, map[uintptr]string{}
	celestiaPointers(reflect.ValueOf(a), "v", pa)
	celestiaPointers(reflect.ValueOf(b), "v", pb)
	require.NotEmpty(t, pa)
	for p, path := range pa {
		if q, ok := pb[p]; ok {
			t.Fatalf("shared memory: %s and %s", path, q)
		}
	}
}

func TestCelestiaValidatorCache(t *testing.T) {
	defer sdk.CelestiaSetExecFlags(true)()

	app, ctx, _, addrVals := bootstrapValidatorTest(t, 1000, 20)
	cdc := app.AppCodec()
	store := ctx.KVStore(app.GetKey(types.StoreKey))

	for i, power := range []int64{9, 8, 7} {
		v := teststaking.NewValidator(t, addrVals[i], PKs[i])
		v, _ = v.AddTokensFromDel(app.StakingKeeper.TokensFromConsensusPower(ctx, power))
		v.Commission = types.NewCommission(sdk.NewDecWithPrec(1, 1), sdk.NewDecWithPrec(2, 1), sdk.NewDecWithPrec(1, 2))
		v.MinSelfDelegation = sdk.NewInt(7)
		keeper.TestingUpdateValidator(app.StakingKeeper, ctx, v, true)
		app.StakingKeeper.SetValidatorByConsAddr(ctx, v)
	}

	key := types.GetValidatorKey(addrVals[0])
	raw := store.Get(key)
	fresh := types.MustUnmarshalValidator(cdc, raw)

	check := func(v types.Validator, raw []byte) {
		t.Helper()
		require.Equal(t, raw, types.MustMarshalValidator(cdc, &v))
		f := types.MustUnmarshalValidator(cdc, raw)
		require.Equal(t, f.String(), v.String())
		require.True(t, reflect.DeepEqual(f, v), "differs from a fresh decode")
		ca, err := v.GetConsAddr()
		require.NoError(t, err)
		fa, err := f.GetConsAddr()
		require.NoError(t, err)
		require.Equal(t, fa, ca)
		pk, err := v.ConsPubKey()
		require.NoError(t, err)
		require.True(t, pk.Equals(PKs[0]) || pk.Equals(PKs[1]) || pk.Equals(PKs[2]))
	}

	v1, found := app.StakingKeeper.GetValidator(ctx, addrVals[0])
	require.True(t, found)
	v2, found := app.StakingKeeper.GetValidator(ctx, addrVals[0]) // hit
	require.True(t, found)
	require.Greater(t, app.StakingKeeper.CelestiaValidatorCacheLen(), 0)
	master, ok := app.StakingKeeper.CelestiaValidatorCacheMaster(key)
	require.True(t, ok)
	check(v1, raw)
	check(v2, raw)
	check(master, raw)
	requireNoSharedMemory(t, v1, v2)
	requireNoSharedMemory(t, v2, master)
	requireNoSharedMemory(t, v1, master)
	requireNoSharedMemory(t, fresh, v2)

	// mutating a returned validator in place does not affect later returns
	v2.DelegatorShares.AddMut(sdk.NewDec(1000))
	v2.Commission.Rate.AddMut(sdk.OneDec())
	v2.Commission.MaxRate.AddMut(sdk.OneDec())
	v2.Commission.MaxChangeRate.AddMut(sdk.OneDec())
	v2.ConsensusPubkey.Value[0] ^= 0xff
	v2.ConsensusPubkey.TypeUrl = "x"
	v2.Description.Moniker = "mutated"
	v2.Jailed = !v2.Jailed
	v2.Tokens = v2.Tokens.AddRaw(1)
	v3, found := app.StakingKeeper.GetValidator(ctx, addrVals[0])
	require.True(t, found)
	check(v3, raw)

	// a write invalidates (raw bytes differ)
	upd := v3
	upd.Tokens = upd.Tokens.AddRaw(5)
	upd.Description.Moniker = "updated"
	app.StakingKeeper.SetValidator(ctx, upd)
	raw2 := store.Get(key)
	require.NotEqual(t, raw, raw2)
	v4, found := app.StakingKeeper.GetValidator(ctx, addrVals[0])
	require.True(t, found)
	check(v4, raw2)
	require.Equal(t, "updated", v4.Description.Moniker)
	require.True(t, upd.Tokens.Equal(v4.Tokens))

	// a write in a discarded cache context is seen there, and not in the parent
	cctx, _ := ctx.CacheContext()
	upd2 := v4
	upd2.Jailed = true
	app.StakingKeeper.SetValidator(cctx, upd2)
	vc, found := app.StakingKeeper.GetValidator(cctx, addrVals[0])
	require.True(t, found)
	require.True(t, vc.Jailed)
	vp, found := app.StakingKeeper.GetValidator(ctx, addrVals[0])
	require.True(t, found)
	require.False(t, vp.Jailed)
	check(vp, raw2)
	vc, _ = app.StakingKeeper.GetValidator(cctx, addrVals[0])
	require.True(t, vc.Jailed)

	// results with the cache on equal results with it off
	type snapshot struct {
		last, bonded, all []types.Validator
		byCons            types.Validator
	}
	take := func() snapshot {
		bc, found := app.StakingKeeper.GetValidatorByConsAddr(ctx, sdk.GetConsAddress(PKs[1]))
		require.True(t, found)
		return snapshot{
			last:   app.StakingKeeper.GetLastValidators(ctx),
			bonded: app.StakingKeeper.GetBondedValidatorsByPower(ctx),
			all:    app.StakingKeeper.GetAllValidators(ctx),
			byCons: bc,
		}
	}
	on1 := take()
	on2 := take()
	sdk.CelestiaValidatorCache = false
	off := take()
	sdk.CelestiaValidatorCache = true
	for _, on := range []snapshot{on1, on2} {
		require.Equal(t, len(off.last), len(on.last))
		require.Equal(t, len(off.bonded), len(on.bonded))
		require.NotZero(t, len(on.last))
		for i := range off.last {
			require.Equal(t, types.MustMarshalValidator(cdc, &off.last[i]), types.MustMarshalValidator(cdc, &on.last[i]))
		}
		for i := range off.bonded {
			require.Equal(t, types.MustMarshalValidator(cdc, &off.bonded[i]), types.MustMarshalValidator(cdc, &on.bonded[i]))
		}
		require.Equal(t, types.MustMarshalValidator(cdc, &off.byCons), types.MustMarshalValidator(cdc, &on.byCons))
	}
	requireNoSharedMemory(t, on1.last[0], on2.last[0])
	requireNoSharedMemory(t, on1.byCons, on2.byCons)
}

// TestCelestiaHistoricalSortMatchesStock compares the memoized voting-power
// sort with the stock sort.SliceStable comparator, including power ties
// (address tie-break) and unbonded validators (power 0).
func TestCelestiaHistoricalSortMatchesStock(t *testing.T) {
	_, app, ctx := createTestInput(t)
	r := app.StakingKeeper.PowerReduction(ctx)
	for seed := 0; seed < 50; seed++ {
		n := 1 + seed*3
		vals := make([]types.Validator, n)
		for i := range vals {
			v := teststaking.NewValidator(t, sdk.ValAddress(PKs[i].Address()), PKs[i])
			power := int64((i*7 + seed) % 5) // many ties
			v.Tokens = app.StakingKeeper.TokensFromConsensusPower(ctx, power)
			if i%6 == 5 {
				// GetConsAddr error (no cached pubkey): the comparator's error=>false branch
				v.ConsensusPubkey = &codectypes.Any{}
			}
			if (i+seed)%4 == 0 {
				v.Status = types.Unbonded
			} else {
				v.Status = types.Bonded
			}
			vals[i] = v
		}
		a := append([]types.Validator(nil), vals...)
		b := append([]types.Validator(nil), vals...)

		restore := sdk.CelestiaSetExecFlags(false)
		ha := types.NewHistoricalInfo(ctx.BlockHeader(), a, r)
		restore()
		restore = sdk.CelestiaSetExecFlags(true)
		hb := types.NewHistoricalInfo(ctx.BlockHeader(), b, r)
		restore()

		require.Equal(t, len(ha.Valset), len(hb.Valset))
		for i := range ha.Valset {
			require.Equal(t, ha.Valset[i].OperatorAddress, hb.Valset[i].OperatorAddress, "seed %d idx %d", seed, i)
			require.Equal(t, a[i].OperatorAddress, b[i].OperatorAddress) // in-place order too
		}
		require.Equal(t, app.AppCodec().MustMarshal(&ha), app.AppCodec().MustMarshal(&hb))
	}
}

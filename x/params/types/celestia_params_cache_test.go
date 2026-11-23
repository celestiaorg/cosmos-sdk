package types_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/log"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
	dbm "github.com/tendermint/tm-db"

	"github.com/cosmos/cosmos-sdk/simapp"
	"github.com/cosmos/cosmos-sdk/store"
	storetypes "github.com/cosmos/cosmos-sdk/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/params/types"
)

func TestCelestiaParamsCache(t *testing.T) {
	ckey := sdk.NewKVStoreKey("celestiaparams")
	ctkey := sdk.NewTransientStoreKey("celestiaparams_t")
	db := dbm.NewMemDB()
	ms := store.NewCommitMultiStore(db)
	ms.MountStoreWithDB(ckey, storetypes.StoreTypeIAVL, db)
	ms.MountStoreWithDB(ctkey, storetypes.StoreTypeTransient, db)
	require.NoError(t, ms.LoadLatestVersion())
	ctx := sdk.NewContext(ms, tmproto.Header{}, false, log.NewNopLogger())
	enc := simapp.MakeTestEncodingConfig()

	var (
		kDec, kDur, kU16, kStr, kList, kI64 = []byte("Dec"), []byte("Dur"), []byte("U16"), []byte("Str"), []byte("List"), []byte("I64")
		dec                                 sdk.Dec
		dur                                 time.Duration
		u16                                 uint16
		str                                 string
		list                                []string
		i64                                 int64
	)
	ok := func(interface{}) error { return nil }
	table := types.NewKeyTable(
		types.NewParamSetPair(kDec, &dec, ok), types.NewParamSetPair(kDur, &dur, ok),
		types.NewParamSetPair(kU16, &u16, ok), types.NewParamSetPair(kStr, &str, ok),
		types.NewParamSetPair(kList, &list, ok), types.NewParamSetPair(kI64, &i64, ok),
	)
	ss := types.NewSubspace(enc.Codec, enc.Amino, ckey, ctkey, "celestiatest").WithKeyTable(table)
	// a second subspace with the same keys must not see the first one's values
	ss2 := types.NewSubspace(enc.Codec, enc.Amino, ckey, ctkey, "celestiatest2").WithKeyTable(table)

	ss.Set(ctx, kDec, sdk.NewDecWithPrec(5, 2))
	ss.Set(ctx, kDur, time.Hour)
	ss.Set(ctx, kU16, uint16(7))
	ss.Set(ctx, kStr, "utia")
	ss.Set(ctx, kList, []string{"a", "b"})
	ss.Set(ctx, kI64, int64(-3))
	ss2.Set(ctx, kDec, sdk.NewDecWithPrec(9, 1))

	read := func(s types.Subspace) (sdk.Dec, time.Duration, uint16, string, []string, int64) {
		var (
			d  sdk.Dec
			du time.Duration
			u  uint16
			st string
			l  []string
			i  int64
		)
		s.Get(ctx, kDec, &d)
		s.Get(ctx, kDur, &du)
		s.Get(ctx, kU16, &u)
		s.GetIfExists(ctx, kStr, &st)
		s.Get(ctx, kList, &l)
		s.Get(ctx, kI64, &i)
		return d, du, u, st, l, i
	}

	defer sdk.SetExecutionCaches(sdk.ExecutionCachesEnabled())
	sdk.SetExecutionCaches(false)
	d0, du0, u0, st0, l0, i0 := read(ss)

	sdk.SetExecutionCaches(true)
	for round := 0; round < 3; round++ {
		d, du, u, st, l, i := read(ss)
		require.True(t, d0.Equal(d))
		require.Equal(t, d0.String(), d.String())
		require.Equal(t, du0, du)
		require.Equal(t, u0, u)
		require.Equal(t, st0, st)
		require.Equal(t, l0, l)
		require.Equal(t, i0, i)
		// in-place mutation of the returned Dec must not leak into the memo
		d.AddMut(sdk.OneDec())
		l[0] = "mutated"
	}
	var d2 sdk.Dec
	ss2.Get(ctx, kDec, &d2)
	require.Equal(t, sdk.NewDecWithPrec(9, 1).String(), d2.String())

	// a write is seen (raw bytes differ)
	ss.Set(ctx, kDec, sdk.NewDecWithPrec(7, 3))
	ss.Set(ctx, kU16, uint16(9))
	d, _, u, _, _, _ := read(ss)
	require.Equal(t, sdk.NewDecWithPrec(7, 3).String(), d.String())
	require.Equal(t, uint16(9), u)

	// missing key still panics in Get, and GetIfExists stays a no-op
	var missing sdk.Dec
	ss3 := types.NewSubspace(enc.Codec, enc.Amino, ckey, ctkey, "celestiatest3").WithKeyTable(table)
	require.Panics(t, func() { ss3.Get(ctx, kDec, &missing) })
	ss3.GetIfExists(ctx, kDec, &missing)
	require.True(t, missing.IsNil())

	// type mismatch still panics
	var wrong int32
	require.Panics(t, func() { ss.Get(ctx, kDec, &wrong) })
}

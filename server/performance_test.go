package server

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/log"
	dbm "github.com/tendermint/tm-db"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/store/rootmulti"
)

// A [performance] key missing from app.toml means its default (on).
func TestSignaturePreverifyOption(t *testing.T) {
	defer secp256k1.EnableSigCache(false)
	for _, tc := range []struct {
		set  interface{}
		want bool
	}{{nil, true}, {true, true}, {false, false}, {"false", false}} {
		v := viper.New()
		v.Set(flags.FlagHome, t.TempDir())
		v.Set(FlagPruning, "everything")
		if tc.set != nil {
			v.Set(FlagSignaturePreverify, tc.set)
		}
		baseapp.NewBaseApp("test", log.NewNopLogger(), dbm.NewMemDB(), nil, DefaultBaseappOptions(v)...)
		require.Equal(t, tc.want, secp256k1.SigCacheEnabled(), "set=%v", tc.set)
	}
}

func TestDeferredPersistenceOptions(t *testing.T) {
	v := viper.New()
	mode, interval, snap := deferredPersistenceOptions(v)
	require.Equal(t, rootmulti.DeferredSync, mode)
	require.EqualValues(t, rootmulti.DefaultDeferredFlushInterval, interval)
	require.Zero(t, snap)

	v.Set(FlagDeferredPersistence, rootmulti.DeferredOff)
	v.Set(FlagDeferredFlushInterval, 500)
	v.Set(FlagStateSyncSnapshotInterval, 1000)
	mode, interval, snap = deferredPersistenceOptions(v)
	require.Equal(t, rootmulti.DeferredOff, mode)
	require.EqualValues(t, 500, interval)
	require.EqualValues(t, 1000, snap)

	// export / rollback
	v.Set(KeyReadOnlyLoad, true)
	mode, _, _ = deferredPersistenceOptions(v)
	require.Equal(t, rootmulti.DeferredOff, mode)
}

func TestBoolDefaultTrue(t *testing.T) {
	v := viper.New()
	require.True(t, boolDefaultTrue(v, FlagParallelCommit), "a missing key means true")
	v.Set(FlagParallelCommit, false)
	require.False(t, boolDefaultTrue(v, FlagParallelCommit))
	v.Set(FlagParallelCommit, "true")
	require.True(t, boolDefaultTrue(v, FlagParallelCommit))
	require.True(t, boolDefaultTrue(v, FlagExecutionCaches), "a missing key means true")
}

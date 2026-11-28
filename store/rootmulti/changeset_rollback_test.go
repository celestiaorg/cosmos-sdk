package rootmulti

import (
	"testing"

	dbm "github.com/cosmos/cosmos-db"
	iavltree "github.com/cosmos/iavl"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/metrics"
	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/types"
	"cosmossdk.io/store/wrapper"
)

// A deferred flush the gate refuses at a store-removal height keeps the
// removed store until a later flush persists its deletes.
func TestDeferredFlushGateKeepsRemovedStoreUntilFlushed(t *testing.T) {
	db := dbm.NewMemDB()
	open := func(names []string, u *types.StoreUpgrades, gate func(int64, []byte) bool) *Store {
		rs := NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
		rs.SetChangesetRecorder(nil)
		rs.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningEverything))
		require.NoError(t, rs.SetDeferredPersistence(DeferredAlways, 100))
		rs.SetDeferredFlushGate(gate)
		for _, n := range names {
			rs.MountStoreWithDB(types.NewKVStoreKey(n), types.StoreTypeIAVL, nil)
		}
		if u != nil {
			require.NoError(t, rs.LoadLatestVersionAndUpgrade(u))
		} else {
			require.NoError(t, rs.LoadLatestVersion())
		}
		return rs
	}
	rs := open([]string{"a", "x"}, nil, nil)
	for h := 1; h <= 2; h++ {
		rs.GetKVStore(rs.StoreKeysByName()["a"]).Set([]byte{byte(h)}, []byte("a"))
		rs.GetKVStore(rs.StoreKeysByName()["x"]).Set([]byte{byte(h)}, []byte("x"))
		rs.Commit()
	}
	require.NoError(t, rs.Close())

	allow := int64(0)
	rs = open([]string{"a", "x"}, &types.StoreUpgrades{Deleted: []string{"x"}}, func(v int64, _ []byte) bool { return v <= allow })
	rs.GetKVStore(rs.StoreKeysByName()["a"]).Set([]byte{3}, []byte("a"))
	rs.Commit() // removal flush at 3 refused
	require.EqualValues(t, 2, GetLatestVersion(db))
	allow = 4
	rs.GetKVStore(rs.StoreKeysByName()["a"]).Set([]byte{4}, []byte("a"))
	rs.Commit() // the removal flush is retried and approved
	require.EqualValues(t, 4, GetLatestVersion(db))
	require.NoError(t, rs.Close())

	x := iavltree.NewMutableTree(wrapper.NewDBWrapper(dbm.NewPrefixDB(db, []byte("s/k:x/"))), 100, true, log.NewNopLogger())
	latest, err := x.LoadVersion(0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, latest, int64(3), "the deletes of the removed store were never flushed")
	v, err := x.Get([]byte{1})
	require.NoError(t, err)
	require.Nil(t, v)
	rs = open([]string{"a"}, nil, nil)
	require.EqualValues(t, 4, rs.LastCommitID().Version)
	require.NoError(t, rs.Close())
}

package rootmulti

import (
	"math/rand"
	"testing"
	"time"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/metrics"
	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/types"
)

// TestCelestiaDeferredPruningNothing: with pruning "nothing" the env is ignored.
func TestCelestiaDeferredPruningNothing(t *testing.T) {
	ms := NewStore(dbm.NewMemDB(), log.NewNopLogger(), metrics.NewNoOpMetrics())
	ms.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningNothing))
	ms.celestiaDeferred = &deferredDriver{mode: DeferredAlways, every: 7, maxPending: 1 << 30, now: time.Now}
	ms.MountStoreWithDB(types.NewKVStoreKey("store1"), types.StoreTypeIAVL, nil)
	require.NoError(t, ms.LoadLatestVersion())
	require.False(t, ms.celestiaDeferred.isActive())
}

// TestCelestiaDeferredUncleanThenUpstream documents the handoff rule: after a
// kill during a flush (a store ahead of the metadata), an upstream multistore
// cannot continue on the DB; one start in deferred mode repairs it, after
// which upstream continues with identical commit IDs.
func TestCelestiaDeferredUncleanThenUpstream(t *testing.T) {
	r := rand.New(rand.NewSource(3)) //nolint:gosec // deterministic test data
	names := []string{"store1", "store2", "store3"}
	up := &deferredHarness{t: t, k: 0, fastNode: true, names: names, db: dbm.NewMemDB()}
	df := &deferredHarness{t: t, k: 7, fastNode: true, names: names, db: dbm.NewMemDB()}
	up.open(nil)
	df.open(nil)
	var blocks []deferredBlock
	var ids []types.CommitID
	for h := 1; h <= 10; h++ {
		b := randBlock(r, int64(h), names, false)
		blocks = append(blocks, b)
		id := up.commit(b)
		require.Equal(t, id, df.commit(b))
		ids = append(ids, id)
	}
	// kill after store1 flushed version 10 (metadata at 7)
	require.NoError(t, df.iavlStore("store1").FlushDeferred())
	require.Equal(t, int64(7), metadataVersion(df.db))

	snapshot := dbm.NewMemDB()
	it, err := df.db.Iterator(nil, nil)
	require.NoError(t, err)
	for ; it.Valid(); it.Next() {
		require.NoError(t, snapshot.Set(it.Key(), it.Value()))
	}
	_ = it.Close()

	// upstream directly on the unclean DB: the load or the replay of 8..10 fails
	failed := func() (f bool) {
		defer func() {
			if recover() != nil {
				f = true
			}
		}()
		ms := NewStore(snapshot, log.NewNopLogger(), metrics.NewNoOpMetrics())
		ms.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningEverything))
		for _, n := range names {
			ms.MountStoreWithDB(types.NewKVStoreKey(n), types.StoreTypeIAVL, nil)
		}
		if err := ms.LoadLatestVersion(); err != nil {
			t.Logf("upstream load fails: %v", err)
			return true
		}
		st := &deferredHarness{t: t, names: names, db: snapshot, ms: ms}
		for h := 8; h <= 10; h++ {
			if id := st.commit(blocks[h-1]); id.Version != ids[h-1].Version || string(id.Hash) != string(ids[h-1].Hash) {
				return true
			}
		}
		return false
	}()
	require.True(t, failed, "upstream is expected to fail on a DB left by a kill mid-flush")

	// one deferred start repairs it; then upstream continues
	df.open(nil)
	require.Equal(t, int64(7), df.ms.LastCommitID().Version)
	for h := 8; h <= 10; h++ {
		require.Equal(t, ids[h-1], df.commit(blocks[h-1]))
	}
	require.NoError(t, df.ms.FlushDeferred())
	df.k = 0
	df.open(nil)
	for h := 11; h <= 20; h++ {
		b := randBlock(r, int64(h), names, false)
		require.Equal(t, up.commit(b), df.commit(b))
	}
}

// TestCelestiaDeferredFailedRollbackKeepsMode: a RollbackToVersion that fails
// after its pre-reload flush must leave the deferred mode on. Otherwise later
// commits take the upstream path, which writes the commit metadata while the
// deferred trees keep their versions in memory, and the next start fails with
// a store behind the metadata.
func TestCelestiaDeferredFailedRollbackKeepsMode(t *testing.T) {
	r := rand.New(rand.NewSource(5)) //nolint:gosec // deterministic test data
	names := []string{"store1", "store2"}
	h := &deferredHarness{t: t, k: 7, fastNode: true, names: names, db: dbm.NewMemDB()}
	h.open(nil)
	for v := 1; v <= 10; v++ {
		b := randBlock(r, int64(v), names, false)
		h.apply(b)
		h.ms.Commit()
	}
	require.Error(t, h.ms.RollbackToVersion(9)) // version 9 was never written
	require.True(t, h.ms.celestiaDeferred.isActive())
	var last types.CommitID
	for v := 11; v <= 12; v++ {
		b := randBlock(r, int64(v), names, false)
		h.apply(b)
		last = h.ms.Commit()
	}
	require.NoError(t, h.ms.FlushDeferred())
	h.open(nil)
	require.Equal(t, last, h.ms.LastCommitID())
}

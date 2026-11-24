package rootmulti

import (
	"io"
	"math/rand"
	"testing"
	"time"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/metrics"
	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/snapshots"
	snapshottypes "cosmossdk.io/store/snapshots/types"
	"cosmossdk.io/store/types"
)

// Heights of the first-flush tests: state sync restores at restoreHeight,
// "sync" turns on at the next commit and first flushes at firstFlush.
const (
	restoreHeight = 37
	firstFlush    = 50
)

func firstFlushBlock(h int64) *rand.Rand { return rand.New(rand.NewSource(h)) }

// stateSyncedSyncNode returns the reference commit IDs for heights
// 1..firstFlush and a "sync" store restored from a snapshot at restoreHeight
// (so every tree holds one version) whose wall clock says it is catching up.
func stateSyncedSyncNode(t *testing.T) ([]types.CommitID, dbm.DB, *Store) {
	t.Helper()
	ref := openDeferredTest(t, dbm.NewMemDB(), DeferredOff, 0, nil)
	var want []types.CommitID
	for h := int64(1); h <= restoreHeight; h++ {
		want = append(want, commitBlock(ref, firstFlushBlock(h), h))
	}

	db := dbm.NewMemDB()
	now := func() time.Time { return headerTime(10_000) }
	ms := openDeferredTest(t, db, DeferredSync, firstFlush, now)
	chunks := make(chan io.ReadCloser, 100)
	go func() {
		w := snapshots.NewStreamWriter(chunks)
		defer w.Close()
		if err := ref.Snapshot(restoreHeight, w); err != nil {
			w.CloseWithError(err)
		}
	}()
	r, err := snapshots.NewStreamReader(chunks)
	require.NoError(t, err)
	_, err = ms.Restore(restoreHeight, snapshottypes.CurrentFormat, r)
	require.NoError(t, err)
	require.Equal(t, want[restoreHeight-1], ms.LastCommitID())
	require.False(t, ms.celestiaDeferred.isActive())

	for h := int64(restoreHeight + 1); h <= firstFlush; h++ {
		want = append(want, commitBlock(ref, firstFlushBlock(h), h))
	}
	return want, db, ms
}

// killAt commits height h on ms and panics in the given test hook phase.
func killAt(t *testing.T, ms *Store, phase string, h int64) {
	t.Helper()
	celestiaDeferredTestHook = func(p string, _ int64) {
		if p == phase {
			panic("injected kill")
		}
	}
	defer func() { celestiaDeferredTestHook = nil }()
	require.Panics(t, func() { commitBlock(ms, firstFlushBlock(h), h) })
}

// openStock opens db without deferred persistence.
func openStock(db dbm.DB) (*Store, error) {
	ms := NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	ms.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningDefault))
	for _, key := range deferredTestKeys {
		ms.MountStoreWithDB(key, types.StoreTypeIAVL, nil)
	}
	return ms, ms.LoadLatestVersion()
}

// replay commits heights from..firstFlush and checks the reference IDs.
func replay(t *testing.T, ms *Store, want []types.CommitID, from int64) {
	t.Helper()
	for h := from; h <= firstFlush; h++ {
		require.Equal(t, want[h-1], commitBlock(ms, firstFlushBlock(h), h), "height %d", h)
	}
}

// TestCelestiaDeferredKillInFirstFlush: "sync" turns on at runtime on a
// state-synced DB and the node is killed after the store phase of its first
// flush, leaving trees at {restoreHeight, firstFlush} with metadata at
// restoreHeight. A "sync" restart repairs and replays; an "off" restart is
// refused without writing.
func TestCelestiaDeferredKillInFirstFlush(t *testing.T) {
	want, db, ms := stateSyncedSyncNode(t)
	for h := int64(restoreHeight + 1); h < firstFlush; h++ {
		require.Equal(t, want[h-1], commitBlock(ms, firstFlushBlock(h), h))
	}
	require.True(t, ms.celestiaDeferred.isActive())
	killAt(t, ms, "stores", firstFlush)
	require.Equal(t, int64(restoreHeight), GetLatestVersion(db))
	last, deferred, err := ReadDeferredMarker(db)
	require.NoError(t, err)
	require.True(t, deferred, "the marker was written when sync turned on")
	require.Equal(t, int64(restoreHeight), last)

	before := dbDigest(t, db)
	_, err = openStock(db)
	require.ErrorContains(t, err, "left by deferred persistence")
	require.ErrorContains(t, err, `deferred-persistence = "sync"`)
	require.Equal(t, before, dbDigest(t, db), "a refused open writes nothing")

	ms = openDeferredTest(t, db, DeferredSync, firstFlush, func() time.Time { return headerTime(10_000) })
	require.Equal(t, want[restoreHeight-1], ms.LastCommitID())
	replay(t, ms, want, restoreHeight+1)
	require.NoError(t, ms.Close())
}

// TestCelestiaDeferredKillAfterMarker: a kill after the marker is written but
// before the first soft commit reopens in either mode.
func TestCelestiaDeferredKillAfterMarker(t *testing.T) {
	want, db, ms := stateSyncedSyncNode(t)
	killAt(t, ms, "marker", restoreHeight+1)
	_, deferred, err := ReadDeferredMarker(db)
	require.NoError(t, err)
	require.True(t, deferred)

	stock, err := openStock(db)
	require.NoError(t, err)
	require.Equal(t, want[restoreHeight-1], stock.LastCommitID())

	ms = openDeferredTest(t, db, DeferredSync, firstFlush, func() time.Time { return headerTime(10_000) })
	require.Equal(t, want[restoreHeight-1], ms.LastCommitID())
	replay(t, ms, want, restoreHeight+1)
	require.NoError(t, ms.Close())
}

// TestCelestiaDeferredEnableFailureWritesNoMarker: when a tree holds history,
// "sync" stays off and no deferred marker is written.
func TestCelestiaDeferredEnableFailureWritesNoMarker(t *testing.T) {
	db := dbm.NewMemDB()
	ms := openDeferredTest(t, db, DeferredSync, 5, func() time.Time { return headerTime(10) })
	r := rand.New(rand.NewSource(3))
	for h := int64(1); h <= 4; h++ {
		commitBlock(ms, r, h) // at the tip: stock commits, trees keep history
	}
	ms.celestiaDeferred.now = func() time.Time { return headerTime(10_000) }
	commitBlock(ms, r, 5)
	require.False(t, ms.celestiaDeferred.isActive())
	bz, err := db.Get([]byte(persistenceKey))
	require.NoError(t, err)
	require.Nil(t, bz, "no deferred marker")
}

package rootmulti

import (
	"math/rand"
	"testing"
	"time"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/store/iavl"
	"cosmossdk.io/store/types"
)

// TestCelestiaDeferredSyncRestartAndKill: a node killed while catching up
// restarts in deferred mode at its last flush, refuses an app-state rollback,
// replays to the same hashes and flushes on a graceful stop.
func TestCelestiaDeferredSyncRestartAndKill(t *testing.T) {
	block := func(h int64) *rand.Rand { return rand.New(rand.NewSource(h)) } //nolint:gosec // deterministic test data
	ref := openDeferredTest(t, dbm.NewMemDB(), DeferredOff, 0, nil)
	var want []types.CommitID
	for h := int64(1); h <= 13; h++ {
		want = append(want, commitBlock(ref, block(h), h))
	}
	now := func() time.Time { return headerTime(1000) }
	db := dbm.NewMemDB()
	ms := openDeferredTest(t, db, DeferredSync, 5, now)
	for h := int64(1); h <= 13; h++ {
		require.Equal(t, want[h-1], commitBlock(ms, block(h), h))
	}

	// kill: no flush since 10
	ms = openDeferredTest(t, db, DeferredSync, 5, now)
	require.Equal(t, want[9], ms.LastCommitID())
	require.True(t, ms.celestiaDeferred.isActive())
	last, written, err := ReadDeferredMarker(db)
	require.NoError(t, err)
	require.True(t, written)
	require.Equal(t, int64(10), last)
	var rbErr ErrDeferredRollback
	require.ErrorAs(t, ms.RollbackToVersion(9), &rbErr)
	require.Equal(t, int64(10), rbErr.LastFlushed)

	for h := int64(11); h <= 13; h++ {
		require.Equal(t, want[h-1], commitBlock(ms, block(h), h))
	}
	require.NoError(t, ms.Close())
	require.Equal(t, int64(13), GetLatestVersion(db), "graceful stop flushes")
}

// TestCelestiaDeferredPruneWaitsForReaders: a flush never deletes a version
// that a snapshot export is reading; snapshot heights are always flushed.
func TestCelestiaDeferredPruneWaitsForReaders(t *testing.T) {
	r := rand.New(rand.NewSource(2)) //nolint:gosec // deterministic test data
	db := dbm.NewMemDB()
	ms := openDeferredTest(t, db, DeferredAlways, 3, nil)
	ms.SetSnapshotInterval(5)
	for h := int64(1); h <= 5; h++ {
		commitBlock(ms, r, h)
	}
	require.Equal(t, int64(5), GetLatestVersion(db), "snapshot height flushed")

	st := ms.GetCommitKVStore(testStoreKey1).(*iavl.Store)
	exp, err := st.Export(5)
	require.NoError(t, err)
	// Drain the export first: IAVL's exporter goroutine races with tree
	// mutations on shared nodes (upstream, also without deferred mode). The
	// exporter keeps version 5 registered as read until Close.
	for {
		if _, err := exp.Next(); err != nil {
			break
		}
	}
	for h := int64(6); h <= 9; h++ { // periodic flush at 6 and 9
		commitBlock(ms, r, h)
	}
	_, err = st.GetImmutable(5)
	require.NoError(t, err, "version 5 is still being exported")
	exp.Close()
	commitBlock(ms, r, 10)
	commitBlock(ms, r, 11)
	commitBlock(ms, r, 12)
	require.Equal(t, int64(12), GetLatestVersion(db))
	require.False(t, st.VersionExists(5))
}

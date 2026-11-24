package rootmulti

import (
	"fmt"
	"io"
	"math/rand"
	"testing"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/metrics"
	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/snapshots"
	snapshottypes "cosmossdk.io/store/snapshots/types"
	"cosmossdk.io/store/types"
)

// First-flush tests: a node state-synced to H holds one version per tree and
// no marker. "sync" mode turning on must record deferred mode before its
// first flush, or a kill in that flush leaves trees with versions {H, L}
// that IAVL's stock load cannot open. H and the flush interval are chosen
// so IAVL's first-version search misses H (it assumes contiguous versions).
const (
	firstFlushH     = int64(13)
	firstFlushEvery = int64(37) // first flush at 37
	firstFlushEnd   = int64(45)
)

// seededBlock commits a block for height h that writes to every store, so
// each restored tree has its root at the snapshot height (IAVL import keeps
// node versions, and a tree unchanged at H also holds an older root).
func seededBlock(ms *Store, h int64) types.CommitID {
	r := rand.New(rand.NewSource(h))
	ms.SetCommitHeader(cmtproto.Header{Height: h, Time: headerTime(h)})
	cms := ms.CacheMultiStore()
	for _, key := range deferredTestKeys {
		st := cms.GetKVStore(key)
		for j, n := 0, 1+r.Intn(12); j < n; j++ {
			k := []byte(fmt.Sprintf("k%03d", r.Intn(150)))
			if j > 0 && r.Intn(4) == 0 {
				st.Delete(k)
			} else {
				st.Set(k, []byte(fmt.Sprintf("v%d-%d", h, j)))
			}
		}
	}
	cms.Write()
	return ms.Commit()
}

// stateSyncedDB returns a DB state-synced to firstFlushH and the reference
// commit IDs of a stock node up to firstFlushEnd.
func stateSyncedDB(t *testing.T) (dbm.DB, map[int64]types.CommitID) {
	t.Helper()
	ref := openDeferredTest(t, dbm.NewMemDB(), DeferredOff, 0, nil)
	ids := map[int64]types.CommitID{}
	for h := int64(1); h <= firstFlushH; h++ {
		ids[h] = seededBlock(ref, h)
	}

	db := dbm.NewMemDB()
	target := openDeferredTest(t, db, DeferredSync, firstFlushEvery, nil)
	chunks := make(chan io.ReadCloser, 100)
	errc := make(chan error, 1)
	go func() {
		w := snapshots.NewStreamWriter(chunks)
		errc <- ref.Snapshot(uint64(firstFlushH), w)
		w.Close()
	}()
	r, err := snapshots.NewStreamReader(chunks)
	require.NoError(t, err)
	_, err = target.Restore(uint64(firstFlushH), snapshottypes.CurrentFormat, r)
	require.NoError(t, err)
	require.NoError(t, <-errc)
	require.Equal(t, ids[firstFlushH], target.LastCommitID())
	require.NoError(t, target.Close())

	for h := firstFlushH + 1; h <= firstFlushEnd; h++ {
		ids[h] = seededBlock(ref, h)
	}
	return db, ids
}

// behindClock makes every block look an hour old: "sync" mode turns on.
func behindClock(h *int64) func() time.Time {
	return func() time.Time { return headerTime(*h).Add(time.Hour) }
}

func copyMemDB(t *testing.T, db dbm.DB) dbm.DB {
	t.Helper()
	out := dbm.NewMemDB()
	it, err := db.Iterator(nil, nil)
	require.NoError(t, err)
	defer it.Close()
	for ; it.Valid(); it.Next() {
		require.NoError(t, out.Set(it.Key(), it.Value()))
	}
	return out
}

func openStockNoLoad(db dbm.DB) *Store {
	o := NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	o.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningDefault))
	for _, key := range deferredTestKeys {
		o.MountStoreWithDB(key, types.StoreTypeIAVL, nil)
	}
	return o
}

// replayFrom reopens db in mode and replays the blocks after its committed
// version, checking every commit ID against the reference.
func replayFrom(t *testing.T, db dbm.DB, mode string, ids map[int64]types.CommitID) {
	t.Helper()
	var cur int64
	ms := openDeferredTest(t, db, mode, firstFlushEvery, behindClock(&cur))
	from := ms.LastCommitID().Version
	require.Equal(t, ids[from], ms.LastCommitID())
	for h := from + 1; h <= firstFlushEnd; h++ {
		cur = h
		require.Equal(t, ids[h], seededBlock(ms, h), "mode %q: replayed height %d", mode, h)
	}
	require.NoError(t, ms.Close())
}

// TestCelestiaDeferredFirstFlushKill: a kill in the store phase of the first
// flush after "sync" turns on in a state-synced DB. A "sync" start repairs it
// and replays to the reference hashes; an "off" start refuses with the
// repair hint and writes nothing.
func TestCelestiaDeferredFirstFlushKill(t *testing.T) {
	db, ids := stateSyncedDB(t)

	var cur int64
	ms := openDeferredTest(t, db, DeferredSync, firstFlushEvery, behindClock(&cur))
	celestiaDeferredTestHook = func(phase string, v int64) {
		if phase == "stores" && v == firstFlushEvery {
			panic("injected kill")
		}
	}
	defer func() { celestiaDeferredTestHook = nil }()
	for h := firstFlushH + 1; h < firstFlushEvery; h++ {
		cur = h
		require.Equal(t, ids[h], seededBlock(ms, h))
		require.True(t, ms.celestiaDeferred.isActive(), "height %d", h)
	}
	cur = firstFlushEvery
	require.Panics(t, func() { seededBlock(ms, firstFlushEvery) })
	celestiaDeferredTestHook = nil

	// The torn state: commit metadata at H, trees ahead at the first flush.
	require.Equal(t, firstFlushH, GetLatestVersion(db))
	last, written, err := ReadDeferredMarker(db)
	require.NoError(t, err)
	require.True(t, written)
	require.Equal(t, firstFlushH, last)

	// "off" refuses with the repair hint, without writing.
	off := copyMemDB(t, db)
	before := dbDigest(t, off)
	err = openStockNoLoad(off).LoadLatestVersion()
	require.ErrorContains(t, err, "left by deferred persistence")
	require.ErrorContains(t, err, `deferred-persistence = "sync"`)
	require.ErrorContains(t, err, `pruning = "nothing"`)
	require.Equal(t, before, dbDigest(t, off))

	// "sync" repairs it and replays to the reference.
	replayFrom(t, db, DeferredSync, ids)
}

// TestCelestiaDeferredKillBeforeFirstSoftCommit: a kill right after "sync"
// recorded deferred mode, before any soft commit. Both "sync" and "off"
// starts open the DB and replay to the reference hashes.
func TestCelestiaDeferredKillBeforeFirstSoftCommit(t *testing.T) {
	db, ids := stateSyncedDB(t)

	cur := firstFlushH + 1
	ms := openDeferredTest(t, db, DeferredSync, firstFlushEvery, behindClock(&cur))
	ms.SetCommitHeader(cmtproto.Header{Height: cur, Time: headerTime(cur)})
	ms.deferredBeforeCommit()
	require.True(t, ms.celestiaDeferred.isActive())
	last, written, err := ReadDeferredMarker(db)
	require.NoError(t, err)
	require.True(t, written)
	require.Equal(t, firstFlushH, last)

	for _, mode := range []string{DeferredSync, DeferredOff} {
		replayFrom(t, copyMemDB(t, db), mode, ids)
	}
}

// TestCelestiaDeferredEnableFailureWritesNoMarker: "sync" does not turn on
// over trees with history, and then records nothing in the marker.
func TestCelestiaDeferredEnableFailureWritesNoMarker(t *testing.T) {
	db := dbm.NewMemDB()
	ms := openDeferredTest(t, db, DeferredOff, 0, nil)
	for h := int64(1); h <= 20; h++ {
		seededBlock(ms, h)
	}
	require.NoError(t, ms.Close())

	cur := int64(21)
	ms = openDeferredTest(t, db, DeferredSync, firstFlushEvery, behindClock(&cur))
	seededBlock(ms, cur)
	require.False(t, ms.celestiaDeferred.isActive())
	bz, err := db.Get([]byte(persistenceKey))
	require.NoError(t, err)
	require.Nil(t, bz)
	require.False(t, ms.celestiaMarkerDeferred)
}

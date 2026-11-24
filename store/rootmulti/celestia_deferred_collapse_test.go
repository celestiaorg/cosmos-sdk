package rootmulti

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"os"
	"testing"
	"time"

	dbm "github.com/cosmos/cosmos-db"
	iavltree "github.com/cosmos/iavl"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/metrics"
	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/types"
	"cosmossdk.io/store/wrapper"
)

// Tests for turning "sync" mode on over trees that hold history: the trees
// are collapsed to their latest version first when the pruning policy and
// snapshots allow it.

func openCollapseTest(t *testing.T, db dbm.DB, mode, pruning string, now func() time.Time) *Store {
	t.Helper()
	ms := NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	ms.SetPruning(pruningtypes.NewPruningOptionsFromString(pruning))
	require.NoError(t, ms.SetDeferredPersistence(mode, 4))
	if ms.celestiaDeferred != nil {
		ms.celestiaDeferred.now = now
	}
	for _, key := range deferredTestKeys {
		ms.MountStoreWithDB(key, types.StoreTypeIAVL, nil)
	}
	require.NoError(t, ms.LoadLatestVersion())
	return ms
}

// farBehind is a wall clock far ahead of every test header: catching up.
func farBehind() time.Time { return headerTime(1_000_000) }

// commitSeeded commits a block whose writes depend only on h, so a block can
// be replayed after a kill.
func commitSeeded(ms *Store, h int64) types.CommitID {
	return commitBlock(ms, rand.New(rand.NewSource(h)), h) //nolint:gosec // deterministic test data
}

func copyMemDB(t *testing.T, src dbm.DB) *dbm.MemDB {
	t.Helper()
	dst := dbm.NewMemDB()
	it, err := src.Iterator(nil, nil)
	require.NoError(t, err)
	defer it.Close()
	for ; it.Valid(); it.Next() {
		require.NoError(t, dst.Set(bytes.Clone(it.Key()), bytes.Clone(it.Value())))
	}
	return dst
}

// stockDB returns a DB written upstream with heights 1..n.
func stockDB(t *testing.T, pruning string, n int64) *dbm.MemDB {
	t.Helper()
	db := dbm.NewMemDB()
	ms := openCollapseTest(t, db, DeferredOff, pruning, nil)
	for h := int64(1); h <= n; h++ {
		commitSeeded(ms, h)
	}
	require.NoError(t, ms.Close())
	return db
}

func storeVersions(ms *Store) [][]int {
	var out [][]int
	for _, st := range ms.iavlStores() {
		out = append(out, st.GetAllVersions())
	}
	return out
}

func countCollapses(t *testing.T) *int {
	t.Helper()
	n := new(int)
	celestiaDeferredTestHook = func(phase string, _ int64) {
		if phase == "collapse" {
			*n++
		}
	}
	t.Cleanup(func() { celestiaDeferredTestHook = nil })
	return n
}

// TestCelestiaDeferredCollapseEverything: a stock DB with three versions per
// tree, under pruning "everything", turns "sync" on at the first commit while
// catching up; the commit IDs equal an upstream run throughout and after a
// restart.
func TestCelestiaDeferredCollapseEverything(t *testing.T) {
	const pruning = pruningtypes.PruningOptionEverything
	db := stockDB(t, pruning, 3)
	ref := openCollapseTest(t, copyMemDB(t, db), DeferredOff, pruning, nil)
	collapses := countCollapses(t)

	ms := openCollapseTest(t, db, DeferredSync, pruning, farBehind)
	require.Equal(t, [][]int{{1, 2, 3}, {1, 2, 3}, {1, 2, 3}}, storeVersions(ms))
	for h := int64(4); h <= 40; h++ {
		require.Equal(t, commitSeeded(ref, h), commitSeeded(ms, h), "height %d", h)
		require.True(t, ms.celestiaDeferred.isActive(), "height %d", h)
	}
	require.Equal(t, len(deferredTestKeys), *collapses)
	require.NoError(t, ms.Close())

	ms = openCollapseTest(t, db, DeferredSync, pruning, farBehind)
	require.Equal(t, ref.LastCommitID(), ms.LastCommitID())
	require.Equal(t, commitSeeded(ref, 41), commitSeeded(ms, 41))
}

// TestCelestiaDeferredCollapseKeepsHistory: under the default pruning a tree
// with many versions stays stock and loses nothing; a tree with one version
// below the latest (the shape a binary hop leaves) is collapsed.
func TestCelestiaDeferredCollapseKeepsHistory(t *testing.T) {
	const pruning = pruningtypes.PruningOptionDefault
	collapses := countCollapses(t)

	db := stockDB(t, pruning, 30)
	ms := openCollapseTest(t, db, DeferredSync, pruning, farBehind)
	for h := int64(31); h <= 35; h++ {
		commitSeeded(ms, h)
		require.False(t, ms.celestiaDeferred.isActive(), "height %d", h)
	}
	require.Equal(t, "the pruning strategy keeps history", ms.celestiaDeferred.offReason)
	require.Zero(t, *collapses)
	for _, v := range storeVersions(ms) {
		require.Len(t, v, 35)
		require.Equal(t, 1, v[0])
	}

	db = stockDB(t, pruning, 2)
	ref := openCollapseTest(t, copyMemDB(t, db), DeferredOff, pruning, nil)
	ms = openCollapseTest(t, db, DeferredSync, pruning, farBehind)
	require.Equal(t, commitSeeded(ref, 3), commitSeeded(ms, 3))
	require.True(t, ms.celestiaDeferred.isActive())
	require.Equal(t, len(deferredTestKeys), *collapses)
}

// TestCelestiaDeferredCollapseSnapshotGate: a version a snapshot may still
// need keeps the mode off.
func TestCelestiaDeferredCollapseSnapshotGate(t *testing.T) {
	const pruning = pruningtypes.PruningOptionEverything
	db := stockDB(t, pruning, 3)
	ms := openCollapseTest(t, db, DeferredSync, pruning, farBehind)
	ms.SetSnapshotInterval(3) // latest (3) is a snapshot height
	commitSeeded(ms, 4)
	require.False(t, ms.celestiaDeferred.isActive())
	require.Equal(t, "a snapshot may need an older version", ms.celestiaDeferred.offReason)
	require.Equal(t, [][]int{{1, 2, 3, 4}, {1, 2, 3, 4}, {1, 2, 3, 4}}, storeVersions(ms))

	ms.SetSnapshotInterval(0)
	ms.deferredSnapshotStart()
	commitSeeded(ms, 5)
	require.False(t, ms.celestiaDeferred.isActive(), "snapshot running")
	ms.deferredSnapshotDone(3)
	commitSeeded(ms, 6)
	require.True(t, ms.celestiaDeferred.isActive())
}

// loadLegacyFixture writes, into every test store, a tree in the legacy
// (iavl v0.19/v0.20) node format holding only version 3, plus the multistore
// metadata for version 3, as a v3 (cosmos-sdk v0.46) binary would leave it.
// testdata/legacy_iavl_v020_at3.bin was written with iavl v0.20.0: versions
// 1-3 of 20 sets each ("k%03d" -> "legacy-v<v>-<j>") and one removal, then
// DeleteVersionsRange(1, 3); records are uvarint-length-prefixed key, value.
func loadLegacyFixture(t *testing.T) *dbm.MemDB {
	t.Helper()
	bz, err := os.ReadFile("testdata/legacy_iavl_v020_at3.bin")
	require.NoError(t, err)
	db := dbm.NewMemDB()
	cInfo := &types.CommitInfo{Version: 3}
	for _, key := range deferredTestKeys {
		pdb := dbm.NewPrefixDB(db, []byte("s/k:"+key.Name()+"/"))
		for rest := bz; len(rest) > 0; {
			kl, n := binary.Uvarint(rest)
			k := rest[n : n+int(kl)]
			rest = rest[n+int(kl):]
			vl, n := binary.Uvarint(rest)
			v := rest[n : n+int(vl)]
			rest = rest[n+int(vl):]
			require.NoError(t, pdb.Set(k, v))
		}
		tree := iavltree.NewMutableTree(wrapper.NewDBWrapper(pdb), 0, false, iavltree.NewNopLogger())
		_, err := tree.LoadVersion(3)
		require.NoError(t, err)
		require.ErrorIs(t, tree.EnableDeferred(), iavltree.ErrDeferredLegacy)
		cInfo.StoreInfos = append(cInfo.StoreInfos, types.StoreInfo{
			Name:     key.Name(),
			CommitId: types.CommitID{Version: 3, Hash: tree.Hash()},
		})
	}
	(&Store{logger: log.NewNopLogger()}).flushMetadata(db, 3, cInfo)
	return db
}

// TestCelestiaDeferredCollapseLegacy: trees in the legacy format, as the
// binary hop from v0.46 leaves them, stay off at the first commit (a legacy
// latest version cannot be collapsed) and turn on at the second, under
// "everything" and under the default pruning (hop shape).
func TestCelestiaDeferredCollapseLegacy(t *testing.T) {
	for _, pruning := range []string{pruningtypes.PruningOptionEverything, pruningtypes.PruningOptionDefault} {
		t.Run(pruning, func(t *testing.T) {
			db := loadLegacyFixture(t)
			ref := openCollapseTest(t, copyMemDB(t, db), DeferredOff, pruning, nil)
			ms := openCollapseTest(t, db, DeferredSync, pruning, farBehind)
			require.Equal(t, ref.LastCommitID(), ms.LastCommitID())

			require.Equal(t, commitSeeded(ref, 4), commitSeeded(ms, 4))
			require.False(t, ms.celestiaDeferred.isActive())
			require.Equal(t, "collapsing to the latest version failed", ms.celestiaDeferred.offReason)
			for h := int64(5); h <= 30; h++ {
				require.Equal(t, commitSeeded(ref, h), commitSeeded(ms, h), "height %d", h)
				require.True(t, ms.celestiaDeferred.isActive(), "height %d", h)
			}
			require.NoError(t, ms.Close())
			it, err := db.Iterator(nil, nil)
			require.NoError(t, err)
			for ; it.Valid(); it.Next() {
				k, found := bytes.CutPrefix(it.Key(), []byte("s/k:"))
				if _, inner, ok := bytes.Cut(k, []byte("/")); found && ok && len(inner) > 0 {
					require.NotEqual(t, byte('r'), inner[0], "legacy root %q left", it.Key())
				}
			}
			require.NoError(t, it.Close())
			ms = openCollapseTest(t, db, DeferredSync, pruning, farBehind)
			require.Equal(t, ref.LastCommitID(), ms.LastCommitID())
		})
	}
}

// TestCelestiaDeferredCollapseKill kills the collapse after the first and
// after the last store: the DB stays stock and loads upstream with the
// committed hashes, an app-state rollback is refused without writing while
// the stores disagree on their first version, and the next "sync" start
// collapses again and turns on.
func TestCelestiaDeferredCollapseKill(t *testing.T) {
	const pruning = pruningtypes.PruningOptionEverything
	for _, killAt := range []int{1, len(deferredTestKeys)} {
		t.Run(map[int]string{1: "first", len(deferredTestKeys): "last"}[killAt], func(t *testing.T) {
			db := stockDB(t, pruning, 3)
			ref := openCollapseTest(t, copyMemDB(t, db), DeferredOff, pruning, nil)
			want3 := ref.LastCommitID()

			n := 0
			celestiaDeferredTestHook = func(phase string, _ int64) {
				if phase == "collapse" {
					if n++; n == killAt {
						panic("kill")
					}
				}
			}
			defer func() { celestiaDeferredTestHook = nil }()
			ms := openCollapseTest(t, db, DeferredSync, pruning, farBehind)
			require.Panics(t, func() { commitSeeded(ms, 4) })
			celestiaDeferredTestHook = nil
			_, written, err := ReadDeferredMarker(db)
			require.NoError(t, err)
			require.False(t, written, "the marker follows the collapse")

			st := openCollapseTest(t, db, DeferredOff, pruning, nil)
			require.Equal(t, want3, st.LastCommitID())
			versions := storeVersions(st)
			for i, v := range versions {
				if i < killAt {
					require.Equal(t, []int{3}, v)
				} else {
					require.Equal(t, []int{1, 2, 3}, v)
				}
			}
			before := dbDigest(t, db)
			require.ErrorContains(t, st.RollbackToVersion(2), "has no version 2")
			require.Equal(t, before, dbDigest(t, db), "a refused rollback writes nothing")
			require.Equal(t, versions, storeVersions(st))
			require.NoError(t, st.Close())

			ms = openCollapseTest(t, db, DeferredSync, pruning, farBehind)
			require.Equal(t, want3, ms.LastCommitID())
			for h := int64(4); h <= 20; h++ {
				require.Equal(t, commitSeeded(ref, h), commitSeeded(ms, h), "height %d", h)
				require.True(t, ms.celestiaDeferred.isActive(), "height %d", h)
			}
		})
	}
}

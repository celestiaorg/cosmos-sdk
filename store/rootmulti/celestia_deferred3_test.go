package rootmulti

import (
	"crypto/sha256"
	"fmt"
	"math/rand"
	"testing"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/metrics"
	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/types"
)

var deferredTestKeys = []types.StoreKey{testStoreKey1, testStoreKey2, testStoreKey3}

// genesisTime is the header time of height 0; heights are 6 s apart.
var genesisTime = time.Date(2023, 10, 31, 0, 0, 0, 0, time.UTC)

func headerTime(h int64) time.Time { return genesisTime.Add(time.Duration(h) * 6 * time.Second) }

func openDeferredTest(t *testing.T, db dbm.DB, mode string, k int64, now func() time.Time) *Store {
	t.Helper()
	ms := NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	ms.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningDefault))
	require.NoError(t, ms.SetDeferredPersistence(mode, k))
	if ms.celestiaDeferred != nil && now != nil {
		ms.celestiaDeferred.now = now
	}
	for _, key := range deferredTestKeys {
		ms.MountStoreWithDB(key, types.StoreTypeIAVL, nil)
	}
	require.NoError(t, ms.LoadLatestVersion())
	return ms
}

// commitBlock writes a seeded block for height h and commits it.
func commitBlock(ms *Store, r *rand.Rand, h int64) types.CommitID {
	ms.SetCommitHeader(cmtproto.Header{Height: h, Time: headerTime(h)})
	cms := ms.CacheMultiStore()
	for _, key := range deferredTestKeys {
		st := cms.GetKVStore(key)
		for j, n := 0, r.Intn(12); j < n; j++ {
			k := []byte(fmt.Sprintf("k%03d", r.Intn(150)))
			if r.Intn(4) == 0 {
				st.Delete(k)
			} else {
				st.Set(k, []byte(fmt.Sprintf("v%d-%d", h, j)))
			}
		}
	}
	cms.Write()
	return ms.Commit()
}

func dbDigest(t *testing.T, db dbm.DB) [32]byte {
	t.Helper()
	it, err := db.Iterator(nil, nil)
	require.NoError(t, err)
	defer it.Close()
	h := sha256.New()
	for ; it.Valid(); it.Next() {
		fmt.Fprintf(h, "%X=%X;", it.Key(), it.Value())
	}
	return [32]byte(h.Sum(nil))
}

// TestCelestiaDeferredOpenWritesNothing: opening a DB, as rollback, export or
// a dry run do, never writes, whatever the mode; a DB with deferred leftovers
// is refused by a non-deferred open, still without writing.
func TestCelestiaDeferredOpenWritesNothing(t *testing.T) {
	r := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic test data
	stock := dbm.NewMemDB()
	ms := openDeferredTest(t, stock, DeferredOff, 0, nil)
	for h := int64(1); h <= 20; h++ {
		commitBlock(ms, r, h)
	}
	before := dbDigest(t, stock)
	for _, mode := range []string{DeferredOff, DeferredSync} {
		openDeferredTest(t, stock, mode, 5, nil)
		require.Equal(t, before, dbDigest(t, stock), "open in mode %q wrote to a stock DB", mode)
	}

	// A deferred DB killed after phase (1) of a flush: stores are ahead of the metadata.
	df := dbm.NewMemDB()
	ms = openDeferredTest(t, df, DeferredAlways, 5, nil)
	for h := int64(1); h <= 9; h++ {
		commitBlock(ms, r, h)
	}
	celestiaDeferredTestHook = func(phase string, _ int64) {
		if phase == "stores" {
			panic("kill")
		}
	}
	defer func() { celestiaDeferredTestHook = nil }()
	require.Panics(t, func() { commitBlock(ms, r, 10) })
	celestiaDeferredTestHook = nil
	before = dbDigest(t, df)
	o := NewStore(df, log.NewNopLogger(), metrics.NewNoOpMetrics())
	o.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningDefault))
	for _, key := range deferredTestKeys {
		o.MountStoreWithDB(key, types.StoreTypeIAVL, nil)
	}
	require.ErrorContains(t, o.LoadLatestVersion(), "left by deferred persistence")
	require.Equal(t, before, dbDigest(t, df))

	// A "sync" start repairs it.
	ms = openDeferredTest(t, df, DeferredSync, 5, nil)
	require.Equal(t, int64(5), ms.LastCommitID().Version)
	require.True(t, ms.celestiaDeferred.isActive())
}

// TestCelestiaDeferredSyncMatchesStock moves the wall clock so the node is
// catching up, reaches the tip, falls behind again and catches up again. The
// commit IDs must equal an upstream run at every height: the catch-up signal
// only changes when data is written. It also checks when the mode is on, and
// that leaving it restores per-block persistence and rollback.
func TestCelestiaDeferredSyncMatchesStock(t *testing.T) {
	rs, rd := rand.New(rand.NewSource(9)), rand.New(rand.NewSource(9)) //nolint:gosec // deterministic test data
	ref := openDeferredTest(t, dbm.NewMemDB(), DeferredOff, 0, nil)
	var now time.Time
	db := dbm.NewMemDB()
	ms := openDeferredTest(t, db, DeferredSync, 4, func() time.Time { return now })

	behind := func(h int64) bool { return h < 30 || (h >= 45 && h < 60) }
	for h := int64(1); h <= 80; h++ {
		if behind(h) {
			now = headerTime(h).Add(time.Hour)
		} else {
			now = headerTime(h).Add(2 * time.Second)
		}
		require.Equal(t, commitBlock(ref, rs, h), commitBlock(ms, rd, h), "height %d", h)
		switch {
		case h < 30:
			require.True(t, ms.celestiaDeferred.isActive(), "height %d: catching up from an empty DB", h)
		default:
			// Back at the tip the trees keep history, so falling behind again
			// does not turn the mode on (it would delete that history).
			require.False(t, ms.celestiaDeferred.isActive(), "height %d", h)
			require.Equal(t, h, GetLatestVersion(db), "height %d: per-block persistence", h)
		}
	}
	_, written, err := ReadDeferredMarker(db)
	require.NoError(t, err)
	require.False(t, written)
	require.NoError(t, ms.RollbackToVersion(79))
	require.Equal(t, int64(79), ms.LastCommitID().Version)
}

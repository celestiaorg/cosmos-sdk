package rootmulti

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/libs/log"
	dbm "github.com/tendermint/tm-db"

	pruningtypes "github.com/cosmos/cosmos-sdk/pruning/types"
	"github.com/cosmos/cosmos-sdk/store/types"
)

func openModeStore(t *testing.T, db dbm.DB, mode string, k int64, pruning pruningtypes.PruningOptions, snapshotInterval uint64) *Store {
	rs := NewStore(db, log.NewNopLogger())
	rs.SetPruning(pruning)
	rs.SetRepairAhead(true)
	require.NoError(t, rs.SetDeferredPersistence(mode, k, snapshotInterval))
	for _, key := range []types.StoreKey{defKey1, defKey2} {
		rs.MountStoreWithDB(key, types.StoreTypeIAVL, nil)
	}
	require.NoError(t, rs.LoadLatestVersion())
	return rs
}

var everything = pruningtypes.NewPruningOptions(pruningtypes.PruningEverything)

// "sync" mode: soft commits while catching up, then a flush and per-block
// persistence. Whatever the catch-up signal does, every CommitID equals
// upstream's: the signal only changes when data is written.
func TestDeferredSyncModeCatchUpToTip(t *testing.T) {
	keys := []types.StoreKey{defKey1, defKey2}
	for seed := int64(1); seed <= 4; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			r := rand.New(rand.NewSource(seed))
			tip := int64(10 + r.Intn(40)) // first block that is not catching up
			up := openModeStore(t, dbm.NewMemDB(), DeferredOff, 2, everything, 0)
			db := dbm.NewMemDB()
			rs := openModeStore(t, db, DeferredSync, 7, everything, 0)
			require.True(t, rs.deferredEnabled())
			for h := int64(1); h <= 60; h++ {
				blockOps(h, up.GetKVStore, keys)
				blockOps(h, rs.GetKVStore, keys)
				// after the tip the signal may flap; leaving is one-way
				rs.SetCatchingUp(h < tip || (h > tip && r.Intn(2) == 0))
				require.Equal(t, up.Commit(), rs.Commit(), "height %d", h)
				if h >= tip {
					require.False(t, rs.deferredEnabled(), "height %d", h)
					require.Equal(t, h, GetLatestVersion(db), "per-block persistence after the tip")
				}
			}
			deferred, last, found, err := DeferredMarker(db)
			require.NoError(t, err)
			require.True(t, found)
			require.False(t, deferred)
			require.Equal(t, tip, last)

			// per-block again: rolling back one height works
			require.NoError(t, rs.RollbackToVersion(59))
			require.Equal(t, int64(59), GetLatestVersion(db))
		})
	}
}

// Deferred persistence stays off when the node keeps history it would not
// write.
func TestDeferredAutoOff(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pruning  pruningtypes.PruningOptions
		snapshot uint64
		active   bool
	}{
		{"everything", everything, 0, true},
		{"nothing", pruningtypes.NewPruningOptions(pruningtypes.PruningNothing), 0, false},
		{"default", pruningtypes.NewPruningOptions(pruningtypes.PruningDefault), 0, false},
		{"custom_small", pruningtypes.NewCustomPruningOptions(50, 10), 0, true},
		{"custom_large", pruningtypes.NewCustomPruningOptions(500, 10), 0, false},
		{"snapshots", everything, 100, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := openModeStore(t, dbm.NewMemDB(), DeferredSync, 100, tc.pruning, tc.snapshot)
			require.Equal(t, tc.active, rs.deferredEnabled())
		})
	}
}

func TestValidateDeferredPersistence(t *testing.T) {
	require.NoError(t, ValidateDeferredPersistence(DeferredSync, DefaultDeferredFlushInterval))
	require.NoError(t, ValidateDeferredPersistence(DeferredSync, MaxDeferredFlushInterval))
	require.Error(t, ValidateDeferredPersistence("yes", 100))
	require.Error(t, ValidateDeferredPersistence(DeferredSync, 1))
	require.Error(t, ValidateDeferredPersistence(DeferredSync, MaxDeferredFlushInterval+1))
}

// Heights between the last flush and the latest version are not kept: queries
// get a clear error instead of a missing-version failure.
func TestDeferredUnflushedHeightQuery(t *testing.T) {
	keys := []types.StoreKey{defKey1, defKey2}
	rs := openModeStore(t, dbm.NewMemDB(), DeferredSync, 7, everything, 0)
	for h := int64(1); h <= 10; h++ {
		blockOps(h, rs.GetKVStore, keys)
		rs.GetKVStore(defKey1).Set([]byte("always"), []byte{byte(h)})
		rs.Commit()
	}
	for _, h := range []int64{7, 10} {
		_, err := rs.CacheMultiStoreWithVersion(h)
		require.NoError(t, err, "height %d", h)
	}
	_, err := rs.CacheMultiStoreWithVersion(8)
	require.ErrorContains(t, err, "height 8 is not available")
	res := rs.Query(abci.RequestQuery{Path: "/store1/key", Data: []byte("always"), Height: 9})
	require.NotZero(t, res.Code)
	require.Contains(t, res.Log, "height 9 is not available")
	res = rs.Query(abci.RequestQuery{Path: "/store1/key", Data: []byte("always"), Height: 10})
	require.Zero(t, res.Code, res.Log)
	require.Equal(t, []byte{10}, res.Value)
}

// An app-state rollback of a DB last written by a deferred flush is refused
// with the last flushed version, and nothing is written.
func TestDeferredRollbackRefused(t *testing.T) {
	keys := []types.StoreKey{defKey1, defKey2}
	for _, graceful := range []bool{false, true} {
		t.Run(fmt.Sprintf("graceful=%v", graceful), func(t *testing.T) {
			db := dbm.NewMemDB()
			rs := openModeStore(t, db, DeferredSync, 7, everything, 0)
			for h := int64(1); h <= 20; h++ {
				blockOps(h, rs.GetKVStore, keys)
				rs.Commit()
			}
			want := int64(14)
			if graceful {
				require.NoError(t, rs.ShutdownFlush())
				want = 20
			}
			// the rollback command loads read-only: deferred off, no repair, no
			// fast-index upgrade (see server.KeyReadOnlyLoad)
			before := dbDigest(t, db)
			ro := NewStore(db, log.NewNopLogger())
			ro.SetIAVLDisableFastNode(true) // read-only load: no fast-index upgrade
			for _, key := range keys {
				ro.MountStoreWithDB(key, types.StoreTypeIAVL, nil)
			}
			require.NoError(t, ro.LoadLatestVersion())
			err := ro.RollbackToVersion(19)
			require.True(t, errors.Is(err, ErrRollbackDeferred), "%v", err)
			require.ErrorContains(t, err, fmt.Sprintf("holds only version %d", want))
			require.Equal(t, before, dbDigest(t, db))
			deferred, last, _, err := DeferredMarker(db)
			require.NoError(t, err)
			require.True(t, deferred)
			require.Equal(t, want, last)
		})
	}
}

// Queries read the latest soft-committed version while later blocks are
// committed and flushed (run with -race).
func TestDeferredConcurrentQueries(t *testing.T) {
	keys := []types.StoreKey{defKey1, defKey2}
	rs := openModeStore(t, dbm.NewMemDB(), DeferredSync, 5, everything, 0)
	heights := make(chan int64, 4)
	done := make(chan error)
	go func() {
		var err error
		for h := range heights {
			cms, e := rs.CacheMultiStoreWithVersion(h)
			if e != nil {
				// the height may already be superseded: only the clear
				// deferred-persistence error is acceptable
				if err == nil && !strings.Contains(e.Error(), "is not available") {
					err = e
				}
				continue
			}
			it := cms.GetKVStore(defKey1).Iterator(nil, nil)
			for ; it.Valid(); it.Next() {
				_ = it.Value()
			}
			it.Close()
		}
		done <- err
	}()
	for h := int64(1); h <= 80; h++ {
		blockOps(h, rs.GetKVStore, keys)
		rs.Commit()
		heights <- h
	}
	close(heights)
	require.NoError(t, <-done)
}

// Blocks commit on one goroutine (as BaseApp.Commit, under the write lock)
// while SIGTERM triggers ShutdownFlush on another (run with -race). The flush
// writes exactly the last committed block and no Commit completes after it.
func TestDeferredShutdownFlushConcurrentWithBlocks(t *testing.T) {
	keys := []types.StoreKey{defKey1, defKey2}
	for seed := int64(1); seed <= 3; seed++ {
		db := dbm.NewMemDB()
		rs := openModeStore(t, db, DeferredSync, 5, everything, 0)
		var committed atomic.Int64
		go func() {
			for h := int64(1); ; h++ {
				l := rs.WriteLock()
				l.Lock()
				blockOps(h, rs.GetKVStore, keys)
				committed.Store(rs.Commit().Version)
				l.Unlock()
			}
		}()
		time.Sleep(time.Duration(5+seed*7) * time.Millisecond)
		require.NoError(t, rs.ShutdownFlush())
		last := committed.Load()
		require.Equal(t, last, GetLatestVersion(db), "seed %d", seed)
		time.Sleep(50 * time.Millisecond)
		require.Equal(t, last, committed.Load(), "a Commit completed after ShutdownFlush")
	}
}

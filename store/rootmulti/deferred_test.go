package rootmulti

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/log"
	dbm "github.com/tendermint/tm-db"

	pruningtypes "github.com/cosmos/cosmos-sdk/pruning/types"
	"github.com/cosmos/cosmos-sdk/store/cache"
	"github.com/cosmos/cosmos-sdk/store/iavl"
	"github.com/cosmos/cosmos-sdk/store/types"
)

var (
	defKey1 = types.NewKVStoreKey("store1")
	defKey2 = types.NewKVStoreKey("store2")
	defKey3 = types.NewKVStoreKey("store3")
	defKey4 = types.NewKVStoreKey("store4")
)

type defNode struct {
	t        *testing.T
	db       dbm.DB
	rs       *Store
	flushK   int64 // 0 = upstream
	cacheOn  bool
	keys     []types.StoreKey
	reopened int
}

func (n *defNode) open() {
	rs := NewStore(n.db, log.NewNopLogger())
	rs.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningEverything))
	rs.SetRepairAhead(true)
	if n.flushK > 0 {
		require.NoError(n.t, rs.SetDeferredPersistence(DeferredSync, n.flushK, 0))
	}
	if n.cacheOn {
		rs.SetInterBlockCache(cache.NewCommitKVStoreCacheManager(cache.DefaultCommitKVStoreCacheSize))
	}
	for _, k := range n.keys {
		rs.MountStoreWithDB(k, types.StoreTypeIAVL, nil)
	}
	require.NoError(n.t, rs.LoadLatestVersion())
	require.Equal(n.t, n.flushK > 0, rs.deferredEnabled())
	n.rs = rs
	n.reopened++
}

// blockOps deterministically derives the writes of a block from its height.
func blockOps(height int64, kv func(types.StoreKey) types.KVStore, keys []types.StoreKey) {
	r := rand.New(rand.NewSource(height))
	n := r.Intn(30) // some blocks write nothing
	for i := 0; i < n; i++ {
		store := kv(keys[r.Intn(len(keys))])
		k := []byte(fmt.Sprintf("k%03d", r.Intn(200)))
		if r.Intn(4) == 0 {
			store.Delete(k)
			continue
		}
		v := make([]byte, 1+r.Intn(32))
		r.Read(v)
		store.Set(k, v)
	}
}

func (n *defNode) applyBlock(height int64) {
	blockOps(height, n.rs.GetKVStore, n.keys)
}

func TestDeferredPersistenceMultiStore(t *testing.T) {
	for _, cacheOn := range []bool{true, false} {
		t.Run(fmt.Sprintf("interBlockCache=%v", cacheOn), func(t *testing.T) {
			testDeferredPersistenceMultiStore(t, cacheOn)
		})
	}
}

func testDeferredPersistenceMultiStore(t *testing.T, cacheOn bool) {
	const (
		flushK        = 7
		lastHeight    = 300
		upgradeHeight = 150 // not a multiple of flushK
	)
	keys := []types.StoreKey{defKey1, defKey2, defKey3}
	up := &defNode{t: t, db: dbm.NewMemDB(), cacheOn: cacheOn, keys: keys}
	def := &defNode{t: t, db: dbm.NewMemDB(), flushK: flushK, cacheOn: cacheOn, keys: keys}
	up.open()
	def.open()

	hashes := map[int64]types.CommitID{}
	lastFlushed := int64(0)

	commitBoth := func(h int64) {
		before := dbDigest(t, def.db)
		cu := up.rs.Commit()
		cd := def.rs.Commit()
		require.Equal(t, h, cu.Version)
		require.Equal(t, cu, cd, "height %d", h)
		require.Equal(t, up.rs.LastCommitID(), def.rs.LastCommitID())
		hashes[h] = cu
		if def.rs.deferred.lastFlushed == h {
			lastFlushed = h
			require.Equal(t, h, GetLatestVersion(def.db))
			// only the flushed version is kept on disk
			for _, k := range def.keys {
				st := def.rs.GetCommitKVStore(k).(*iavl.Store)
				require.Equal(t, []int{int(h)}, st.GetAllVersions(), "store %s", k.Name())
			}
		} else {
			// soft commit: not a single byte written
			require.Equal(t, before, dbDigest(t, def.db), "height %d wrote to the DB", h)
			require.Equal(t, lastFlushed, GetLatestVersion(def.db))
		}
	}

	replayTo := func(target int64) {
		for h := def.rs.LastCommitID().Version + 1; h <= target; h++ {
			def.applyBlock(h)
			cd := def.rs.Commit()
			require.Equal(t, hashes[h], cd, "replayed height %d", h)
		}
	}

	for h := int64(1); h <= lastHeight; h++ {
		if h == upgradeHeight {
			// baseapp.Commit sequence at an app-version change.
			require.NoError(t, def.rs.FlushDeferred()) // before deliverState.ms.Write()
			def.rs.ForceFlushNextCommit()
			for _, n := range []*defNode{up, def} {
				n.applyBlock(h) // deliverState.ms.Write()
				n.rs.MountStoreWithDB(defKey4, types.StoreTypeIAVL, nil)
				require.NoError(t, n.rs.LoadLatestVersionAndUpgrade(&types.StoreUpgrades{
					Added:   []string{defKey4.Name()},
					Deleted: []string{defKey3.Name()},
				}))
				n.keys = []types.StoreKey{defKey1, defKey2, defKey4}
				// module migrations write into the new store
				blockOps(h*1000, n.rs.GetKVStore, []types.StoreKey{defKey4})
			}
			commitBoth(h)
			require.Equal(t, h, lastFlushed, "upgrade commit must be flushed")
			continue
		}

		up.applyBlock(h)
		def.applyBlock(h)
		commitBoth(h)

		// queries at the latest (unflushed) height are served from memory
		if h%flushK == 3 {
			cms, err := def.rs.CacheMultiStoreWithVersion(h)
			require.NoError(t, err)
			for i := 0; i < 200; i++ {
				k := []byte(fmt.Sprintf("k%03d", i))
				require.Equal(t, up.rs.GetKVStore(defKey1).Get(k), cms.GetKVStore(defKey1).Get(k))
			}
		}

		switch h {
		case 60, 200:
			// graceful shutdown (BaseApp.Close) + restart
			require.NoError(t, def.rs.FlushDeferred())
			require.Equal(t, h, GetLatestVersion(def.db))
			lastFlushed = h
			def.open()
			require.Equal(t, hashes[h], def.rs.LastCommitID())
		case 100, 230:
			// crash at a non-flush height: restart from the last flush, the
			// consensus engine replays the missing blocks.
			require.NotEqual(t, int64(0), h%flushK)
			def.open()
			require.Equal(t, lastFlushed, def.rs.LastCommitID().Version)
			require.Equal(t, hashes[lastFlushed], def.rs.LastCommitID())
			replayTo(h)
		}
	}

	// final restart
	require.NoError(t, def.rs.FlushDeferred())
	def.open()
	require.Equal(t, hashes[lastHeight], def.rs.LastCommitID())
	require.Equal(t, up.rs.LastCommitID(), def.rs.LastCommitID())
}

// A crash after some stores flushed version V but before the multistore
// metadata was written leaves IAVL versions above the committed version; they
// are removed on load and the replay proceeds.
func TestDeferredPersistencePartialFlushRecovery(t *testing.T) {
	keys := []types.StoreKey{defKey1, defKey2}
	up := &defNode{t: t, db: dbm.NewMemDB(), keys: keys}
	def := &defNode{t: t, db: dbm.NewMemDB(), flushK: 5, keys: keys}
	up.open()
	def.open()
	hashes := map[int64]types.CommitID{}
	for h := int64(1); h <= 8; h++ {
		up.applyBlock(h)
		def.applyBlock(h)
		hashes[h] = up.rs.Commit()
		require.Equal(t, hashes[h], def.rs.Commit())
	}
	require.Equal(t, int64(5), GetLatestVersion(def.db))
	// phase 1 for one store only, then "crash"
	require.NoError(t, def.rs.GetCommitKVStore(defKey1).(*iavl.Store).FlushDeferred())
	def.open()
	require.Equal(t, hashes[5], def.rs.LastCommitID())
	require.Equal(t, []int{5}, def.rs.GetCommitKVStore(defKey1).(*iavl.Store).GetAllVersions())
	for h := int64(6); h <= 8; h++ { // handshake replay
		def.applyBlock(h)
		require.Equal(t, hashes[h], def.rs.Commit(), "replayed height %d", h)
	}
	for h := int64(9); h <= 12; h++ {
		up.applyBlock(h)
		def.applyBlock(h)
		cu := up.rs.Commit()
		require.Equal(t, cu, def.rs.Commit(), "height %d", h)
	}
	require.Equal(t, int64(10), GetLatestVersion(def.db))
	require.Equal(t, []int{10}, def.rs.GetCommitKVStore(defKey1).(*iavl.Store).GetAllVersions())
}

// Reloading stores with pending soft commits while the working trees are
// dirty must fail loudly rather than persist a mixed state.
func TestDeferredPersistenceDirtyReloadFails(t *testing.T) {
	def := &defNode{t: t, db: dbm.NewMemDB(), flushK: 10, keys: []types.StoreKey{defKey1}}
	def.open()
	for h := int64(1); h <= 3; h++ {
		def.applyBlock(h)
		def.rs.Commit()
	}
	def.rs.GetKVStore(defKey1).Set([]byte("dirty"), []byte("x"))
	require.Error(t, def.rs.LoadLatestVersion())
}

// --- crash / shutdown recovery -------------------------------------------

// lockstep commits heights from..to on both nodes and checks equal CommitIDs.
func lockstep(t *testing.T, up, def *defNode, from, to int64, hashes map[int64]types.CommitID) {
	for h := from; h <= to; h++ {
		up.applyBlock(h)
		def.applyBlock(h)
		cu := up.rs.Commit()
		require.Equal(t, cu, def.rs.Commit(), "height %d", h)
		hashes[h] = cu
	}
}

// replay re-executes heights after def's loaded version up to target (what the
// consensus handshake does) and checks the hashes.
func replay(t *testing.T, def *defNode, target int64, hashes map[int64]types.CommitID) {
	for h := def.rs.LastCommitID().Version + 1; h <= target; h++ {
		def.applyBlock(h)
		require.Equal(t, hashes[h], def.rs.Commit(), "replayed height %d", h)
	}
}

func onDiskVersions(t *testing.T, n *defNode) map[string][]int {
	out := map[string][]int{}
	for _, k := range n.keys {
		out[k.Name()] = n.rs.GetCommitKVStore(k).(*iavl.Store).GetAllVersions()
	}
	return out
}

func requireAllOnDisk(t *testing.T, n *defNode, versions ...int) {
	for name, vs := range onDiskVersions(t, n) {
		require.Equal(t, versions, vs, "store %s", name)
	}
}

// (a) SIGTERM while a block is half applied (deliverState written into the
// IAVL working trees, Commit not yet called): the shutdown flush must write
// the previous height for every store, and Commit must never return.
func TestDeferredPersistenceShutdownMidBlock(t *testing.T) {
	for _, cacheOn := range []bool{true, false} {
		t.Run(fmt.Sprintf("interBlockCache=%v", cacheOn), func(t *testing.T) {
			keys := []types.StoreKey{defKey1, defKey2, defKey3}
			up := &defNode{t: t, db: dbm.NewMemDB(), cacheOn: cacheOn, keys: keys}
			def := &defNode{t: t, db: dbm.NewMemDB(), flushK: 7, cacheOn: cacheOn, keys: keys}
			up.open()
			def.open()
			hashes := map[int64]types.CommitID{}
			lockstep(t, up, def, 1, 19, hashes)
			require.Equal(t, int64(14), GetLatestVersion(def.db))

			// block 20 in progress (as BaseApp.Commit: stores written under
			// the write lock): every store dirty
			lock := def.rs.WriteLock()
			lock.Lock()
			blockOps(20, def.rs.GetKVStore, keys)
			blockOps(20, up.rs.GetKVStore, keys)
			// the strict flush refuses and writes nothing
			require.Error(t, def.rs.FlushDeferred())
			requireAllOnDisk(t, def, 14)

			// SIGTERM now: the shutdown flush waits for the block to commit
			flushed := make(chan error, 1)
			go func() { flushed <- def.rs.ShutdownFlush() }()
			select {
			case <-flushed:
				t.Fatal("ShutdownFlush ran while the block was being written")
			case <-time.After(100 * time.Millisecond):
			}
			hashes[20] = up.rs.Commit()
			require.Equal(t, hashes[20], def.rs.Commit())
			lock.Unlock()
			require.NoError(t, <-flushed)
			require.NoError(t, def.rs.ShutdownFlush()) // idempotent (Close after hook)
			require.Equal(t, int64(20), GetLatestVersion(def.db))
			requireAllOnDisk(t, def, 20)

			// The next block must never commit: no app hash for a block the
			// DB will never contain.
			done := make(chan struct{})
			closedRS := def.rs // def.rs is replaced by the restart below
			go func() {
				l := closedRS.WriteLock()
				l.Lock()
				defer l.Unlock()
				blockOps(21, closedRS.GetKVStore, keys)
				closedRS.Commit()
				close(done)
			}()
			select {
			case <-done:
				t.Fatal("Commit returned after ShutdownFlush")
			case <-time.After(200 * time.Millisecond):
			}
			require.NoError(t, closedRS.ShutdownFlush()) // must not deadlock on the held lock

			// restart: height 20, block 21 is replayed, then normal operation
			def.open()
			require.Equal(t, hashes[20], def.rs.LastCommitID())
			lockstep(t, up, def, 21, 45, hashes)
			require.NoError(t, def.rs.ShutdownFlush())
			def.open()
			require.Equal(t, hashes[45], def.rs.LastCommitID())
		})
	}
}

// (b) process killed during phase 1 (a subset of stores wrote version V, no
// commit info for V): reload must roll those stores back and replay, in
// deferred mode and also when restarted without the env var (the incident:
// "must save consecutive versions; expected 350034, got 350001").
func TestDeferredPersistenceKillDuringStoreFlush(t *testing.T) {
	for _, restartK := range []int64{1000, 0} {
		for _, lastHeight := range []int64{30, 2} { // gap 29 / gap 1 (upstream idempotent path)
			t.Run(fmt.Sprintf("restartK=%d/gap=%d", restartK, lastHeight-1), func(t *testing.T) {
				keys := []types.StoreKey{defKey1, defKey2, defKey3}
				up := &defNode{t: t, db: dbm.NewMemDB(), keys: keys}
				def := &defNode{t: t, db: dbm.NewMemDB(), flushK: 1000, keys: keys}
				up.open()
				def.open()
				hashes := map[int64]types.CommitID{}
				lockstep(t, up, def, 1, lastHeight, hashes)
				require.Equal(t, int64(1), GetLatestVersion(def.db))

				// phase 1 for two of three stores, then kill
				require.NoError(t, def.rs.GetCommitKVStore(defKey1).(*iavl.Store).FlushDeferred())
				require.NoError(t, def.rs.GetCommitKVStore(defKey3).(*iavl.Store).FlushDeferred())

				def.flushK = restartK
				def.open()
				require.Equal(t, hashes[1], def.rs.LastCommitID())
				if restartK > 0 || lastHeight > 2 {
					requireAllOnDisk(t, def, 1) // rolled back
				}
				replay(t, def, lastHeight, hashes)
				lockstep(t, up, def, lastHeight+1, lastHeight+20, hashes)
				require.NoError(t, def.rs.ShutdownFlush())
				def.open()
				require.Equal(t, hashes[lastHeight+20], def.rs.LastCommitID())
			})
		}
	}
}

// (c) process killed after the commit info was written but before the old
// versions were deleted: loads the new version, old versions go at the next
// flush.
func TestDeferredPersistenceKillBeforeDeletions(t *testing.T) {
	keys := []types.StoreKey{defKey1, defKey2, defKey3}
	up := &defNode{t: t, db: dbm.NewMemDB(), keys: keys}
	def := &defNode{t: t, db: dbm.NewMemDB(), flushK: 7, keys: keys}
	up.open()
	def.open()
	hashes := map[int64]types.CommitID{}
	lockstep(t, up, def, 1, 12, hashes)
	requireAllOnDisk(t, def, 7)

	// phases 1 and 2 only
	for _, k := range keys {
		require.NoError(t, def.rs.GetCommitKVStore(k).(*iavl.Store).FlushDeferred())
	}
	def.rs.flushMetadata(def.db, 12, def.rs.lastCommitInfo)

	def.open()
	require.Equal(t, hashes[12], def.rs.LastCommitID())
	requireAllOnDisk(t, def, 7, 12)
	lockstep(t, up, def, 13, 30, hashes)
	requireAllOnDisk(t, def, 28)
}

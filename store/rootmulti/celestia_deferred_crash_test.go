package rootmulti

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/cache"
	"cosmossdk.io/store/iavl"
	"cosmossdk.io/store/metrics"
	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/types"
)

var deferredTestStores = []string{"store1", "store2", "store3", "upgrade"}

type deferredBlock struct {
	ops map[string][][2][]byte // store -> (key, value|nil)
}

type deferredHarness struct {
	t        *testing.T
	k        int64 // 0 = upstream
	ibc      bool  // inter-block cache
	fastNode bool
	names    []string
	db       dbm.DB
	ms       *Store
}

func (h *deferredHarness) open(upgrades *types.StoreUpgrades) {
	ms := NewStore(h.db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	ms.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningEverything))
	ms.SetIAVLDisableFastNode(!h.fastNode)
	if h.ibc {
		ms.SetInterBlockCache(cache.NewCommitKVStoreCacheManager(cache.DefaultCommitKVStoreCacheSize))
	}
	if h.k > 1 {
		ms.celestiaDeferred = &deferredDriver{mode: DeferredAlways, every: h.k, maxPending: 1 << 30, now: time.Now}
	} else {
		ms.celestiaDeferred = nil
	}
	for _, n := range h.names {
		ms.MountStoreWithDB(types.NewKVStoreKey(n), types.StoreTypeIAVL, nil)
	}
	require.NoError(h.t, ms.LoadLatestVersionAndUpgrade(upgrades))
	require.Equal(h.t, h.k > 1, ms.celestiaDeferred.isActive())
	h.ms = ms
}

// apply writes a block through the root branch (as FinalizeBlock does) and
// returns the working hash.
func (h *deferredHarness) apply(b deferredBlock) []byte {
	cms := h.ms.CacheMultiStore()
	names := make([]string, 0, len(b.ops))
	for n := range b.ops {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		key := h.ms.keysByName[n]
		if key == nil {
			continue
		}
		st := cms.GetKVStore(key)
		for _, kv := range b.ops[n] {
			if kv[1] == nil {
				st.Delete(kv[0])
			} else {
				st.Set(kv[0], kv[1])
			}
		}
	}
	cms.Write()
	return h.ms.WorkingHash()
}

func (h *deferredHarness) commit(b deferredBlock) types.CommitID {
	wh := h.apply(b)
	id := h.ms.Commit()
	require.Equal(h.t, wh, id.Hash)
	return id
}

func randBlock(r *rand.Rand, height int64, names []string, plan bool) deferredBlock {
	b := deferredBlock{ops: map[string][][2][]byte{}}
	for _, n := range names {
		if n == "upgrade" {
			continue
		}
		if r.Intn(8) == 0 {
			continue // untouched store this block
		}
		for i := 0; i < r.Intn(25); i++ {
			k := []byte(fmt.Sprintf("k%03d", r.Intn(300)))
			if r.Intn(5) == 0 {
				b.ops[n] = append(b.ops[n], [2][]byte{k, nil})
			} else {
				b.ops[n] = append(b.ops[n], [2][]byte{k, []byte(fmt.Sprintf("v%d-%d", height, r.Int()))})
			}
		}
	}
	if plan {
		b.ops["upgrade"] = append(b.ops["upgrade"], [2][]byte{{0x00}, []byte("plan")})
	}
	return b
}

func metadataVersion(db dbm.DB) int64 { return GetLatestVersion(db) }

func (h *deferredHarness) iavlStore(name string) *iavl.Store {
	return h.ms.GetCommitKVStore(h.ms.keysByName[name]).(*iavl.Store)
}

// TestCelestiaDeferredMatchesUpstream runs an upstream multistore and a
// deferred one (K=7) side by side over random blocks, comparing working
// hashes and commit IDs at every height, with graceful restarts, crashes
// before a flush, a mid-block shutdown, kills after a partial store flush,
// after all stores but before the metadata, and after the metadata but
// before the deletions, an x/upgrade plan, a store upgrade (add/rename/
// delete), and finally a handoff to an upstream multistore on the same DB.
func TestCelestiaDeferredMatchesUpstream(t *testing.T) {
	for _, ibc := range []bool{false, true} {
		for _, fast := range []bool{true, false} {
			t.Run(fmt.Sprintf("interblockcache=%v/fastnode=%v", ibc, fast), func(t *testing.T) {
				testCelestiaDeferred(t, ibc, fast)
			})
		}
	}
}

func testCelestiaDeferred(t *testing.T, ibc, fast bool) {
	r := rand.New(rand.NewSource(7))
	names := append([]string(nil), deferredTestStores...)
	up := &deferredHarness{t: t, k: 0, ibc: ibc, fastNode: fast, names: names, db: dbm.NewMemDB()}
	df := &deferredHarness{t: t, k: 7, ibc: ibc, fastNode: fast, names: names, db: dbm.NewMemDB()}
	up.open(nil)
	df.open(nil)

	blocks := map[int64]deferredBlock{}
	ids := map[int64]types.CommitID{}
	height := int64(0)

	step := func(b deferredBlock) {
		height++
		blocks[height] = b
		uid := up.commit(b)
		did := df.commit(b)
		require.Equal(t, uid, did, "height %d", height)
		require.Equal(t, uid, df.ms.LastCommitID())
		ids[height] = uid
	}
	// replay re-executes blocks after a restart of the deferred store.
	restart := func(upgrades *types.StoreUpgrades) {
		df.open(upgrades)
		from := df.ms.LastCommitID().Version
		require.Equal(t, metadataVersion(df.db), from)
		if from > 0 {
			require.Equal(t, ids[from], df.ms.LastCommitID())
		}
		for hh := from + 1; hh <= height; hh++ {
			id := df.commit(blocks[hh])
			require.Equal(t, ids[hh], id, "replayed height %d", hh)
		}
	}
	checkOneVersion := func() {
		v := metadataVersion(df.db)
		for _, n := range df.names {
			st := df.iavlStore(n)
			require.Equal(t, []int{int(v)}, filterLE(st.GetAllVersions(), v), "store %s on-disk versions", n)
			requireNoLeakedNodes(t, df.db, n, st, v)
		}
	}

	for i := 0; i < 220; i++ {
		plan := i == 150 // x/upgrade plan stored at this height (signal upgrade)
		step(randBlock(r, height+1, names, plan))
		if plan {
			require.Equal(t, height, metadataVersion(df.db), "plan pending forces a flush")
			// the next block applies and deletes the plan, as x/upgrade does
			b := randBlock(r, height+1, names, false)
			b.ops["upgrade"] = [][2][]byte{{{0x00}, nil}}
			step(b)
		}
		if height%7 == 0 {
			require.Equal(t, height, metadataVersion(df.db))
			checkOneVersion()
		} else if !plan && height > 1 {
			require.Less(t, metadataVersion(df.db), height)
		}

		// queries at the latest (soft) height and the one before
		for _, qh := range []int64{height, height - 1} {
			if qh < 1 || (qh < height && qh < metadataVersion(df.db)) {
				continue // heights below the last flush are gone (one version on disk)
			}
			cmsD, err := df.ms.CacheMultiStoreWithVersion(qh)
			require.NoError(t, err, "query at %d", qh)
			cmsU, err := up.ms.CacheMultiStoreWithVersion(qh)
			require.NoError(t, err)
			k := []byte(fmt.Sprintf("k%03d", r.Intn(300)))
			require.Equal(t, cmsU.GetKVStore(up.ms.keysByName["store1"]).Get(k), cmsD.GetKVStore(df.ms.keysByName["store1"]).Get(k))
		}

		switch {
		case i == 20 || i == 120:
			// graceful restart
			require.NoError(t, df.ms.FlushDeferred())
			require.Equal(t, height, metadataVersion(df.db))
			restart(nil)
		case i == 30 || i == 90:
			// crash (kill -9) between flushes: no flush, replay from metadata
			restart(nil)
		case i == 40:
			// mid-block shutdown: the next block is written into the trees
			// (FinalizeBlock) when the shutdown flush runs; it must flush
			// every store and leave the uncommitted block to be replayed.
			nb := randBlock(r, height+1, names, false)
			df.apply(nb)
			require.NoError(t, df.ms.FlushDeferred())
			require.Equal(t, height, metadataVersion(df.db))
			restart(nil)
			checkOneVersion()
		case i == 50:
			// kill after a partial store flush: store1 wrote, others did not
			require.NoError(t, df.iavlStore("store1").FlushDeferred())
			restart(nil)
		case i == 60 || i == 70:
			// kill after all stores flushed, before the metadata (i==60),
			// or after the metadata, before the deletions (i==70)
			phase := map[int]string{60: "stores", 70: "metadata"}[i]
			celestiaDeferredTestHook = func(p string, _ int64) {
				if p == phase {
					panic("injected kill")
				}
			}
			for {
				b := randBlock(r, height+1, names, false)
				height++
				blocks[height] = b
				uid := up.commit(b)
				ids[height] = uid
				killed := func() (k bool) {
					defer func() {
						if rec := recover(); rec != nil {
							k = true
						}
					}()
					df.commit(b)
					return false
				}()
				if killed {
					break
				}
			}
			celestiaDeferredTestHook = nil
			restart(nil)
			if phase == "metadata" {
				require.Equal(t, height, metadataVersion(df.db))
			}
			checkOneVersion()
		case i == 100:
			// store upgrade: add store4, rename store2 -> restore2, delete store3
			require.NoError(t, df.ms.FlushDeferred())
			newNames := []string{"store1", "restore2", "store3", "store4", "upgrade"}
			upg := &types.StoreUpgrades{Added: []string{"store4"}, Renamed: []types.StoreRename{{OldKey: "store2", NewKey: "restore2"}}, Deleted: []string{"store3"}}
			up.names, df.names = newNames, newNames
			up.open(upg)
			df.open(upg)
			names = []string{"store1", "restore2", "store4", "upgrade"}
			step(randBlock(r, height+1, names, false))
			require.Equal(t, height, metadataVersion(df.db), "store removal forces a flush")
			up.names = names
			df.names = names
			up.open(nil)
			restart(nil)
		}
	}

	// a store behind the metadata fails the load loudly
	{
		require.NoError(t, df.ms.FlushDeferred())
		v := metadataVersion(df.db)
		ci, err := df.ms.GetCommitInfo(v)
		require.NoError(t, err)
		ci2 := *ci
		ci2.Version = v + 3
		bad := dbm.NewMemDB()
		it, err := df.db.Iterator(nil, nil)
		require.NoError(t, err)
		for ; it.Valid(); it.Next() {
			require.NoError(t, bad.Set(it.Key(), it.Value()))
		}
		it.Close()
		tmp := NewStore(bad, log.NewNopLogger(), metrics.NewNoOpMetrics())
		tmp.flushMetadata(bad, v+3, &ci2)
		h := &deferredHarness{t: t, k: 7, names: df.names, db: bad}
		ms := NewStore(bad, log.NewNopLogger(), metrics.NewNoOpMetrics())
		ms.celestiaDeferred = &deferredDriver{mode: DeferredAlways, every: 7, maxPending: 1 << 30, now: time.Now}
		for _, n := range h.names {
			ms.MountStoreWithDB(types.NewKVStoreKey(n), types.StoreTypeIAVL, nil)
		}
		require.Error(t, ms.LoadLatestVersion())
	}

	// handoff: an upstream multistore continues on the deferred DB
	require.NoError(t, df.ms.FlushDeferred())
	df.k = 0
	df.open(nil)
	require.Equal(t, ids[height], df.ms.LastCommitID())
	for i := 0; i < 25; i++ {
		b := randBlock(r, height+1, names, false)
		height++
		require.Equal(t, up.commit(b), df.commit(b), "handoff height %d", height)
	}
}

func filterLE(vs []int, v int64) []int {
	var out []int
	for _, x := range vs {
		if int64(x) <= v {
			out = append(out, x)
		}
	}
	return out
}

// requireNoLeakedNodes checks that the store's on-disk node keys are exactly
// the nodes of version v (2n-1 for n leaves) plus a root record when v's root
// is a reference or empty: nothing from older versions survives.
func requireNoLeakedNodes(t *testing.T, db dbm.DB, name string, st *iavl.Store, v int64) {
	t.Helper()
	pdb := dbm.NewPrefixDB(db, []byte("s/k:"+name+"/"))
	it, err := pdb.Iterator([]byte("s"), []byte("t"))
	require.NoError(t, err)
	nodes := 0
	for ; it.Valid(); it.Next() {
		nodes++
	}
	it.Close()
	imm, err := st.GetImmutable(v)
	require.NoError(t, err)
	leaves := 0
	itr := imm.Iterator(nil, nil)
	for ; itr.Valid(); itr.Next() {
		leaves++
	}
	itr.Close()
	want := 0
	if leaves > 0 {
		want = 2*leaves - 1
	}
	var rk [13]byte
	rk[0] = 's'
	binary.BigEndian.PutUint64(rk[1:], uint64(v))
	binary.BigEndian.PutUint32(rk[9:], 1)
	val, err := pdb.Get(rk[:])
	require.NoError(t, err)
	if val != nil && (len(val) == 0 || val[0] == 's') {
		want++ // empty root or reference root record
	}
	require.Equal(t, want, nodes, "store %s: node keys on disk at version %d", name, v)
}

package rootmulti

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"testing"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/cache"
	"cosmossdk.io/store/metrics"
	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/types"
)

// TestCelestiaKnobsMatchStock runs the same seeded workload with parallel
// commit and async pruning off and on, and requires a byte-identical trace of
// working hashes, commit IDs and store contents.
func TestCelestiaKnobsMatchStock(t *testing.T) {
	off := celestiaEquivalenceWorkload(t, false)
	on := celestiaEquivalenceWorkload(t, true)
	require.NotEmpty(t, off)
	if !bytes.Equal(off, on) {
		offLines, onLines := bytes.Split(off, []byte("\n")), bytes.Split(on, []byte("\n"))
		for i := range offLines {
			if i >= len(onLines) || !bytes.Equal(offLines[i], onLines[i]) {
				t.Fatalf("first difference at line %d:\noff: %s\non:  %s", i, offLines[i], onLines[i])
			}
		}
		t.Fatalf("traces differ in length: off %d lines, on %d lines", len(offLines), len(onLines))
	}
}

// celestiaEquivalenceWorkload commits a seeded random workload over 8 IAVL
// stores, a transient and a memory store, with the inter-block cache on, a
// store deletion upgrade and reloads, and returns one trace line per
// observable result.
func celestiaEquivalenceWorkload(t *testing.T, on bool) []byte {
	t.Helper()
	db := dbm.NewMemDB()
	iavlKeys := make([]*types.KVStoreKey, 8)
	for i := range iavlKeys {
		iavlKeys[i] = types.NewKVStoreKey(fmt.Sprintf("s%d", i))
	}
	tkey := types.NewTransientStoreKey("t")
	mkey := types.NewMemoryStoreKey("m")
	open := func(keys []*types.KVStoreKey) *Store {
		ms := NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
		ms.SetPruning(pruningtypes.NewCustomPruningOptions(5, 3))
		ms.SetInterBlockCache(cache.NewCommitKVStoreCacheManager(cache.DefaultCommitKVStoreCacheSize))
		ms.SetParallelCommit(on)
		ms.SetAsyncPruning(on)
		for _, k := range keys {
			ms.MountStoreWithDB(k, types.StoreTypeIAVL, nil)
		}
		ms.MountStoreWithDB(tkey, types.StoreTypeTransient, nil)
		ms.MountStoreWithDB(mkey, types.StoreTypeMemory, nil)
		return ms
	}

	var trace bytes.Buffer
	rng := rand.New(rand.NewSource(42))
	block := func(ms *Store, v int, live []*types.KVStoreKey) {
		cms := ms.CacheMultiStore()
		for _, k := range live {
			if rng.Intn(4) == 0 {
				continue // some stores are untouched in some blocks
			}
			st := cms.GetKVStore(k)
			for j, n := 0, rng.Intn(40); j < n; j++ {
				key := []byte(fmt.Sprintf("k%04d", rng.Intn(600)))
				if rng.Intn(5) == 0 {
					st.Delete(key)
					continue
				}
				val := make([]byte, 1+rng.Intn(64))
				rng.Read(val)
				st.Set(key, val)
			}
		}
		cms.GetKVStore(tkey).Set([]byte("t"), []byte{byte(v)})
		cms.GetKVStore(mkey).Set([]byte("m"), []byte{byte(v)})
		cms.Write()
		wh := ms.WorkingHash()
		id := ms.Commit()
		require.Equal(t, wh, id.Hash)
		fmt.Fprintf(&trace, "v=%d working=%X commit=%d/%X\n", v, wh, id.Version, id.Hash)
		for _, k := range live {
			cid := ms.GetCommitKVStore(k).LastCommitID()
			fmt.Fprintf(&trace, "  %s %d/%X\n", k.Name(), cid.Version, cid.Hash)
		}
	}
	dump := func(ms *Store, live []*types.KVStoreKey) {
		for _, k := range live {
			h := sha256.New()
			it := ms.GetKVStore(k).Iterator(nil, nil)
			for ; it.Valid(); it.Next() {
				h.Write(it.Key())
				h.Write(it.Value())
			}
			require.NoError(t, it.Close())
			fmt.Fprintf(&trace, "  contents %s %X\n", k.Name(), h.Sum(nil))
		}
	}

	ms := open(iavlKeys)
	require.NoError(t, ms.LoadLatestVersion())
	for v := 1; v <= 120; v++ {
		block(ms, v, iavlKeys)
	}
	dump(ms, iavlKeys)

	// Reopen with a store deletion: the next commit has a non-empty removal map.
	require.NoError(t, ms.Close())
	ms = open(iavlKeys)
	require.NoError(t, ms.LoadLatestVersionAndUpgrade(&types.StoreUpgrades{Deleted: []string{"s7"}}))
	fmt.Fprintf(&trace, "reload %d/%X\n", ms.LastCommitID().Version, ms.LastCommitID().Hash)
	live := iavlKeys[:7]
	for v := 121; v <= 200; v++ {
		block(ms, v, live)
	}
	dump(ms, live)

	require.NoError(t, ms.Close())
	ms = open(live)
	require.NoError(t, ms.LoadLatestVersion())
	fmt.Fprintf(&trace, "reload %d/%X\n", ms.LastCommitID().Version, ms.LastCommitID().Hash)
	dump(ms, live)

	require.NoError(t, ms.Close())
	return trace.Bytes()
}

// TestCelestiaPartialCommitRecovers simulates a kill during a parallel commit,
// which can leave any subset of stores (not only a prefix in key order) at the
// new version while the metadata is still at the previous one. The reload must
// roll those stores back so replaying the block gives the reference hash.
func TestCelestiaPartialCommitRecovers(t *testing.T) {
	keys := []types.StoreKey{testStoreKey1, testStoreKey2, testStoreKey3}
	write := func(ms *Store, v int64) {
		cms := ms.CacheMultiStore()
		for i, k := range keys {
			cms.GetKVStore(k).Set([]byte(fmt.Sprintf("k%d", v%7)), []byte(fmt.Sprintf("v%d-%d", v, i)))
		}
		cms.Write()
	}
	ref := newMultiStoreWithMounts(dbm.NewMemDB(), pruningtypes.NewPruningOptions(pruningtypes.PruningNothing))
	require.NoError(t, ref.LoadLatestVersion())
	var want types.CommitID
	for v := int64(1); v <= 5; v++ {
		write(ref, v)
		want = ref.Commit()
	}

	for _, subset := range [][]int{{1}, {2}, {0, 2}, {1, 2}, {0, 1, 2}} {
		t.Run(fmt.Sprint(subset), func(t *testing.T) {
			db := dbm.NewMemDB()
			ms := newMultiStoreWithMounts(db, pruningtypes.NewPruningOptions(pruningtypes.PruningNothing))
			ms.SetParallelCommit(true)
			require.NoError(t, ms.LoadLatestVersion())
			for v := int64(1); v <= 4; v++ {
				write(ms, v)
				ms.Commit()
			}
			// Block 5: only the subset reaches disk, then the process dies.
			write(ms, 5)
			for _, i := range subset {
				ms.GetCommitKVStore(keys[i]).Commit()
			}

			ms = newMultiStoreWithMounts(db, pruningtypes.NewPruningOptions(pruningtypes.PruningNothing))
			require.NoError(t, ms.LoadLatestVersion())
			require.Equal(t, int64(4), ms.LastCommitID().Version)
			write(ms, 5)
			require.Equal(t, want, ms.Commit())
		})
	}
}

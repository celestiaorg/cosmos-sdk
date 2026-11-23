package rootmulti

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/log"
	dbm "github.com/tendermint/tm-db"

	pruningtypes "github.com/cosmos/cosmos-sdk/pruning/types"
	"github.com/cosmos/cosmos-sdk/store/types"
)

// TestParallelCommitDeterminism checks that the CELESTIA_PARALLEL_STORES code
// paths (parallel cachemulti Write and parallel commitStores) produce the same
// CommitID sequence as the sequential paths. The parallel cachemulti Write is
// covered when the test runs with CELESTIA_PARALLEL_STORES=1.
func TestParallelCommitDeterminism(t *testing.T) {
	run := func(parallel bool) []types.CommitID {
		prev := parallelCommitEnabled
		parallelCommitEnabled = parallel
		defer func() { parallelCommitEnabled = prev }()

		ms := newMultiStoreWithMounts(dbm.NewMemDB(), pruningtypes.NewPruningOptions(pruningtypes.PruningNothing))
		require.NoError(t, ms.LoadLatestVersion())

		keys := []types.StoreKey{testStoreKey1, testStoreKey2, testStoreKey3}
		ids := make([]types.CommitID, 0, 50)
		for v := 1; v <= 50; v++ {
			cms := ms.CacheMultiStore()
			for i, key := range keys {
				kv := cms.GetKVStore(key)
				for j := 0; j < 20+i*v%7; j++ {
					kv.Set([]byte(fmt.Sprintf("k-%d-%d", j, (v*j)%13)), []byte(fmt.Sprintf("v-%d-%d-%d", v, i, j)))
				}
				if v%3 == 0 {
					kv.Delete([]byte(fmt.Sprintf("k-%d-%d", 1, v%13)))
				}
			}
			cms.Write()
			ids = append(ids, ms.Commit())
		}
		return ids
	}

	require.Equal(t, run(false), run(true))
}

// TestParallelCommitMixedStores checks that the parallel commit builds the same
// CommitInfo as the sequential one with transient (left out of the infos) and
// memory stores mounted and a store pending removal, across many versions.
func TestParallelCommitMixedStores(t *testing.T) {
	run := func(parallel bool) []types.CommitID {
		prev := parallelCommitEnabled
		parallelCommitEnabled = parallel
		defer func() { parallelCommitEnabled = prev }()

		ms := NewStore(dbm.NewMemDB(), log.NewNopLogger())
		ms.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningNothing))
		iavlKeys := make([]types.StoreKey, 6)
		for i := range iavlKeys {
			iavlKeys[i] = types.NewKVStoreKey(fmt.Sprintf("iavl%d", i))
			ms.MountStoreWithDB(iavlKeys[i], types.StoreTypeIAVL, nil)
		}
		tkey := types.NewTransientStoreKey("transient")
		mkey := types.NewMemoryStoreKey("memory")
		ms.MountStoreWithDB(tkey, types.StoreTypeTransient, nil)
		ms.MountStoreWithDB(mkey, types.StoreTypeMemory, nil)
		require.NoError(t, ms.LoadLatestVersion())

		var ids []types.CommitID
		for v := 1; v <= 20; v++ {
			cms := ms.CacheMultiStore()
			for i, key := range append(append([]types.StoreKey{}, iavlKeys...), tkey, mkey) {
				if v > 10 && key == iavlKeys[2] {
					continue // removed at v == 10
				}
				for j := 0; j < 5+(i*v)%11; j++ {
					cms.GetKVStore(key).Set([]byte(fmt.Sprintf("k%d", (j*v)%17)), []byte(fmt.Sprintf("v%d-%d", v, j)))
				}
			}
			cms.Write()
			if v == 10 {
				// a store deleted by an upgrade is committed one last time but
				// left out of the commit info
				ms.removalMap[iavlKeys[2]] = true
			}
			id := ms.Commit()
			ids = append(ids, id)
			require.Len(t, ms.lastCommitInfo.StoreInfos, map[bool]int{true: 7, false: 6}[v < 10]) // 6 IAVL + memory
		}
		return ids
	}
	require.Equal(t, run(false), run(true))
}

// TestIAVLCacheBudgetDoesNotChangeHashes checks that CELESTIA_IAVL_CACHE_NODES
// (node-local) only changes the IAVL cache size, never the commit hashes.
func TestIAVLCacheBudgetDoesNotChangeHashes(t *testing.T) {
	run := func(budget string) []types.CommitID {
		t.Setenv("CELESTIA_IAVL_CACHE_NODES", budget)
		ms := newMultiStoreWithMounts(dbm.NewMemDB(), pruningtypes.NewPruningOptions(pruningtypes.PruningNothing))
		require.NoError(t, ms.LoadLatestVersion())
		var ids []types.CommitID
		for v := 1; v <= 20; v++ {
			cms := ms.CacheMultiStore()
			for _, key := range []types.StoreKey{testStoreKey1, testStoreKey2, testStoreKey3} {
				for j := 0; j < 50; j++ {
					cms.GetKVStore(key).Set([]byte(fmt.Sprintf("k%d", (j*v)%97)), []byte(fmt.Sprintf("v%d", v)))
				}
			}
			cms.Write()
			ids = append(ids, ms.Commit())
		}
		return ids
	}
	stock := run("")
	require.Equal(t, stock, run("1"))         // clamped up to 10000 per store
	require.Equal(t, stock, run("30"))        // tiny budget
	require.Equal(t, stock, run("100000000")) // large budget
	require.Equal(t, stock, run("garbage"))   // ignored

	ms := newMultiStoreWithMounts(dbm.NewMemDB(), pruningtypes.NewPruningOptions(pruningtypes.PruningNothing))
	t.Setenv("CELESTIA_IAVL_CACHE_NODES", "")
	require.Equal(t, ms.iavlCacheSize, ms.effectiveIAVLCacheSize())
	t.Setenv("CELESTIA_IAVL_CACHE_NODES", "300000")
	require.Equal(t, 100000, ms.effectiveIAVLCacheSize())
}

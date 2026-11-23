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

// TestParallelCommitDeterminism checks that parallel commit (and the parallel
// Write of the branch returned by CacheMultiStore) gives the same CommitID
// sequence as the sequential paths.
func TestParallelCommitDeterminism(t *testing.T) {
	run := func(parallel bool) []types.CommitID {
		ms := newMultiStoreWithMounts(dbm.NewMemDB(), pruningtypes.NewPruningOptions(pruningtypes.PruningNothing))
		ms.SetParallelCommit(parallel)
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
		ms := NewStore(dbm.NewMemDB(), log.NewNopLogger())
		ms.SetParallelCommit(parallel)
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

// unsyncedListener records writes without locking, so -race reports any
// concurrent OnWrite calls.
type unsyncedListener struct {
	writes []string
}

func (l *unsyncedListener) OnWrite(sk types.StoreKey, key, value []byte, del bool) error {
	l.writes = append(l.writes, fmt.Sprintf("%s/%s=%s/%v", sk.Name(), key, value, del))
	return nil
}

// TestParallelCommitKeepsListenersSequential checks that with ADR-038
// listeners attached the branch from CacheMultiStore writes its stores one at
// a time, so a listener shared by several stores is never called concurrently
// (run with -race).
func TestParallelCommitKeepsListenersSequential(t *testing.T) {
	ms := newMultiStoreWithMounts(dbm.NewMemDB(), pruningtypes.NewPruningOptions(pruningtypes.PruningNothing))
	ms.SetParallelCommit(true)
	require.NoError(t, ms.LoadLatestVersion())
	l := &unsyncedListener{}
	keys := []types.StoreKey{testStoreKey1, testStoreKey2, testStoreKey3}
	for _, k := range keys {
		ms.AddListeners(k, []types.WriteListener{l})
	}
	for v := 0; v < 20; v++ {
		cms := ms.CacheMultiStore()
		for _, k := range keys {
			for j := 0; j < 50; j++ {
				cms.GetKVStore(k).Set([]byte(fmt.Sprintf("k%02d", j)), []byte{byte(v)})
			}
		}
		cms.Write()
		ms.Commit()
	}
	require.Len(t, l.writes, 20*3*50)
}

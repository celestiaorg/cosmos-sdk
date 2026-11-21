package rootmulti

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
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

package rootmulti

import (
	"fmt"
	"os"
	"testing"
	"time"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/types"
)

// TestCelestiaCommitDeterminism commits 300 versions with data in 3 stores
// (pruning everything) through the root-branch Write path and records the
// commit hashes. Run with and without CELESTIA_PARALLEL_STORES /
// CELESTIA_IAVL_ASYNC_PRUNING and compare CELESTIA_TEST_HASHES output files.
// With async pruning it also checks that old versions are eventually deleted
// and that the DB reopens at the latest version with the same hash and data.
func TestCelestiaCommitDeterminism(t *testing.T) {
	db := dbm.NewMemDB()
	ms := newMultiStoreWithMounts(db, pruningtypes.NewPruningOptions(pruningtypes.PruningEverything))
	require.NoError(t, ms.LoadLatestVersion())
	keys := []types.StoreKey{testStoreKey1, testStoreKey2, testStoreKey3}
	out := ""
	var last types.CommitID
	for v := int64(1); v <= 300; v++ {
		cms := ms.CacheMultiStore()
		for si, k := range keys {
			st := cms.GetKVStore(k)
			for j := int64(0); j < 20; j++ {
				st.Set([]byte(fmt.Sprintf("k%d-%d", si, (v*7+j*13)%500)), []byte(fmt.Sprintf("v%d-%d", v, j)))
			}
			if v%5 == 0 {
				st.Delete([]byte(fmt.Sprintf("k%d-%d", si, (v*3)%500)))
			}
		}
		cms.Write()
		wh := ms.WorkingHash()
		last = ms.Commit()
		require.Equal(t, wh, last.Hash)
		out += fmt.Sprintf("%d %X\n", last.Version, last.Hash)
	}
	if p := os.Getenv("CELESTIA_TEST_HASHES"); p != "" {
		require.NoError(t, os.WriteFile(p, []byte(out), 0o644))
	}
	// pruning everything keeps the last 2 versions (+ interval 10): version 100 must go.
	require.Eventually(t, func() bool {
		_, err := ms.CacheMultiStoreWithVersion(100)
		return err != nil
	}, 5*time.Second, 10*time.Millisecond)
	_, err := ms.CacheMultiStoreWithVersion(300)
	require.NoError(t, err)

	ms2 := newMultiStoreWithMounts(db, pruningtypes.NewPruningOptions(pruningtypes.PruningEverything))
	require.NoError(t, ms2.LoadLatestVersion())
	require.Equal(t, last, ms2.LastCommitID())
	require.Equal(t, []byte("v300-0"), ms2.GetKVStore(testStoreKey1).Get([]byte(fmt.Sprintf("k0-%d", (300*7)%500))))
}

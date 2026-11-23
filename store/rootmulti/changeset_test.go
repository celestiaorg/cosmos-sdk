package rootmulti

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/log"
	dbm "github.com/tendermint/tm-db"

	pruningtypes "github.com/cosmos/cosmos-sdk/pruning/types"
	"github.com/cosmos/cosmos-sdk/store/changeset"
	"github.com/cosmos/cosmos-sdk/store/types"
)

// The changeset log is written (and synced) before the commit info that makes
// a height the one a restart loads. If recording fails, the commit info must
// still be at the previous height, so the height is replayed and recorded
// again instead of leaving a permanent gap in the log.
func TestChangesetRecordedBeforeCommitInfo(t *testing.T) {
	for _, k := range []int64{0, 5} {
		t.Run(fmt.Sprintf("flushEvery=%d", k), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cs")
			rec, err := changeset.New(dir, changeset.Options{BlocksPerFile: 1})
			require.NoError(t, err)
			db := dbm.NewMemDB()
			rs := NewStore(db, log.NewNopLogger())
			rs.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningEverything))
			rs.SetChangesetRecorder(rec)
			if k > 0 {
				require.NoError(t, rs.SetDeferredPersistence(DeferredSync, k, 0))
			}
			rs.MountStoreWithDB(defKey1, types.StoreTypeIAVL, nil)
			require.NoError(t, rs.LoadLatestVersion())

			for h := int64(1); h <= 9; h++ {
				rs.GetKVStore(defKey1).Set([]byte(fmt.Sprintf("k%d", h)), []byte("v"))
				require.Equal(t, h, rs.Commit().Version)
			}
			before := GetLatestVersion(db)
			// the next block needs a new file (BlocksPerFile=1): make that fail
			require.NoError(t, os.RemoveAll(dir))
			rs.GetKVStore(defKey1).Set([]byte("k10"), []byte("v"))
			require.Panics(t, func() { rs.Commit() })
			require.Equal(t, before, GetLatestVersion(db), "commit info of the unrecorded height reached the disk")
		})
	}
}

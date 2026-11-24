package rootmulti

import (
	"fmt"
	"math/rand"
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

// Recording must never change what the store computes: the same block
// sequence (writes, deletes, same-value rewrites, a store upgrade with
// migration writes) yields identical CommitIDs at every height with the
// recorder on and off, with deferred persistence on and off. The recorded app
// hash of every block is the commit hash.
func TestChangesetRecordingDoesNotChangeCommitHashes(t *testing.T) {
	const nBlocks, upgradeAt = 60, 30
	run := func(t *testing.T, rec *changeset.Recorder, k int64) []types.CommitID {
		rs := NewStore(dbm.NewMemDB(), log.NewNopLogger())
		rs.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningEverything))
		rs.SetChangesetRecorder(rec)
		if k > 0 {
			require.NoError(t, rs.SetDeferredPersistence(DeferredSync, k, 0))
		}
		keys := []*types.KVStoreKey{defKey1, defKey2}
		for _, key := range keys {
			rs.MountStoreWithDB(key, types.StoreTypeIAVL, nil)
		}
		require.NoError(t, rs.LoadLatestVersion())
		rng := rand.New(rand.NewSource(7))
		var ids []types.CommitID
		for h := int64(1); h <= nBlocks; h++ {
			deliver := rs.CacheMultiStore()
			for i := 0; i < 1+rng.Intn(20); i++ {
				kv := deliver.GetKVStore(keys[rng.Intn(len(keys))])
				key := []byte(fmt.Sprintf("k%03d", rng.Intn(150)))
				switch r := rng.Intn(10); {
				case r < 6:
					kv.Set(key, []byte(fmt.Sprintf("v%d", rng.Int63())))
				case r < 8:
					if v := kv.Get(key); v != nil {
						kv.Set(key, v)
					}
				default:
					kv.Delete(key)
				}
			}
			if h == upgradeAt { // baseapp.Commit with an app-version change
				require.NoError(t, rs.FlushDeferred())
				rs.ForceFlushNextCommit()
				deliver.Write()
				rs.MountStoreWithDB(defKey3, types.StoreTypeIAVL, nil)
				require.NoError(t, rs.LoadLatestVersionAndUpgrade(&types.StoreUpgrades{Added: []string{defKey3.Name()}}))
				keys = append(keys, defKey3)
				mig := rs.CacheMultiStore()
				mig.GetKVStore(defKey3).Set([]byte("genesis"), []byte("x"))
				mig.Write()
			} else {
				deliver.Write()
			}
			id := rs.Commit()
			require.Equal(t, h, id.Version)
			ids = append(ids, id)
		}
		return ids
	}
	for _, k := range []int64{0, 7} {
		t.Run(fmt.Sprintf("flushEvery=%d", k), func(t *testing.T) {
			off := run(t, nil, k)
			dir := t.TempDir()
			rec, err := changeset.New(dir, changeset.Options{BlocksPerFile: 25, StoreInfos: true, Zstd: true})
			require.NoError(t, err)
			on := run(t, rec, k)
			require.NoError(t, rec.Close())
			require.Equal(t, off, on, "recording changed a commit hash")

			var blocks []*changeset.Block
			require.NoError(t, changeset.Stream(dir, 0, func(b *changeset.Block) error {
				blocks = append(blocks, b)
				return nil
			}))
			require.Len(t, blocks, nBlocks)
			for i, b := range blocks {
				require.Equal(t, on[i].Version, b.Height)
				require.Equal(t, on[i].Hash, b.AppHash, "height %d", b.Height)
			}
			require.NotNil(t, blocks[upgradeAt-1].Upgrade)
		})
	}
}

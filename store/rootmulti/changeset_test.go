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

// A store added at an upgrade height h whose tree was saved at h before a
// crash (commit info still at h-1) is not committed again when h is
// re-executed. The ops it received during the re-execution are what its
// saved version h holds, so they belong to block h; they also stay in the
// working tree and are saved with h+1, so block h+1 records them again.
func TestChangesetStoreSavedBeforeCrashAtUpgrade(t *testing.T) {
	const upgradeAt = int64(5)
	dir := t.TempDir()
	db := dbm.NewMemDB()
	open := func(rec *changeset.Recorder) *Store {
		rs := NewStore(db, log.NewNopLogger())
		rs.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningNothing))
		rs.SetChangesetRecorder(rec)
		rs.MountStoreWithDB(defKey1, types.StoreTypeIAVL, nil)
		require.NoError(t, rs.LoadLatestVersion())
		return rs
	}
	xOps := func(h int64) []changeset.Op {
		return []changeset.Op{
			{Kind: changeset.OpSet, Key: []byte(fmt.Sprintf("x%d", h)), Value: []byte("v")},
			{Kind: changeset.OpSet, Key: []byte("hot"), Value: []byte(fmt.Sprint(h))},
		}
	}
	// block h up to (excluding) Commit: writes, and the upgrade at upgradeAt
	block := func(rs *Store, h int64) {
		rs.GetKVStore(defKey1).Set([]byte(fmt.Sprintf("k%d", h)), []byte("v"))
		if h == upgradeAt {
			rs.MountStoreWithDB(defKey3, types.StoreTypeIAVL, nil)
			require.NoError(t, rs.LoadLatestVersionAndUpgrade(&types.StoreUpgrades{Added: []string{defKey3.Name()}}))
		}
		if h >= upgradeAt {
			for _, op := range xOps(h) {
				rs.GetKVStore(defKey3).Set(op.Key, op.Value)
			}
		}
	}

	rec1, err := changeset.New(dir, changeset.Options{BlocksPerFile: 100})
	require.NoError(t, err)
	rs := open(rec1)
	for h := int64(1); h < upgradeAt; h++ {
		block(rs, h)
		rs.Commit()
	}
	require.NoError(t, rec1.Close())

	// the crashed run: the added store saved upgradeAt, nothing else did
	rs = open(nil)
	block(rs, upgradeAt)
	rs.IAVLStore(defKey3).Commit()
	require.Equal(t, upgradeAt-1, GetLatestVersion(db))

	// restart: upgradeAt is executed again, then two more blocks
	rec2, err := changeset.New(dir, changeset.Options{BlocksPerFile: 100})
	require.NoError(t, err)
	rs = open(rec2)
	for h := upgradeAt; h <= upgradeAt+2; h++ {
		block(rs, h)
		require.Equal(t, h, rs.Commit().Version)
	}
	require.NoError(t, rec2.Close())

	x := map[int64][]changeset.Op{}
	require.NoError(t, changeset.Stream(dir, 0, func(b *changeset.Block) error {
		for _, s := range b.Stores {
			if s.Name == defKey3.Name() {
				x[b.Height] = s.Ops
			}
		}
		return nil
	}))
	require.Equal(t, xOps(upgradeAt), x[upgradeAt])
	require.Equal(t, append(xOps(upgradeAt), xOps(upgradeAt+1)...), x[upgradeAt+1])
	require.Equal(t, xOps(upgradeAt+2), x[upgradeAt+2])
}

// A store type the format cannot express is never recorded as another type.
func TestChangesetStoreTypes(t *testing.T) {
	for _, st := range []types.StoreType{types.StoreTypeIAVL, types.StoreTypeMemory, types.StoreTypeTransient, types.StoreTypeDB} {
		c, ok := storeTypeToChangeset(st)
		require.True(t, ok, st)
		back, ok := ChangesetToStoreType(c)
		require.True(t, ok, st)
		require.Equal(t, st, back)
	}
	_, ok := storeTypeToChangeset(types.StoreTypeMulti)
	require.False(t, ok)
	_, ok = ChangesetToStoreType(0)
	require.False(t, ok)
}

// ChangesetRollback of a store the Commit created removes exactly what that
// Commit wrote. Its DB prefix is deleted only for a valid name (never '/');
// for a store named bank/n, whose prefix s/k:bank/n/ lies inside bank's
// namespace, the rollback is refused and nothing is deleted.
func TestChangesetRollbackCreatedStoreKeepsNeighbours(t *testing.T) {
	for _, name := range []string{"newstore", "bank/n"} {
		t.Run(name, func(t *testing.T) {
			db := dbm.NewMemDB()
			bank := types.NewKVStoreKey("bank")
			open := func() *Store {
				rs := NewStore(db, log.NewNopLogger())
				rs.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningNothing))
				rs.MountStoreWithDB(bank, types.StoreTypeIAVL, nil)
				require.NoError(t, rs.LoadLatestVersion())
				return rs
			}
			keys := func() map[string]bool {
				out := map[string]bool{}
				it, err := db.Iterator(nil, nil)
				require.NoError(t, err)
				defer it.Close()
				for ; it.Valid(); it.Next() {
					out[string(it.Key())] = true
				}
				return out
			}
			rs := open()
			for h := 1; h <= 3; h++ {
				for i := 0; i < 200; i++ {
					rs.GetKVStore(bank).Set([]byte(fmt.Sprintf("k%d-%d", h, i)), []byte("v"))
				}
				rs.Commit()
			}
			before := keys()

			created := types.NewKVStoreKey(name)
			rs.MountStoreWithDB(created, types.StoreTypeIAVL, nil)
			require.NoError(t, rs.LoadLatestVersionAndUpgrade(&types.StoreUpgrades{Added: []string{name}}))
			rs.GetKVStore(created).Set([]byte("x"), []byte("y"))
			rs.GetKVStore(bank).Set([]byte("k4"), []byte("v"))
			p, err := rs.ChangesetRollbackPoint()
			require.NoError(t, err)
			require.EqualValues(t, 4, rs.Commit().Version)
			committed := keys()
			err = rs.ChangesetRollback(p)
			if name == "bank/n" {
				require.ErrorContains(t, err, "cannot be rolled back")
				require.Equal(t, committed, keys(), "a refused rollback wrote")
				return
			}
			require.NoError(t, err)
			require.Equal(t, before, keys(), "the rollback must leave exactly the keys of height 3")
			rs = open()
			require.EqualValues(t, 3, rs.LastCommitID().Version)
			n := 0
			iter := rs.GetKVStore(bank).Iterator(nil, nil)
			for ; iter.Valid(); iter.Next() {
				n++
			}
			iter.Close()
			require.Equal(t, 600, n)
		})
	}
}

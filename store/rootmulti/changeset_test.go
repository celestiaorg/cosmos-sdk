package rootmulti

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/changeset"
	"cosmossdk.io/store/metrics"
	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/types"
)

// csProcess is one process of the test chain: the stores it mounts and the
// store upgrade its loader runs (x/upgrade UpgradeStoreLoader semantics).
type csProcess struct {
	start    int64
	iavl     []string
	upgrades *types.StoreUpgrades
}

var csProcesses = []csProcess{
	{start: 1, iavl: []string{"acc", "bank", "gone", "params", "staking"}},
	{start: 17, iavl: []string{"acc", "bank", "gone", "params", "staking"}}, // plain restart
	{start: 30, iavl: []string{"acc", "bank", "gone", "newmod", "params2", "staking"}, upgrades: &types.StoreUpgrades{
		Added: []string{"newmod"}, Deleted: []string{"gone", "unmounted"},
		Renamed: []types.StoreRename{{OldKey: "params", NewKey: "params2"}},
	}},
}

const csBlocks = 60

// runCSChain executes the test chain and returns the CommitID of every
// height. dir == "" runs without a recorder; flushEvery > 0 runs with
// deferred persistence ("always").
func runCSChain(t *testing.T, dir string, flushEvery int64) map[int64]types.CommitID {
	t.Helper()
	db := dbm.NewMemDB()
	rng := rand.New(rand.NewSource(7)) //nolint:gosec // deterministic test workload
	ids := map[int64]types.CommitID{}
	var (
		rs   *Store
		keys map[string]types.StoreKey
		live []string
	)
	for h := int64(1); h <= csBlocks; h++ {
		for _, p := range csProcesses {
			if p.start != h {
				continue
			}
			if rs != nil {
				require.NoError(t, rs.Close())
			}
			rs = NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
			rs.SetChangesetRecorder(nil) // independent of CELESTIA_CHANGESET_OUT
			if dir != "" {
				rec, err := changeset.New(dir, changeset.Options{BlocksPerFile: 11, StoreInfos: true, Zstd: true})
				require.NoError(t, err)
				rs.SetChangesetRecorder(rec)
			}
			rs.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningEverything))
			if flushEvery > 0 {
				require.NoError(t, rs.SetDeferredPersistence(DeferredAlways, flushEvery))
			}
			keys = map[string]types.StoreKey{}
			live = nil
			for _, n := range p.iavl {
				keys[n] = types.NewKVStoreKey(n)
				rs.MountStoreWithDB(keys[n], types.StoreTypeIAVL, nil)
				if !p.upgrades.IsDeleted(n) {
					live = append(live, n)
				}
			}
			keys["mem"] = types.NewMemoryStoreKey("mem")
			rs.MountStoreWithDB(keys["mem"], types.StoreTypeMemory, nil)
			keys["tr"] = types.NewTransientStoreKey("tr")
			rs.MountStoreWithDB(keys["tr"], types.StoreTypeTransient, nil)
			if p.upgrades != nil {
				require.NoError(t, rs.LoadLatestVersionAndUpgrade(p.upgrades))
			} else {
				require.NoError(t, rs.LoadLatestVersion())
			}
		}
		block := rs.CacheMultiStore()
		for tx := 0; tx < 1+rng.Intn(5); tx++ {
			txs := block.CacheMultiStore()
			for i := 0; i < 1+rng.Intn(10); i++ {
				kv := txs.GetKVStore(keys[live[rng.Intn(len(live))]])
				key := []byte(fmt.Sprintf("k%03d", rng.Intn(200)))
				switch r := rng.Intn(10); {
				case r < 6:
					kv.Set(key, []byte(fmt.Sprintf("v%d", rng.Int63())))
				case r < 8:
					kv.Delete(key)
				default: // same-value rewrite
					if v := kv.Get(key); v != nil {
						kv.Set(key, v)
					}
				}
			}
			txs.GetKVStore(keys["mem"]).Set([]byte("m"), []byte(fmt.Sprint(rng.Int())))
			txs.GetKVStore(keys["tr"]).Set([]byte("t"), []byte(fmt.Sprint(rng.Int())))
			if rng.Intn(4) != 0 { // some txs fail and are dropped
				txs.Write()
			}
		}
		block.Write()
		wh := rs.WorkingHash()
		id := rs.Commit()
		require.Equal(t, h, id.Version)
		require.Equal(t, wh, id.Hash)
		ids[h] = id
	}
	require.NoError(t, rs.Close())
	return ids
}

// Recording must not change any commit: the chain run with the recorder on
// and off yields the same CommitID at every height, with and without
// deferred persistence, and the log holds every height with that app hash.
func TestChangesetRecordingIsDeterministic(t *testing.T) {
	for _, k := range []int64{0, 7} {
		t.Run(fmt.Sprintf("flushEvery=%d", k), func(t *testing.T) {
			off := runCSChain(t, "", k)
			dir := t.TempDir()
			on := runCSChain(t, dir, k)
			require.Len(t, on, csBlocks)
			for h := int64(1); h <= csBlocks; h++ {
				require.Equal(t, off[h], on[h], "height %d", h)
			}

			var got []int64
			require.NoError(t, changeset.Stream(dir, 0, func(b *changeset.Block) error {
				require.True(t, b.StoreV1)
				require.Equal(t, on[b.Height].Hash, b.AppHash, "height %d", b.Height)
				for _, si := range b.StoreInfos {
					require.NotEqual(t, "mem", si.Name)
					require.NotEqual(t, "tr", si.Name)
				}
				if b.Height == 30 {
					require.True(t, b.UpgradeAtLoad)
					require.Equal(t, &changeset.Upgrade{
						Added: []string{"newmod"}, Deleted: []string{"gone"}, // "unmounted" has no effect
						Renamed: []changeset.Rename{{Old: "params", New: "params2"}},
					}, b.Upgrade)
				} else {
					require.Nil(t, b.Upgrade, "height %d", b.Height)
				}
				got = append(got, b.Height)
				return nil
			}))
			require.Len(t, got, csBlocks)
			require.Equal(t, int64(1), got[0])
			require.Equal(t, int64(csBlocks), got[len(got)-1])
		})
	}
}

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
			rs := NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
			rs.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningEverything))
			rs.SetChangesetRecorder(rec)
			if k > 0 {
				// flushes at 5 and 10 (and the first commit)
				require.NoError(t, rs.SetDeferredPersistence(DeferredAlways, k))
			}
			key := types.NewKVStoreKey("store1")
			rs.MountStoreWithDB(key, types.StoreTypeIAVL, nil)
			require.NoError(t, rs.LoadLatestVersion())

			for h := int64(1); h <= 9; h++ {
				rs.GetKVStore(key).Set([]byte(fmt.Sprintf("k%d", h)), []byte("v"))
				require.Equal(t, h, rs.Commit().Version)
			}
			before := GetLatestVersion(db)
			// the next block needs a new file (BlocksPerFile=1): make that fail
			require.NoError(t, os.RemoveAll(dir))
			rs.GetKVStore(key).Set([]byte("k10"), []byte("v"))
			require.Panics(t, func() { rs.Commit() })
			require.Equal(t, before, GetLatestVersion(db), "commit info of the unrecorded height reached the disk")
		})
	}
}

// A block that commits after Close (the shutdown racing the in-flight block)
// is still recorded, in its own file.
func TestChangesetBlockAfterCloseIsRecorded(t *testing.T) {
	dir := t.TempDir()
	rec, err := changeset.New(dir, changeset.Options{BlocksPerFile: 100, Zstd: true})
	require.NoError(t, err)
	rs := NewStore(dbm.NewMemDB(), log.NewNopLogger(), metrics.NewNoOpMetrics())
	rs.SetChangesetRecorder(rec)
	key := types.NewKVStoreKey("store1")
	rs.MountStoreWithDB(key, types.StoreTypeIAVL, nil)
	require.NoError(t, rs.LoadLatestVersion())
	rs.GetKVStore(key).Set([]byte("a"), []byte("1"))
	rs.Commit()
	require.NoError(t, rec.Close()) // what rs.Close does first on shutdown
	rs.GetKVStore(key).Set([]byte("b"), []byte("2"))
	id := rs.Commit()

	var got []*changeset.Block
	require.NoError(t, changeset.Stream(dir, 0, func(b *changeset.Block) error { got = append(got, b); return nil }))
	require.Len(t, got, 2)
	require.Equal(t, id.Hash, got[1].AppHash)
	require.Equal(t, []changeset.StoreOps{{Name: "store1", Ops: []changeset.Op{{Kind: changeset.OpSet, Key: []byte("b"), Value: []byte("2")}}}}, got[1].Stores)
	files, err := changeset.ListFiles(dir)
	require.NoError(t, err)
	require.Len(t, files, 2)
}

// With recording off (the default) no store keeps a recorder or buffers ops.
func TestChangesetOffByDefault(t *testing.T) {
	if os.Getenv(changeset.EnvOut) != "" {
		t.Skip(changeset.EnvOut + " is set")
	}
	rs := NewStore(dbm.NewMemDB(), log.NewNopLogger(), metrics.NewNoOpMetrics())
	require.Nil(t, rs.ChangesetRecorder())
	key := types.NewKVStoreKey("store1")
	rs.MountStoreWithDB(key, types.StoreTypeIAVL, nil)
	require.NoError(t, rs.LoadLatestVersion())
	st := rs.GetCommitKVStore(key)
	allocs := testing.AllocsPerRun(100, func() { st.Set([]byte("k"), []byte("v")) })
	// the same store with a recorder attached copies every op
	rec, err := changeset.New(t.TempDir(), changeset.Options{})
	require.NoError(t, err)
	rs.SetChangesetRecorder(rec)
	rs.attachChangeset(key, st)
	allocsOn := testing.AllocsPerRun(100, func() { st.Set([]byte("k"), []byte("v")) })
	require.Greater(t, allocsOn, allocs, "with recording off, Set skips the op copy")
}

package applier_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/cache"
	"cosmossdk.io/store/changeset"
	"cosmossdk.io/store/changeset/applier"
	"cosmossdk.io/store/metrics"
	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/rootmulti"
	"cosmossdk.io/store/types"
)

// The source chain emulates how a v0.50 celestia child uses the multistore:
// one process per segment (every child switch / restart is a fresh rootmulti
// on the same DB, with its own recorder and the node's inter-block cache and
// fast-node index), x/upgrade's UpgradeStoreLoader (LoadLatestVersionAndUpgrade
// when upgradeHeight == latest+1), a finalizeBlockState cache multistore per
// block with nested per-tx caches (written only for successful txs), the
// sorted cachekv flush into the IAVL trees before WorkingHash, then Commit.

type segment struct {
	start    int64 // first block executed by this process
	iavl     []string
	mem      []string
	trans    []string
	upgrades *types.StoreUpgrades // passed to the loader at start (x/upgrade at upgradeHeight == start)
}

// Process layout of the test chain (heights are the first block of each process).
var segments = []segment{
	{
		start: 1, iavl: []string{"acc", "bank", "blobstream", "distribution", "params", "slashing", "staking", "upgrade"},
		mem: []string{"mem_capability"}, trans: []string{"transient_params"},
	},
	// plain restart, same set (mid-file for the reader: new recorder file)
	{
		start: 23, iavl: []string{"acc", "bank", "blobstream", "distribution", "params", "slashing", "staking", "upgrade"},
		mem: []string{"mem_capability"}, trans: []string{"transient_params"},
	},
	// app-version change with store upgrade: add 2, delete the non-empty
	// blobstream store, rename the non-empty params store, and delete an
	// unmounted name (no-op, as v4.1.0's crisis/blobstream)
	{
		start: 40, iavl: []string{"acc", "bank", "blobstream", "circuit", "consensus", "distribution", "params2", "slashing", "staking", "upgrade"},
		mem: []string{"mem_capability"}, trans: []string{"transient_params"},
		upgrades: &types.StoreUpgrades{
			Added: []string{"circuit", "consensus"}, Deleted: []string{"blobstream", "crisis"},
			Renamed: []types.StoreRename{{OldKey: "params", NewKey: "params2"}},
		},
	},
	// new process with a different memory/transient set and no upgrade record
	{
		start: 70, iavl: []string{"acc", "bank", "circuit", "consensus", "distribution", "params2", "slashing", "staking", "upgrade"},
		mem: []string{"mem_capability", "mem_other"},
	},
	// second upgrade (add only), like v9 -> v10 (extra, valaddr)
	{
		start: 95, iavl: []string{"acc", "bank", "circuit", "consensus", "distribution", "extra", "params2", "slashing", "staking", "upgrade"},
		mem: []string{"mem_capability"}, upgrades: &types.StoreUpgrades{Added: []string{"extra"}},
	},
}

const nBlocks = 120

type chain struct {
	t          *testing.T
	db         dbm.DB
	dir        string
	perFile    int64
	flushEvery int64
	rs         *rootmulti.Store
	rec        *changeset.Recorder
	keys       map[string]types.StoreKey
	iavlNames  []string
	rng        *rand.Rand
	seed       int64
	// interruptedAt: the commit of this height was interrupted once, so the
	// retry skips stores that already saved it (WorkingHash then differs)
	interruptedAt int64
	hashes        map[int64][]byte
	snaps         map[int64]dbm.DB
	snapAt        map[int64]bool
}

func newChain(t *testing.T, dir string, perFile, flushEvery, seed int64, snapAt ...int64) *chain {
	c := &chain{
		t: t, db: dbm.NewMemDB(), dir: dir, perFile: perFile, flushEvery: flushEvery,
		seed:   seed,
		hashes: map[int64][]byte{}, snaps: map[int64]dbm.DB{}, snapAt: map[int64]bool{},
	}
	for _, h := range snapAt {
		c.snapAt[h] = true
	}
	return c
}

// startProcess emulates a (re)started child: fresh rootmulti, mounts, store loader.
func (c *chain) startProcess(s segment) {
	if c.rs != nil {
		require.NoError(c.t, c.rs.Close()) // App.Close: flushes, closes the recorder
	}
	rec, err := changeset.New(c.dir, changeset.Options{BlocksPerFile: c.perFile, StoreInfos: true, Zstd: true})
	require.NoError(c.t, err)
	rs := rootmulti.NewStore(c.db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	rs.SetChangesetRecorder(rec)
	rs.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningEverything))
	rs.SetInterBlockCache(cache.NewCommitKVStoreCacheManager(cache.DefaultCommitKVStoreCacheSize))
	if c.flushEvery > 0 {
		require.NoError(c.t, rs.SetDeferredPersistence(rootmulti.DeferredAlways, c.flushEvery))
	}
	c.keys = map[string]types.StoreKey{}
	for _, n := range s.iavl {
		k := types.NewKVStoreKey(n)
		c.keys[n] = k
		rs.MountStoreWithDB(k, types.StoreTypeIAVL, nil)
	}
	for _, n := range s.mem {
		k := types.NewMemoryStoreKey(n)
		c.keys[n] = k
		rs.MountStoreWithDB(k, types.StoreTypeMemory, nil)
	}
	for _, n := range s.trans {
		k := types.NewTransientStoreKey(n)
		c.keys[n] = k
		rs.MountStoreWithDB(k, types.StoreTypeTransient, nil)
	}
	// UpgradeStoreLoader: upgradeHeight == latest+1 with a non-empty upgrade
	if s.upgrades != nil && rootmulti.GetLatestVersion(c.db)+1 == s.start {
		require.NoError(c.t, rs.LoadLatestVersionAndUpgrade(s.upgrades))
	} else {
		require.NoError(c.t, rs.LoadLatestVersion())
	}
	c.iavlNames = nil
	for _, n := range s.iavl {
		if s.upgrades == nil || !s.upgrades.IsDeleted(n) {
			c.iavlNames = append(c.iavlNames, n)
		}
	}
	c.rs, c.rec = rs, rec
}

func (c *chain) randomTx(ms types.MultiStore) {
	n := 1 + c.rng.Intn(12)
	for i := 0; i < n; i++ {
		name := c.iavlNames[c.rng.Intn(len(c.iavlNames))]
		kv := ms.GetKVStore(c.keys[name])
		key := []byte(fmt.Sprintf("k%04d", c.rng.Intn(400)))
		switch r := c.rng.Intn(100); {
		case r < 55:
			kv.Set(key, []byte(fmt.Sprintf("v%d", c.rng.Int63())))
		case r < 70: // same-value rewrite (still bumps the IAVL node version)
			if v := kv.Get(key); v != nil {
				kv.Set(key, v)
			} else {
				kv.Set(key, []byte("x"))
			}
		case r < 90:
			kv.Delete(key)
		default: // delete of a key that never exists
			kv.Delete([]byte(fmt.Sprintf("missing%d", c.rng.Intn(1000))))
		}
	}
	for name, k := range c.keys {
		switch k.(type) {
		case *types.MemoryStoreKey, *types.TransientStoreKey:
			ms.GetKVStore(k).Set([]byte("m"+name), []byte(fmt.Sprint(c.rng.Int())))
		}
	}
}

func (c *chain) block(h int64, s segment) types.CommitID {
	fb := c.rs.CacheMultiStore() // finalizeBlockState
	c.writeBlock(fb, h, s)
	fb.Write() // workingHash: finalizeBlockState.ms.Write()
	wh := c.rs.WorkingHash()
	id := c.rs.Commit()
	require.Equal(c.t, h, id.Version)
	if h != c.interruptedAt {
		require.Equal(c.t, wh, id.Hash)
	}
	c.hashes[h] = id.Hash
	if c.snapAt[h] {
		require.NoError(c.t, c.rs.FlushDeferred())
		c.snaps[h] = copyDB(c.t, c.db)
	}
	return id
}

// writeBlock executes block h into fb; the same h always writes the same.
func (c *chain) writeBlock(fb types.CacheMultiStore, h int64, s segment) {
	c.rng = rand.New(rand.NewSource(c.seed*1_000_003 + h)) //nolint:gosec // deterministic test workload
	if s.upgrades != nil && h == s.start {
		// PreBlock: x/upgrade runs the handler (migrations, InitGenesis of new modules)
		for _, n := range s.upgrades.Added {
			kv := fb.GetKVStore(c.keys[n])
			for i := 0; i < 30; i++ {
				kv.Set([]byte(fmt.Sprintf("genesis%02d", i)), []byte(n))
			}
		}
		fb.GetKVStore(c.keys["upgrade"]).Set([]byte("done/"+fmt.Sprint(h)), []byte{1})
		if _, ok := c.keys["params2"]; ok {
			// migration touching the renamed store
			fb.GetKVStore(c.keys["params2"]).Set([]byte("k0001"), []byte("migrated"))
		}
	}
	if h%9 != 0 { // every 9th block is empty
		// BeginBlock: hot per-validator keys rewritten every block
		dist := fb.GetKVStore(c.keys["distribution"])
		for v := 0; v < 20; v++ {
			dist.Set([]byte(fmt.Sprintf("val%03d/rewards", v)), []byte(fmt.Sprintf("%d", h*int64(v+1))))
		}
		if h%10 == 3 { // a batch of new accounts (sorted inserts)
			acc := fb.GetKVStore(c.keys["acc"])
			for i := 0; i < 15; i++ {
				acc.Set([]byte(fmt.Sprintf("new/%d/%d", h, c.rng.Intn(1e9))), []byte("account"))
			}
		}
		for i := 0; i < 1+c.rng.Intn(6); i++ {
			tx := fb.CacheMultiStore()
			c.randomTx(tx)
			if c.rng.Intn(5) != 0 { // 20% of txs fail and are not written
				tx.Write()
			}
		}
	}
}

func (c *chain) run(from, to int64) {
	for h := from; h <= to; h++ {
		for _, s := range segments {
			if s.start == h {
				c.startProcess(s)
			}
		}
		c.block(h, segmentAt(h))
	}
}

func (c *chain) finish() {
	require.NoError(c.t, c.rs.Close())
}

func segmentAt(h int64) segment {
	var cur segment
	for _, s := range segments {
		if s.start <= h {
			cur = s
		}
	}
	return cur
}

func (c *chain) expected(h int64) ([]byte, bool, error) {
	v, ok := c.hashes[h]
	return v, ok, nil
}

func copyDB(t *testing.T, src dbm.DB) dbm.DB {
	dst := dbm.NewMemDB()
	it, err := src.Iterator(nil, nil)
	require.NoError(t, err)
	defer it.Close()
	for ; it.Valid(); it.Next() {
		require.NoError(t, dst.Set(append([]byte{}, it.Key()...), append([]byte{}, it.Value()...)))
	}
	return dst
}

func readAll(t *testing.T, dir string) []*changeset.Block {
	var out []*changeset.Block
	require.NoError(t, changeset.Stream(dir, 0, func(b *changeset.Block) error {
		out = append(out, b)
		return nil
	}))
	return out
}

func record(t *testing.T, flushEvery, perFile int64, snapAt ...int64) (string, *chain) {
	dir := t.TempDir()
	c := newChain(t, dir, perFile, flushEvery, 42, snapAt...)
	c.run(1, nBlocks)
	c.finish()
	return dir, c
}

func apply(t *testing.T, db dbm.DB, dir string, flushEvery int64, exp func(int64) ([]byte, bool, error), parallel bool) (*applier.Applier, error) {
	opts := applier.DefaultOptions()
	opts.DeferredFlushInterval = flushEvery
	opts.Parallel = parallel
	opts.AllowUnverified = exp == nil // recorded hashes only
	opts.Logf = t.Logf
	return applier.Run(db, dir, 0, opts, exp, nil)
}

func TestRecordedStructure(t *testing.T) {
	dir, _ := record(t, 0, 25)
	blocks := readAll(t, dir)
	require.Len(t, blocks, nBlocks)
	for _, b := range blocks {
		require.True(t, b.StoreV1)
		if b.Upgrade != nil {
			require.True(t, b.UpgradeAtLoad)
		}
	}
	u := blocks[39].Upgrade
	require.NotNil(t, u)
	require.Equal(t, []string{"circuit", "consensus"}, u.Added)
	require.Equal(t, []string{"blobstream"}, u.Deleted) // "crisis" was not mounted: filtered
	require.Equal(t, []changeset.Rename{{Old: "params", New: "params2"}}, u.Renamed)
	require.NotNil(t, blocks[94].Upgrade)
	for i, b := range blocks {
		if i != 39 && i != 94 {
			require.Nil(t, b.Upgrade, "height %d", b.Height)
		}
	}
	// migration ops are recorded: blobstream deletes, params -> params2 moves (first ops of the stores)
	byName := map[string][]changeset.Op{}
	for _, s := range blocks[39].Stores {
		byName[s.Name] = s.Ops
	}
	require.NotEmpty(t, byName["blobstream"])
	for _, op := range byName["blobstream"] {
		require.Equal(t, changeset.OpDelete, op.Kind)
	}
	require.NotEmpty(t, byName["params"])
	require.NotEmpty(t, byName["params2"])
	require.True(t, len(byName["params2"]) > len(byName["params"])) // moves + migration write
	require.Equal(t, changeset.OpSet, byName["params2"][0].Kind)
	// memory stores are not in the commit info (v0.50)
	for _, si := range blocks[50].StoreInfos {
		require.NotContains(t, si.Name, "mem_")
	}
	// one file per process start, plus rotation
	files, err := changeset.ListFiles(dir)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(files), 6)
	// manifests: the file starting at 40 holds the pre-upgrade set of the new process
	var m40 []string
	require.NoError(t, changeset.Stream(dir, 40, func(b *changeset.Block) error {
		if b.Height == 40 {
			for _, d := range b.Manifest {
				m40 = append(m40, d.Name)
			}
		}
		return nil
	}))
	sort.Strings(m40)
	require.Equal(t, []string{"acc", "bank", "blobstream", "distribution", "mem_capability", "params", "slashing", "staking", "transient_params", "upgrade"}, m40)
}

func TestReplayFromGenesisMatchesEveryHeight(t *testing.T) {
	for _, tc := range []struct {
		src, dst int64
		parallel bool
	}{{0, 0, false}, {7, 0, false}, {0, 5, false}, {7, 13, true}, {3, 1000, false}} {
		t.Run(fmt.Sprintf("src_K=%d_dst_K=%d_par=%v", tc.src, tc.dst, tc.parallel), func(t *testing.T) {
			dir, c := record(t, tc.src, 25)
			db := dbm.NewMemDB()
			a, err := apply(t, db, dir, tc.dst, c.expected, tc.parallel)
			require.NoError(t, err)
			require.EqualValues(t, nBlocks, a.Applied)
			require.EqualValues(t, nBlocks, a.CheckedExt)
			require.EqualValues(t, 2, a.Upgrades)
			require.EqualValues(t, nBlocks, a.Latest)
			_, deferred, err := rootmulti.ReadDeferredMarker(db)
			require.NoError(t, err)
			require.Equal(t, tc.dst > 0, deferred)
			compareWithSource(t, db, c)
		})
	}
}

// The log must not depend on the source's persistence mode.
func TestRecordingIndependentOfDeferredMode(t *testing.T) {
	d1, _ := record(t, 0, 1000)
	d2, _ := record(t, 7, 1000)
	b1, b2 := readAll(t, d1), readAll(t, d2)
	require.Equal(t, len(b1), len(b2))
	for i := range b1 {
		require.True(t, reflect.DeepEqual(b1[i], b2[i]), "block %d differs", b1[i].Height)
	}
}

// A snapshot of the node DB at height H plus the changesets of H+1.. (files
// starting before H, mid-file start, across upgrades) reproduces every later hash.
func TestReplayFromSnapshot(t *testing.T) {
	for _, tc := range []struct {
		src, dst int64
		snap     int64
	}{{0, 0, 30}, {0, 0, 39}, {0, 6, 40}, {7, 0, 63}, {7, 7, 69}, {0, 0, 94}, {7, 5, 100}} {
		t.Run(fmt.Sprintf("src_K=%d_dst_K=%d_H=%d", tc.src, tc.dst, tc.snap), func(t *testing.T) {
			dir, c := record(t, tc.src, 25, tc.snap)
			a, err := apply(t, c.snaps[tc.snap], dir, tc.dst, c.expected, false)
			require.NoError(t, err)
			require.EqualValues(t, nBlocks-tc.snap, a.Applied)
			require.EqualValues(t, nBlocks-tc.snap, a.CheckedExt)
		})
	}
}

// Applying in several runs (tool restarts, incl. one right before an upgrade block) is the same.
func TestReplayInChunks(t *testing.T) {
	dir, c := record(t, 0, 25)
	db := dbm.NewMemDB()
	opts := applier.DefaultOptions()
	opts.DeferredFlushInterval = 6
	opts.Logf = t.Logf
	var total int64
	for _, to := range []int64{17, 39, 40, 41, 69, 94, 0} {
		a, err := applier.Run(db, dir, to, opts, c.expected, nil)
		require.NoError(t, err)
		total += a.CheckedExt
	}
	require.EqualValues(t, nBlocks, total)
}

// Mutations of the log must be caught exactly at the mutated height.
func TestTamperedChangesetDetected(t *testing.T) {
	dir, c := record(t, 0, 1000)
	blocks := readAll(t, dir)

	model := map[string]map[string]string{}
	sameH, sameS, sameI := int64(-1), -1, -1
	revH, revS := int64(-1), -1
	for _, b := range blocks {
		for si, s := range b.Stores {
			m := model[s.Name]
			if m == nil {
				m = map[string]string{}
				model[s.Name] = m
			}
			inserts := 0
			for oi, op := range s.Ops {
				k := string(op.Key)
				old, had := m[k]
				if op.Kind == changeset.OpSet {
					if had && old == string(op.Value) && sameH < 0 && b.Height > 5 {
						sameH, sameS, sameI = b.Height, si, oi
					}
					if !had {
						inserts++
					}
					m[k] = string(op.Value)
				} else {
					delete(m, k)
				}
			}
			if inserts >= 10 && revH < 0 && b.Height > 50 {
				revH, revS = b.Height, si
			}
		}
	}
	require.Positive(t, sameH)
	require.Positive(t, revH)

	tamper := func(name string, h int64, wantStore string, mut func(b *changeset.Block)) {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			fw, err := changeset.CreateFile(filepath.Join(out, "cs-000000000001.bin"))
			require.NoError(t, err)
			for _, b := range readAll(t, dir) {
				if b.Height == h {
					mut(b)
				}
				require.NoError(t, fw.Write(b))
			}
			require.NoError(t, fw.Close())
			for _, exp := range []func(int64) ([]byte, bool, error){c.expected, nil} {
				db := dbm.NewMemDB()
				a, err := apply(t, db, out, 0, exp, false)
				var mm *applier.MismatchError
				require.True(t, errors.As(err, &mm), "expected mismatch, got %v", err)
				require.Equal(t, h, mm.Height)
				// the unverified height is rolled back exactly (also at an
				// upgrade height): the genuine log then applies to the end
				require.EqualValues(t, h-1, rootmulti.GetLatestVersion(db), mm.Detail)
				require.Contains(t, mm.Detail, fmt.Sprintf("rolled the DB back to height %d", h-1))
				if exp != nil {
					rest, err := apply(t, db, dir, 0, exp, false)
					require.NoError(t, err)
					require.EqualValues(t, nBlocks-h+1, rest.CheckedExt)
				}
				require.EqualValues(t, h, a.Applied) // Applied counts the failing block
				if wantStore != "" {
					require.Contains(t, mm.Detail, wantStore+":")
				}
				t.Log(mm.Error())
			}
		})
	}
	tamper("drop_same_value_set", sameH, blocks[sameH-1].Stores[sameS].Name, func(b *changeset.Block) {
		ops := b.Stores[sameS].Ops
		b.Stores[sameS].Ops = append(append([]changeset.Op{}, ops[:sameI]...), ops[sameI+1:]...)
	})
	tamper("reverse_insert_order", revH, blocks[revH-1].Stores[revS].Name, func(b *changeset.Block) {
		ops := b.Stores[revS].Ops
		for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
			ops[i], ops[j] = ops[j], ops[i]
		}
	})
	tamper("altered_value", 77, "", func(b *changeset.Block) {
		for si := range b.Stores {
			for oi := range b.Stores[si].Ops {
				if b.Stores[si].Ops[oi].Kind == changeset.OpSet {
					b.Stores[si].Ops[oi].Value = append([]byte("tampered"), b.Stores[si].Ops[oi].Value...)
					return
				}
			}
		}
	})
	// dropping the recorded move of one key into the renamed store
	tamper("dropped_rename_move", 40, "params2", func(b *changeset.Block) {
		for si := range b.Stores {
			if b.Stores[si].Name == "params2" {
				b.Stores[si].Ops = b.Stores[si].Ops[1:]
			}
		}
	})
	// an upgrade record without the add of a store
	t.Run("upgrade_without_add", func(t *testing.T) {
		out := t.TempDir()
		fw, err := changeset.CreateFile(filepath.Join(out, "cs-000000000001.bin"))
		require.NoError(t, err)
		for _, b := range readAll(t, dir) {
			if b.Height == 95 {
				b.Upgrade.Added = nil
				var keep []changeset.StoreOps
				for _, s := range b.Stores {
					if s.Name != "extra" {
						keep = append(keep, s)
					}
				}
				b.Stores = keep
			}
			require.NoError(t, fw.Write(b))
		}
		require.NoError(t, fw.Close())
		_, err = apply(t, dbm.NewMemDB(), out, 0, c.expected, false)
		var mm *applier.MismatchError
		require.True(t, errors.As(err, &mm), "got %v", err)
		require.EqualValues(t, 95, mm.Height)
	})
}

// InitChain with initial height > 1 (SetInitialVersion) is recorded and replayed.
func TestInitialVersion(t *testing.T) {
	dir := t.TempDir()
	c := newChain(t, dir, 10, 0, 1)
	c.startProcess(segments[0])
	require.NoError(t, c.rs.SetInitialVersion(1000))
	for h := int64(1000); h < 1030; h++ {
		c.block(h, segments[0])
	}
	c.finish()
	a, err := apply(t, dbm.NewMemDB(), dir, 0, c.expected, false)
	require.NoError(t, err)
	require.EqualValues(t, 30, a.CheckedExt)
}

// v0.46 blocks are refused.
func TestRefusesV046Blocks(t *testing.T) {
	out := t.TempDir()
	fw, err := changeset.CreateFile(filepath.Join(out, "cs-000000000001.bin"))
	require.NoError(t, err)
	require.NoError(t, fw.Write(&changeset.Block{Height: 1, Manifest: []changeset.StoreDecl{{Name: "acc", Type: changeset.StoreIAVL}}}))
	require.NoError(t, fw.Close())
	_, err = apply(t, dbm.NewMemDB(), out, 0, nil, false)
	require.ErrorIs(t, err, applier.ErrV046Block)
}

// The applied DB is a normal DB: an upstream (non-recording, non-deferred)
// multistore opens it and reads the same state as the source.
func TestAppliedDBReadable(t *testing.T) {
	t.Run("from_genesis", func(t *testing.T) {
		dir, c := record(t, 0, 25)
		db := dbm.NewMemDB()
		_, err := apply(t, db, dir, 9, c.expected, false)
		require.NoError(t, err)
		compareWithSource(t, db, c)
	})
	// The source snapshot carries a fast-node index at 63; the applier runs with
	// fast node off, so the index is stale afterwards. A node with fast node on
	// (the open below uses the default) must detect that and serve current data.
	t.Run("from_snapshot_stale_fast_index", func(t *testing.T) {
		dir, c := record(t, 0, 25, 63)
		db := c.snaps[63]
		_, err := apply(t, db, dir, 9, c.expected, false)
		require.NoError(t, err)
		compareWithSource(t, db, c)
	})
}

func compareWithSource(t *testing.T, db dbm.DB, c *chain) {
	open := func(db dbm.DB) *rootmulti.Store {
		rs := rootmulti.NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
		rs.SetChangesetRecorder(nil)
		for _, n := range segmentAt(nBlocks).iavl {
			rs.MountStoreWithDB(types.NewKVStoreKey(n), types.StoreTypeIAVL, nil)
		}
		require.NoError(t, rs.LoadLatestVersion())
		return rs
	}
	compareStores(t, open(db), open(c.db))
}

// compareStores checks that a holds the same commit and contents as b.
func compareStores(t *testing.T, a, b *rootmulti.Store) {
	t.Helper()
	require.Equal(t, b.LastCommitID(), a.LastCommitID())
	for _, n := range segmentAt(nBlocks).iavl {
		ka, kb := a.GetKVStore(a.StoreKeysByName()[n]), b.GetKVStore(b.StoreKeysByName()[n])
		ia, ib := ka.Iterator(nil, nil), kb.Iterator(nil, nil)
		cnt := 0
		for ; ia.Valid() && ib.Valid(); ia.Next() {
			require.True(t, bytes.Equal(ia.Key(), ib.Key()) && bytes.Equal(ia.Value(), ib.Value()), "store %s", n)
			ib.Next()
			cnt++
		}
		require.False(t, ia.Valid() || ib.Valid(), "store %s length differs", n)
		require.NoError(t, ia.Close())
		require.NoError(t, ib.Close())
		require.Positive(t, cnt, n)
	}
}

// Cross-fork fixtures: CS_FIXTURE_OUT=dir records the test chain with this
// store fork into dir/cs plus dir/expected.tsv; CS_FIXTURE_IN=dir applies such a
// recording (made by another fork / IAVL version) and checks every height.
func TestWriteFixture(t *testing.T) {
	out := os.Getenv("CS_FIXTURE_OUT")
	if out == "" {
		t.Skip("CS_FIXTURE_OUT not set")
	}
	require.NoError(t, os.MkdirAll(filepath.Join(out, "cs"), 0o750))
	c := newChain(t, filepath.Join(out, "cs"), 25, 7, 42)
	c.run(1, nBlocks)
	c.finish()
	var sb strings.Builder
	for h := int64(1); h <= nBlocks; h++ {
		fmt.Fprintf(&sb, "%d\t%X\n", h, c.hashes[h])
	}
	require.NoError(t, os.WriteFile(filepath.Join(out, "expected.tsv"), []byte(sb.String()), 0o600))
}

func TestApplyFixture(t *testing.T) {
	in := os.Getenv("CS_FIXTURE_IN")
	if in == "" {
		t.Skip("CS_FIXTURE_IN not set")
	}
	raw, err := os.ReadFile(filepath.Join(in, "expected.tsv")) //nolint:gosec // test fixture path
	require.NoError(t, err)
	exp := map[int64][]byte{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var h int64
		var hx string
		_, err := fmt.Sscanf(line, "%d\t%s", &h, &hx)
		require.NoError(t, err)
		exp[h], err = hex.DecodeString(hx)
		require.NoError(t, err)
	}
	a, err := apply(t, dbm.NewMemDB(), filepath.Join(in, "cs"), 5, func(h int64) ([]byte, bool, error) {
		v, ok := exp[h]
		return v, ok, nil
	}, true)
	require.NoError(t, err)
	require.EqualValues(t, nBlocks, a.CheckedExt)
}

// writeRaw writes blocks into a single changeset file; mut may change the
// encoded payload of a block before it is framed (the frame checksum is then
// computed over the changed payload).
func writeRaw(t *testing.T, blocks []*changeset.Block, mut func(b *changeset.Block, payload []byte)) string {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("CSET1\n")
	for _, b := range blocks {
		payload := changeset.AppendBlock(nil, b)
		if mut != nil {
			mut(b, payload)
		}
		require.NoError(t, changeset.WriteFrame(&buf, payload))
	}
	out := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(out, "cs-000000000001.bin"), buf.Bytes(), 0o600))
	return out
}

// Flipping one byte of a recorded value (with a valid frame checksum, i.e.
// a well-formed but wrong changeset) is an APP HASH MISMATCH error at that
// height, not a panic, with deferred persistence off and on; the height does
// not stay on disk. Flipping a byte without fixing the checksum is a
// corruption error.
func TestFlippedByteIsAppHashMismatch(t *testing.T) {
	const h = 77
	dir, c := record(t, 0, 1000)
	blocks := readAll(t, dir)
	raw := writeRaw(t, blocks, nil)
	p := filepath.Join(raw, "cs-000000000001.bin")
	bz, err := os.ReadFile(p) //nolint:gosec // test file
	require.NoError(t, err)
	var target []byte // a recorded value of height h found nowhere else in the file
	for _, s := range blocks[h-1].Stores {
		for _, op := range s.Ops {
			if target == nil && op.Kind == changeset.OpSet && len(op.Value) > 8 && bytes.Count(bz, op.Value) == 1 {
				target = append([]byte(nil), op.Value...)
			}
		}
	}
	require.NotNil(t, target)
	flipIn := func(bz []byte) {
		i := bytes.Index(bz, target)
		require.Positive(t, i)
		require.Equal(t, -1, bytes.Index(bz[i+1:], target), "value not unique")
		bz[i+len(target)-1] ^= 0x01
	}

	out := writeRaw(t, blocks, func(b *changeset.Block, payload []byte) {
		if b.Height == h {
			flipIn(payload)
		}
	})
	for _, k := range []int64{0, 5} {
		for _, exp := range []func(int64) ([]byte, bool, error){c.expected, nil} {
			db := dbm.NewMemDB()
			var err error
			require.NotPanics(t, func() { _, err = apply(t, db, out, k, exp, false) })
			var mm *applier.MismatchError
			require.True(t, errors.As(err, &mm), "got %v", err)
			require.EqualValues(t, h, mm.Height)
			require.Contains(t, err.Error(), "APP HASH MISMATCH at height 77")
			require.Less(t, rootmulti.GetLatestVersion(db), int64(h))
		}
	}

	// the same flip in the file itself fails the frame checksum
	flipIn(bz)
	require.NoError(t, os.WriteFile(p, bz, 0o600))
	db := dbm.NewMemDB()
	require.NotPanics(t, func() { _, err = apply(t, db, raw, 0, c.expected, false) })
	require.ErrorIs(t, err, changeset.ErrCorrupt)
	require.Less(t, rootmulti.GetLatestVersion(db), int64(h))
}

// Changesets decoded from one buffer: the applier must not hand those
// slices to IAVL. Reusing or overwriting every decoded payload after the
// apply leaves the applied state (and a reopened DB) unchanged.
func TestApplierDoesNotAliasDecodedBuffers(t *testing.T) {
	dir, c := record(t, 0, 1000)
	db := dbm.NewMemDB()
	a := applier.New(db, applier.DefaultOptions())
	a.Expected = c.expected
	var decoded []*changeset.Block
	require.NoError(t, changeset.Stream(dir, 0, func(b *changeset.Block) error {
		_, err := a.Apply(b)
		decoded = append(decoded, b)
		return err
	}))
	for _, b := range decoded {
		for _, s := range b.Stores {
			for _, op := range s.Ops {
				for i := range op.Key {
					op.Key[i] = 'x'
				}
				for i := range op.Value {
					op.Value[i] = 'x'
				}
			}
		}
	}
	// the live trees (node cache included) still hold the recorded state
	src := rootmulti.NewStore(c.db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	src.SetChangesetRecorder(nil)
	for _, n := range segmentAt(nBlocks).iavl {
		src.MountStoreWithDB(types.NewKVStoreKey(n), types.StoreTypeIAVL, nil)
	}
	require.NoError(t, src.LoadLatestVersion())
	compareStores(t, a.RS, src)
	require.NoError(t, a.Close())
	compareWithSource(t, db, c)
}

// writeBlocks writes blocks into a single changeset file.
func writeBlocks(t *testing.T, blocks []*changeset.Block) string {
	t.Helper()
	return writeRaw(t, blocks, nil)
}

func upTo(c *chain, last int64) func(int64) ([]byte, bool, error) {
	return func(h int64) ([]byte, bool, error) {
		if h > last {
			return nil, false, nil
		}
		return c.expected(h)
	}
}

// By default every height needs a trusted hash before it is applied: a run
// stops cleanly before the first height without one, a run without Expected
// applies nothing, and a block without a recorded app hash is refused.
func TestStrictRequiresTrustedHash(t *testing.T) {
	dir, c := record(t, 0, 25)
	db := dbm.NewMemDB()
	opts := applier.DefaultOptions()
	opts.Logf = t.Logf

	a, err := applier.Run(db, dir, 0, opts, nil, nil)
	require.NoError(t, err)
	require.Zero(t, a.Applied)
	require.Zero(t, rootmulti.GetLatestVersion(db))

	a, err = applier.Run(db, dir, 0, opts, upTo(c, 60), nil)
	require.NoError(t, err)
	require.EqualValues(t, 60, a.LastTrusted)
	require.EqualValues(t, 60, a.CheckedExt)
	require.EqualValues(t, 60, rootmulti.GetLatestVersion(db))

	// a forged last block whose recorded hash matches its forged content is
	// still not applied without a trusted hash for it
	blocks := readAll(t, dir)
	last := blocks[len(blocks)-1]
	for si := range last.Stores {
		for oi := range last.Stores[si].Ops {
			if op := &last.Stores[si].Ops[oi]; op.Kind == changeset.OpSet {
				op.Value = []byte("forged")
			}
		}
	}
	last.AppHash = []byte("forged hash")
	forged := writeBlocks(t, blocks)
	a, err = applier.Run(db, forged, 0, opts, upTo(c, nBlocks-1), nil)
	require.NoError(t, err)
	require.EqualValues(t, nBlocks-1, a.LastTrusted)
	require.EqualValues(t, nBlocks-1, rootmulti.GetLatestVersion(db))

	// no recorded app hash: refused before applying
	blocks[len(blocks)-1].AppHash = nil
	_, err = applier.Run(db, writeBlocks(t, blocks), 0, opts, c.expected, nil)
	require.ErrorContains(t, err, "no recorded app hash")
	require.EqualValues(t, nBlocks-1, rootmulti.GetLatestVersion(db))
}

// Errors are raised before anything is committed where possible: a trusted
// hash lookup error, and a first block on an empty DB that is not the
// initial height.
func TestApplierErrorsBeforeCommit(t *testing.T) {
	dir, c := record(t, 0, 25)
	db := dbm.NewMemDB()
	opts := applier.DefaultOptions()
	boom := errors.New("header store unavailable")
	_, err := applier.Run(db, dir, 0, opts, func(h int64) ([]byte, bool, error) {
		if h == 10 {
			return nil, false, boom
		}
		return c.expected(h)
	}, nil)
	require.ErrorIs(t, err, boom)
	require.EqualValues(t, 9, rootmulti.GetLatestVersion(db))

	blocks := readAll(t, dir)
	empty := dbm.NewMemDB()
	blocks[4].Manifest = blocks[0].Manifest
	_, err = applier.Run(empty, writeBlocks(t, blocks[4:]), 0, opts, c.expected, nil)
	require.ErrorContains(t, err, "first block must be height 1")
	require.Zero(t, rootmulti.GetLatestVersion(empty))
}

// A commit interrupted after a store added at an upgrade height saved that
// height (the restart re-executes the block and the multistore skips the
// store's commit) is still recorded faithfully: the log replays to the same
// hash at every height.
func TestRecordedAcrossInterruptedUpgradeCommit(t *testing.T) {
	dir := t.TempDir()
	c := newChain(t, dir, 1000, 0, 5)
	c.run(1, 39)
	up := segments[2] // upgrade at 40
	require.EqualValues(t, 40, up.start)

	// first attempt at 40: the new store saves 40, then the process dies
	// before the multistore commit
	c.startProcess(up)
	crashed := c.rs
	fb := crashed.CacheMultiStore()
	c.writeBlock(fb, 40, up)
	fb.Write()
	require.EqualValues(t, 40, crashed.GetCommitKVStore(c.keys["circuit"]).Commit().Version)
	c.rs = nil // no Close: the process died
	c.interruptedAt = 40

	// restart: the same upgrade load (the DB is still at 39) and block 40
	require.EqualValues(t, 39, rootmulti.GetLatestVersion(c.db))
	c.run(40, 60)
	c.finish()

	a, err := apply(t, dbm.NewMemDB(), dir, 0, c.expected, false)
	require.NoError(t, err)
	require.EqualValues(t, 60, a.CheckedExt)
}

// A block whose manifest omits a mounted store fails verification; the
// rollback restores the previous commit info byte for byte (not rebuilt from
// the stores the applier mounted), so the DB still opens with every store
// at the previous height and hash.
func TestRollbackKeepsStoresTheManifestOmits(t *testing.T) {
	dir := t.TempDir()
	rec, err := changeset.New(dir, changeset.Options{BlocksPerFile: 100, StoreInfos: true})
	require.NoError(t, err)
	db := dbm.NewMemDB()
	open := func(db dbm.DB, rec *changeset.Recorder) *rootmulti.Store {
		rs := rootmulti.NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
		rs.SetChangesetRecorder(rec)
		rs.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningEverything))
		for _, n := range []string{"a", "bank"} {
			rs.MountStoreWithDB(types.NewKVStoreKey(n), types.StoreTypeIAVL, nil)
		}
		require.NoError(t, rs.LoadLatestVersion())
		return rs
	}
	src := open(db, rec)
	hashes := map[int64][]byte{}
	for h := int64(1); h <= 3; h++ {
		for _, n := range []string{"a", "bank"} {
			src.GetKVStore(src.StoreKeysByName()[n]).Set([]byte(fmt.Sprint("k", h)), []byte(n))
		}
		hashes[h] = src.Commit().Hash
	}
	require.NoError(t, src.Close())
	blocks := readAll(t, dir)
	// block 3 claims only store "a" is mounted (and carries only its ops)
	blocks[2].Manifest = []changeset.StoreDecl{{Name: "a", Type: changeset.StoreIAVL}}
	blocks[2].Stores = blocks[2].Stores[:1]
	exp := func(h int64) ([]byte, bool, error) { v, ok := hashes[h]; return v, ok, nil }

	dst := dbm.NewMemDB()
	_, err = applier.Run(dst, writeBlocks(t, blocks), 0, applier.DefaultOptions(), exp, nil)
	var mm *applier.MismatchError
	require.True(t, errors.As(err, &mm), "got %v", err)
	require.EqualValues(t, 3, mm.Height)
	require.Contains(t, mm.Detail, "rolled the DB back to height 2")
	rs := open(dst, nil)
	require.Equal(t, types.CommitID{Version: 2, Hash: hashes[2]}, rs.LastCommitID())
	require.Equal(t, []byte("bank"), rs.GetKVStore(rs.StoreKeysByName()["bank"]).Get([]byte("k2")))
	require.Nil(t, rs.GetKVStore(rs.StoreKeysByName()["a"]).Get([]byte("k3")))
	require.NoError(t, rs.Close())

	// the genuine block 3 then applies
	a, err := applier.Run(dst, dir, 0, applier.DefaultOptions(), exp, nil)
	require.NoError(t, err)
	require.EqualValues(t, 3, a.LastTrusted)
}

// Manifests and upgrades must agree with the commit info of the latest height
// before anything is applied.
func TestStoreSetChecks(t *testing.T) {
	dir, c := record(t, 0, 1000)
	for name, tc := range map[string]struct {
		h   int64
		mut func(b *changeset.Block)
		msg string
	}{
		"unknown_manifest_store": {20, func(b *changeset.Block) {
			m := append(segmentAt(20).decls(), changeset.StoreDecl{Name: "zzz", Type: changeset.StoreIAVL})
			sort.Slice(m, func(i, j int) bool { return m[i].Name < m[j].Name })
			b.Manifest = m
		}, `manifest store "zzz" does not exist`},
		"add_existing":   {40, func(b *changeset.Block) { b.Upgrade.Added = append(b.Upgrade.Added, "acc") }, `adds existing store "acc"`},
		"delete_missing": {95, func(b *changeset.Block) { b.Upgrade.Deleted = []string{"crisis"} }, `deletes missing store "crisis"`},
		"rename_missing_old": {95, func(b *changeset.Block) {
			b.Upgrade.Renamed = []changeset.Rename{{Old: "nope", New: "new2"}}
		}, `renames "nope" -> "new2"`},
		"rename_to_existing": {95, func(b *changeset.Block) {
			b.Upgrade.Renamed = []changeset.Rename{{Old: "bank", New: "acc"}}
		}, `renames "bank" -> "acc"`},
	} {
		t.Run(name, func(t *testing.T) {
			blocks := readAll(t, dir)
			tc.mut(blocks[tc.h-1])
			db := dbm.NewMemDB()
			_, err := apply(t, db, writeBlocks(t, blocks), 0, c.expected, false)
			require.ErrorContains(t, err, tc.msg)
			require.EqualValues(t, tc.h-1, rootmulti.GetLatestVersion(db))
		})
	}
}

func (s segment) decls() []changeset.StoreDecl {
	var out []changeset.StoreDecl
	for _, n := range s.iavl {
		out = append(out, changeset.StoreDecl{Name: n, Type: changeset.StoreIAVL})
	}
	for _, n := range s.mem {
		out = append(out, changeset.StoreDecl{Name: n, Type: changeset.StoreMemory})
	}
	for _, n := range s.trans {
		out = append(out, changeset.StoreDecl{Name: n, Type: changeset.StoreTransient})
	}
	return out
}

// With deferred persistence, a height that fails verification at a flush
// height is never flushed (the applier's flush gate), so it rolls back
// cleanly instead of "discard this DB": the DB stays at a verified height
// and the genuine log applies from there.
func TestDeferredFlushGateKeepsFailedHeightOffDisk(t *testing.T) {
	const k, h = 5, 75 // 75 is a flush height
	dir, c := record(t, 0, 1000)
	blocks := readAll(t, dir)
	for si := range blocks[h-1].Stores {
		for oi := range blocks[h-1].Stores[si].Ops {
			if op := &blocks[h-1].Stores[si].Ops[oi]; op.Kind == changeset.OpSet {
				op.Value = append([]byte("tampered"), op.Value...)
			}
		}
	}
	db := dbm.NewMemDB()
	_, err := apply(t, db, writeBlocks(t, blocks), k, c.expected, false)
	var mm *applier.MismatchError
	require.True(t, errors.As(err, &mm), "got %v", err)
	require.EqualValues(t, h, mm.Height)
	require.NotContains(t, mm.Detail, "discard this DB")
	latest := rootmulti.GetLatestVersion(db)
	require.Less(t, latest, int64(h))
	require.Positive(t, latest)

	// the DB opens at that verified height and the genuine log applies
	a, err := apply(t, db, dir, k, c.expected, false)
	require.NoError(t, err)
	require.EqualValues(t, nBlocks-latest, a.CheckedExt)
	compareWithSource(t, db, c)
}

// A store deleted by an upgrade at H-1 still has a tree with version H-1 (a
// removed store commits its deletes). A block H that adds it again (or
// renames to it) is refused when the stores load, before anything is
// committed (IAVL: "initial version set to H, but found earlier version"),
// so no rollback can leave that tree at H. The genuine block H then applies.
func TestReAddOfDeletedStoreRefusedBeforeCommit(t *testing.T) {
	dir := t.TempDir()
	db := dbm.NewMemDB()
	hashes := map[int64][]byte{}
	var rs *rootmulti.Store
	start := func(u *types.StoreUpgrades) {
		if rs != nil {
			require.NoError(t, rs.Close())
		}
		rec, err := changeset.New(dir, changeset.Options{BlocksPerFile: 100, StoreInfos: true})
		require.NoError(t, err)
		rs = rootmulti.NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
		rs.SetChangesetRecorder(rec)
		rs.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningEverything))
		for _, n := range []string{"a", "x"} {
			rs.MountStoreWithDB(types.NewKVStoreKey(n), types.StoreTypeIAVL, nil)
		}
		if u != nil {
			require.NoError(t, rs.LoadLatestVersionAndUpgrade(u))
		} else {
			require.NoError(t, rs.LoadLatestVersion())
		}
	}
	commit := func(h int64, names ...string) {
		for _, n := range names {
			rs.GetKVStore(rs.StoreKeysByName()[n]).Set([]byte(fmt.Sprint("k", h)), []byte(n))
		}
		id := rs.Commit()
		require.Equal(t, h, id.Version)
		hashes[h] = id.Hash
	}
	start(nil)
	commit(1, "a", "x")
	commit(2, "a", "x")
	start(&types.StoreUpgrades{Deleted: []string{"x"}})
	commit(3, "a")
	commit(4, "a")
	require.NoError(t, rs.Close())
	exp := func(h int64) ([]byte, bool, error) { v, ok := hashes[h]; return v, ok, nil }

	for name, u := range map[string]*changeset.Upgrade{
		"add":    {Added: []string{"x"}},
		"rename": {Renamed: []changeset.Rename{{Old: "a", New: "x"}}},
	} {
		t.Run(name, func(t *testing.T) {
			blocks := readAll(t, dir)
			blocks[3].Upgrade, blocks[3].UpgradeAtLoad = u, true
			dst := dbm.NewMemDB()
			_, err := applier.Run(dst, writeBlocks(t, blocks), 0, applier.DefaultOptions(), exp, nil)
			require.ErrorContains(t, err, "initial version set to 4, but found earlier version")
			require.EqualValues(t, 3, rootmulti.GetLatestVersion(dst))
			a, err := applier.Run(dst, dir, 0, applier.DefaultOptions(), exp, nil)
			require.NoError(t, err)
			require.EqualValues(t, 4, a.LastTrusted)
		})
	}
}

// With AllowUnverified, blocks that carry neither a trusted nor a recorded
// app hash are accepted, so the deferred flush gate must let them reach the
// disk (else the unflushed state grows without bound).
func TestFlushGateAllowsUnverifiedBlocks(t *testing.T) {
	dir, c := record(t, 0, 1000)
	blocks := readAll(t, dir)
	for _, b := range blocks {
		b.AppHash = nil
	}
	db := dbm.NewMemDB()
	opts := applier.DefaultOptions()
	opts.DeferredFlushInterval = 5
	opts.AllowUnverified = true
	opts.Logf = t.Logf
	a, err := applier.Run(db, writeBlocks(t, blocks), 0, opts, nil, func(_ *applier.Applier, b *changeset.Block) {
		// flushed at least every 5 blocks while running, not only at Close
		require.GreaterOrEqual(t, rootmulti.GetLatestVersion(db), b.Height-5, "height %d", b.Height)
	})
	require.NoError(t, err)
	require.EqualValues(t, nBlocks, a.Unverified)
	require.EqualValues(t, nBlocks, rootmulti.GetLatestVersion(db))
	compareWithSource(t, db, c)
}

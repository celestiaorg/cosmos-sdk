package applier_test

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/log"
	dbm "github.com/tendermint/tm-db"

	pruningtypes "github.com/cosmos/cosmos-sdk/pruning/types"
	"github.com/cosmos/cosmos-sdk/store/cache"
	"github.com/cosmos/cosmos-sdk/store/changeset"
	"github.com/cosmos/cosmos-sdk/store/changeset/applier"
	"github.com/cosmos/cosmos-sdk/store/rootmulti"
	"github.com/cosmos/cosmos-sdk/store/types"
)

// chain emulates the store usage of baseapp: a deliver-state cache multistore
// per block, nested per-tx caches (written only for successful txs), the
// sorted cachekv flush into the IAVL trees at Commit, a memory and a transient
// store, and the mid-Commit store upgrade sequence of an app-version change.
type chain struct {
	t      *testing.T
	rs     *rootmulti.Store
	keys   map[string]*types.KVStoreKey
	mem    *types.MemoryStoreKey
	tkey   *types.TransientStoreKey
	rng    *rand.Rand
	hashes map[int64][]byte
}

var baseStores = []string{"acc", "bank", "distribution", "slashing", "staking", "blobstream"}

// upgrade schedule of the test chain
var upgrades = map[int64]*types.StoreUpgrades{
	40: {Added: []string{"signal", "icahost"}, Deleted: []string{"blobstream"}},
	70: {}, // app-version change without store changes (v2 -> v3)
}

func newChain(t *testing.T, db dbm.DB, rec *changeset.Recorder, flushEvery string, seed int64) *chain {
	rs := rootmulti.NewStore(db, log.NewNopLogger())
	rs.SetChangesetRecorder(rec)
	rs.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningEverything))
	if k := flushInterval(flushEvery); k > 0 {
		require.NoError(t, rs.SetDeferredPersistence(rootmulti.DeferredSync, k, 0))
	}
	rs.SetInterBlockCache(cache.NewCommitKVStoreCacheManager(cache.DefaultCommitKVStoreCacheSize))
	c := &chain{t: t, rs: rs, keys: map[string]*types.KVStoreKey{}, rng: rand.New(rand.NewSource(seed)), hashes: map[int64][]byte{}}
	for _, n := range baseStores {
		c.keys[n] = types.NewKVStoreKey(n)
		rs.MountStoreWithDB(c.keys[n], types.StoreTypeIAVL, nil)
	}
	c.mem = types.NewMemoryStoreKey("mem_capability")
	rs.MountStoreWithDB(c.mem, types.StoreTypeMemory, nil)
	c.tkey = types.NewTransientStoreKey("transient_params")
	rs.MountStoreWithDB(c.tkey, types.StoreTypeTransient, nil)
	require.NoError(t, rs.LoadLatestVersion())
	return c
}

// flushInterval parses a test's deferred flush interval ("" = upstream).
func flushInterval(s string) int64 {
	if s == "" {
		return 0
	}
	k, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		panic(err)
	}
	return k
}

func (c *chain) storeNames() []string {
	var out []string
	for _, n := range append(append([]string{}, baseStores...), "signal", "icahost") {
		if _, ok := c.keys[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

func (c *chain) randomTx(ms types.MultiStore) {
	names := c.storeNames()
	n := 1 + c.rng.Intn(12)
	for i := 0; i < n; i++ {
		name := names[c.rng.Intn(len(names))]
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
	ms.GetKVStore(c.mem).Set([]byte("m"), []byte(fmt.Sprint(c.rng.Int())))
	ms.GetKVStore(c.tkey).Set([]byte("t"), []byte("x"))
}

func (c *chain) block(h int64) types.CommitID {
	deliver := c.rs.CacheMultiStore()
	if h%9 != 0 { // every 9th block is empty
		// BeginBlock: hot per-validator keys rewritten every block
		dist := deliver.GetKVStore(c.keys["distribution"])
		for v := 0; v < 20; v++ {
			dist.Set([]byte(fmt.Sprintf("val%03d/rewards", v)), []byte(fmt.Sprintf("%d", h*int64(v+1))))
		}
		if h%10 == 3 { // a batch of new accounts (sorted inserts)
			acc := deliver.GetKVStore(c.keys["acc"])
			for i := 0; i < 15; i++ {
				acc.Set([]byte(fmt.Sprintf("new/%d/%d", h, c.rng.Intn(1e9))), []byte("account"))
			}
		}
		for i := 0; i < 1+c.rng.Intn(6); i++ {
			tx := deliver.CacheMultiStore()
			c.randomTx(tx)
			if c.rng.Intn(5) != 0 { // 20% of txs fail and are not written
				tx.Write()
			}
		}
	}
	up, migrating := upgrades[h]
	if migrating { // baseapp.Commit
		require.NoError(c.t, c.rs.FlushDeferred())
		c.rs.ForceFlushNextCommit()
	}
	deliver.Write()
	if migrating {
		for _, n := range up.Added {
			c.keys[n] = types.NewKVStoreKey(n)
			c.rs.MountStoreWithDB(c.keys[n], types.StoreTypeIAVL, nil)
		}
		require.NoError(c.t, c.rs.LoadLatestVersionAndUpgrade(up))
		for _, n := range up.Deleted {
			delete(c.keys, n)
		}
		// module migrations / InitGenesis of new modules
		mig := c.rs.CacheMultiStore()
		for _, n := range up.Added {
			kv := mig.GetKVStore(c.keys[n])
			for i := 0; i < 30; i++ {
				kv.Set([]byte(fmt.Sprintf("genesis%02d", i)), []byte(n))
			}
		}
		mig.GetKVStore(c.keys["bank"]).Set([]byte("params/version"), []byte(fmt.Sprint(h)))
		mig.Write()
	}
	id := c.rs.Commit()
	require.Equal(c.t, h, id.Version)
	c.hashes[h] = id.Hash
	return id
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

const nBlocks = 120

// record runs the chain and returns the changeset dir, the chain and the DB
// snapshots taken after the given heights.
func record(t *testing.T, flushEvery string, perFile int64, snapAt ...int64) (string, *chain, map[int64]dbm.DB) {
	dir := t.TempDir()
	rec, err := changeset.New(dir, changeset.Options{BlocksPerFile: perFile, StoreInfos: true, Zstd: true})
	require.NoError(t, err)
	db := dbm.NewMemDB()
	c := newChain(t, db, rec, flushEvery, 42)
	snaps := map[int64]dbm.DB{}
	for h := int64(1); h <= nBlocks; h++ {
		c.block(h)
		for _, s := range snapAt {
			if s == h {
				snaps[h] = copyDB(t, db)
			}
		}
	}
	require.NoError(t, rec.Close())
	return dir, c, snaps
}

func apply(t *testing.T, db dbm.DB, dir string, flushEvery string, exp func(int64) ([]byte, bool, error), parallel bool) (*applier.Applier, error) {
	opts := applier.DefaultOptions()
	opts.DeferredFlushInterval = flushInterval(flushEvery)
	opts.Parallel = parallel
	return applier.Run(db, dir, 0, opts, exp, nil)
}

func TestReplayFromGenesisMatchesEveryHeight(t *testing.T) {
	for _, tc := range []struct{ src, dst string }{{"", ""}, {"7", ""}, {"", "5"}, {"7", "13"}} {
		t.Run(fmt.Sprintf("src_K=%q_dst_K=%q", tc.src, tc.dst), func(t *testing.T) {
			dir, c, _ := record(t, tc.src, 25)
			files, err := changeset.ListFiles(dir)
			require.NoError(t, err)
			require.Len(t, files, 5)
			for _, f := range files {
				require.Equal(t, ".zst", filepath.Ext(f))
			}
			blocks := readAll(t, dir)
			require.Len(t, blocks, nBlocks)
			require.NotNil(t, blocks[39].Upgrade)
			require.Equal(t, []string{"blobstream"}, blocks[39].Upgrade.Deleted)
			require.NotNil(t, blocks[69].Upgrade)

			a, err := apply(t, dbm.NewMemDB(), dir, tc.dst, c.expected, tc.dst == "13")
			require.NoError(t, err)
			require.EqualValues(t, nBlocks, a.Applied)
			require.EqualValues(t, nBlocks, a.CheckedExt)
			require.Equal(t, c.hashes[nBlocks], a.RS.LastCommitID().Hash)
		})
	}
}

// The log must not depend on the source's persistence mode.
func TestRecordingIndependentOfDeferredMode(t *testing.T) {
	d1, _, _ := record(t, "", 1000)
	d2, _, _ := record(t, "7", 1000)
	b1, b2 := readAll(t, d1), readAll(t, d2)
	require.Equal(t, len(b1), len(b2))
	for i := range b1 {
		require.True(t, reflect.DeepEqual(b1[i], b2[i]), "block %d differs", b1[i].Height)
	}
}

// A snapshot of the node DB at height H plus the changesets of H+1.. (files
// starting before H, mid-file start) reproduces every later app hash.
func TestReplayFromSnapshot(t *testing.T) {
	for _, tc := range []struct {
		src, dst string
		snap     int64
	}{{"", "", 30}, {"", "", 40}, {"", "6", 63}, {"7", "", 35}, {"7", "7", 70}} {
		t.Run(fmt.Sprintf("src_K=%q_dst_K=%q_H=%d", tc.src, tc.dst, tc.snap), func(t *testing.T) {
			dir, c, snaps := record(t, tc.src, 25, tc.snap)
			a, err := apply(t, snaps[tc.snap], dir, tc.dst, c.expected, false)
			require.NoError(t, err)
			require.EqualValues(t, nBlocks-tc.snap, a.Applied)
			require.EqualValues(t, nBlocks-tc.snap, a.CheckedExt)
		})
	}
}

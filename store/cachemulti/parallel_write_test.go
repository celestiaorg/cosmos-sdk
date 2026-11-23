package cachemulti

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/log"
	dbm "github.com/tendermint/tm-db"

	"github.com/cosmos/cosmos-sdk/store/dbadapter"
	"github.com/cosmos/cosmos-sdk/store/iavl"
	"github.com/cosmos/cosmos-sdk/store/listenkv"
	"github.com/cosmos/cosmos-sdk/store/types"
)

func dbadapterStore() dbadapter.Store { return dbadapter.Store{DB: dbm.NewMemDB()} }

func withParallelStores(t *testing.T, on bool) {
	t.Helper()
	prev := parallelStoresEnabled
	parallelStoresEnabled = on
	t.Cleanup(func() { parallelStoresEnabled = prev })
}

// runParallelWriteWorkload writes a seeded random workload into nStores IAVL
// stores through a cachemulti store, including a nested branch, and returns the
// committed root hash of every store after every round.
func runParallelWriteWorkload(t *testing.T, parallel bool, nStores, rounds int) [][]byte {
	withParallelStores(t, parallel)
	rnd := rand.New(rand.NewSource(42))

	keys := make([]types.StoreKey, nStores)
	parents := make(map[types.StoreKey]types.CacheWrapper, nStores)
	trees := make([]*iavl.Store, nStores)
	keysByName := make(map[string]types.StoreKey, nStores)
	for i := range keys {
		keys[i] = types.NewKVStoreKey(fmt.Sprintf("s%02d", i))
		st, err := iavl.LoadStore(dbm.NewMemDB(), log.NewNopLogger(), keys[i], types.CommitID{}, false, 1000, false)
		require.NoError(t, err)
		trees[i] = st.(*iavl.Store)
		parents[keys[i]] = trees[i]
		keysByName[keys[i].Name()] = keys[i]
	}

	var hashes [][]byte
	for r := 0; r < rounds; r++ {
		cms := NewFromKVStore(dbadapterStore(), parents, keysByName, nil, nil)
		nested := cms.CacheMultiStore()
		for _, branch := range []types.CacheMultiStore{nested, cms} {
			for _, k := range keys {
				kv := branch.GetKVStore(k)
				for j := rnd.Intn(30); j > 0; j-- {
					key := []byte(fmt.Sprintf("k%03d", rnd.Intn(200)))
					if rnd.Intn(4) == 0 {
						kv.Delete(key)
					} else {
						kv.Set(key, []byte(fmt.Sprintf("v%d-%d", r, rnd.Int63())))
					}
				}
			}
		}
		nested.Write() // nested branch writes into cms's cachekv stores
		cms.Write()    // cms writes into the IAVL stores
		for _, tr := range trees {
			hashes = append(hashes, tr.Commit().Hash)
		}
	}
	return hashes
}

// TestParallelWriteMatchesSequential checks that the parallel cachemulti Write
// (CELESTIA_PARALLEL_STORES) leaves every store with the same root hashes as
// the sequential Write. Run with -race to check the goroutines.
func TestParallelWriteMatchesSequential(t *testing.T) {
	seq := runParallelWriteWorkload(t, false, 8, 30)
	par := runParallelWriteWorkload(t, true, 8, 30)
	require.Equal(t, seq, par)
}

type panicWriteStore struct {
	types.CacheKVStore
}

func (panicWriteStore) Write() { panic("boom") }

type countingWriteStore struct {
	types.CacheKVStore
	n *int
}

func (c countingWriteStore) Write() { *c.n++ }

// TestParallelWritePanicPropagates checks that a panic in one store's Write
// reaches the caller with its original value, as in the sequential path.
func TestParallelWritePanicPropagates(t *testing.T) {
	withParallelStores(t, true)
	counts := make([]int, 4)
	stores := map[types.StoreKey]types.CacheWrap{
		types.NewKVStoreKey("p"): panicWriteStore{},
	}
	for i := range counts {
		stores[types.NewKVStoreKey(fmt.Sprintf("c%d", i))] = countingWriteStore{n: &counts[i]}
	}
	cms := Store{db: dbadapterStore().CacheWrap().(types.CacheKVStore), stores: stores}
	require.PanicsWithValue(t, "boom", cms.Write)
	// Unlike the sequential path, the other stores are still written. A panic
	// here means the parent store failed (I/O error) and the node halts either
	// way; the sequential path's partial write depends on map order, so it is
	// not reproducible either.
	for _, n := range counts {
		require.Equal(t, 1, n)
	}
}

type recordingListener struct {
	buf *bytes.Buffer
}

func (l recordingListener) OnWrite(sk types.StoreKey, key, value []byte, del bool) error {
	fmt.Fprintf(l.buf, "%s/%s=%s/%v;", sk.Name(), key, value, del)
	return nil
}

// TestParallelWriteKeepsListenersSequential checks that a branch whose stores
// stream to ADR-038 listeners writes them on the sequential path, so the
// listener sees writes in the same order. Store-level order is map order in
// both paths, so the comparison is per store.
func TestParallelWriteKeepsListenersSequential(t *testing.T) {
	run := func(parallel bool) map[string]string {
		withParallelStores(t, parallel)
		out := map[string]string{}
		parents := map[types.StoreKey]types.CacheWrapper{}
		bufs := map[string]*bytes.Buffer{}
		keysByName := map[string]types.StoreKey{}
		for i := 0; i < 4; i++ {
			k := types.NewKVStoreKey(fmt.Sprintf("l%d", i))
			bufs[k.Name()] = &bytes.Buffer{}
			parents[k] = listenkv.NewStore(dbadapterStore(), k, []types.WriteListener{recordingListener{bufs[k.Name()]}})
			keysByName[k.Name()] = k
		}
		cms := NewFromKVStore(dbadapterStore(), parents, keysByName, nil, nil)
		require.True(t, cms.hasListeners)
		for k := range parents {
			for j := 0; j < 10; j++ {
				cms.GetKVStore(k).Set([]byte(fmt.Sprintf("k%d", 9-j)), []byte{byte('a' + j)})
			}
		}
		cms.Write()
		for name, b := range bufs {
			out[name] = b.String()
		}
		return out
	}
	require.Equal(t, run(false), run(true))
}

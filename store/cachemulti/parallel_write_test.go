package cachemulti

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/log"
	dbm "github.com/tendermint/tm-db"

	"github.com/cosmos/cosmos-sdk/store/dbadapter"
	"github.com/cosmos/cosmos-sdk/store/iavl"
	"github.com/cosmos/cosmos-sdk/store/types"
)

func dbadapterStore() dbadapter.Store { return dbadapter.Store{DB: dbm.NewMemDB()} }

// runParallelWriteWorkload writes a seeded random workload into nStores IAVL
// stores through a cachemulti store, including a nested branch, and returns the
// committed root hash of every store after every round.
func runParallelWriteWorkload(t *testing.T, parallel bool, nStores, rounds int) [][]byte {
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
		if parallel {
			cms = WithParallelWrite(cms)
		}
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
// leaves every store with the same root hashes as
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
	counts := make([]int, 4)
	stores := map[types.StoreKey]types.CacheWrap{
		types.NewKVStoreKey("p"): panicWriteStore{},
	}
	for i := range counts {
		stores[types.NewKVStoreKey(fmt.Sprintf("c%d", i))] = countingWriteStore{n: &counts[i]}
	}
	cms := WithParallelWrite(Store{db: dbadapterStore().CacheWrap().(types.CacheKVStore), stores: stores})
	require.PanicsWithValue(t, "boom", cms.Write)
	// Unlike the sequential path, the other stores are still written. A panic
	// here means the parent store failed (I/O error) and the node halts either
	// way; the sequential path's partial write depends on map order, so it is
	// not reproducible either.
	for _, n := range counts {
		require.Equal(t, 1, n)
	}
}

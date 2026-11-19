package cachemulti

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/store/dbadapter"
	"cosmossdk.io/store/types"
)

func TestStoreGetKVStore(t *testing.T) {
	require := require.New(t)

	s := NewStore(dbm.NewMemDB(), map[types.StoreKey]types.CacheWrapper{}, nil, nil, nil)
	key := types.NewKVStoreKey("abc")
	errMsg := fmt.Sprintf("kv store with key %v has not been registered in stores", key)

	require.PanicsWithValue(errMsg,
		func() { s.GetStore(key) })

	require.PanicsWithValue(errMsg,
		func() { s.GetKVStore(key) })

	child := s.CacheMultiStore()
	require.PanicsWithValue(errMsg,
		func() { child.GetKVStore(key) })
}

var otherKey = types.NewKVStoreKey("b")

func newTestStore() (Store, types.StoreKey, types.KVStore) {
	key := types.NewKVStoreKey("a")
	base := dbadapter.Store{DB: dbm.NewMemDB()}
	stores := map[types.StoreKey]types.CacheWrapper{key: base, otherKey: dbadapter.Store{DB: dbm.NewMemDB()}}
	return NewStore(dbm.NewMemDB(), stores, nil, nil, nil), key, base
}

// A substore branched after its parent was written reads the parent's
// current value, as an eagerly branched one would.
func TestLazyBranchReadsParentWrites(t *testing.T) {
	cms, key, _ := newTestStore()
	child := cms.CacheMultiStore()
	grandchild := child.CacheMultiStore()

	cms.GetKVStore(key).Set([]byte("k"), []byte("root"))
	child.GetKVStore(key).Set([]byte("k2"), []byte("child"))
	require.Equal(t, []byte("root"), grandchild.GetKVStore(key).Get([]byte("k")))
	require.Equal(t, []byte("child"), grandchild.GetKVStore(key).Get([]byte("k2")))
	require.Nil(t, cms.GetKVStore(key).Get([]byte("k2")))
}

// Writes reach the base store only once every level is written, in order.
func TestLazyBranchWriteFlowsThroughEveryLevel(t *testing.T) {
	cms, key, base := newTestStore()
	child := cms.CacheMultiStore()
	grandchild := child.CacheMultiStore()

	grandchild.GetKVStore(key).Set([]byte("k"), []byte("v"))
	discarded := child.CacheMultiStore()
	discarded.GetKVStore(key).Set([]byte("k"), []byte("discarded"))

	grandchild.Write()
	require.Equal(t, []byte("v"), child.GetKVStore(key).Get([]byte("k")))
	require.Nil(t, cms.GetKVStore(key).Get([]byte("k")))

	child.Write()
	require.Nil(t, base.Get([]byte("k")))
	cms.Write()
	require.Equal(t, []byte("v"), base.Get([]byte("k")))
	for _, s := range []Store{cms, child.(Store), grandchild.(Store)} {
		require.NotContains(t, s.stores, otherKey)
	}
}

// Copies of a Store share its substores, including ones branched after the
// copy was made, so a write through any copy is written by all.
func TestLazyBranchCopiesShareSubstores(t *testing.T) {
	cms, key, base := newTestStore()
	child := cms.CacheMultiStore().(Store)
	copied := child
	traced := child.SetTracingContext(types.TraceContext{"k": "v"}).(Store)

	copied.GetKVStore(key).Set([]byte("k"), []byte("v"))
	require.Same(t, copied.GetKVStore(key), child.GetKVStore(key))
	require.Same(t, traced.GetKVStore(key), child.GetKVStore(key))

	child.Write()
	cms.Write()
	require.Equal(t, []byte("v"), base.Get([]byte("k")))
}

// A child's trace context does not reach the parent, whose substores may be
// branched only once the child reads them.
func TestLazyBranchTraceContextStaysInChild(t *testing.T) {
	var buf bytes.Buffer
	key := types.NewKVStoreKey("a")
	stores := map[types.StoreKey]types.CacheWrapper{key: dbadapter.Store{DB: dbm.NewMemDB()}}
	cms := NewStore(dbm.NewMemDB(), stores, nil, &buf, types.TraceContext{"blockHeight": 1})

	child := cms.CacheMultiStore().SetTracingContext(types.TraceContext{"txHash": "A"}).(Store)
	child.GetKVStore(key).Set([]byte("k"), []byte("v"))
	child.Write()
	require.NotContains(t, cms.traceContext, "txHash")

	buf.Reset()
	cms.GetKVStore(key).Get([]byte("missing"))
	cms.Write()
	require.Contains(t, buf.String(), "blockHeight")
	require.NotContains(t, buf.String(), "txHash")
}

func TestLazyBranchConcurrentUse(t *testing.T) {
	cms, key, _ := newTestStore()
	child := cms.CacheMultiStore()

	var wg sync.WaitGroup
	got := make([]types.KVStore, 16)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = child.GetKVStore(key)
			got[i].Set([]byte{byte(i)}, []byte{1})
			child.CacheMultiStore().GetKVStore(key).Get([]byte{0})
		}(i)
	}
	wg.Wait()
	for _, s := range got {
		require.Same(t, got[0], s)
	}
	child.Write()
	require.Equal(t, []byte{1}, cms.GetKVStore(key).Get([]byte{15}))
}

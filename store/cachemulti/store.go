package cachemulti

import (
	"fmt"
	"io"
	"sync"

	dbm "github.com/cosmos/cosmos-db"

	"cosmossdk.io/store/cachekv"
	"cosmossdk.io/store/dbadapter"
	"cosmossdk.io/store/tracekv"
	"cosmossdk.io/store/types"
)

// storeNameCtxKey is the TraceContext metadata key that identifies
// the store which emitted a given trace.
const storeNameCtxKey = "store_name"

//----------------------------------------
// Store

// Store holds many branched stores.
// Implements MultiStore.
// NOTE: a Store (and MultiStores in general) should never expose the
// keys for the substores.
type Store struct {
	db     types.CacheKVStore
	stores map[types.StoreKey]types.CacheWrap // substores branched so far
	keys   map[string]types.StoreKey

	// A substore is branched on first use, from base (a branch of a root
	// store) or from the parent's substore (a branch of a branch). mu guards
	// stores and is a pointer so that copies of a Store share it.
	mu     *sync.RWMutex
	base   map[types.StoreKey]types.CacheWrapper
	parent *Store

	traceWriter  io.Writer
	traceContext types.TraceContext
}

var _ types.CacheMultiStore = Store{}

// NewFromKVStore creates a new Store object from a mapping of store keys to
// CacheWrapper objects and a KVStore as the database. Each CacheWrapper store
// is a branched store.
func NewFromKVStore(
	store types.KVStore, stores map[types.StoreKey]types.CacheWrapper,
	keys map[string]types.StoreKey, traceWriter io.Writer, traceContext types.TraceContext,
) Store {
	return Store{
		db:           cachekv.NewStore(store),
		stores:       make(map[types.StoreKey]types.CacheWrap),
		keys:         keys,
		mu:           &sync.RWMutex{},
		base:         stores,
		traceWriter:  traceWriter,
		traceContext: traceContext,
	}
}

// NewStore creates a new Store object from a mapping of store keys to
// CacheWrapper objects. Each CacheWrapper store is a branched store.
func NewStore(
	db dbm.DB, stores map[types.StoreKey]types.CacheWrapper, keys map[string]types.StoreKey,
	traceWriter io.Writer, traceContext types.TraceContext,
) Store {
	return NewFromKVStore(dbadapter.Store{DB: db}, stores, keys, traceWriter, traceContext)
}

func newCacheMultiStoreFromCMS(cms Store) Store {
	// SetTracingContext merges in place: a shared map would leak a child's txHash
	// into the trace wrappers the parent branches later.
	var tctx types.TraceContext
	if cms.traceContext != nil {
		tctx = cms.traceContext.Clone()
	}
	return Store{
		db:           cachekv.NewStore(cms.db),
		stores:       make(map[types.StoreKey]types.CacheWrap),
		mu:           &sync.RWMutex{},
		parent:       &cms,
		traceWriter:  cms.traceWriter,
		traceContext: tctx,
	}
}

// SetTracer sets the tracer for the MultiStore that the underlying
// stores will utilize to trace operations. A MultiStore is returned.
func (cms Store) SetTracer(w io.Writer) types.MultiStore {
	cms.traceWriter = w
	return cms
}

// SetTracingContext updates the tracing context for the MultiStore by merging
// the given context with the existing context by key. Any existing keys will
// be overwritten. It is implied that the caller should update the context when
// necessary between tracing operations. It returns a modified MultiStore.
func (cms Store) SetTracingContext(tc types.TraceContext) types.MultiStore {
	if cms.traceContext != nil {
		for k, v := range tc {
			cms.traceContext[k] = v
		}
	} else {
		cms.traceContext = tc
	}

	return cms
}

// TracingEnabled returns if tracing is enabled for the MultiStore.
func (cms Store) TracingEnabled() bool {
	return cms.traceWriter != nil
}

// LatestVersion returns the branch version of the store
func (cms Store) LatestVersion() int64 {
	panic("cannot get latest version from branch cached multi-store")
}

// GetStoreType returns the type of the store.
func (cms Store) GetStoreType() types.StoreType {
	return types.StoreTypeMulti
}

// Write calls Write on each underlying store.
func (cms Store) Write() {
	cms.db.Write()
	cms.mu.Lock()
	defer cms.mu.Unlock()
	for _, store := range cms.stores {
		store.Write()
	}
}

// branch returns the substore for key, branching it on first use, or nil if
// key is not registered. A branch of a branch first branches key in its parent.
func (cms Store) branch(key types.StoreKey) types.CacheWrap {
	cms.mu.RLock()
	s, ok := cms.stores[key]
	cms.mu.RUnlock()
	if ok {
		return s
	}
	cms.mu.Lock()
	defer cms.mu.Unlock()
	if s, ok := cms.stores[key]; ok {
		return s
	}
	var src types.CacheWrapper
	if cms.parent != nil {
		src = cms.parent.branch(key)
	} else {
		src = cms.base[key]
	}
	if src == nil {
		return nil
	}
	kv := src.(types.KVStore)
	if cms.TracingEnabled() {
		tctx := cms.traceContext.Clone().Merge(types.TraceContext{
			storeNameCtxKey: key.Name(),
		})
		kv = tracekv.NewStore(kv, cms.traceWriter, tctx)
	}
	s = cachekv.NewStore(kv)
	cms.stores[key] = s
	return s
}

// Implements CacheWrapper.
func (cms Store) CacheWrap() types.CacheWrap {
	return cms.CacheMultiStore().(types.CacheWrap)
}

// CacheWrapWithTrace implements the CacheWrapper interface.
func (cms Store) CacheWrapWithTrace(_ io.Writer, _ types.TraceContext) types.CacheWrap {
	return cms.CacheWrap()
}

// Implements MultiStore.
func (cms Store) CacheMultiStore() types.CacheMultiStore {
	return newCacheMultiStoreFromCMS(cms)
}

// CacheMultiStoreWithVersion implements the MultiStore interface. It will panic
// as an already cached multi-store cannot load previous versions.
//
// TODO: The store implementation can possibly be modified to support this as it
// seems safe to load previous versions (heights).
func (cms Store) CacheMultiStoreWithVersion(_ int64) (types.CacheMultiStore, error) {
	panic("cannot branch cached multi-store with a version")
}

// GetStore returns an underlying Store by key.
func (cms Store) GetStore(key types.StoreKey) types.Store {
	s := cms.branch(key)
	if s == nil {
		panic(fmt.Sprintf("kv store with key %v has not been registered in stores", key))
	}
	return s.(types.Store)
}

// GetKVStore returns an underlying KVStore by key.
func (cms Store) GetKVStore(key types.StoreKey) types.KVStore {
	store := cms.branch(key)
	if store == nil {
		panic(fmt.Sprintf("kv store with key %v has not been registered in stores", key))
	}
	return store.(types.KVStore)
}

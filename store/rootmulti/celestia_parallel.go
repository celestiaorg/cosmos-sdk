package rootmulti

import (
	"errors"
	"io"
	"sync"

	"cosmossdk.io/store/types"
)

// SetParallelCommit makes WorkingHash, Commit and Write of the root branch
// work on every store concurrently. Results are assembled in store-key order,
// so the commit info and app hash do not change.
func (rs *Store) SetParallelCommit(enabled bool) {
	rs.parallelCommit = enabled
}

// SetAsyncPruning makes IAVL stores prune old versions in the background.
// It applies to stores loaded after the call.
func (rs *Store) SetAsyncPruning(enabled bool) {
	rs.asyncPruning = enabled
}

// Close flushes deferred commits and stops the background pruning of every
// mounted IAVL store; the stores
// must not be used afterwards. The DB stays open. Stores replaced by a reload
// are not closed, since callers may still hold them.
func (rs *Store) Close() error {
	var errs []error
	// Persist soft commits first; this never waits for readers.
	if err := rs.FlushDeferred(); err != nil {
		errs = append(errs, err)
	}
	for key := range rs.stores {
		if c, ok := rs.GetCommitKVStore(key).(io.Closer); ok {
			if err := c.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// runParallel runs f(i) for i in [0,n) concurrently and re-panics the first
// panic on the calling goroutine.
func runParallel(n int, f func(i int)) {
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		panicVal any
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					if panicVal == nil {
						panicVal = r
					}
					mu.Unlock()
				}
			}()
			f(i)
		}(i)
	}
	wg.Wait()
	if panicVal != nil {
		panic(panicVal)
	}
}

// workingHashes returns store.WorkingHash() for each key, in order.
func workingHashes(stores map[types.StoreKey]types.CommitKVStore, keys []types.StoreKey, parallel bool) [][]byte {
	hashes := make([][]byte, len(keys))
	if parallel && len(keys) > 1 {
		runParallel(len(keys), func(i int) { hashes[i] = stores[keys[i]].WorkingHash() })
		return hashes
	}
	for i, key := range keys {
		hashes[i] = stores[key].WorkingHash()
	}
	return hashes
}

// commitStoreAt commits store unless it already reached version, which happens
// when a commit was interrupted after this store saved it.
func commitStoreAt(store types.CommitKVStore, version int64) types.CommitID {
	last := store.LastCommitID()
	if last.Version >= version {
		last.Version = version
		return last
	}
	return store.Commit()
}

// commitIDs commits every store and returns the commit IDs indexed like keys.
func commitIDs(version int64, storeMap map[types.StoreKey]types.CommitKVStore, keys []types.StoreKey, parallel bool) []types.CommitID {
	ids := make([]types.CommitID, len(keys))
	if parallel && len(keys) > 1 {
		runParallel(len(keys), func(i int) { ids[i] = commitStoreAt(storeMap[keys[i]], version) })
		return ids
	}
	for i, key := range keys {
		ids[i] = commitStoreAt(storeMap[key], version)
	}
	return ids
}

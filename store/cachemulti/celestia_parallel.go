package cachemulti

import (
	"sync"

	"cosmossdk.io/store/types"
)

// WithParallelWrite makes Write flush each substore into its parent on its own
// goroutine. Use it only on a branch of the root multistore without listeners:
// every parent is then a distinct store, and the writes into each one keep
// their order, so the resulting state is the same.
func WithParallelWrite(cms Store) Store {
	cms.parallelWrite = true
	return cms
}

// writeParallelLocked writes every branched substore concurrently. cms.mu is
// held, so no substore is branched meanwhile.
func (cms Store) writeParallelLocked() {
	var (
		wg       sync.WaitGroup
		panicMu  sync.Mutex
		panicVal any
	)
	for _, store := range cms.stores {
		wg.Add(1)
		go func(s types.CacheWrap) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicMu.Lock()
					if panicVal == nil {
						panicVal = r
					}
					panicMu.Unlock()
				}
			}()
			s.Write()
		}(store)
	}
	wg.Wait()
	if panicVal != nil {
		panic(panicVal)
	}
}

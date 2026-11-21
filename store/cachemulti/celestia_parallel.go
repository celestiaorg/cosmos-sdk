package cachemulti

import (
	"os"
	"sync"
	"sync/atomic"
	"time"

	"cosmossdk.io/store/types"
)

// Celestia sync-speedup prototype.
//
// CELESTIA_PARALLEL_STORES=1: Write() of a branch created directly from the
// root multistore (i.e. the per-block finalizeBlockState branch, not the
// per-tx branches) flushes each module store's cache into its parent store
// on its own goroutine. Each parent is a distinct IAVL tree (or transient /
// memory store) and the writes into one store keep their order, so the
// resulting trees and hashes are identical. Disabled when tracing is on.
//
// CELESTIA_STORE_TIMING=1 records the duration of the last root-branch Write
// (read by rootmulti for its per-block timing line).
var (
	celestiaParallelStores = os.Getenv("CELESTIA_PARALLEL_STORES") == "1"
	celestiaStoreTiming    = os.Getenv("CELESTIA_STORE_TIMING") == "1"

	lastRootWriteNanos atomic.Int64
)

// LastRootWriteDuration returns the duration of the most recent Write of a
// root branch (only recorded with CELESTIA_STORE_TIMING=1).
func LastRootWriteDuration() time.Duration { return time.Duration(lastRootWriteNanos.Load()) }

// MarkRoot marks cms as a branch of the root multistore.
func MarkRoot(cms Store) Store {
	cms.root = true
	return cms
}

func (cms Store) writeRoot() {
	var start time.Time
	if celestiaStoreTiming {
		start = time.Now()
	}
	if celestiaParallelStores && !cms.TracingEnabled() && len(cms.stores) > 1 {
		cms.db.Write()
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
	} else {
		cms.db.Write()
		for _, store := range cms.stores {
			store.Write()
		}
	}
	if celestiaStoreTiming {
		lastRootWriteNanos.Store(int64(time.Since(start)))
	}
}

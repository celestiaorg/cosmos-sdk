package rootmulti

import (
	"fmt"
	"os"
	"sync"
	"time"

	"cosmossdk.io/store/cachemulti"
	"cosmossdk.io/store/types"
)

// Celestia sync-speedup prototype.
//
// CELESTIA_PARALLEL_STORES=1: per-block WorkingHash and Commit (SaveVersion)
// run one goroutine per store. Every store is an independent IAVL tree with
// its own nodeDB and batch over a prefix of the shared (thread-safe) DB, and
// the results are collected by index and then assembled in the original,
// sorted order, so CommitInfo and the app hash are identical.
//
// CELESTIA_STORE_TIMING=1: prints one line per Commit to stderr:
//
//	store-timing h=<v> write=<root-branch Write in FinalizeBlock> hash=<WorkingHash>
//	  commit=<commitStores> prune=<handlePruning> meta=<flushMetadata>
var (
	celestiaParallelStores = os.Getenv("CELESTIA_PARALLEL_STORES") == "1"
	celestiaStoreTiming    = os.Getenv("CELESTIA_STORE_TIMING") == "1"

	lastWorkingHashDur time.Duration // guarded by the single-threaded ABCI flow
)

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

// celestiaWorkingHashes returns store.WorkingHash() for each key, in order.
func celestiaWorkingHashes(stores map[types.StoreKey]types.CommitKVStore, keys []types.StoreKey) [][]byte {
	var start time.Time
	if celestiaStoreTiming {
		start = time.Now()
	}
	hashes := make([][]byte, len(keys))
	if celestiaParallelStores && len(keys) > 1 {
		runParallel(len(keys), func(i int) { hashes[i] = stores[keys[i]].WorkingHash() })
	} else {
		for i, key := range keys {
			hashes[i] = stores[key].WorkingHash()
		}
	}
	if celestiaStoreTiming {
		lastWorkingHashDur = time.Since(start)
	}
	return hashes
}

// celestiaParallelCommit commits every store concurrently and returns the
// commit IDs indexed like keys, or nil when the parallel path is disabled (the
// caller then runs the original sequential loop).
func celestiaParallelCommit(version int64, storeMap map[types.StoreKey]types.CommitKVStore, keys []types.StoreKey) []types.CommitID {
	if !celestiaParallelStores || len(keys) < 2 {
		return nil
	}
	ids := make([]types.CommitID, len(keys))
	runParallel(len(keys), func(i int) {
		store := storeMap[keys[i]]
		last := store.LastCommitID()
		// Same rule as commitStores: a store already at (or past) version
		// after an interrupted commit is not committed again.
		if last.Version >= version {
			last.Version = version
			ids[i] = last
			return
		}
		ids[i] = store.Commit()
	})
	return ids
}

type commitTiming struct {
	enabled                        bool
	version                        int64
	start, last                    time.Time
	commitStores, prune, flushMeta time.Duration
}

func celestiaStartCommitTiming(version int64) *commitTiming {
	if !celestiaStoreTiming {
		return &commitTiming{}
	}
	now := time.Now()
	return &commitTiming{enabled: true, version: version, start: now, last: now}
}

func (t *commitTiming) mark(d *time.Duration) {
	if !t.enabled {
		return
	}
	now := time.Now()
	*d = now.Sub(t.last)
	t.last = now
}

func (t *commitTiming) done() {
	if !t.enabled {
		return
	}
	fmt.Fprintf(os.Stderr, "store-timing h=%d write=%.2fms hash=%.2fms commit=%.2fms prune=%.2fms meta=%.2fms total_commit=%.2fms\n",
		t.version,
		ms(cachemulti.LastRootWriteDuration()), ms(lastWorkingHashDur),
		ms(t.commitStores), ms(t.prune), ms(t.flushMeta), ms(time.Since(t.start)))
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

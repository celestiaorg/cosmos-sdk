package rootmulti

import (
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/cosmos/cosmos-sdk/store/types"
)

// Sync-speedup prototype (env CELESTIA_PARALLEL_STORES=1): commit each
// substore on its own goroutine. Substores are independent IAVL trees, and the
// resulting StoreInfos are sorted by name exactly as in the sequential path, so
// the commit hash is identical.
var parallelCommitEnabled = os.Getenv("CELESTIA_PARALLEL_STORES") == "1"

func commitStoresParallel(version int64, storeMap map[types.StoreKey]types.CommitKVStore, removalMap map[types.StoreKey]bool) *types.CommitInfo {
	var (
		mu         sync.Mutex
		wg         sync.WaitGroup
		storeInfos = make([]types.StoreInfo, 0, len(storeMap))
		panicVal   any
	)
	for key, store := range storeMap {
		wg.Add(1)
		go func(key types.StoreKey, store types.CommitKVStore) {
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
			last := store.LastCommitID()
			var commitID types.CommitID
			if last.Version >= version {
				last.Version = version
				commitID = last
			} else {
				commitID = store.Commit()
			}
			if store.GetStoreType() == types.StoreTypeTransient {
				return
			}
			if !removalMap[key] {
				mu.Lock()
				storeInfos = append(storeInfos, types.StoreInfo{Name: key.Name(), CommitId: commitID})
				mu.Unlock()
			}
		}(key, store)
	}
	wg.Wait()
	if panicVal != nil {
		panic(panicVal) // re-raise in the caller, as the sequential path would
	}
	sort.SliceStable(storeInfos, func(i, j int) bool {
		return strings.Compare(storeInfos[i].Name, storeInfos[j].Name) < 0
	})
	return &types.CommitInfo{Version: version, StoreInfos: storeInfos}
}

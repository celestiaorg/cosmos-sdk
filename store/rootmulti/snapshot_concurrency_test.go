//go:build !race

// Excluded under -race: queries that overlap the IAVL import report races
// inside IAVL that this test does not cover.

package rootmulti_test

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/cache"
	"cosmossdk.io/store/metrics"
	"cosmossdk.io/store/rootmulti"
	"cosmossdk.io/store/snapshots"
	snapshottypes "cosmossdk.io/store/snapshots/types"
	"cosmossdk.io/store/types"
)

// TestMultistoreRestore_ConcurrentStoreQuery runs store queries from several
// goroutines while a restore resets and reloads the inter-block cache.
func TestMultistoreRestore_ConcurrentStoreQuery(t *testing.T) {
	const stores = 30
	source := newMultiStoreWithGeneratedData(dbm.NewMemDB(), stores, 20)
	version := uint64(source.LastCommitID().Version)

	for iter := 0; iter < 5; iter++ {
		target := rootmulti.NewStore(dbm.NewMemDB(), log.NewNopLogger(), metrics.NewNoOpMetrics())
		target.SetInterBlockCache(cache.NewCommitKVStoreCacheManager(cache.DefaultCommitKVStoreCacheSize))
		for i := 0; i < stores; i++ {
			target.MountStoreWithDB(types.NewKVStoreKey(fmt.Sprintf("store%d", i)), types.StoreTypeIAVL, nil)
		}
		require.NoError(t, target.LoadLatestVersion())

		var (
			stop    atomic.Bool
			queries atomic.Int64
			wg      sync.WaitGroup
		)
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := g; !stop.Load(); i++ {
					req := &types.RequestQuery{Path: fmt.Sprintf("/store%d/key", i%stores), Data: []byte{0, 0, 0, 0, 0, 0, 0, 1}}
					_, _ = target.Query(req) // errors are expected while the stores reload
					queries.Add(1)
				}
			}(g)
		}

		chunks := make(chan io.ReadCloser, 100)
		go func() {
			streamWriter := snapshots.NewStreamWriter(chunks)
			defer streamWriter.Close()
			require.NoError(t, source.Snapshot(version, streamWriter))
		}()
		streamReader, err := snapshots.NewStreamReader(chunks)
		require.NoError(t, err)
		_, err = target.Restore(version, snapshottypes.CurrentFormat, streamReader)
		stop.Store(true)
		wg.Wait()
		require.NoError(t, err)
		require.Positive(t, queries.Load())
		require.Equal(t, source.LastCommitID(), target.LastCommitID())
	}
}

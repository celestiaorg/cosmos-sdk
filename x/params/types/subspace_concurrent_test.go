package types_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/log"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
	dbm "github.com/tendermint/tm-db"

	"github.com/cosmos/cosmos-sdk/simapp"
	"github.com/cosmos/cosmos-sdk/store"
	storetypes "github.com/cosmos/cosmos-sdk/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/params/types"
)

// TestSubspaceConcurrentReads reads params from several goroutines through one
// Subspace, as gRPC queries do while blocks execute. Run with -race: the store
// prefix must not be built by appending into the shared name slice.
func TestSubspaceConcurrentReads(t *testing.T) {
	db := dbm.NewMemDB()
	ms := store.NewCommitMultiStore(db)
	ms.MountStoreWithDB(key, storetypes.StoreTypeIAVL, db)
	ms.MountStoreWithDB(tkey, storetypes.StoreTypeTransient, db)
	require.NoError(t, ms.LoadLatestVersion())

	enc := simapp.MakeTestEncodingConfig()
	// a 7-byte name: []byte(name) gets capacity 8, so append(name, '/') would
	// write into the shared backing array
	ss := types.NewSubspace(enc.Codec, enc.Amino, key, tkey, "staking").WithKeyTable(paramKeyTable())
	ctx := sdk.NewContext(ms, tmproto.Header{}, false, log.NewNopLogger())
	ss.Set(ctx, keyUnbondingTime, time.Hour)
	ms.Commit()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// each goroutine has its own branch, like a query context
			qctx := sdk.NewContext(ms.CacheMultiStore(), tmproto.Header{}, true, log.NewNopLogger())
			for i := 0; i < 200; i++ {
				var v time.Duration
				ss.Get(qctx, keyUnbondingTime, &v)
				if v != time.Hour {
					t.Errorf("got %v", v)
					return
				}
				ss.GetIfExists(qctx, keyUnbondingTime, &v)
				_ = ss.Has(qctx, keyUnbondingTime)
				_ = ss.Modified(qctx, keyUnbondingTime) // transient store prefix
			}
		}()
	}
	wg.Wait()
}

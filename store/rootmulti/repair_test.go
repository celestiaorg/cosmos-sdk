package rootmulti

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/log"
	dbm "github.com/tendermint/tm-db"

	pruningtypes "github.com/cosmos/cosmos-sdk/pruning/types"
	"github.com/cosmos/cosmos-sdk/store/iavl"
	"github.com/cosmos/cosmos-sdk/store/types"
)

var (
	repKey1 = types.NewKVStoreKey("store1")
	repKey2 = types.NewKVStoreKey("store2")
)

func openRepairStore(t *testing.T, db dbm.DB, repair bool) *Store {
	rs := NewStore(db, log.NewNopLogger())
	rs.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningNothing))
	rs.SetRepairAhead(repair)
	rs.MountStoreWithDB(repKey1, types.StoreTypeIAVL, nil)
	rs.MountStoreWithDB(repKey2, types.StoreTypeIAVL, nil)
	require.NoError(t, rs.LoadLatestVersion())
	return rs
}

func commitBlock(rs *Store, h int64) types.CommitID {
	for _, k := range []types.StoreKey{repKey1, repKey2} {
		rs.GetKVStore(k).Set([]byte(fmt.Sprintf("k%d", h%7)), []byte(fmt.Sprintf("v%d", h)))
	}
	return rs.Commit()
}

func dbDigest(t *testing.T, db dbm.DB) string {
	it, err := db.Iterator(nil, nil)
	require.NoError(t, err)
	defer it.Close()
	h := sha256.New()
	for ; it.Valid(); it.Next() {
		h.Write(it.Key())
		h.Write(it.Value())
	}
	return fmt.Sprintf("%X", h.Sum(nil))
}

// A cut flush leaves store1 ahead of the commit info. Repair rolls it back
// when it is ahead by more than one version (upstream fails on that state)
// and leaves ahead-by-one to upstream's idempotent SaveVersion. A read-only
// load (repair off: export, rollback) leaves the DB byte-unchanged.
func TestRepairStoreAhead(t *testing.T) {
	for _, ahead := range []int64{1, 3} {
		t.Run(fmt.Sprintf("ahead=%d", ahead), func(t *testing.T) {
			ref, db := openRepairStore(t, dbm.NewMemDB(), true), dbm.NewMemDB()
			rs := openRepairStore(t, db, true)
			for h := int64(1); h <= 10; h++ {
				require.Equal(t, commitBlock(ref, h), commitBlock(rs, h))
			}
			// store1 alone writes versions 11..10+ahead (no commit info)
			st := rs.GetCommitKVStore(repKey1).(*iavl.Store)
			for i := int64(0); i < ahead; i++ {
				st.Set([]byte("stray"), []byte{byte(i)})
				st.Commit()
			}

			before := dbDigest(t, db)
			ro := openRepairStore(t, db, false)
			require.Equal(t, int64(10), ro.LastCommitID().Version)
			require.Equal(t, before, dbDigest(t, db), "read-only load modified the DB")

			rs = openRepairStore(t, db, true)
			want := 10 + ahead
			if ahead > 1 {
				want = 10
			}
			got := rs.GetCommitKVStore(repKey1).(*iavl.Store).GetAllVersions()
			require.Equal(t, int(want), got[len(got)-1])
			if ahead == 1 {
				return // upstream state; its SaveVersion decides
			}
			for h := int64(11); h <= 15; h++ {
				require.Equal(t, commitBlock(ref, h), commitBlock(rs, h), "height %d", h)
			}
		})
	}
}

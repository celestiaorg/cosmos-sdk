package server

import (
	"encoding/binary"
	"testing"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/metrics"
	"cosmossdk.io/store/rootmulti"
	storetypes "cosmossdk.io/store/types"
)

func TestReadDeferredMarker(t *testing.T) {
	db := dbm.NewMemDB()
	rs := rootmulti.NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	rs.MountStoreWithDB(storetypes.NewKVStoreKey("a"), storetypes.StoreTypeIAVL, nil)
	require.NoError(t, rs.LoadLatestVersion())
	rs.Commit()
	rs.Commit()

	setMarker := func(mode byte, version uint64) {
		bz := make([]byte, 9)
		bz[0] = mode
		binary.BigEndian.PutUint64(bz[1:], version)
		require.NoError(t, db.Set([]byte(deferredPersistenceKey), bz))
	}

	last, deferred, err := readDeferredMarker(db)
	require.NoError(t, err)
	require.False(t, deferred, "no marker")
	require.Zero(t, last)

	setMarker(deferredMarkerDeferred, 2)
	last, deferred, err = readDeferredMarker(db)
	require.NoError(t, err)
	require.True(t, deferred)
	require.Equal(t, int64(2), last)

	setMarker(deferredMarkerDeferred, 1)
	_, deferred, err = readDeferredMarker(db)
	require.NoError(t, err)
	require.False(t, deferred, "a stale marker is ignored")

	setMarker(deferredMarkerStock, 2)
	_, deferred, err = readDeferredMarker(db)
	require.NoError(t, err)
	require.False(t, deferred)

	require.NoError(t, db.Set([]byte(deferredPersistenceKey), []byte{1, 2}))
	_, _, err = readDeferredMarker(db)
	require.ErrorContains(t, err, "invalid deferred persistence marker")
}

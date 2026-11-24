package server

import (
	"encoding/binary"
	"errors"
	"testing"

	cmtcfg "github.com/cometbft/cometbft/config"
	cmtstate "github.com/cometbft/cometbft/state"
	cmttypes "github.com/cometbft/cometbft/types"
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

func setV046Marker(t *testing.T, db dbm.DB, deferred bool, lastFlushed uint64) {
	t.Helper()
	bz := []byte{0}
	if deferred {
		bz[0] = 1
	}
	bz = binary.AppendUvarint(bz, lastFlushed)
	require.NoError(t, db.Set([]byte(v046DeferredMarkerKey), bz))
}

func TestReadV046DeferredMarker(t *testing.T) {
	db := dbm.NewMemDB()
	_, _, found, err := readV046DeferredMarker(db)
	require.NoError(t, err)
	require.False(t, found)

	setV046Marker(t, db, true, 300)
	deferred, last, found, err := readV046DeferredMarker(db)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, deferred)
	require.Equal(t, int64(300), last)

	setV046Marker(t, db, false, 7)
	deferred, last, found, err = readV046DeferredMarker(db)
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, deferred)
	require.Equal(t, int64(7), last)

	for _, bad := range [][]byte{{1}, {2, 1}, {1, 0x80}, {1, 1, 1}} {
		require.NoError(t, db.Set([]byte(v046DeferredMarkerKey), bad))
		_, _, _, err = readV046DeferredMarker(db)
		require.ErrorContains(t, err, "invalid v0.46 deferred persistence marker", "%X", bad)
	}
}

// newCometHome writes a CometBFT state at lastHeight under a temp home.
func newCometHome(t *testing.T, lastHeight int64) *cmtcfg.Config {
	t.Helper()
	cfg := cmtcfg.TestConfig().SetRoot(t.TempDir())
	cfg.DBBackend = "goleveldb" // the test config's memdb does not survive a reopen
	pv := cmttypes.NewMockPV()
	pub, err := pv.GetPubKey()
	require.NoError(t, err)
	st, err := cmtstate.MakeGenesisState(&cmttypes.GenesisDoc{
		ChainID:    "test",
		Validators: []cmttypes.GenesisValidator{{PubKey: pub, Power: 1}},
	})
	require.NoError(t, err)
	st.LastBlockHeight = lastHeight
	st.LastValidators = st.Validators.Copy()
	stateDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "state", Config: cfg})
	require.NoError(t, err)
	require.NoError(t, cmtstate.NewStore(stateDB, cmtstate.StoreOptions{}).Save(st))
	require.NoError(t, stateDB.Close())
	return cfg
}

// TestRollbackV046Deferred checks that an app DB last written by the v0.46
// line in deferred mode is refused before CometBFT state is touched.
func TestRollbackV046Deferred(t *testing.T) {
	newAppDB := func(versions int) dbm.DB {
		db := dbm.NewMemDB()
		rs := rootmulti.NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
		rs.MountStoreWithDB(storetypes.NewKVStoreKey("a"), storetypes.StoreTypeIAVL, nil)
		require.NoError(t, rs.LoadLatestVersion())
		for i := 0; i < versions; i++ {
			rs.Commit()
		}
		return db
	}

	t.Run("no marker", func(t *testing.T) {
		cfg := newCometHome(t, 5)
		handled, err := rollbackV046Deferred(cfg, newAppDB(5), false)
		require.NoError(t, err)
		require.False(t, handled, "the regular rollback runs")
	})

	t.Run("deferred flush at the tip", func(t *testing.T) {
		db := newAppDB(5)
		setV046Marker(t, db, true, 5)
		cfg := newCometHome(t, 5)
		_, err := rollbackV046Deferred(cfg, db, false)
		require.ErrorAs(t, err, &errDeferredRollback{})
		require.ErrorContains(t, err, "holds only version 5")
		h, err := cometLastBlockHeight(cfg)
		require.NoError(t, err)
		require.Equal(t, int64(5), h, "CometBFT state is untouched")
	})

	t.Run("stock after deferred, target below the last flush", func(t *testing.T) {
		db := newAppDB(5)
		setV046Marker(t, db, false, 5)
		cfg := newCometHome(t, 5)
		_, err := rollbackV046Deferred(cfg, db, false)
		require.ErrorAs(t, err, &errDeferredRollback{})
		h, err := cometLastBlockHeight(cfg)
		require.NoError(t, err)
		require.Equal(t, int64(5), h, "CometBFT state is untouched")
	})

	t.Run("stock after deferred, target at or above the last flush", func(t *testing.T) {
		db := newAppDB(6)
		setV046Marker(t, db, false, 4)
		handled, err := rollbackV046Deferred(newCometHome(t, 6), db, false)
		require.NoError(t, err)
		require.False(t, handled, "the regular rollback runs")
	})

	t.Run("stale deferred marker", func(t *testing.T) {
		// Written later without the v0.46 binary: versions 4..6 are on disk.
		db := newAppDB(6)
		setV046Marker(t, db, true, 4)
		handled, err := rollbackV046Deferred(newCometHome(t, 6), db, false)
		require.NoError(t, err)
		require.False(t, handled, "the regular rollback runs")
	})
}

func TestV046RollbackPlan(t *testing.T) {
	db := dbm.NewMemDB()
	rs := rootmulti.NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	rs.MountStoreWithDB(storetypes.NewKVStoreKey("a"), storetypes.StoreTypeIAVL, nil)
	require.NoError(t, rs.LoadLatestVersion())
	for i := 0; i < 5; i++ {
		rs.Commit()
	}
	// Deferred flush at the app's tip: CometBFT-only rollback (it checks the target itself).
	cometOnly, err := v046RollbackPlan(db, true, 5, 6)
	require.NoError(t, err)
	require.True(t, cometOnly)
	// Stale deferred marker or stock marker: refuse only below the last flush.
	_, err = v046RollbackPlan(db, true, 3, 2)
	require.ErrorAs(t, err, &errDeferredRollback{})
	cometOnly, err = v046RollbackPlan(db, false, 3, 4)
	require.NoError(t, err)
	require.False(t, cometOnly)
}

type fakeRollbackChecker struct {
	target int64
	err    error
}

func (f *fakeRollbackChecker) CheckRollbackToVersion(target int64) ([]byte, error) {
	f.target = target
	return nil, f.err
}

// TestCheckAppRollback: the app store is checked at the CometBFT rollback
// target before CometBFT state is touched; an upstream store is not checked.
func TestCheckAppRollback(t *testing.T) {
	cfg := newCometHome(t, 5)
	require.NoError(t, checkAppRollback(cfg, struct{}{}), "a store without the check passes")

	ok := &fakeRollbackChecker{}
	require.NoError(t, checkAppRollback(cfg, ok))
	require.Equal(t, int64(4), ok.target, "state 5, empty block store: target 4")

	bad := &fakeRollbackChecker{err: errors.New("store s1 has no version 4")}
	err := checkAppRollback(cfg, bad)
	require.ErrorContains(t, err, "cannot roll back the app state to height 4, nothing was changed")
	h, err := cometLastBlockHeight(cfg)
	require.NoError(t, err)
	require.Equal(t, int64(5), h, "CometBFT state is untouched")
}

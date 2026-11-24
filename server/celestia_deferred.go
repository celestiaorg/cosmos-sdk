package server

import (
	"encoding/binary"
	"fmt"
	"math"

	dbm "github.com/cosmos/cosmos-db"

	"cosmossdk.io/store/rootmulti"
)

// The root module builds against upstream cosmossdk.io/store, which has no
// deferred persistence, so the marker decode is duplicated here.
// Keep in sync with store/rootmulti/celestia_deferred.go.
const (
	deferredOff              = "off"                    // rootmulti.DeferredOff
	deferredPersistenceKey   = "s/celestia/persistence" // rootmulti persistenceKey
	deferredMarkerStock      = byte(0)
	deferredMarkerDeferred   = byte(1)
	deferredMarkerEncodedLen = 9 // [mode byte][last flushed version, big endian]
)

// readDeferredMarker returns the last flushed version and whether the app DB
// was last written with deferred persistence. It does not write.
// Keep in sync with rootmulti.ReadDeferredMarker.
func readDeferredMarker(db dbm.DB) (lastFlushed int64, deferred bool, err error) {
	bz, err := db.Get([]byte(deferredPersistenceKey))
	if err != nil || bz == nil {
		return 0, false, err
	}
	if len(bz) != deferredMarkerEncodedLen || (bz[0] != deferredMarkerStock && bz[0] != deferredMarkerDeferred) {
		return 0, false, fmt.Errorf("invalid deferred persistence marker %X", bz)
	}
	v := binary.BigEndian.Uint64(bz[1:])
	if v > math.MaxInt64 {
		return 0, false, fmt.Errorf("invalid deferred persistence marker %X", bz)
	}
	lastFlushed = int64(v)
	if bz[0] != deferredMarkerDeferred {
		return lastFlushed, false, nil
	}
	// A stale marker left by an older binary is ignored.
	return lastFlushed, lastFlushed == rootmulti.GetLatestVersion(db), nil
}

// errDeferredRollback mirrors rootmulti.ErrDeferredRollback.
type errDeferredRollback struct {
	LastFlushed int64
}

func (e errDeferredRollback) Error() string {
	return fmt.Sprintf("app state was last written with deferred persistence and holds only version %d; "+
		"an app-state rollback is impossible. If the rollback target is at or above %d, roll back CometBFT state only "+
		"and the node replays the blocks after %d on start", e.LastFlushed, e.LastFlushed, e.LastFlushed)
}

// v046DeferredMarkerKey is the deferred persistence marker of the cosmos-sdk
// v0.46 line (the v3 child): one byte (1 = last written by a deferred flush,
// 0 = by a regular commit after leaving deferred mode) and the uvarint
// version of the last deferred flush. Keep in sync with the v0.46 line's
// store/rootmulti/deferred.go (deferredMarkerKey, DeferredMarker).
const v046DeferredMarkerKey = "s/deferred"

// readV046DeferredMarker reads the v0.46 marker: whether the DB was last
// written by a deferred flush, the version of the last deferred flush, and
// whether the marker exists. It does not write.
func readV046DeferredMarker(db dbm.DB) (deferred bool, lastFlushed int64, found bool, err error) {
	bz, err := db.Get([]byte(v046DeferredMarkerKey))
	if err != nil || bz == nil {
		return false, 0, false, err
	}
	if len(bz) < 2 || (bz[0] != 0 && bz[0] != 1) {
		return false, 0, false, fmt.Errorf("invalid v0.46 deferred persistence marker %X", bz)
	}
	v, n := binary.Uvarint(bz[1:])
	if n != len(bz)-1 || v > math.MaxInt64 {
		return false, 0, false, fmt.Errorf("invalid v0.46 deferred persistence marker %X", bz)
	}
	return bz[0] == 1, int64(v), true, nil
}

// v046RollbackPlan decides a rollback to target on a DB carrying the v0.46
// marker. No app version below lastFlushed is on disk, so a lower target is
// refused. A DB whose latest version is its last deferred flush holds only
// that version: then only CometBFT state may be rolled back (cometOnly).
func v046RollbackPlan(db dbm.DB, deferred bool, lastFlushed, target int64) (cometOnly bool, err error) {
	if deferred && lastFlushed == rootmulti.GetLatestVersion(db) {
		return true, nil
	}
	if target < lastFlushed {
		return false, errDeferredRollback{LastFlushed: lastFlushed}
	}
	return false, nil
}

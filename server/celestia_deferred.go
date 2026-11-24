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

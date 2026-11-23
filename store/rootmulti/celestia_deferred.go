package rootmulti

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	dbm "github.com/cosmos/cosmos-db"
	iavltree "github.com/cosmos/iavl"

	"cosmossdk.io/store/iavl"
	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/types"
)

// Deferred IAVL persistence.
//
// In deferred mode Commit computes every store hash and the app hash exactly
// as upstream, but IAVL keeps the new tree nodes in memory. A flush writes,
// in order: (1) every store's latest version, (2) the commit metadata, which
// is the commit point, (3) the deletion of each store's previous version. A
// kill at any point is recovered on the next deferred load and CometBFT
// replays the blocks after the last flush. Only one version stays on disk.
//
// Modes (app.toml performance.deferred-persistence):
//   - "off": upstream persistence.
//   - "sync" (default): deferred only while the node is catching up, judged by
//     the block header time against the wall clock. That only changes when
//     data is written, never what is computed. It turns on only for trees
//     with a single version on disk, so it never deletes retained history,
//     and back at the tip it flushes and returns to upstream persistence.
//   - "always": deferred from the first load, which prunes the DB to one
//     version. Opt-in.
//
// The DB records the last write mode and flushed version (persistenceKey).
// An app-state rollback is refused while that says deferred.

// Deferred persistence modes.
const (
	DeferredOff    = "off"
	DeferredSync   = "sync"
	DeferredAlways = "always"
)

const (
	upgradeStoreName = "upgrade"                // x/upgrade StoreKey; PlanKey() = {0x00}
	persistenceKey   = "s/celestia/persistence" // [mode byte][last flushed version, big endian]

	markerStock    byte = 0
	markerDeferred byte = 1

	// defaultDeferredMaxPending bounds unflushed nodes (roughly 1-2 GB).
	defaultDeferredMaxPending = 4_000_000
	// catchUpEnterLag and catchUpLeaveLag set when "sync" mode turns on and off.
	catchUpEnterLag = 10 * time.Minute
	catchUpLeaveLag = time.Minute
	// shutdownBusyWait bounds how long a shutdown flush waits for readers.
	shutdownBusyWait = 5 * time.Second
)

// ErrDeferredRollback is returned by RollbackToVersion on a DB last written in
// deferred mode, which keeps no version below LastFlushed.
type ErrDeferredRollback struct {
	LastFlushed int64
}

func (e ErrDeferredRollback) Error() string {
	return fmt.Sprintf("app state was last written with deferred persistence and holds only version %d; "+
		"an app-state rollback is impossible. If the rollback target is at or above %d, roll back CometBFT state only "+
		"and the node replays the blocks after %d on start", e.LastFlushed, e.LastFlushed, e.LastFlushed)
}

type deferredDriver struct {
	mode       string
	every      int64
	maxPending int
	now        func() time.Time // wall clock: persistence timing only

	active    atomic.Bool
	mu        sync.Mutex // serialises Commit and flushes
	forceNext bool
	flushed   int64 // version recorded in the commit metadata
	planNoted bool
}

// SetDeferredPersistence sets the deferred persistence mode and the number of
// blocks between flushes. It applies from the next load.
func (rs *Store) SetDeferredPersistence(mode string, flushInterval int64) error {
	switch mode {
	case "", DeferredOff:
		rs.celestiaDeferred = nil
		return nil
	case DeferredSync, DeferredAlways:
	default:
		return fmt.Errorf("invalid deferred-persistence %q: want %q, %q or %q", mode, DeferredSync, DeferredAlways, DeferredOff)
	}
	if flushInterval < 1 {
		return fmt.Errorf("invalid deferred-flush-interval %d: must be at least 1", flushInterval)
	}
	rs.celestiaDeferred = &deferredDriver{mode: mode, every: flushInterval, maxPending: defaultDeferredMaxPending, now: time.Now}
	return nil
}

func (d *deferredDriver) isActive() bool {
	return d != nil && d.active.Load()
}

// ReadDeferredMarker returns the last flushed version and whether the DB was
// last written in deferred mode. It does not write.
func ReadDeferredMarker(db dbm.DB) (lastFlushed int64, deferred bool, err error) {
	bz, err := db.Get([]byte(persistenceKey))
	if err != nil || bz == nil {
		return 0, false, err
	}
	if len(bz) != 9 {
		return 0, false, fmt.Errorf("invalid deferred persistence marker %X", bz)
	}
	return int64(binary.BigEndian.Uint64(bz[1:])), bz[0] == markerDeferred, nil //nolint:gosec // written by setMarker
}

func setMarker(batch dbm.Batch, mode byte, version int64) {
	bz := make([]byte, 9)
	bz[0] = mode
	binary.BigEndian.PutUint64(bz[1:], uint64(version)) //nolint:gosec // versions are positive
	if err := batch.Set([]byte(persistenceKey), bz); err != nil {
		panic(err)
	}
}

// deferredLoadOptions picks the store load options for loadLatestVersion.
func (rs *Store) deferredLoadOptions() (iavl.LoadStoreOptions, error) {
	opts := iavl.LoadStoreOptions{DiscardVersionsAboveTarget: true}
	_, written, err := ReadDeferredMarker(rs.db)
	if err != nil {
		return opts, err
	}
	d := rs.celestiaDeferred
	if d == nil || rs.pruningManager.GetOptions().GetPruningStrategy() == pruningtypes.PruningNothing {
		// A deferred DB may hold unrecorded versions; refuse rather than replay on top of them.
		opts.RefuseVersionsAboveTarget = written
		return opts, nil
	}
	if d.mode == DeferredAlways || written {
		opts.Deferred = true
	}
	return opts, nil
}

// deferredBeforeLoad flushes pending soft commits before a store reload.
// The mode stays on until the reload replaces the stores.
func (rs *Store) deferredBeforeLoad() error {
	if !rs.celestiaDeferred.isActive() {
		return nil
	}
	if err := rs.FlushDeferred(); err != nil {
		return fmt.Errorf("deferred persistence: flush before reload: %w", err)
	}
	return nil
}

// deferredAfterLoad turns the mode on or off to match the stores just loaded.
func (rs *Store) deferredAfterLoad(ver int64, enabled bool) {
	d := rs.celestiaDeferred
	if d == nil {
		return
	}
	d.mu.Lock()
	d.flushed = ver
	d.forceNext = false
	d.mu.Unlock()
	d.active.Store(enabled)
	if enabled {
		rs.logger.Info("deferred IAVL persistence on", "mode", d.mode, "flush_interval", d.every, "version", ver)
	}
}

// ForceFlushNextCommit makes the next Commit flush (deferred mode only).
func (rs *Store) ForceFlushNextCommit() {
	if d := rs.celestiaDeferred; d.isActive() {
		d.mu.Lock()
		d.forceNext = true
		d.mu.Unlock()
	}
}

// FlushDeferred durably writes the last soft-committed version. Safe to call
// mid-block and from another goroutine; it is serialised with Commit.
func (rs *Store) FlushDeferred() error {
	return rs.flushDeferredWait(0)
}

func (rs *Store) flushDeferredWait(maxWait time.Duration) error {
	d := rs.celestiaDeferred
	if !d.isActive() {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	ci := rs.getLastCommitInfo()
	if ci == nil || ci.Version <= d.flushed {
		return nil
	}
	return rs.flushDeferredLocked(ci.Version, ci, true, maxWait)
}

// deferredRollbackCheck refuses an app-state rollback on a DB last written in
// deferred mode. It does not write.
func (rs *Store) deferredRollbackCheck() error {
	last, written, err := ReadDeferredMarker(rs.db)
	if err != nil {
		return err
	}
	if written {
		return ErrDeferredRollback{LastFlushed: last}
	}
	return nil
}

// catchingUp reports whether the block being committed is old enough that
// the node is syncing. It only decides when data is written.
func (d *deferredDriver) catchingUp(header time.Time, lag time.Duration) bool {
	if header.IsZero() {
		return false
	}
	return d.now().Sub(header) > lag
}

// deferredBeforeCommit turns "sync" mode on when the node is catching up and
// every IAVL tree holds a single version, so no history is deleted.
func (rs *Store) deferredBeforeCommit() {
	d := rs.celestiaDeferred
	if d == nil || d.mode != DeferredSync || d.isActive() || !d.catchingUp(rs.commitHeader.Time, catchUpEnterLag) {
		return
	}
	if rs.pruningManager.GetOptions().GetPruningStrategy() == pruningtypes.PruningNothing {
		return
	}
	stores := rs.iavlStores()
	for i, st := range stores {
		if err := st.EnableDeferred(); err != nil {
			for _, s := range stores[:i] {
				_ = s.DisableDeferred()
			}
			rs.logger.Debug("deferred IAVL persistence stays off", "reason", err)
			return
		}
	}
	rs.deferredAfterLoad(rs.getLastCommitInfo().GetVersion(), true)
}

func (rs *Store) commitDeferred() types.CommitID {
	d := rs.celestiaDeferred
	d.mu.Lock()
	defer d.mu.Unlock()

	var version int64
	lastCommitInfo := rs.getLastCommitInfo()
	if lastCommitInfo.GetVersion() == 0 && rs.initialVersion > 1 {
		version = rs.initialVersion
	} else {
		version = lastCommitInfo.GetVersion() + 1
	}
	if rs.commitHeader.Height != version {
		rs.logger.Debug("commit header and version mismatch", "header_height", rs.commitHeader.Height, "version", version)
	}

	newCommitInfo := commitStores(version, rs.stores, rs.removalMap, rs.parallelCommit)
	newCommitInfo.Timestamp = rs.commitHeader.Time
	rs.setLastCommitInfo(newCommitInfo)

	leave := d.mode == DeferredSync && !d.catchingUp(rs.commitHeader.Time, catchUpLeaveLag)
	reason, mandatory := rs.deferredFlushReason(version)
	if leave {
		reason, mandatory = "caught up", true
	}
	if reason != "" {
		wait := time.Duration(0)
		if !mandatory {
			wait = -1 // skip this flush if a reader blocks the prune
		}
		if err := rs.flushDeferredLocked(version, newCommitInfo, false, wait); err != nil {
			panic(fmt.Errorf("deferred persistence: flush at %d (%s): %w", version, reason, err))
		}
		rs.logger.Debug("deferred IAVL flush", "version", version, "reason", reason)
	}
	if leave {
		rs.leaveDeferredLocked(version)
	}

	for sk := range rs.removalMap {
		if _, ok := rs.stores[sk]; ok {
			delete(rs.stores, sk)
			delete(rs.storesParams, sk)
			delete(rs.keysByName, sk.Name())
		}
	}
	rs.removalMap = make(map[types.StoreKey]bool)

	return types.CommitID{Version: version, Hash: newCommitInfo.Hash()}
}

// leaveDeferredLocked returns every tree to upstream persistence after a full
// flush and records it in the marker. d.mu is held.
func (rs *Store) leaveDeferredLocked(version int64) {
	d := rs.celestiaDeferred
	if _, err := rs.pruneDeferred(rs.deferredStores(), 0); err != nil {
		panic(fmt.Errorf("deferred persistence: leaving at %d: %w", version, err))
	}
	for _, st := range rs.iavlStores() {
		if err := st.DisableDeferred(); err != nil {
			panic(fmt.Errorf("deferred persistence: leaving at %d: %w", version, err))
		}
	}
	batch := rs.db.NewBatch()
	defer batch.Close()
	setMarker(batch, markerStock, version)
	if err := batch.WriteSync(); err != nil {
		panic(fmt.Errorf("deferred persistence: marker at %d: %w", version, err))
	}
	d.active.Store(false)
	rs.logger.Info("deferred IAVL persistence off: caught up", "version", version)
}

// deferredFlushReason returns why this commit must flush, if at all, and
// whether the flush must wait for readers instead of being skipped.
func (rs *Store) deferredFlushReason(version int64) (string, bool) {
	d := rs.celestiaDeferred
	switch {
	case d.forceNext:
		return "forced", true
	case d.flushed == 0:
		return "first commit", true
	case len(rs.removalMap) > 0:
		return "store removal", true
	case rs.upgradePlanPending():
		return "upgrade plan pending", true
	case rs.snapshotInterval > 0 && uint64(version)%rs.snapshotInterval == 0: //nolint:gosec // version > 0
		return "snapshot height", true
	case version%d.every == 0:
		return "periodic", false
	}
	n := 0
	for _, st := range rs.iavlStores() {
		n += st.DeferredPendingNodes()
	}
	if n > d.maxPending {
		return fmt.Sprintf("%d pending nodes", n), true
	}
	return "", false
}

// upgradePlanPending reports whether x/upgrade has a plan stored, so the
// blocks up to an app-version change are flushed before a binary switch.
func (rs *Store) upgradePlanPending() bool {
	key := rs.keysByName[upgradeStoreName]
	if key == nil {
		return false
	}
	kv, ok := rs.GetCommitKVStore(key).(types.KVStore)
	if !ok {
		return false
	}
	return kv.Get([]byte{0x00}) != nil
}

// iavlStores returns the IAVL stores sorted by name, including stores about
// to be removed.
func (rs *Store) iavlStores() []*iavl.Store {
	keys := make([]types.StoreKey, 0, len(rs.stores))
	for k := range rs.stores {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name() < keys[j].Name() })
	out := make([]*iavl.Store, 0, len(keys))
	for _, k := range keys {
		if st, ok := rs.GetCommitKVStore(k).(*iavl.Store); ok {
			out = append(out, st)
		}
	}
	return out
}

func (rs *Store) forEachStore(stores []*iavl.Store, f func(*iavl.Store) error) error {
	errs := make([]error, len(stores))
	if rs.parallelCommit && len(stores) > 1 {
		runParallel(len(stores), func(i int) { errs[i] = f(stores[i]) })
	} else {
		for i, st := range stores {
			errs[i] = f(st)
		}
	}
	return errors.Join(errs...)
}

// pruneDeferred finishes the previous flush's deletions. With maxWait < 0 it
// returns false at once if a reader blocks them; otherwise it waits up to
// maxWait (0 means no limit).
func (rs *Store) pruneDeferred(stores []*iavl.Store, maxWait time.Duration) (bool, error) {
	start := time.Now()
	for {
		err := rs.forEachStore(stores, (*iavl.Store).PruneDeferred)
		if !errors.Is(err, iavltree.ErrDeferredBusy) {
			return err == nil, err
		}
		if maxWait < 0 || (maxWait > 0 && time.Since(start) > maxWait) {
			return false, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// flushDeferredLocked runs the flush phases for version. d.mu is held. With
// maxWait < 0 the flush is skipped while a reader blocks the previous prune.
func (rs *Store) flushDeferredLocked(version int64, cInfo *types.CommitInfo, explicit bool, maxWait time.Duration) error {
	d := rs.celestiaDeferred
	stores := rs.deferredStores()

	// (0) finish the previous deletions, so each store keeps at most two versions.
	ok, err := rs.pruneDeferred(stores, maxWait)
	if err != nil {
		return err
	}
	if !ok {
		if explicit {
			return fmt.Errorf("deferred persistence: version %d not flushed: a reader holds version %d", version, d.flushed)
		}
		rs.logger.Debug("deferred IAVL flush postponed: a reader holds the previous version", "version", version)
		return nil
	}

	// (1) stores. A failure leaves the metadata at d.flushed; the stores that
	// did write are rolled back on the next load.
	if err := rs.forEachStore(stores, (*iavl.Store).FlushDeferred); err != nil {
		return err
	}
	if celestiaDeferredTestHook != nil {
		celestiaDeferredTestHook("stores", version)
	}

	// (2) commit point, with the marker.
	rs.flushDeferredMetadata(version, cInfo)
	d.flushed = version
	d.forceNext = false
	if celestiaDeferredTestHook != nil {
		celestiaDeferredTestHook("metadata", version)
	}

	// (3) deletions; a reader or failure postpones them to the next flush or load.
	if _, err := rs.pruneDeferred(stores, -1); err != nil {
		rs.logger.Error("deferred persistence: pruning the previous version failed; will retry", "err", err)
	}
	return nil
}

func (rs *Store) flushDeferredMetadata(version int64, cInfo *types.CommitInfo) {
	batch := rs.db.NewBatch()
	defer batch.Close()
	flushCommitInfo(batch, version, cInfo)
	flushLatestVersion(batch, version)
	setMarker(batch, markerDeferred, version)
	if err := batch.WriteSync(); err != nil {
		panic(fmt.Errorf("error on batch write %w", err))
	}
}

// deferredStores returns the IAVL stores in deferred mode.
func (rs *Store) deferredStores() []*iavl.Store {
	all := rs.iavlStores()
	out := all[:0]
	for _, st := range all {
		if st.IsDeferred() {
			out = append(out, st)
		}
	}
	return out
}

// celestiaDeferredTestHook, if set (tests only), is called after the store
// phase ("stores") and after the metadata phase ("metadata") of a flush.
var celestiaDeferredTestHook func(phase string, version int64)

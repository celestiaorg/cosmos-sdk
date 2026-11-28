package rootmulti

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
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
	// maxHeldFlushes bounds the versions a snapshot keeps on disk: after this
	// many flushes it is ignored (a Snapshot call that never returns).
	maxHeldFlushes = 16
	// catchUpEnterLag and catchUpLeaveLag set when "sync" mode turns on and off.
	catchUpEnterLag = 10 * time.Minute
	catchUpLeaveLag = time.Minute
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

	active  atomic.Bool
	mu      sync.Mutex // orders Commit and flushes
	flushed int64      // version recorded in the commit metadata

	offReason        string       // last reason "sync" stayed off (logged once at Info)
	snapshotHold     int64        // snapshot version every store keeps until its export is done
	snapshotsRunning atomic.Int32 // Snapshot calls in progress
	heldFlushes      int          // flushes whose pruning a snapshot held back
}

// SetDeferredPersistence sets the deferred persistence mode and the number of
// blocks between flushes. It applies from the next load.
func (rs *Store) SetDeferredPersistence(mode string, flushInterval int64) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
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

// ReadDeferredMarker returns the last flushed version and whether the DB is
// in deferred state: the marker says deferred and matches the latest commit
// metadata (a stale marker left by an older binary is ignored). It does not write.
func ReadDeferredMarker(db dbm.DB) (lastFlushed int64, deferred bool, err error) {
	lastFlushed, deferred, err = readMarker(db)
	if err != nil || !deferred {
		return lastFlushed, false, err
	}
	return lastFlushed, lastFlushed == GetLatestVersion(db), nil
}

// readMarker decodes the persisted marker as written.
func readMarker(db dbm.DB) (int64, bool, error) {
	bz, err := db.Get([]byte(persistenceKey))
	if err != nil || bz == nil {
		return 0, false, err
	}
	if len(bz) != 9 || (bz[0] != markerStock && bz[0] != markerDeferred) {
		return 0, false, fmt.Errorf("invalid deferred persistence marker %X", bz)
	}
	v := binary.BigEndian.Uint64(bz[1:])
	if v > math.MaxInt64 {
		return 0, false, fmt.Errorf("invalid deferred persistence marker %X", bz)
	}
	return int64(v), bz[0] == markerDeferred, nil
}

// markStock records an upstream commit in the marker of a DB last
// written in deferred mode, so a later rollback does not trust a stale marker.
func (rs *Store) markStock(batch dbm.Batch, version int64) {
	if rs.celestiaMarkerDeferred {
		setMarker(batch, markerStock, version)
		rs.celestiaMarkerDeferred = false
	}
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
	_, raw, err := readMarker(rs.db)
	if err != nil {
		return opts, err
	}
	rs.celestiaMarkerDeferred = raw
	_, written, err := ReadDeferredMarker(rs.db)
	if err != nil {
		return opts, err
	}
	d := rs.celestiaDeferred
	// The rollback app never loads in deferred mode, so its checks never write.
	if d == nil || rs.allowPendingRollback || rs.pruningManager.GetOptions().GetPruningStrategy() == pruningtypes.PruningNothing {
		// A deferred DB may hold unrecorded versions; refuse rather than replay on top of them.
		opts.RefuseVersionsAboveTarget = written
		return opts, nil
	}
	switch {
	case d.mode == DeferredAlways:
		opts.Deferred = true
	case written:
		// "sync": repair what deferred mode left, never delete other history.
		opts.Deferred, opts.DeferredKeepHistory = true, true
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
	d.mu.Unlock()
	d.active.Store(enabled)
	if enabled {
		rs.logger.Info("deferred IAVL persistence on", "mode", d.mode, "flush_interval", d.every, "version", ver)
	}
}

// FlushDeferred durably writes the last soft-committed version. It never
// waits for readers, may run mid-block and from another goroutine, and never
// runs at the same time as Commit.
func (rs *Store) FlushDeferred() error {
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
	return rs.flushDeferredLocked(ci.Version, ci)
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

// deferredBeforeCommit turns "sync" mode on when the node is catching up.
// Trees that hold only their latest version are switched directly. Trees with
// older versions (history from a stock run, or legacy-format versions from an
// older binary) are first collapsed to their latest version, but only when
// that deletes nothing the operator asked to keep and the work is small:
// when every tree holds at most one version below the latest (the shape a
// binary hop leaves) or, under pruning "everything", no more versions than
// stock pruning leaves; never while a snapshot may need a version that
// would go. A refusal or failure leaves the mode off and is
// retried at the next commit. It records deferred mode in the marker
// before the first soft commit, so a kill during the first flush is
// repaired by the next deferred load.
func (rs *Store) deferredBeforeCommit() {
	d := rs.celestiaDeferred
	if d == nil || d.mode != DeferredSync || d.isActive() || !d.catchingUp(rs.commitHeader.Time, catchUpEnterLag) {
		return
	}
	if rs.pruningManager.GetOptions().GetPruningStrategy() == pruningtypes.PruningNothing {
		rs.deferredStaysOff("pruning is \"nothing\"", nil)
		return
	}
	stores := rs.namedIAVLStores()
	if err := enableDeferredAll(stores); err != nil {
		if !errors.Is(err, iavltree.ErrDeferredHistory) && !errors.Is(err, iavltree.ErrDeferredLegacy) {
			rs.deferredStaysOff("enabling failed", err)
			return
		}
		if !rs.collapseForDeferred(stores) {
			return
		}
		if err := enableDeferredAll(stores); err != nil {
			rs.deferredStaysOff("enabling after the collapse failed", err)
			return
		}
	}
	version := rs.getLastCommitInfo().GetVersion()
	if version > 0 {
		// Without the marker, a kill during the first flush leaves trees with
		// versions {version, flushed} that only a deferred load can repair.
		if err := rs.writeMarker(markerDeferred, version); err != nil {
			for _, s := range stores {
				_ = s.DisableDeferred()
			}
			rs.logger.Error("deferred IAVL persistence stays off", "err", err)
			return
		}
		rs.celestiaMarkerDeferred = true
	}
	d.offReason = ""
	rs.deferredAfterLoad(version, true)
}

// enableDeferredAll turns deferred mode on in every store, or in none.
func enableDeferredAll(stores []namedIAVLStore) error {
	for i, st := range stores {
		if err := st.EnableDeferred(); err != nil {
			for _, s := range stores[:i] {
				_ = s.DisableDeferred()
			}
			return fmt.Errorf("store %s: %w", st.name, err)
		}
	}
	return nil
}

// collapseForDeferred deletes every version below the latest in each store,
// in store-name order, if the pruning policy and snapshots allow it. Nothing
// is written unless both gates pass. A kill part-way leaves every store's
// latest version intact and the DB stock (the marker is written only after
// the collapse), so the next start loads it upstream and retries here.
func (rs *Store) collapseForDeferred(stores []namedIAVLStore) bool {
	d := rs.celestiaDeferred
	version := rs.getLastCommitInfo().GetVersion()
	// The collapse runs inside Commit, so bound it: at most one version below
	// the latest (the hop shape), or under "everything" the history stock
	// pruning leaves in steady state. Larger history is left to stock
	// (async) pruning, or kept as the strategy asks. FirstVersion counts
	// legacy-format versions.
	maxBelow, reason := int64(1), "the pruning strategy keeps history"
	if opts := rs.pruningManager.GetOptions(); opts.GetPruningStrategy() == pruningtypes.PruningEverything {
		maxBelow = int64(opts.KeepRecent+opts.Interval) + 1 //nolint:gosec,nolintlint // "everything" is 2 and 10
		reason = "history larger than the pruning strategy keeps; waiting for stock pruning"
	}
	for _, st := range stores {
		first, err := st.FirstVersion()
		if err != nil {
			rs.deferredStaysOff("reading the first version failed", fmt.Errorf("store %s: %w", st.name, err))
			return false
		}
		if first > 0 && first < version-maxBelow {
			rs.deferredStaysOff(reason, fmt.Errorf("store %s holds versions %d to %d", st.name, first, version))
			return false
		}
	}
	d.mu.Lock()
	hold := d.snapshotHold
	d.mu.Unlock()
	if d.snapshotsRunning.Load() > 0 || hold != 0 || rs.isSnapshotHeight(version) ||
		rs.isSnapshotHeight(version-1) || version-1 > rs.pruningManager.SnapshotPruneLimit() {
		rs.deferredStaysOff("a snapshot may need an older version", nil)
		return false
	}
	for _, st := range stores {
		if err := st.CollapseToLatest(); err != nil {
			rs.deferredStaysOff("collapsing to the latest version failed", fmt.Errorf("store %s: %w", st.name, err))
			return false
		}
		if celestiaDeferredTestHook != nil {
			celestiaDeferredTestHook("collapse", version)
		}
	}
	rs.logger.Info("deferred persistence: deleted the versions below the latest to turn the mode on", "version", version)
	return true
}

// deferredStaysOff logs why "sync" mode stays off: at Info the first time a
// reason comes up, at Debug while it repeats, so it is visible without
// logging every block.
func (rs *Store) deferredStaysOff(reason string, err error) {
	d := rs.celestiaDeferred
	if d.offReason == reason {
		rs.logger.Debug("deferred IAVL persistence stays off", "reason", reason, "err", err)
		return
	}
	d.offReason = reason
	rs.logger.Info("deferred IAVL persistence stays off while catching up", "reason", reason, "err", err)
}

// writeMarker durably writes the marker on its own.
func (rs *Store) writeMarker(mode byte, version int64) error {
	batch := rs.db.NewBatch()
	defer batch.Close()
	setMarker(batch, mode, version)
	return batch.WriteSync()
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
	rs.recordCommit(version, false) // a flush of version syncs the log first

	leave := d.mode == DeferredSync && !d.catchingUp(rs.commitHeader.Time, catchUpLeaveLag)
	reason := rs.deferredFlushReason(version)
	if leave {
		reason = "caught up"
	}
	if reason != "" {
		if err := rs.flushDeferredLocked(version, newCommitInfo); err != nil {
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
// flush and records it in the marker. d.mu is held. While a snapshot or a
// reader still holds an older version it stays deferred, flushing every
// block, and retries on the next commit.
func (rs *Store) leaveDeferredLocked(version int64) {
	d := rs.celestiaDeferred
	done, err := rs.pruneDeferred(rs.deferredStores(), false)
	if err != nil {
		panic(fmt.Errorf("deferred persistence: leaving at %d: %w", version, err))
	}
	if !done {
		return
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
	rs.celestiaMarkerDeferred = false
	d.active.Store(false)
	rs.logger.Info("deferred IAVL persistence off: caught up", "version", version)
}

// deferredFlushReason returns why this commit must flush, if at all.
func (rs *Store) deferredFlushReason(version int64) string {
	d := rs.celestiaDeferred
	switch {
	case d.flushed == 0:
		return "first commit"
	case len(rs.removalMap) > 0:
		return "store removal"
	case rs.upgradePlanPending():
		return "upgrade plan pending"
	case rs.isSnapshotHeight(version):
		return "snapshot height"
	case version%d.every == 0:
		return "periodic"
	}
	n := 0
	for _, st := range rs.iavlStores() {
		n += st.DeferredPendingNodes()
	}
	if n > d.maxPending {
		return fmt.Sprintf("%d pending nodes", n)
	}
	return ""
}

func (rs *Store) isSnapshotHeight(version int64) bool {
	return rs.snapshotInterval > 0 && version > 0 && uint64(version)%rs.snapshotInterval == 0
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
	named := rs.namedIAVLStores()
	out := make([]*iavl.Store, len(named))
	for i, st := range named {
		out[i] = st.Store
	}
	return out
}

type namedIAVLStore struct {
	*iavl.Store
	name string
}

// namedIAVLStores is iavlStores with the store names.
func (rs *Store) namedIAVLStores() []namedIAVLStore {
	keys := make([]types.StoreKey, 0, len(rs.stores))
	for k := range rs.stores {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name() < keys[j].Name() })
	out := make([]namedIAVLStore, 0, len(keys))
	for _, k := range keys {
		if st, ok := rs.GetCommitKVStore(k).(*iavl.Store); ok {
			out = append(out, namedIAVLStore{Store: st, name: k.Name()})
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

// pruneDeferred deletes the versions each store left below its flushed one.
// It never waits: it does nothing while a snapshot runs, keeps the latest
// snapshot height (and later versions) until its snapshot is taken, and
// leaves a version that has readers for a later flush. After
// maxHeldFlushes flushes held back by a snapshot, or with force (shutdown:
// the snapshot cannot finish), it ignores the snapshot. It reports whether
// every store is down to its flushed version.
func (rs *Store) pruneDeferred(stores []*iavl.Store, force bool) (bool, error) {
	d := rs.celestiaDeferred
	if !force && (d.snapshotsRunning.Load() > 0 || d.snapshotHold != 0) && d.heldFlushes >= maxHeldFlushes {
		rs.logger.Info("deferred persistence: a snapshot held old versions too long; pruning them", "snapshot_height", d.snapshotHold)
		force = true
	}
	if force {
		d.snapshotHold = 0
	} else if d.snapshotsRunning.Load() > 0 {
		d.heldFlushes++
		return false, nil
	}
	limit := int64(math.MaxInt64)
	if d.snapshotHold != 0 {
		limit = d.snapshotHold
		d.heldFlushes++
	} else {
		d.heldFlushes = 0
	}
	err := rs.forEachStore(stores, func(st *iavl.Store) error { return st.PruneDeferredBelow(limit) })
	if errors.Is(err, iavltree.ErrDeferredBusy) {
		return false, nil
	}
	return err == nil && limit == math.MaxInt64, err
}

// closeDeferred flushes and prunes for a shutdown: a running snapshot cannot
// finish, so its hold is dropped; only versions with readers stay queued
// (IAVL finishes such a queue on the next prune or load).
func (rs *Store) closeDeferred() error {
	d := rs.celestiaDeferred
	if !d.isActive() {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if ci := rs.getLastCommitInfo(); ci != nil && ci.Version > d.flushed {
		if err := rs.flushDeferredLocked(ci.Version, ci); err != nil {
			return err
		}
	}
	_, err := rs.pruneDeferred(rs.deferredStores(), true)
	return err
}

// flushDeferredLocked runs the flush phases for version. d.mu is held. It
// never waits for readers.
func (rs *Store) flushDeferredLocked(version int64, cInfo *types.CommitInfo) error {
	d := rs.celestiaDeferred
	stores := rs.deferredStores()

	// (0) the changeset log reaches the disk before the commit info does.
	if err := rs.syncChangeset(); err != nil {
		return err
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
	if rs.isSnapshotHeight(version) {
		// Keep this version in every store until its snapshot is taken.
		d.snapshotHold = version
	}
	if celestiaDeferredTestHook != nil {
		celestiaDeferredTestHook("metadata", version)
	}

	// (3) deletions; a snapshot or a reader postpones them to a later flush or load.
	if _, err := rs.pruneDeferred(stores, false); err != nil {
		rs.logger.Error("deferred persistence: pruning older versions failed; will retry", "err", err)
	}
	return nil
}

// deferredSnapshotStart and deferredSnapshotDone bracket Snapshot(height), so
// deferred pruning keeps the snapshot version in every store until the
// export is done.
func (rs *Store) deferredSnapshotStart() {
	if d := rs.celestiaDeferred; d != nil {
		d.snapshotsRunning.Add(1)
	}
}

func (rs *Store) deferredSnapshotDone(height int64) {
	d := rs.celestiaDeferred
	if d == nil {
		return
	}
	d.mu.Lock()
	if height >= d.snapshotHold {
		d.snapshotHold = 0
	}
	d.mu.Unlock()
	d.snapshotsRunning.Add(-1)
}

func (rs *Store) flushDeferredMetadata(version int64, cInfo *types.CommitInfo) {
	batch := rs.db.NewBatch()
	defer batch.Close()
	flushCommitInfo(batch, version, cInfo)
	flushLatestVersion(batch, version)
	setMarker(batch, markerDeferred, version)
	rs.celestiaMarkerDeferred = true
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
// phase ("stores") and after the metadata phase ("metadata") of a flush
// and after each store's collapse ("collapse").
var celestiaDeferredTestHook func(phase string, version int64)

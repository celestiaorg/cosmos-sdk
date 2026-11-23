package rootmulti

// Deferred persistence ([performance] deferred-persistence).
//
// While it is active, every Commit still computes every IAVL root hash and the
// app hash exactly as upstream, but IAVL nodes, orphans, roots and the
// multistore metadata only reach the DB on flush commits: every
// deferred-flush-interval versions, the first commit after loading, commits
// that remove stores, the commit after ForceFlushNextCommit (baseapp: app
// version change), FlushDeferred (before a store reload, at the halt height)
// and ShutdownFlush (SIGINT/SIGTERM, BaseApp.Close).
//
// A flush writes (1) every IAVL store, (2) the commit info, latest version and
// the deferred marker (one WriteSync batch), then (3) deletes the older
// on-disk versions. A crash at any point leaves a loadable DB: stores ahead of
// the commit info are rolled back on load (repair.go) and the consensus
// engine replays the missing blocks.
//
// Modes: "off"; "sync" (default): deferred only while catching up, i.e.
// while the block time is older than CatchUpThreshold (callers that never
// report the signal, such as tools, stay deferred). The first block
// that is not is flushed and the store returns to per-block persistence for
// the rest of the process. The signal only changes when data is written,
// never what is computed (tests compare CommitIDs with upstream).
//
// Deferred persistence is turned off (logged once) when the node keeps
// history that it would not write: pruning that keeps more recent versions
// than the flush interval (nothing, default, large custom keep-recent), state
// sync snapshots, or lazy loading.
//
// Memory: up to deferred-flush-interval blocks of new IAVL nodes stay in RAM;
// at most (interval x nodes written per block), and never more than the tree.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	dbm "github.com/tendermint/tm-db"

	pruningtypes "github.com/cosmos/cosmos-sdk/pruning/types"
	"github.com/cosmos/cosmos-sdk/server/shutdown"
	"github.com/cosmos/cosmos-sdk/store/iavl"
	"github.com/cosmos/cosmos-sdk/store/types"
)

// Deferred persistence modes.
const (
	DeferredOff  = "off"
	DeferredSync = "sync"
)

const (
	// DefaultDeferredFlushInterval is small enough for a shutdown flush to
	// finish well within the 6 s the multiplexer waits before SIGKILL.
	DefaultDeferredFlushInterval = 100
	// MaxDeferredFlushInterval bounds the memory held by unflushed versions.
	MaxDeferredFlushInterval = 1000
	// CatchUpThreshold: in "sync" mode a block older than this is replayed
	// while catching up.
	CatchUpThreshold = 10 * time.Minute
)

// deferredMarkerKey records how the metadata was last written: one byte (1 =
// deferred flush, 0 = per-block after leaving deferred mode) and the version
// of the last deferred flush (uvarint). Absent on DBs never written deferred.
const deferredMarkerKey = "s/deferred"

// ErrRollbackDeferred is returned by RollbackToVersion on a DB whose app state
// cannot be rolled back by one height (see DeferredMarker).
var ErrRollbackDeferred = errors.New("app state cannot be rolled back")

// ValidateDeferredPersistence checks the [performance] settings.
func ValidateDeferredPersistence(mode string, interval int64) error {
	switch mode {
	case DeferredOff, DeferredSync:
	default:
		return fmt.Errorf("deferred-persistence must be %q or %q, got %q", DeferredSync, DeferredOff, mode)
	}
	if interval < 2 || interval > MaxDeferredFlushInterval {
		return fmt.Errorf("deferred-flush-interval must be between 2 and %d, got %d", MaxDeferredFlushInterval, interval)
	}
	return nil
}

type deferredState struct {
	mode             string // configured mode
	every            int64
	snapshotInterval uint64
	decided          bool  // effective mode decided (first load)
	active           bool  // soft commits in progress
	lastFlushed      int64 // latest version whose state and metadata are on disk
	forceNext        bool  // make the next Commit a flush
	notCatchingUp    bool  // sync mode: reached the tip, leave at next commit
	closed           bool  // ShutdownFlush ran: no further commits
	shutdownOnce     sync.Once
	shutdownErr      error
	mtx              sync.Mutex
}

// SetDeferredPersistence configures deferred persistence. It must be called
// before the stores are loaded. snapshotInterval is the state sync snapshot
// interval (deferred persistence is off when it is > 0).
func (rs *Store) SetDeferredPersistence(mode string, every int64, snapshotInterval uint64) error {
	if err := ValidateDeferredPersistence(mode, every); err != nil {
		return err
	}
	rs.deferred.mode, rs.deferred.every, rs.deferred.snapshotInterval = mode, every, snapshotInterval
	if mode != DeferredOff {
		// fast-node index writes are part of SaveVersion; not compatible
		// with soft commits
		rs.iavlDisableFastNode = true
	}
	return nil
}

// decideDeferred fixes the effective mode at the first load, when pruning and
// lazy loading are known.
func (rs *Store) decideDeferred() {
	d := &rs.deferred
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.decided || d.mode == "" || d.mode == DeferredOff {
		d.decided = true
		return
	}
	d.decided = true
	reason := ""
	opts := rs.pruningManager.GetOptions()
	switch {
	case opts.GetPruningStrategy() == pruningtypes.PruningNothing:
		reason = "pruning keeps every version"
	case opts.KeepRecent > uint64(d.every):
		reason = fmt.Sprintf("pruning keep-recent %d exceeds deferred-flush-interval %d", opts.KeepRecent, d.every)
	case d.snapshotInterval > 0:
		reason = "state sync snapshots are enabled"
	case rs.lazyLoading:
		reason = "IAVL lazy loading is enabled"
	}
	if reason != "" {
		rs.logger.Info("deferred IAVL persistence disabled", "reason", reason)
		d.mode = DeferredOff
		return
	}
	d.active = true
	rs.iavlDisableFastNode = true
	rs.logger.Info("deferred IAVL persistence enabled", "mode", d.mode, "flush_interval", d.every)
	registerDeferredStore(rs)
}

// SetCatchingUp tells a "sync" mode store whether the block being committed
// is replayed while catching up. When it is not, the next Commit flushes and
// per-block persistence resumes. Only affects when data is written.
func (rs *Store) SetCatchingUp(catchingUp bool) {
	rs.deferred.mtx.Lock()
	defer rs.deferred.mtx.Unlock()
	if rs.deferred.mode == DeferredSync && !catchingUp {
		rs.deferred.notCatchingUp = true
	}
}

func (rs *Store) deferredEnabled() bool {
	rs.deferred.mtx.Lock()
	defer rs.deferred.mtx.Unlock()
	return rs.deferred.active
}

// ForceFlushNextCommit makes the next Commit write everything to disk.
func (rs *Store) ForceFlushNextCommit() {
	rs.deferred.mtx.Lock()
	rs.deferred.forceNext = true
	rs.deferred.mtx.Unlock()
}

// FlushDeferred writes the latest soft-committed version and its metadata.
// No-op when nothing is pending. It fails, writing nothing, if an IAVL working
// tree holds uncommitted changes, so it must be called between blocks.
func (rs *Store) FlushDeferred() error {
	rs.deferred.mtx.Lock()
	defer rs.deferred.mtx.Unlock()
	if !rs.deferred.active || rs.deferred.closed || rs.lastCommitInfo == nil || rs.lastCommitInfo.Version <= rs.deferred.lastFlushed {
		return nil
	}
	return rs.flushDeferredLocked(rs.lastCommitInfo.Version, rs.lastCommitInfo)
}

// ShutdownFlush writes the latest committed version and its metadata and
// closes the store for commits. It takes the block write lock (WriteLock), so
// it waits for a block in progress to finish committing and never runs while
// the IAVL working trees are being written. Any later Commit blocks forever:
// the process is exiting, and it must not answer for a block that is not on
// disk. Idempotent; no-op when deferred persistence is not active.
func (rs *Store) ShutdownFlush() error {
	// Once: concurrent callers (signal hook, BaseApp.Close) wait for the
	// first flush to finish; later callers return at once, even while a
	// Commit after the flush holds the write lock forever.
	rs.deferred.shutdownOnce.Do(func() { rs.deferred.shutdownErr = rs.shutdownFlush() })
	return rs.deferred.shutdownErr
}

func (rs *Store) shutdownFlush() error {
	rs.writeMtx.Lock()
	defer rs.writeMtx.Unlock()
	rs.deferred.mtx.Lock()
	defer rs.deferred.mtx.Unlock()
	if !rs.deferred.active || rs.deferred.closed {
		return nil
	}
	rs.deferred.closed = true
	unregisterDeferredStore(rs)
	if rs.lastCommitInfo == nil || rs.lastCommitInfo.Version <= rs.deferred.lastFlushed {
		return nil
	}
	if err := rs.flushDeferredLocked(rs.lastCommitInfo.Version, rs.lastCommitInfo); err != nil {
		return err
	}
	rs.logger.Info("deferred persistence: flushed on shutdown", "version", rs.deferred.lastFlushed)
	return nil
}

// WriteLock serialises block execution with ShutdownFlush. BaseApp.Commit
// holds it from writing the deliver state into the stores until the commit
// returns, the only time the IAVL working trees change.
func (rs *Store) WriteLock() sync.Locker { return &rs.writeMtx }

// flushDeferredLocked writes version (see the file comment). It checks every
// store before writing anything, so a dirty working tree aborts the flush
// with nothing written.
func (rs *Store) flushDeferredLocked(version int64, cInfo *types.CommitInfo) error {
	type named struct {
		key types.StoreKey
		st  *iavl.Store
	}
	var stores []named
	for key, store := range rs.stores {
		if store.GetStoreType() != types.StoreTypeIAVL {
			continue
		}
		st, ok := rs.GetCommitKVStore(key).(*iavl.Store)
		if !ok {
			continue
		}
		if st.IsDirty() {
			return fmt.Errorf("deferred flush at version %d: store %s has uncommitted changes; nothing written", version, key.Name())
		}
		stores = append(stores, named{key, st})
	}

	// (1) IAVL nodes, orphans and roots; one batch per store
	for _, n := range stores {
		if err := n.st.FlushDeferred(); err != nil {
			return fmt.Errorf("deferred flush of store %s at version %d: %w", n.key.Name(), version, err)
		}
	}

	// (2) metadata and marker; from here on a restart loads version
	rs.flushMetadataWithMarker(version, cInfo, true, version)
	rs.deferred.lastFlushed = version

	// (3) older on-disk versions (the pruning manager is bypassed)
	for _, n := range stores {
		if err := n.st.DeleteVersionsBefore(version); err != nil {
			// not fatal: retried at the next flush
			rs.logger.Error("deferred persistence: failed to delete old versions", "store", n.key.Name(), "version", version, "err", err)
		}
	}
	rs.logger.Debug("deferred persistence: flushed", "version", version)
	return nil
}

// flushMetadataWithMarker is flushMetadata plus the deferred marker, in the
// same atomic batch.
func (rs *Store) flushMetadataWithMarker(version int64, cInfo *types.CommitInfo, deferred bool, lastFlushed int64) {
	batch := rs.db.NewBatch()
	defer batch.Close()
	flushCommitInfo(batch, version, cInfo)
	flushLatestVersion(batch, version)
	v := []byte{0}
	if deferred {
		v[0] = 1
	}
	v = binary.AppendUvarint(v, uint64(lastFlushed))
	if err := batch.Set([]byte(deferredMarkerKey), v); err != nil {
		panic(fmt.Errorf("error on batch set %w", err))
	}
	if err := batch.WriteSync(); err != nil {
		panic(fmt.Errorf("error on batch write %w", err))
	}
}

// DeferredMarker reads how the DB's metadata was last written: deferred is
// true if by a deferred flush (only the last flushed version, lastFlushed, is
// on disk). found is false for DBs never written in deferred mode.
func DeferredMarker(db dbm.DB) (deferred bool, lastFlushed int64, found bool, err error) {
	bz, err := db.Get([]byte(deferredMarkerKey))
	if err != nil || bz == nil {
		return false, 0, false, err
	}
	v, n := binary.Uvarint(bz[1:])
	if len(bz) < 2 || n <= 0 {
		return false, 0, false, fmt.Errorf("corrupt deferred persistence marker %X", bz)
	}
	return bz[0] == 1, int64(v), true, nil
}

// checkRollbackAllowed refuses an app state rollback to target on a DB last
// written by a deferred flush (only that version is on disk) or whose last
// deferred flush is above target.
func (rs *Store) checkRollbackAllowed(target int64) error {
	deferred, lastFlushed, found, err := DeferredMarker(rs.db)
	if err != nil || !found {
		return err
	}
	if deferred || target < lastFlushed {
		return fmt.Errorf("%w: the application DB was written with deferred IAVL persistence and holds only version %d (last flush), not %d; "+
			"roll back only the CometBFT state (rollback --cometbft-only) and restart: the handshake replays the blocks after %d",
			ErrRollbackDeferred, lastFlushed, target, lastFlushed)
	}
	return nil
}

// checkQueryHeight returns a clear error for heights deferred persistence
// does not keep (between the last flush and the latest version).
func (rs *Store) checkQueryHeight(version int64) error {
	rs.deferred.mtx.Lock()
	defer rs.deferred.mtx.Unlock()
	if !rs.deferred.active || rs.lastCommitInfo == nil {
		return nil
	}
	latest := rs.lastCommitInfo.Version
	if version > 0 && version != latest && version != rs.deferred.lastFlushed {
		return fmt.Errorf("height %d is not available: deferred IAVL persistence keeps only heights %d (last flush) and %d (latest) while catching up", version, rs.deferred.lastFlushed, latest)
	}
	return nil
}

// commitDeferred is Commit while deferred persistence is active.
func (rs *Store) commitDeferred() types.CommitID {
	rs.deferred.mtx.Lock()
	defer rs.deferred.mtx.Unlock()
	if rs.deferred.closed {
		// see ShutdownFlush: never answer for a block that is not on disk
		rs.logger.Error("deferred persistence: Commit after shutdown flush; blocking until the process exits")
		rs.deferred.mtx.Unlock()
		select {}
	}

	var version int64
	if rs.lastCommitInfo.GetVersion() == 0 && rs.initialVersion > 1 {
		version = rs.initialVersion
	} else {
		version = rs.lastCommitInfo.GetVersion() + 1
	}

	// soft commits: hashes computed, nothing written
	rs.lastCommitInfo = commitStores(version, rs.stores, rs.removalMap)

	leave := rs.deferred.notCatchingUp
	flush := leave || rs.deferred.forceNext ||
		version%rs.deferred.every == 0 ||
		rs.deferred.lastFlushed == 0 ||
		len(rs.removalMap) > 0
	rs.deferred.forceNext = false
	if flush {
		// before removed stores leave rs.stores, so they are flushed too
		if err := rs.flushDeferredLocked(version, rs.lastCommitInfo); err != nil {
			panic(err)
		}
	}

	// remove remnants of removed stores (as upstream)
	for sk := range rs.removalMap {
		if _, ok := rs.stores[sk]; ok {
			delete(rs.stores, sk)
			delete(rs.storesParams, sk)
			delete(rs.keysByName, sk.Name())
		}
	}
	rs.removalMap = make(map[types.StoreKey]bool)

	if leave {
		rs.leaveDeferredLocked(version)
	}
	// NOTE: no handlePruning (see file comment)
	return types.CommitID{
		Version: version,
		Hash:    rs.lastCommitInfo.Hash(),
	}
}

// leaveDeferredLocked switches every store back to per-block persistence
// after the flush of version.
func (rs *Store) leaveDeferredLocked(version int64) {
	for key, store := range rs.stores {
		if store.GetStoreType() != types.StoreTypeIAVL {
			continue
		}
		if st, ok := rs.GetCommitKVStore(key).(*iavl.Store); ok {
			if err := st.SetDeferredPersistence(false); err != nil {
				panic(fmt.Errorf("leaving deferred persistence: store %s: %w", key.Name(), err))
			}
		}
	}
	rs.flushMetadataWithMarker(version, rs.lastCommitInfo, false, version)
	rs.deferred.active = false
	unregisterDeferredStore(rs)
	rs.logger.Info("deferred persistence: caught up, per-block persistence from now on", "version", version)
}

// One shutdown hook for the process flushes every active deferred store.
var (
	deferredStoresMu sync.Mutex
	deferredStores   = map[*Store]struct{}{}
	registerHookOnce sync.Once
)

func registerDeferredStore(rs *Store) {
	registerHookOnce.Do(func() { shutdown.Register(shutdownFlushAll) })
	deferredStoresMu.Lock()
	deferredStores[rs] = struct{}{}
	deferredStoresMu.Unlock()
}

func unregisterDeferredStore(rs *Store) {
	deferredStoresMu.Lock()
	delete(deferredStores, rs)
	deferredStoresMu.Unlock()
}

func shutdownFlushAll() {
	deferredStoresMu.Lock()
	stores := make([]*Store, 0, len(deferredStores))
	for rs := range deferredStores {
		stores = append(stores, rs)
	}
	deferredStoresMu.Unlock()
	for _, rs := range stores {
		if err := rs.ShutdownFlush(); err != nil {
			rs.logger.Error("deferred persistence: flush on shutdown failed; the restart rolls back to the last complete flush and replays", "err", err)
		}
	}
}

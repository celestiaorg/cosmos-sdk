package rootmulti

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	iavltree "github.com/cosmos/iavl"

	"cosmossdk.io/store/iavl"
	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/types"
)

// Celestia sync-speedup prototype: deferred IAVL persistence driver.
//
// CELESTIA_IAVL_FLUSH_EVERY=K (K > 1), enabled by LoadLatestVersion*:
//   - Commit soft-commits every IAVL store (same versions, hashes, CommitInfo
//     and LastCommitID as upstream) and writes nothing but the fast-node index.
//   - A flush writes, in this order: (1) every IAVL store's latest soft
//     version, (2) the commit metadata (commit info + latest version,
//     WriteSync), (3) deletion of every store's previous on-disk version.
//     The metadata is the commit point: on load every store is rolled back
//     to it (versions above are deleted, a store behind it fails the load),
//     so a kill at any point is recoverable and cometbft replays the blocks
//     after the last flush.
//   - Flush triggers: version % K == 0, the first commit, a commit that
//     removes stores, a pending x/upgrade plan (the app-version-change block
//     and any block before a scheduled upgrade), more than
//     CELESTIA_IAVL_FLUSH_MAX_NODES unflushed nodes (default 4,000,000),
//     ForceFlushNextCommit, and explicit FlushDeferred (shutdown, halt).
//   - FlushDeferred works mid-block: it writes the last soft-committed
//     versions and leaves uncommitted working changes in memory (that block
//     is replayed after a restart).
//   - The pruning manager is bypassed: exactly one version stays on disk
//     (as pruning "everything" with keep-recent 0). With pruning "nothing"
//     the mode stays off. Trees with legacy-format versions (right after the
//     v3->v4 migration) make the multistore load in upstream mode instead. snapshot-interval must be
//     0. CELESTIA_IAVL_ASYNC_PRUNING is ignored. CELESTIA_PARALLEL_STORES
//     also parallelises the flush phases.

const upgradeStoreName = "upgrade" // x/upgrade StoreKey; PlanKey() = {0x00}

type deferredDriver struct {
	every      int64
	maxPending int

	active    atomic.Bool
	mu        sync.Mutex // serialises Commit and flushes
	forceNext bool
	flushed   int64 // version recorded in the commit metadata
	planNoted bool
}

func newDeferredDriver() *deferredDriver {
	every := iavl.CelestiaFlushEvery()
	if every <= 1 {
		return nil
	}
	maxPending := 4_000_000
	if v, err := strconv.Atoi(os.Getenv("CELESTIA_IAVL_FLUSH_MAX_NODES")); err == nil && v > 0 {
		maxPending = v
	}
	return &deferredDriver{every: every, maxPending: maxPending}
}

func (d *deferredDriver) isActive() bool {
	return d != nil && d.active.Load()
}

// deferredWanted reports whether LoadLatestVersion* should enable the mode.
// With pruning "nothing" it stays off: enabling it would irreversibly prune
// the history to one version.
func (rs *Store) deferredWanted() bool {
	d := rs.celestiaDeferred
	if d == nil {
		return false
	}
	if rs.pruningManager.GetOptions().GetPruningStrategy() == pruningtypes.PruningNothing {
		fmt.Fprintf(os.Stderr, "celestia: CELESTIA_IAVL_FLUSH_EVERY ignored: pruning = \"nothing\" (deferred persistence keeps one version)\n")
		return false
	}
	return true
}

// deferredLegacyFallback reports whether a deferred load failed because a
// tree still has legacy-format versions (right after the v3->v4 migration);
// the caller then loads in upstream mode for this run.
func deferredLegacyFallback(err error) bool {
	if err == nil || !errors.Is(err, iavltree.ErrDeferredLegacy) {
		return false
	}
	fmt.Fprintf(os.Stderr, "celestia: deferred IAVL persistence disabled for this run: %v\n", err)
	return true
}

// deferredBeforeLoad flushes pending soft commits before any store reload
// and disables the mode until the next LoadLatestVersion*.
func (rs *Store) deferredBeforeLoad() error {
	d := rs.celestiaDeferred
	if !d.isActive() {
		return nil
	}
	if err := rs.FlushDeferred(); err != nil {
		return fmt.Errorf("deferred persistence: flush before reload: %w", err)
	}
	d.active.Store(false)
	return nil
}

func (rs *Store) deferredAfterLoad(ver int64, enabled bool) {
	d := rs.celestiaDeferred
	if d == nil || !enabled {
		return
	}
	d.mu.Lock()
	d.flushed = ver
	d.forceNext = false
	d.mu.Unlock()
	d.active.Store(true)
	fmt.Fprintf(os.Stderr, "celestia: deferred IAVL persistence enabled: flush every %d blocks, loaded version %d\n", d.every, ver)
}

// ForceFlushNextCommit makes the next Commit flush (deferred mode only).
func (rs *Store) ForceFlushNextCommit() {
	if d := rs.celestiaDeferred; d.isActive() {
		d.mu.Lock()
		d.forceNext = true
		d.mu.Unlock()
	}
}

// FlushDeferred durably writes the last soft-committed version (stores,
// metadata, deletions). Safe to call mid-block and from another goroutine
// than the ABCI flow; it is serialised with Commit.
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
	err := rs.flushDeferredLocked(ci.Version, ci, nil)
	if err == nil {
		fmt.Fprintf(os.Stderr, "celestia: deferred IAVL flush at %d (explicit)\n", ci.Version)
	}
	return err
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

	tm := celestiaStartCommitTiming(version)
	newCommitInfo := commitStores(version, rs.stores, rs.removalMap)
	newCommitInfo.Timestamp = rs.commitHeader.Time
	tm.mark(&tm.commitStores)
	rs.setLastCommitInfo(newCommitInfo)

	if reason := rs.deferredFlushReason(version); reason != "" {
		if err := rs.flushDeferredLocked(version, newCommitInfo, tm); err != nil {
			panic(fmt.Errorf("deferred persistence: flush at %d (%s): %w", version, reason, err))
		}
		if reason != "periodic" && reason != "upgrade plan pending (quiet)" {
			fmt.Fprintf(os.Stderr, "celestia: deferred IAVL flush at %d (%s)\n", version, reason)
		}
	}
	tm.done()

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

func (rs *Store) deferredFlushReason(version int64) string {
	d := rs.celestiaDeferred
	switch {
	case d.forceNext:
		return "forced"
	case d.flushed == 0:
		return "first commit"
	case len(rs.removalMap) > 0:
		return "store removal"
	case rs.upgradePlanPending():
		if !d.planNoted {
			d.planNoted = true
			return "upgrade plan pending"
		}
		return "upgrade plan pending (quiet)"
	case version%d.every == 0:
		return "periodic"
	}
	d.planNoted = false
	n := 0
	for _, st := range rs.deferredStores() {
		n += st.DeferredPendingNodes()
	}
	if n > d.maxPending {
		return fmt.Sprintf("%d pending nodes", n)
	}
	return ""
}

// upgradePlanPending reports whether x/upgrade has a plan stored. The
// celestia signal upgrade schedules one in the EndBlocker of the block that
// changes the app version, so that block's commit is flushed before the
// multiplexer switches binaries.
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

// deferredStores returns the deferred IAVL stores, sorted by name, including
// stores about to be removed.
func (rs *Store) deferredStores() []*iavl.Store {
	keys := make([]types.StoreKey, 0, len(rs.stores))
	for k := range rs.stores {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name() < keys[j].Name() })
	out := make([]*iavl.Store, 0, len(keys))
	for _, k := range keys {
		if st, ok := rs.GetCommitKVStore(k).(*iavl.Store); ok && st.IsDeferred() {
			out = append(out, st)
		}
	}
	return out
}

func forEachStore(stores []*iavl.Store, f func(*iavl.Store) error) error {
	errs := make([]error, len(stores))
	if celestiaParallelStores && len(stores) > 1 {
		runParallel(len(stores), func(i int) { errs[i] = f(stores[i]) })
	} else {
		for i, st := range stores {
			errs[i] = f(st)
		}
	}
	return errors.Join(errs...)
}

// flushDeferredLocked runs the three flush phases for version. d.mu is held.
func (rs *Store) flushDeferredLocked(version int64, cInfo *types.CommitInfo, tm *commitTiming) error {
	d := rs.celestiaDeferred
	stores := rs.deferredStores()
	var start time.Time
	if tm != nil && tm.enabled {
		start = time.Now()
	}

	// (1) stores. A failure leaves the metadata at d.flushed; the stores that
	// did write are rolled back on the next load.
	if err := forEachStore(stores, (*iavl.Store).FlushDeferred); err != nil {
		return err
	}
	if celestiaDeferredTestHook != nil {
		celestiaDeferredTestHook("stores", version)
	}
	var tFlush time.Time
	if !start.IsZero() {
		tFlush = time.Now()
	}

	// (2) commit point
	rs.flushMetadata(rs.db, version, cInfo)
	d.flushed = version
	if celestiaDeferredTestHook != nil {
		celestiaDeferredTestHook("metadata", version)
	}
	var tMeta time.Time
	if !start.IsZero() {
		tMeta = time.Now()
	}

	// (3) deletions; a failure is retried by the next flush or load.
	if err := forEachStore(stores, (*iavl.Store).PruneDeferred); err != nil {
		rs.logger.Error("deferred persistence: pruning the previous version failed; will retry", "err", err)
		fmt.Fprintf(os.Stderr, "celestia: deferred IAVL prune at %d failed (will retry): %v\n", version, err)
	}
	if !start.IsZero() {
		tm.prune = tFlush.Sub(start) + time.Since(tMeta)
		tm.flushMeta = tMeta.Sub(tFlush)
		tm.last = time.Now()
	}
	return nil
}

// celestiaDeferredTestHook, if set (tests only), is called after the store
// phase ("stores") and after the metadata phase ("metadata") of a flush.
var celestiaDeferredTestHook func(phase string, version int64)

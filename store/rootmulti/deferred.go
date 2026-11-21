package rootmulti

// Deferred persistence (celestia genesis-sync prototype).
//
// With CELESTIA_IAVL_FLUSH_EVERY=K (K > 1) every Commit still computes every
// IAVL root hash and the app hash exactly as upstream, but IAVL nodes, orphan
// records, roots and the multistore metadata (commit info + latest version)
// are only written to the DB on "flush" commits:
//   - version % K == 0,
//   - the first commit of a fresh store (nothing on disk yet),
//   - commits that remove stores (store upgrades),
//   - the commit following ForceFlushNextCommit (baseapp: app-version change),
//   - explicitly via FlushDeferred (baseapp: before a store reload, on halt and
//     in BaseApp.Close on SIGTERM/SIGINT).
// A flush is done in three phases so that a crash at any point leaves a
// loadable DB: (1) every IAVL store writes version V, (2) commit info + latest
// version V are written with WriteSync, (3) only then are the older on-disk
// IAVL versions deleted. On restart the latest flushed version is loaded and
// the consensus engine replays the missing blocks (handshake).
//
// The SDK pruning manager is bypassed in this mode (it would queue every height
// and DeleteVersions never-persisted versions, and it does a SetSync per
// block); after each flush every older on-disk version is deleted unless the
// pruning strategy is "nothing". IAVL fast storage is forced off.

import (
	"fmt"
	"os"
	"strconv"
	"sync"

	pruningtypes "github.com/cosmos/cosmos-sdk/pruning/types"
	"github.com/cosmos/cosmos-sdk/server/shutdown"
	"github.com/cosmos/cosmos-sdk/store/iavl"
	"github.com/cosmos/cosmos-sdk/store/types"
)

// FlushEveryEnv is the environment variable enabling deferred persistence.
const FlushEveryEnv = "CELESTIA_IAVL_FLUSH_EVERY"

type deferredState struct {
	every       int64 // flush interval; <= 1 means upstream behaviour
	lastFlushed int64 // latest version whose state + metadata are on disk
	forceNext   bool  // make the next Commit a flush
	closed      bool  // ShutdownFlush ran: no further commits
	// set by loadVersion while loading the latest committed version
	rollbackAhead bool
	mtx           sync.Mutex
}

func flushEveryFromEnv() int64 {
	s := os.Getenv(FlushEveryEnv)
	if s == "" {
		return 0
	}
	k, err := strconv.ParseInt(s, 10, 64)
	if err != nil || k < 0 {
		panic(fmt.Sprintf("invalid %s=%q", FlushEveryEnv, s))
	}
	return k
}

// registerShutdownFlush runs ShutdownFlush on SIGINT/SIGTERM
// (server.WaitForQuitSignals). Output goes to stderr because the multistore
// logger is a no-op logger in baseapp.
func (rs *Store) registerShutdownFlush() {
	fmt.Fprintf(os.Stderr, "IAVL deferred persistence enabled: %s=%d (fast node disabled)\n", FlushEveryEnv, rs.deferred.every)
	shutdown.Register(func() {
		if err := rs.ShutdownFlush(); err != nil {
			fmt.Fprintf(os.Stderr, "deferred persistence: flush on shutdown failed (restart rolls back to the last complete flush and replays): %v\n", err)
		}
	})
}

// ShutdownFlush writes the latest soft-committed version and its metadata and
// then closes the store for commits. It works in the middle of a block: the
// node may be anywhere in block H (e.g. between deliverState.ms.Write() and
// Commit), so IAVL working trees can be dirty. Their changes belong to block
// H, which the consensus engine replays after restart, so they are simply not
// written (FlushDeferredIgnoringWorking flushes lastSaved = H-1 for every
// store, all at the same version). Any later Commit blocks forever: it must
// not return an app hash for block H that the DB will never contain, and the
// working trees must not be committed after the flush (see
// FlushDeferredIgnoringWorking). The process is exiting anyway.
// Idempotent; no-op when deferred persistence is disabled.
func (rs *Store) ShutdownFlush() error {
	if !rs.deferredEnabled() {
		return nil
	}
	rs.deferred.mtx.Lock()
	defer rs.deferred.mtx.Unlock()
	if rs.deferred.closed {
		return nil
	}
	rs.deferred.closed = true
	if rs.lastCommitInfo == nil || rs.lastCommitInfo.Version <= rs.deferred.lastFlushed {
		return nil
	}
	if err := rs.flushDeferredLocked(rs.lastCommitInfo.Version, rs.lastCommitInfo, true); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "deferred persistence: flushed version %d on shutdown\n", rs.deferred.lastFlushed)
	return nil
}

// rollbackStoreAhead rolls back a freshly loaded IAVL store whose on-disk
// versions are ahead of the commit info version (see
// iavl.Store.RollbackVersionsAbove).
func (rs *Store) rollbackStoreAhead(key types.StoreKey, st *iavl.Store, version int64) error {
	latest, rolled, err := st.RollbackVersionsAbove(version, rs.deferredEnabled())
	if err != nil {
		return fmt.Errorf("store %s: %w", key.Name(), err)
	}
	if rolled {
		rs.logger.Info("rolled back IAVL store ahead of commit info", "store", key.Name(), "on_disk", latest, "commit_info", version)
		fmt.Fprintf(os.Stderr, "deferred persistence: store %s had versions up to %d on disk, rolled back to commit info version %d\n", key.Name(), latest, version)
	}
	return nil
}

// DeferredPersistence returns the flush interval (0 or 1 = disabled).
func (rs *Store) DeferredPersistence() int64 { return rs.deferred.every }

func (rs *Store) deferredEnabled() bool { return rs.deferred.every > 1 }

// ForceFlushNextCommit makes the next Commit write everything to disk.
func (rs *Store) ForceFlushNextCommit() {
	rs.deferred.mtx.Lock()
	rs.deferred.forceNext = true
	rs.deferred.mtx.Unlock()
}

// FlushDeferred writes the latest soft-committed version and its metadata to
// disk. No-op when deferred persistence is disabled or nothing is pending.
// It fails (without writing anything for the dirty store) if an IAVL working
// tree holds uncommitted changes, so it must be called between blocks.
func (rs *Store) FlushDeferred() error {
	if !rs.deferredEnabled() {
		return nil
	}
	rs.deferred.mtx.Lock()
	defer rs.deferred.mtx.Unlock()
	if rs.deferred.closed || rs.lastCommitInfo == nil || rs.lastCommitInfo.Version <= rs.deferred.lastFlushed {
		return nil
	}
	return rs.flushDeferredLocked(rs.lastCommitInfo.Version, rs.lastCommitInfo, false)
}

// flushDeferredLocked: phase 0 checks every store before anything is written
// (all-or-nothing for the dirty-tree case); if a write fails or the process
// dies during phase 1, some stores are ahead of the commit info on disk and
// are rolled back on the next load (rollbackStoreAhead).
func (rs *Store) flushDeferredLocked(version int64, cInfo *types.CommitInfo, ignoreWorking bool) error {
	// Phase 0: collect the IAVL stores (unwrapping the inter-block cache) and
	// refuse up front if any working tree is dirty.
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
		if !ignoreWorking && st.IsDirty() {
			return fmt.Errorf("deferred flush at version %d: store %s has uncommitted changes; nothing written", version, key.Name())
		}
		stores = append(stores, named{key, st})
	}

	// Phase 1: IAVL nodes/orphans/roots of every store (one atomic batch per
	// store).
	var flushed []*iavl.Store
	for _, n := range stores {
		flush := n.st.FlushDeferred
		if ignoreWorking {
			flush = n.st.FlushDeferredIgnoringWorking
		}
		if err := flush(); err != nil {
			return fmt.Errorf("deferred flush of store %s at version %d: %w", n.key.Name(), version, err)
		}
		flushed = append(flushed, n.st)
	}

	// Phase 2: multistore metadata (WriteSync). From here on a restart loads
	// version.
	rs.flushMetadata(rs.db, version, cInfo)
	rs.deferred.lastFlushed = version

	// Phase 3: delete the older on-disk versions.
	if rs.pruningManager.GetOptions().GetPruningStrategy() != pruningtypes.PruningNothing {
		for _, st := range flushed {
			if err := st.DeleteVersionsBefore(version); err != nil {
				// not fatal: retried at the next flush
				rs.logger.Error("deferred persistence: failed to delete old versions", "version", version, "err", err)
			}
		}
	}
	rs.logger.Debug("deferred persistence: flushed", "version", version)
	return nil
}

// commitDeferred is Commit in deferred mode.
func (rs *Store) commitDeferred() types.CommitID {
	rs.deferred.mtx.Lock()
	defer rs.deferred.mtx.Unlock()
	if rs.deferred.closed {
		// see ShutdownFlush: never answer for a block that is not on disk
		fmt.Fprintf(os.Stderr, "deferred persistence: Commit after shutdown flush; blocking until the process exits\n")
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

	flush := rs.deferred.forceNext ||
		version%rs.deferred.every == 0 ||
		rs.deferred.lastFlushed == 0 ||
		len(rs.removalMap) > 0
	rs.deferred.forceNext = false
	if flush {
		// before removed stores leave rs.stores, so they are flushed too
		if err := rs.flushDeferredLocked(version, rs.lastCommitInfo, false); err != nil {
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

	// NOTE: no handlePruning (see file comment).
	return types.CommitID{
		Version: version,
		Hash:    rs.lastCommitInfo.Hash(),
	}
}

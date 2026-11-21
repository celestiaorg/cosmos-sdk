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
	mtx         sync.Mutex
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

// registerShutdownFlush flushes on SIGINT/SIGTERM (server.WaitForQuitSignals).
// The node may still be processing a block at that moment: the flush is
// serialised with Commit by the mutex and refuses to run while a working tree
// is dirty (then the restart simply replays from the previous flush). Output
// goes to stderr because the multistore logger is a no-op logger in baseapp.
func (rs *Store) registerShutdownFlush() {
	fmt.Fprintf(os.Stderr, "IAVL deferred persistence enabled: %s=%d (fast node disabled)\n", FlushEveryEnv, rs.deferred.every)
	shutdown.Register(func() {
		if err := rs.FlushDeferred(); err != nil {
			fmt.Fprintf(os.Stderr, "deferred persistence: flush on shutdown failed (restart will replay from the last flush): %v\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "deferred persistence: flushed version %d on shutdown\n", rs.deferred.lastFlushed)
	})
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
	if rs.lastCommitInfo == nil || rs.lastCommitInfo.Version <= rs.deferred.lastFlushed {
		return nil
	}
	return rs.flushDeferredLocked(rs.lastCommitInfo.Version, rs.lastCommitInfo)
}

func (rs *Store) flushDeferredLocked(version int64, cInfo *types.CommitInfo) error {
	// Phase 1: IAVL nodes/orphans/roots of every store.
	var flushed []*iavl.Store
	for key, store := range rs.stores {
		if store.GetStoreType() != types.StoreTypeIAVL {
			continue
		}
		// unwrap the inter-block cache
		st, ok := rs.GetCommitKVStore(key).(*iavl.Store)
		if !ok {
			continue
		}
		if err := st.FlushDeferred(); err != nil {
			return fmt.Errorf("deferred flush of store %s at version %d: %w", key.Name(), version, err)
		}
		flushed = append(flushed, st)
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

	// NOTE: no handlePruning (see file comment).
	return types.CommitID{
		Version: version,
		Hash:    rs.lastCommitInfo.Hash(),
	}
}

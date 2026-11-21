package iavl

import (
	"os"
	"strconv"

	"github.com/cosmos/iavl"
)

// Celestia sync-speedup prototype: deferred IAVL persistence.
//
// CELESTIA_IAVL_FLUSH_EVERY=K (K > 1): the multistore (rootmulti) loads every
// IAVL tree with iavl.MutableTree.LoadVersionDeferred and Commit only
// soft-commits each tree (hashes and versions exactly as upstream, nodes kept
// in memory). Every K blocks, and on forced occasions, rootmulti writes the
// trees (FlushDeferred), then the commit metadata, then deletes the previous
// on-disk version (PruneDeferred). Unset, 0 or 1: upstream behaviour.

var celestiaFlushEvery = func() int64 {
	k, err := strconv.ParseInt(os.Getenv("CELESTIA_IAVL_FLUSH_EVERY"), 10, 64)
	if err != nil || k < 0 {
		return 0
	}
	return k
}()

// CelestiaFlushEvery returns K from CELESTIA_IAVL_FLUSH_EVERY (0 if unset).
func CelestiaFlushEvery() int64 { return celestiaFlushEvery }

func (st *Store) mutableTree() *iavl.MutableTree {
	t, _ := st.tree.(*iavl.MutableTree)
	return t
}

// IsDeferred reports whether the store's tree runs in deferred mode.
func (st *Store) IsDeferred() bool {
	t := st.mutableTree()
	return t != nil && t.IsDeferred()
}

// FlushDeferred writes the latest soft-committed version of the tree.
// Uncommitted working changes are left untouched.
func (st *Store) FlushDeferred() error {
	if t := st.mutableTree(); t != nil {
		return t.FlushDeferred()
	}
	return nil
}

// PruneDeferred deletes the version that was on disk before the last flush.
func (st *Store) PruneDeferred() error {
	if t := st.mutableTree(); t != nil {
		return t.PruneDeferred()
	}
	return nil
}

// DeferredPendingNodes returns the number of unflushed nodes held in memory.
func (st *Store) DeferredPendingNodes() int {
	if t := st.mutableTree(); t != nil {
		return t.DeferredPendingNodes()
	}
	return 0
}

// DeferredFlushedVersion returns the latest on-disk version of the tree.
func (st *Store) DeferredFlushedVersion() int64 {
	if t := st.mutableTree(); t != nil {
		return t.DeferredFlushedVersion()
	}
	return 0
}

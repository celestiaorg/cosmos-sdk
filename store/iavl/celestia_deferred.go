package iavl

import (
	"github.com/cosmos/iavl"
)

// Deferred IAVL persistence: Commit keeps new tree nodes in memory and the
// multistore (rootmulti) writes them every few blocks. Hashes and versions are
// exactly as upstream. See rootmulti/celestia_deferred.go.

func (st *Store) mutableTree() *iavl.MutableTree {
	t, _ := st.tree.(*iavl.MutableTree)
	return t
}

// IsDeferred reports whether the store's tree runs in deferred mode.
func (st *Store) IsDeferred() bool {
	t := st.mutableTree()
	return t != nil && t.IsDeferred()
}

// EnableDeferred turns deferred mode on. It writes nothing and fails if the
// tree has any on-disk version besides its latest.
func (st *Store) EnableDeferred() error {
	if t := st.mutableTree(); t != nil {
		return t.EnableDeferred()
	}
	return nil
}

// DisableDeferred turns deferred mode off once everything is flushed and pruned.
func (st *Store) DisableDeferred() error {
	if t := st.mutableTree(); t != nil {
		return t.DisableDeferred()
	}
	return nil
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
// It returns iavl.ErrDeferredBusy while that version has active readers.
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

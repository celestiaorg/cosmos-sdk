package iavl

// Deferred persistence helpers (celestia genesis-sync prototype). See
// rootmulti/deferred.go for the driver and github.com/cosmos/iavl deferred.go
// for the tree-level semantics.

import (
	"fmt"

	"github.com/cosmos/iavl"
)

func (st *Store) mutableTree() (*iavl.MutableTree, error) {
	mt, ok := st.tree.(*iavl.MutableTree)
	if !ok {
		return nil, fmt.Errorf("deferred persistence requires a mutable IAVL tree, got %T", st.tree)
	}
	return mt, nil
}

// RollbackVersionsAbove removes on-disk versions above loaded (the version
// recorded in the multistore commit info). They are left behind when the
// process died during a deferred flush after some stores wrote version V but
// before the commit info for V was written. Soft commits cannot reuse such
// versions, and the upstream SaveVersion path fails on them with "must save
// consecutive versions" unless the gap is exactly one version (the only
// inconsistency upstream itself can produce, handled by its idempotent
// SaveVersion branch). Therefore: strict (deferred mode) rolls back whenever
// the store is ahead; otherwise only when it is ahead by more than one
// version, so upstream behaviour is unchanged for every state upstream can
// produce. Returns the on-disk latest version found before the rollback.
func (st *Store) RollbackVersionsAbove(loaded int64, strict bool) (int64, bool, error) {
	mt, err := st.mutableTree()
	if err != nil || loaded <= 0 {
		return 0, false, err
	}
	latest, err := mt.LatestVersionOnDisk()
	if err != nil {
		return 0, false, err
	}
	if latest <= loaded || (!strict && latest == loaded+1) {
		return latest, false, nil
	}
	if _, err := mt.LoadVersionForOverwriting(loaded); err != nil {
		return latest, false, fmt.Errorf("rolling back IAVL versions %d..%d to %d: %w", loaded+1, latest, loaded, err)
	}
	return latest, true, nil
}

// EnableDeferredPersistence switches SaveVersion to soft commits. Call
// RollbackVersionsAbove(loaded, true) first.
func (st *Store) EnableDeferredPersistence() error {
	mt, err := st.mutableTree()
	if err != nil {
		return err
	}
	return mt.SetDeferredPersistence(true)
}

// FlushDeferred writes the latest soft-committed version to disk (no-op when
// nothing is pending). Old versions are kept; see DeleteVersionsBefore. Fails
// if the working tree has uncommitted changes.
func (st *Store) FlushDeferred() error {
	mt, err := st.mutableTree()
	if err != nil {
		return err
	}
	return mt.FlushDeferred()
}

// FlushDeferredIgnoringWorking is FlushDeferred for shutdown: it writes the
// latest soft-committed version even if the working tree is dirty (in-progress
// block); the working-tree changes are not written.
func (st *Store) FlushDeferredIgnoringWorking() error {
	mt, err := st.mutableTree()
	if err != nil {
		return err
	}
	return mt.FlushDeferredIgnoringWorking()
}

// IsDirty reports whether the working tree has uncommitted changes.
func (st *Store) IsDirty() bool {
	mt, err := st.mutableTree()
	return err == nil && mt.IsDirty()
}

// HasUnflushedVersions reports whether soft-committed versions are pending.
func (st *Store) HasUnflushedVersions() bool {
	mt, err := st.mutableTree()
	return err == nil && mt.HasUnflushedVersions()
}

// DeleteVersionsBefore removes every on-disk version lower than version.
func (st *Store) DeleteVersionsBefore(version int64) error {
	mt, err := st.mutableTree()
	if err != nil {
		return err
	}
	return mt.DeleteVersionsBefore(version)
}

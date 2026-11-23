package iavl

// Deferred persistence helpers. See rootmulti/deferred.go for the driver and
// the deferred.go of the patched github.com/cosmos/iavl for the tree side.

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

// SetDeferredPersistence switches SaveVersion between soft commits (true) and
// upstream saves. Call RollbackVersionsAbove(loaded, true) before enabling.
// Disabling requires a prior FlushDeferred.
func (st *Store) SetDeferredPersistence(enabled bool) error {
	mt, err := st.mutableTree()
	if err != nil {
		return err
	}
	if !enabled && mt.HasUnflushedVersions() {
		return fmt.Errorf("cannot leave deferred persistence with unflushed versions")
	}
	return mt.SetDeferredPersistence(enabled)
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

// IsDirty reports whether the working tree has uncommitted changes.
func (st *Store) IsDirty() bool {
	mt, err := st.mutableTree()
	return err == nil && mt.IsDirty()
}

// DeleteVersionsBefore removes every on-disk version lower than version.
func (st *Store) DeleteVersionsBefore(version int64) error {
	mt, err := st.mutableTree()
	if err != nil {
		return err
	}
	return mt.DeleteVersionsBefore(version)
}

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

// EnableDeferredPersistence prepares a freshly loaded store (loaded at
// version loaded) for deferred persistence:
//  1. on-disk versions newer than loaded are removed (they are left behind when
//     the process died after some stores flushed version V but before the
//     multistore metadata for V was written; upstream relies on the idempotent
//     SaveVersion path for that case, which soft commits cannot use);
//  2. SaveVersion is switched to soft commits.
func (st *Store) EnableDeferredPersistence(loaded int64) error {
	mt, err := st.mutableTree()
	if err != nil {
		return err
	}
	if loaded > 0 {
		for _, v := range mt.AvailableVersions() {
			if int64(v) > loaded {
				if st.logger != nil {
					st.logger.Info("deferred persistence: removing IAVL versions above the committed version", "loaded", loaded, "found", v)
				}
				if _, err := mt.LoadVersionForOverwriting(loaded); err != nil {
					return err
				}
				break
			}
		}
	}
	return mt.SetDeferredPersistence(true)
}

// FlushDeferred writes the latest soft-committed version to disk (no-op when
// nothing is pending). Old versions are kept; see DeleteVersionsBefore.
func (st *Store) FlushDeferred() error {
	mt, err := st.mutableTree()
	if err != nil {
		return err
	}
	return mt.FlushDeferred()
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

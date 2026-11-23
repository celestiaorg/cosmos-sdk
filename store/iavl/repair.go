package iavl

import (
	"fmt"

	"github.com/cosmos/iavl"
)

// RollbackVersionsAbove removes on-disk versions above loaded (the version in
// the multistore commit info). They are left when the process died during a
// flush after some stores wrote version V but before the commit info for V.
// With strict unset only stores ahead by more than one version are rolled
// back: ahead by exactly one is the only inconsistency upstream itself can
// produce, and its idempotent SaveVersion already handles it. Returns the
// latest on-disk version found and whether a rollback happened. With lazy
// loading the later versions are unknown and nothing is done.
func (st *Store) RollbackVersionsAbove(loaded int64, strict bool) (int64, bool, error) {
	mt, ok := st.tree.(*iavl.MutableTree)
	if !ok || loaded <= 0 {
		return 0, false, nil
	}
	latest := loaded
	for _, v := range mt.AvailableVersions() {
		if int64(v) > latest {
			latest = int64(v)
		}
	}
	if latest == loaded || (!strict && latest == loaded+1) {
		return latest, false, nil
	}
	if _, err := mt.LoadVersionForOverwriting(loaded); err != nil {
		return latest, false, fmt.Errorf("rolling back IAVL versions %d..%d to %d: %w", loaded+1, latest, loaded, err)
	}
	return latest, true, nil
}

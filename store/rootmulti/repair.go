package rootmulti

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/store/iavl"
	"github.com/cosmos/cosmos-sdk/store/types"
)

// repairStoreAhead rolls back a freshly loaded IAVL store whose on-disk
// versions are ahead of the commit info version. A flush that writes all
// stores and then the commit info (deferred persistence) leaves such stores
// behind when it is cut short; upstream SaveVersion then fails with "must
// save consecutive versions" (see iavl.Store.RollbackVersionsAbove).
func (rs *Store) repairStoreAhead(key types.StoreKey, st *iavl.Store, version int64) error {
	latest, rolled, err := st.RollbackVersionsAbove(version, rs.strictRepair())
	if err != nil {
		return fmt.Errorf("store %s: %w", key.Name(), err)
	}
	if rolled {
		rs.logger.Info("rolled back IAVL store ahead of commit info", "store", key.Name(), "on_disk", latest, "commit_info", version)
	}
	return nil
}

// strictRepair reports whether stores ahead by exactly one version are rolled
// back too (only needed when they may have been written by a deferred flush).
func (rs *Store) strictRepair() bool { return rs.deferred.active }

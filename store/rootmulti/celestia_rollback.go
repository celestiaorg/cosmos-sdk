package rootmulti

import (
	"bytes"
	"fmt"

	"cosmossdk.io/store/iavl"
	"cosmossdk.io/store/types"
)

// CheckRollbackToVersion checks, without writing, that RollbackToVersion(target)
// can succeed, and returns the app hash at target. Rollback commands call it
// before they change CometBFT state.
func (rs *Store) CheckRollbackToVersion(target int64) ([]byte, error) {
	if target <= 0 {
		return nil, fmt.Errorf("invalid rollback height target: %d", target)
	}
	// A DB last written in deferred mode keeps no version to roll back to.
	if err := rs.deferredRollbackCheck(); err != nil {
		return nil, err
	}

	cInfo, err := rs.GetCommitInfo(target)
	if err != nil {
		return nil, fmt.Errorf("commit info at version %d: %w", target, err)
	}
	committed := make(map[string][]byte, len(cInfo.StoreInfos))
	for _, si := range cInfo.StoreInfos {
		committed[si.Name] = si.CommitId.Hash
	}

	mounted := make(map[string]bool, len(rs.storesParams))
	for _, key := range keysFromStoreKeyMap(rs.storesParams) {
		typ := rs.storesParams[key].typ
		if typ == types.StoreTypeTransient || typ == types.StoreTypeMemory {
			continue
		}
		mounted[key.Name()] = true
		hash, ok := committed[key.Name()]
		if !ok {
			return nil, fmt.Errorf("store %s not in commit info at version %d", key.Name(), target)
		}
		if typ != types.StoreTypeIAVL {
			continue
		}
		store, ok := rs.GetCommitKVStore(key).(*iavl.Store)
		if !ok {
			return nil, fmt.Errorf("store %s not loaded", key.Name())
		}
		if !store.VersionExists(target) {
			return nil, fmt.Errorf("store %s has no version %d", key.Name(), target)
		}
		tree, err := store.GetImmutable(target)
		if err != nil {
			return nil, fmt.Errorf("store %s at version %d: %w", key.Name(), target, err)
		}
		if !bytes.Equal(tree.LastCommitID().Hash, hash) {
			return nil, fmt.Errorf("store %s hash at version %d does not match commit info", key.Name(), target)
		}
	}
	for _, si := range cInfo.StoreInfos {
		if !mounted[si.Name] {
			return nil, fmt.Errorf("store %s not mounted", si.Name)
		}
	}
	return cInfo.CommitID().Hash, nil
}

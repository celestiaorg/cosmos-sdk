package rootmulti

// Changeset recording (celestia genesis-sync prototype, CELESTIA_CHANGESET_OUT).
// The IAVL stores hand their committed ops to the recorder in iavl.Store.Commit;
// this file closes each block with the commit info, records store upgrades
// and initial versions, and exposes the hooks the changeset applier needs.

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/store/changeset"
	"github.com/cosmos/cosmos-sdk/store/iavl"
	"github.com/cosmos/cosmos-sdk/store/types"
)

// SetChangesetRecorder enables recording into r (tests / tools). Must be called
// before the stores are loaded. FromEnv is used by default.
func (rs *Store) SetChangesetRecorder(r *changeset.Recorder) { rs.cs = r }

// ChangesetRecorder returns the active recorder (nil if none).
func (rs *Store) ChangesetRecorder() *changeset.Recorder { return rs.cs }

func (rs *Store) recordUpgrade(upgrades *types.StoreUpgrades) {
	if rs.cs == nil || upgrades == nil {
		return
	}
	u := &changeset.Upgrade{
		Added:   append([]string(nil), upgrades.Added...),
		Deleted: append([]string(nil), upgrades.Deleted...),
	}
	for _, r := range upgrades.Renamed {
		u.Renamed = append(u.Renamed, changeset.Rename{Old: r.OldKey, New: r.NewKey})
	}
	rs.cs.RecordUpgrade(u)
}

// recordCommit writes the block for version to the changeset log. It runs
// after the IAVL stores committed but before the multistore commit info for
// version is written, and with sync set (whenever that commit info is about
// to reach the disk) it fsyncs the log first. A restart therefore never loads
// a height whose changes are missing from the log; a crash before the commit
// info replays the height, which is then recorded again (readers dedupe).
func (rs *Store) recordCommit(version int64, sync bool) {
	if rs.cs == nil {
		return
	}
	id := types.CommitID{Version: version, Hash: rs.lastCommitInfo.Hash()}
	var infos []changeset.StoreInfo
	if rs.lastCommitInfo != nil {
		infos = make([]changeset.StoreInfo, 0, len(rs.lastCommitInfo.StoreInfos))
		for _, si := range rs.lastCommitInfo.StoreInfos {
			infos = append(infos, changeset.StoreInfo{Name: si.Name, Version: si.CommitId.Version, Hash: si.CommitId.Hash})
		}
	}
	rs.cs.EndBlock(id.Version, id.Hash, infos, rs.StoreManifest())
	if !sync {
		return
	}
	if err := rs.cs.Sync(); err != nil {
		panic(fmt.Sprintf("changeset recorder: height %d: sync: %v", id.Version, err))
	}
}

// StoreManifest returns the mounted store set (name, type).
func (rs *Store) StoreManifest() []changeset.StoreDecl {
	out := make([]changeset.StoreDecl, 0, len(rs.stores))
	for key, store := range rs.stores {
		out = append(out, changeset.StoreDecl{Name: key.Name(), Type: StoreTypeToChangeset(store.GetStoreType())})
	}
	return out
}

// StoreTypeToChangeset maps store types to the manifest encoding.
func StoreTypeToChangeset(t types.StoreType) byte {
	switch t {
	case types.StoreTypeIAVL:
		return changeset.StoreIAVL
	case types.StoreTypeMemory:
		return changeset.StoreMemory
	case types.StoreTypeTransient:
		return changeset.StoreTransient
	case types.StoreTypeDB:
		return changeset.StoreDB
	}
	return 0
}

// ChangesetToStoreType is the inverse of StoreTypeToChangeset.
func ChangesetToStoreType(t byte) types.StoreType {
	switch t {
	case changeset.StoreIAVL:
		return types.StoreTypeIAVL
	case changeset.StoreMemory:
		return types.StoreTypeMemory
	case changeset.StoreTransient:
		return types.StoreTypeTransient
	case changeset.StoreDB:
		return types.StoreTypeDB
	}
	return types.StoreTypeIAVL
}

// IAVLStore returns the underlying *iavl.Store of a mounted IAVL store,
// bypassing the inter-block cache wrapper (applier fast path).
func (rs *Store) IAVLStore(key types.StoreKey) *iavl.Store {
	st, _ := rs.GetCommitKVStore(key).(*iavl.Store)
	return st
}

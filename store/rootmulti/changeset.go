package rootmulti

// Changeset recording (opt-in, CELESTIA_CHANGESET_OUT; see store/changeset).
// The IAVL stores hand their committed ops to the recorder in
// iavl.Store.Commit; this file closes each block with the commit info and
// records store upgrades and initial versions. With no recorder every hook
// returns immediately: nothing is allocated and nothing changes.

import (
	"fmt"
	"sort"

	"cosmossdk.io/store/changeset"
	"cosmossdk.io/store/iavl"
	"cosmossdk.io/store/types"
)

// SetChangesetRecorder sets the recorder (nil disables recording). Call it
// before the stores are loaded. NewStore uses changeset.FromEnv.
func (rs *Store) SetChangesetRecorder(r *changeset.Recorder) { rs.cs = r }

// ChangesetRecorder returns the active recorder (nil if none).
func (rs *Store) ChangesetRecorder() *changeset.Recorder { return rs.cs }

// attachChangeset makes a freshly loaded IAVL store record into rs.cs.
func (rs *Store) attachChangeset(key types.StoreKey, store types.CommitKVStore) {
	if rs.cs == nil {
		return
	}
	if st, ok := store.(*iavl.Store); ok {
		st.SetChangesetRecorder(rs.cs, key.Name())
	}
}

// recordUpgrade records the effective upgrade of a successful loadVersion:
// only names of mounted stores have an effect (loadVersion visits mounted
// keys only).
func (rs *Store) recordUpgrade(upgrades *types.StoreUpgrades) {
	if rs.cs == nil || upgrades == nil {
		return
	}
	mounted := map[string]bool{}
	for key := range rs.storesParams {
		mounted[key.Name()] = true
	}
	u := &changeset.Upgrade{}
	for _, n := range upgrades.Added {
		if mounted[n] {
			u.Added = append(u.Added, n)
		}
	}
	for _, n := range upgrades.Deleted {
		if mounted[n] {
			u.Deleted = append(u.Deleted, n)
		}
	}
	for _, r := range upgrades.Renamed {
		if mounted[r.NewKey] {
			u.Renamed = append(u.Renamed, changeset.Rename{Old: r.OldKey, New: r.NewKey})
		}
	}
	rs.cs.RecordUpgradeAtLoad(u)
}

// recordCommit writes the block for version to the changeset log. It runs
// after the IAVL stores committed but before the multistore commit info for
// version is written, and with sync set (whenever that commit info is about
// to reach the disk) it fsyncs the log first. A restart therefore never loads
// a height whose changes are missing from the log; a crash before the commit
// info replays the height, which is then recorded again (readers dedupe).
// Recording failures panic before the commit info is written.
func (rs *Store) recordCommit(version int64, sync bool) {
	if rs.cs == nil {
		return
	}
	rs.recordSkippedCommits()
	ci := rs.getLastCommitInfo()
	infos := make([]changeset.StoreInfo, 0, len(ci.StoreInfos))
	for _, si := range ci.StoreInfos {
		infos = append(infos, changeset.StoreInfo{Name: si.Name, Version: si.CommitId.Version, Hash: si.CommitId.Hash})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	rs.cs.EndBlock(version, ci.Hash(), infos, rs.storeManifest())
	if !sync {
		return
	}
	if err := rs.cs.Sync(); err != nil {
		panic(fmt.Sprintf("changeset recorder: height %d: sync: %v", version, err))
	}
}

// recordSkippedCommits records the ops of stores whose commit was skipped
// because they had already saved this version (a commit interrupted after
// some stores saved it, e.g. a store added at an upgrade height): the block
// re-applied them to the tree, so they belong to this height. The tree keeps
// them unsaved, so its next SaveVersion records them again, as it saves them
// again.
func (rs *Store) recordSkippedCommits() {
	for key := range rs.stores {
		if st, ok := rs.GetCommitKVStore(key).(*iavl.Store); ok {
			if ops := st.PendingChangesetOps(); len(ops) > 0 {
				rs.cs.CommitStore(key.Name(), ops)
			}
		}
	}
}

// syncChangeset fsyncs the log before a deferred flush writes commit info.
func (rs *Store) syncChangeset() error {
	if rs.cs == nil {
		return nil
	}
	if err := rs.cs.Sync(); err != nil {
		return fmt.Errorf("changeset recorder: sync before flush: %w", err)
	}
	return nil
}

// closeChangeset closes the recorder on shutdown. A block that finishes
// committing afterwards is still recorded, into its own synced file.
func (rs *Store) closeChangeset() error {
	if rs.cs == nil {
		return nil
	}
	return rs.cs.Close()
}

// storeManifest returns the mounted store set (name, type) without stores
// pending removal, sorted by name. A store type the format cannot encode
// panics (recording must fail loudly, never write a wrong manifest).
func (rs *Store) storeManifest() []changeset.StoreDecl {
	out := make([]changeset.StoreDecl, 0, len(rs.stores))
	for key, store := range rs.stores {
		if rs.removalMap[key] {
			continue
		}
		t := storeTypeToChangeset(store.GetStoreType())
		if t == 0 {
			panic(fmt.Sprintf("changeset recorder: store %s has type %v, which the changeset format cannot encode", key.Name(), store.GetStoreType()))
		}
		out = append(out, changeset.StoreDecl{Name: key.Name(), Type: t})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// storeTypeToChangeset maps store types to the manifest encoding (0: none).
func storeTypeToChangeset(t types.StoreType) byte {
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

// SetChangesetApplyMode (changeset applier only): store upgrades skip the
// native data migration (deleteKVStore / moveKVStoreData); the applier
// replays the recorded ops of that migration instead, in recorded order.
func (rs *Store) SetChangesetApplyMode(on bool) { rs.csApply = on }

func (rs *Store) deleteKVStoreUnlessApplying(kv types.KVStore) error {
	if rs.csApply {
		return nil
	}
	return deleteKVStore(kv)
}

func (rs *Store) moveKVStoreDataUnlessApplying(oldDB, newDB types.KVStore) error {
	if rs.csApply {
		return nil
	}
	return moveKVStoreData(oldDB, newDB)
}

// ChangesetToStoreType is the inverse of storeTypeToChangeset; an unknown
// type is an error.
func ChangesetToStoreType(t byte) (types.StoreType, error) {
	switch t {
	case changeset.StoreIAVL:
		return types.StoreTypeIAVL, nil
	case changeset.StoreMemory:
		return types.StoreTypeMemory, nil
	case changeset.StoreTransient:
		return types.StoreTypeTransient, nil
	case changeset.StoreDB:
		return types.StoreTypeDB, nil
	}
	return 0, fmt.Errorf("unknown changeset store type %d", t)
}

// IAVLStoreByName returns the underlying *iavl.Store of a loaded store by
// name, including stores that are only pending removal (deleted, renamed-old)
// until the next Commit; nil if there is none.
func (rs *Store) IAVLStoreByName(name string) *iavl.Store {
	for key := range rs.stores {
		if key.Name() == name {
			st, _ := rs.GetCommitKVStore(key).(*iavl.Store)
			return st
		}
	}
	return nil
}

// LastCommitInfoForChangeset returns the in-memory commit info of the last
// commit (also valid for soft-committed heights in deferred mode).
func (rs *Store) LastCommitInfoForChangeset() *types.CommitInfo { return rs.getLastCommitInfo() }

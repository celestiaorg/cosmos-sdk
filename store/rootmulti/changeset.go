package rootmulti

// Changeset recording (celestia genesis-sync prototype, CELESTIA_CHANGESET_OUT).
// The IAVL stores hand their committed ops to the recorder in iavl.Store.Commit;
// this file closes each block with the commit info, records store upgrades
// and initial versions, and exposes the hooks the changeset applier needs.

import (
	"errors"
	"fmt"

	dbm "github.com/tendermint/tm-db"

	"github.com/cosmos/cosmos-sdk/store/changeset"
	"github.com/cosmos/cosmos-sdk/store/iavl"
	"github.com/cosmos/cosmos-sdk/store/types"
)

// SetChangesetRecorder enables recording into r (tests / tools). Must be called
// before the stores are loaded. FromEnv is used by default.
func (rs *Store) SetChangesetRecorder(r *changeset.Recorder) { rs.cs = r }

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
	manifest, err := rs.storeManifest()
	if err != nil {
		panic(fmt.Sprintf("changeset recorder: height %d: %v", id.Version, err))
	}
	rs.cs.EndBlock(id.Version, id.Hash, infos, manifest)
	if !sync {
		return
	}
	if err := rs.cs.Sync(); err != nil {
		panic(fmt.Sprintf("changeset recorder: height %d: sync: %v", id.Version, err))
	}
}

// storeManifest returns the mounted store set (name, type). A store type
// the format cannot express is an error: recording it as another type would
// make the log replay a different store set.
func (rs *Store) storeManifest() ([]changeset.StoreDecl, error) {
	out := make([]changeset.StoreDecl, 0, len(rs.stores))
	for key, store := range rs.stores {
		t, ok := storeTypeToChangeset(store.GetStoreType())
		if !ok {
			return nil, fmt.Errorf("store %q has type %v, which the changeset format cannot record", key.Name(), store.GetStoreType())
		}
		out = append(out, changeset.StoreDecl{Name: key.Name(), Type: t})
	}
	return out, nil
}

// storeTypeToChangeset maps store types to the manifest encoding.
func storeTypeToChangeset(t types.StoreType) (byte, bool) {
	switch t {
	case types.StoreTypeIAVL:
		return changeset.StoreIAVL, true
	case types.StoreTypeMemory:
		return changeset.StoreMemory, true
	case types.StoreTypeTransient:
		return changeset.StoreTransient, true
	case types.StoreTypeDB:
		return changeset.StoreDB, true
	}
	return 0, false
}

// ChangesetToStoreType is the inverse of storeTypeToChangeset. ok is false
// for a type the format does not define.
func ChangesetToStoreType(t byte) (types.StoreType, bool) {
	switch t {
	case changeset.StoreIAVL:
		return types.StoreTypeIAVL, true
	case changeset.StoreMemory:
		return types.StoreTypeMemory, true
	case changeset.StoreTransient:
		return types.StoreTypeTransient, true
	case changeset.StoreDB:
		return types.StoreTypeDB, true
	}
	return 0, false
}

// skippedChangesetStores returns the IAVL stores commitStores will not commit
// at version: their tree is already saved at version (a crash after they
// saved but before the commit info was written, e.g. stores added at an
// upgrade height). Called before commitStores.
func (rs *Store) skippedChangesetStores(version int64) []*iavl.Store {
	if rs.cs == nil {
		return nil
	}
	var out []*iavl.Store
	for key, store := range rs.stores {
		if store.GetStoreType() != types.StoreTypeIAVL || store.LastCommitID().Version < version {
			continue
		}
		if st := rs.IAVLStore(key); st != nil {
			out = append(out, st)
		}
	}
	return out
}

// recordSkippedStores hands the ops the skipped stores received while this
// height was re-executed to the current block. Their saved version holds
// exactly these ops (the crashed run saved them), so the applier rebuilds
// the same tree at version. The ops stay pending in the store as well: its
// working tree still carries them and saves them with the next version, so
// the next block records them again, as that commit really contains them.
func recordSkippedStores(skipped []*iavl.Store) {
	for _, st := range skipped {
		st.RecordUncommittedOps()
	}
}

// IAVLStore returns the underlying *iavl.Store of a mounted IAVL store,
// bypassing the inter-block cache wrapper (applier fast path).
func (rs *Store) IAVLStore(key types.StoreKey) *iavl.Store {
	st, _ := rs.GetCommitKVStore(key).(*iavl.Store)
	return st
}

// errFlushRefused: the flush gate refused a version (it stays in memory).
var errFlushRefused = errors.New("refused by the flush gate")

// SetDeferredFlushGate makes every deferred flush ask gate(version, appHash)
// first; a refused version is not written. The changeset applier uses it so
// that a height it has not verified never reaches the disk.
func (rs *Store) SetDeferredFlushGate(gate func(version int64, appHash []byte) bool) {
	rs.deferred.mtx.Lock()
	rs.deferred.gate = gate
	rs.deferred.mtx.Unlock()
}

// MetadataSnapshot is the raw multistore metadata (the latest version and
// the commit info of Version) as stored, for RestoreMetadata.
type MetadataSnapshot struct {
	Version int64
	latest  []byte
	info    []byte
}

// SnapshotMetadata reads the raw metadata of version (the current latest).
func SnapshotMetadata(db dbm.DB, version int64) (MetadataSnapshot, error) {
	s := MetadataSnapshot{Version: version}
	var err error
	if s.latest, err = db.Get([]byte(latestVersionKey)); err != nil {
		return s, err
	}
	if version > 0 {
		s.info, err = db.Get([]byte(fmt.Sprintf(commitInfoKeyFmt, version)))
	}
	return s, err
}

// RestoreMetadata writes back the exact bytes of s and deletes the commit
// info of discard, in one synced batch. Unlike RollbackToVersion it does not
// rebuild the commit info from the mounted stores.
func RestoreMetadata(db dbm.DB, s MetadataSnapshot, discard int64) error {
	batch := db.NewBatch()
	defer batch.Close()
	set := func(key string, val []byte) error {
		if val == nil {
			return batch.Delete([]byte(key))
		}
		return batch.Set([]byte(key), val)
	}
	if err := set(latestVersionKey, s.latest); err != nil {
		return err
	}
	if s.Version > 0 {
		if err := set(fmt.Sprintf(commitInfoKeyFmt, s.Version), s.info); err != nil {
			return err
		}
	}
	if err := batch.Delete([]byte(fmt.Sprintf(commitInfoKeyFmt, discard))); err != nil {
		return err
	}
	return batch.WriteSync()
}

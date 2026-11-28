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

// LastCommitInfoForChangeset returns the commit info of the last commit,
// soft (deferred, not yet on disk) commits included.
func (rs *Store) LastCommitInfoForChangeset() *types.CommitInfo { return rs.lastCommitInfo }

// ChangesetCommitInfo reads the commit info of version from db (nil if none).
func ChangesetCommitInfo(db dbm.DB, version int64) (*types.CommitInfo, error) {
	bz, err := db.Get([]byte(fmt.Sprintf(commitInfoKeyFmt, version)))
	if err != nil || bz == nil {
		return nil, err
	}
	ci := &types.CommitInfo{}
	if err := ci.Unmarshal(bz); err != nil {
		return nil, err
	}
	return ci, nil
}

// ChangesetRollbackPoint is what the changeset applier needs to undo the
// next Commit exactly: the raw multistore metadata before it and every IAVL
// tree that will save the new version (stores pending removal included).
type ChangesetRollbackPoint struct {
	version int64  // last version on disk before the Commit
	latest  []byte // raw latest-version record (nil: none)
	info    []byte // raw commit info of version (nil: none)
	stores  map[string]*iavl.Store
	empty   map[string]bool // trees with no saved version before the Commit
}

// ChangesetRollbackPoint captures the state the next Commit changes. Call it
// right before Commit.
func (rs *Store) ChangesetRollbackPoint() (*ChangesetRollbackPoint, error) {
	p := &ChangesetRollbackPoint{version: GetLatestVersion(rs.db), stores: map[string]*iavl.Store{}, empty: map[string]bool{}}
	var err error
	if p.latest, err = rs.db.Get([]byte(latestVersionKey)); err != nil {
		return nil, err
	}
	if p.info, err = rs.db.Get([]byte(fmt.Sprintf(commitInfoKeyFmt, p.version))); err != nil {
		return nil, err
	}
	for key := range rs.stores {
		if st, ok := rs.GetCommitKVStore(key).(*iavl.Store); ok {
			p.stores[key.Name()] = st
			p.empty[key.Name()] = st.LastCommitID().Version == 0
		}
	}
	return p, nil
}

// ChangesetRollback undoes the Commit after p on disk: every tree goes back
// to p's version, a tree created by that Commit is deleted, and the raw
// metadata is restored byte for byte (as the v0.50 store's applier does). It
// never rebuilds commit info from the mounted stores (RollbackToVersion
// does). Nothing is written unless every tree can go back. The store must
// not be used afterwards. A height that only exists in memory (deferred
// persistence, not flushed) needs no rollback.
func (rs *Store) ChangesetRollback(p *ChangesetRollbackPoint) error {
	h := p.version + 1
	if GetLatestVersion(rs.db) < h {
		return nil
	}
	if deferred, _, _, err := DeferredMarker(rs.db); err != nil || deferred {
		return errors.Join(err, fmt.Errorf("height %d was flushed by deferred persistence, which keeps no older version", GetLatestVersion(rs.db)))
	}
	var prev map[string]bool
	if p.info != nil {
		ci := &types.CommitInfo{}
		if err := ci.Unmarshal(p.info); err != nil {
			return err
		}
		prev = map[string]bool{}
		for _, si := range ci.StoreInfos {
			prev[si.Name] = true
		}
	}
	// A tree that had no version before the Commit is deleted with its DB
	// prefix, which then holds only what that Commit wrote: store names
	// never contain '/', so no other store's prefix lies inside it.
	var created []string
	back := map[string]bool{}
	for name, st := range p.stores {
		switch {
		case p.version > 0 && st.VersionExists(p.version):
			back[name] = true
		case !prev[name] && p.empty[name] && changeset.ValidStoreName(name):
			created = append(created, name)
		default:
			return fmt.Errorf("store %s cannot be rolled back to version %d", name, p.version)
		}
	}
	for name, st := range p.stores {
		if back[name] {
			if _, err := st.LoadVersionForOverwriting(p.version); err != nil {
				return fmt.Errorf("store %s: %w", name, err)
			}
		}
	}
	batch := rs.db.NewBatch()
	defer batch.Close()
	for _, name := range created {
		if err := deletePrefix(rs.db, batch, []byte("s/k:"+name+"/")); err != nil {
			return err
		}
	}
	if err := batch.Delete([]byte(fmt.Sprintf(commitInfoKeyFmt, h))); err != nil {
		return err
	}
	for k, v := range map[string][]byte{latestVersionKey: p.latest, fmt.Sprintf(commitInfoKeyFmt, p.version): p.info} {
		var err error
		if v == nil {
			err = batch.Delete([]byte(k))
		} else {
			err = batch.Set([]byte(k), v)
		}
		if err != nil {
			return err
		}
	}
	return batch.WriteSync()
}

// deletePrefix adds the deletion of every key under prefix to batch.
func deletePrefix(db dbm.DB, batch dbm.Batch, prefix []byte) error {
	end := append([]byte(nil), prefix...)
	end[len(end)-1]++ // prefixes end in '/', never 0xff
	it, err := db.Iterator(prefix, end)
	if err != nil {
		return err
	}
	defer it.Close()
	for ; it.Valid(); it.Next() {
		if err := batch.Delete(append([]byte(nil), it.Key()...)); err != nil {
			return err
		}
	}
	return it.Error()
}

// Version is the last on-disk version the rollback point returns to.
func (p *ChangesetRollbackPoint) Version() int64 { return p.version }

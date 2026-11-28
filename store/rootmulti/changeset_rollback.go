package rootmulti

import (
	"errors"
	"fmt"

	dbm "github.com/cosmos/cosmos-db"

	"cosmossdk.io/store/changeset"
	"cosmossdk.io/store/iavl"
	"cosmossdk.io/store/types"
)

// errFlushRefused: the deferred flush gate refused a version.
var errFlushRefused = errors.New("refused by the deferred flush gate")

// SetDeferredFlushGate makes every deferred flush ask gate(version, appHash)
// first; a refused version is not written and stays in memory. nil (the
// default) changes nothing. The changeset applier sets it so that a height it
// has not verified never reaches the disk. Call it after
// SetDeferredPersistence; without deferred persistence it does nothing.
func (rs *Store) SetDeferredFlushGate(gate func(version int64, appHash []byte) bool) {
	if d := rs.celestiaDeferred; d != nil {
		d.mu.Lock()
		d.gate = gate
		d.mu.Unlock()
	}
}

// ChangesetRollbackPoint is what the changeset applier needs to undo the
// next Commit exactly: the raw multistore metadata before it and every IAVL
// tree that will save the new version (stores pending removal included).
type ChangesetRollbackPoint struct {
	version int64  // last committed version before the Commit
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
// to p's version (a tree created by that Commit is deleted) and the raw
// metadata is restored byte for byte. It never rebuilds commit info from the
// mounted stores. Nothing is written unless every tree can go back. The
// store must not be used afterwards. A height that only exists in memory
// (deferred persistence, not flushed) needs no rollback.
func (rs *Store) ChangesetRollback(p *ChangesetRollbackPoint) error {
	h := p.version + 1
	if GetLatestVersion(rs.db) < h {
		return nil
	}
	if _, deferred, err := ReadDeferredMarker(rs.db); err != nil || deferred {
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
			if err := st.LoadVersionForOverwriting(p.version); err != nil {
				return fmt.Errorf("store %s: %w", name, err)
			}
		}
		if err := st.Close(); err != nil {
			return fmt.Errorf("store %s: %w", name, err)
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

// ChangesetCommitInfo reads the commit info of version from db (nil if none).
func ChangesetCommitInfo(db dbm.DB, version int64) (*types.CommitInfo, error) {
	bz, err := db.Get([]byte(fmt.Sprintf(commitInfoKeyFmt, version)))
	if err != nil || bz == nil {
		return nil, err
	}
	ci := &types.CommitInfo{}
	return ci, ci.Unmarshal(bz)
}

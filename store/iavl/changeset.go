package iavl

import "github.com/cosmos/cosmos-sdk/store/changeset"

// SetChangesetRecorder makes the store record every Set/Delete that reaches
// the IAVL tree, in order, and hand them to r (tagged with name) when the
// version is saved in Commit.
func (st *Store) SetChangesetRecorder(r *changeset.Recorder, name string) {
	st.cs, st.csName = r, name
}

func (st *Store) recordOp(kind byte, key, value []byte) {
	op := changeset.Op{Kind: kind, Key: append([]byte(nil), key...)}
	if kind == changeset.OpSet {
		op.Value = append([]byte{}, value...)
	}
	st.csOps = append(st.csOps, op)
}

func (st *Store) flushRecordedOps() {
	ops := st.csOps
	st.csOps = nil
	st.cs.CommitStore(st.csName, ops)
}

// RecordUncommittedOps hands a copy of the ops recorded since the last save
// to the recorder without clearing them. rootmulti uses it for a store whose
// version is already saved, so its Commit is skipped on a re-executed height
// (the pending ops are then saved, and recorded again, with the next version).
func (st *Store) RecordUncommittedOps() {
	if st.cs == nil || len(st.csOps) == 0 {
		return
	}
	st.cs.CommitStore(st.csName, append([]changeset.Op(nil), st.csOps...))
}

// ApplyOps applies recorded ops directly to the IAVL tree, without recording
// (changeset applier). Returns the first tree error.
//
// Set keys and values are copied: changeset.DecodeBlock returns slices that
// alias the whole per-block payload buffer, and the IAVL tree keeps the slices
// it is given (leaf key/value, inner node keys). Passing them through would let
// every node that lands in the IAVL node cache pin its block's entire payload,
// so a long apply run retains (almost) every payload it has read. Each key and
// value gets its own allocation, as on the node's normal write path.
func (st *Store) ApplyOps(ops []changeset.Op) error {
	for i := range ops {
		op := &ops[i]
		switch op.Kind {
		case changeset.OpSet:
			key := append(make([]byte, 0, len(op.Key)), op.Key...)
			value := append(make([]byte, 0, len(op.Value)), op.Value...)
			if _, err := st.tree.Set(key, value); err != nil {
				return err
			}
		case changeset.OpDelete:
			if _, _, err := st.tree.Remove(op.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

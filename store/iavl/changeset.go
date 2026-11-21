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

// ApplyOps applies recorded ops directly to the IAVL tree, without recording
// (changeset applier). Returns the first tree error.
func (st *Store) ApplyOps(ops []changeset.Op) error {
	for i := range ops {
		op := &ops[i]
		switch op.Kind {
		case changeset.OpSet:
			if _, err := st.tree.Set(op.Key, op.Value); err != nil {
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

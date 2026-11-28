package iavl

// Changeset recording (opt-in, CELESTIA_CHANGESET_OUT; see store/changeset).
// Every Set/Delete that reaches the IAVL tree through this store is buffered
// in order and handed to the recorder when the version is saved.

import (
	"fmt"

	"cosmossdk.io/store/changeset"
)

// SetChangesetRecorder makes the store record every Set/Delete that reaches
// the IAVL tree, in order, and hand them to r (tagged with name) when the
// version is saved in Commit. nil disables recording.
func (st *Store) SetChangesetRecorder(r *changeset.Recorder, name string) {
	st.cs, st.csName = r, name
	if r == nil {
		st.csOps = nil
	}
}

// recordOp buffers a copy of the op: callers may reuse key and value.
func (st *Store) recordOp(kind byte, key, value []byte) {
	op := changeset.Op{Kind: kind, Key: append([]byte(nil), key...)}
	if kind == changeset.OpSet {
		op.Value = append([]byte{}, value...)
	}
	st.csOps = append(st.csOps, op)
}

// PendingChangesetOps returns the ops buffered since the last saved version
// (non-empty after a Commit only when the multistore skipped this store's
// commit). They stay buffered.
func (st *Store) PendingChangesetOps() []changeset.Op { return st.csOps }

func (st *Store) flushRecordedOps() {
	ops := st.csOps
	st.csOps = nil
	st.cs.CommitStore(st.csName, ops)
}

// ApplyOps applies recorded ops directly to the IAVL tree, without recording
// (changeset applier). Returns the first tree error.
//
// Set keys and values are copied: changeset.DecodeBlock returns slices that
// alias the whole per-block payload buffer, and the IAVL tree keeps the slices
// it is given (leaf key/value, inner node keys). Passing them through would let
// every node that lands in the IAVL node cache pin its block's entire payload,
// and a caller reusing a buffer would change the tree. Each key and value gets
// its own allocation, as on the node's normal write path.
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
		default:
			return fmt.Errorf("changeset op %d: unknown kind %d", i, op.Kind)
		}
	}
	return nil
}

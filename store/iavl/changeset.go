package iavl

// Changeset recording (opt-in, CELESTIA_CHANGESET_OUT; see store/changeset).
// Every Set/Delete that reaches the IAVL tree through this store is buffered
// in order and handed to the recorder when the version is saved.

import "cosmossdk.io/store/changeset"

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

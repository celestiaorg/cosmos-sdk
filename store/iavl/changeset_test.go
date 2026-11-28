package iavl

import (
	"testing"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/iavl"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/changeset"
	"cosmossdk.io/store/wrapper"
)

// ApplyOps must not hand the decoded (payload-aliasing) slices to the tree:
// the tree keeps them, so a reused or overwritten buffer would change the
// tree (and every cached node would pin its block's payload).
func TestApplyOpsDoesNotAliasOps(t *testing.T) {
	tree := iavl.NewMutableTree(wrapper.NewDBWrapper(dbm.NewMemDB()), cacheSize, false, log.NewNopLogger())
	st := UnsafeNewStore(tree)

	payload := []byte("key1value1key2value2")
	ops := []changeset.Op{
		{Kind: changeset.OpSet, Key: payload[0:4:4], Value: payload[4:10:10]},
		{Kind: changeset.OpSet, Key: payload[10:14:14], Value: payload[14:20:20]},
	}
	require.NoError(t, st.ApplyOps(ops))
	hash := st.WorkingHash()

	for i := range payload {
		payload[i] = 'x'
	}
	require.Equal(t, []byte("value1"), st.Get([]byte("key1")))
	require.Equal(t, []byte("value2"), st.Get([]byte("key2")))
	require.Nil(t, st.Get([]byte("xxxx")))
	require.Equal(t, hash, st.WorkingHash())
	require.Equal(t, hash, st.Commit().Hash)
}

// Unknown op kinds (a malformed changeset) are an error, not a no-op.
func TestApplyOpsRejectsUnknownKind(t *testing.T) {
	tree := iavl.NewMutableTree(wrapper.NewDBWrapper(dbm.NewMemDB()), cacheSize, false, log.NewNopLogger())
	st := UnsafeNewStore(tree)
	require.Error(t, st.ApplyOps([]changeset.Op{{Kind: 7, Key: []byte("k")}}))
}

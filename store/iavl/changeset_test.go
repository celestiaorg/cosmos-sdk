package iavl

import (
	"testing"

	"github.com/cosmos/iavl"
	"github.com/stretchr/testify/require"
	dbm "github.com/tendermint/tm-db"

	"github.com/cosmos/cosmos-sdk/store/changeset"
)

// ApplyOps must not hand the decoded (payload-aliasing) slices to the tree:
// the tree keeps them, which pinned every block payload in cstool apply.
func TestApplyOpsDoesNotAliasOps(t *testing.T) {
	db := dbm.NewMemDB()
	tree, err := iavl.NewMutableTree(db, cacheSize, false)
	require.NoError(t, err)
	st := UnsafeNewStore(tree)

	payload := []byte("key1value1")
	ops := []changeset.Op{{Kind: changeset.OpSet, Key: payload[:4:4], Value: payload[4:]}}
	require.NoError(t, st.ApplyOps(ops))

	for i := range payload {
		payload[i] = 'x'
	}
	require.Equal(t, []byte("value1"), st.Get([]byte("key1")))
	require.Nil(t, st.Get([]byte("xxxx")))
}

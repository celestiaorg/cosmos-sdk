package baseapp

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/log"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
	dbm "github.com/tendermint/tm-db"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Block txs are untrusted: a decoder panic inside the pre-verification hook
// must not escape (in DeliverTx it is recovered by runTx).
func TestPrepareDeliverTxBatchRecoversDecoderPanic(t *testing.T) {
	decoder := func([]byte) (sdk.Tx, error) { panic("malformed tx") }
	app := NewBaseApp("preverify", log.NewNopLogger(), dbm.NewMemDB(), decoder, SetSignaturePreverify(true))
	defer secp256k1.EnableSigCache(false)
	require.NoError(t, app.LoadLatestVersion())
	called := false
	app.SetBatchSigPreverifier(func(sdk.Context, []sdk.Tx) { called = true })
	app.setDeliverState(tmproto.Header{Height: 1})

	require.NotPanics(t, func() { app.PrepareDeliverTxBatch([][]byte{{1}, {2}}) })
	require.False(t, called)
}

// The signature-preverify option alone switches the whole path: the batch
// hook and the secp256k1 cache.
func TestSignaturePreverifyOptionControlsPath(t *testing.T) {
	defer secp256k1.EnableSigCache(false)
	for _, enabled := range []bool{true, false} {
		decoder := func([]byte) (sdk.Tx, error) { return nil, nil }
		app := NewBaseApp("preverify", log.NewNopLogger(), dbm.NewMemDB(), decoder, SetSignaturePreverify(enabled))
		require.NoError(t, app.LoadLatestVersion())
		var got int
		app.SetBatchSigPreverifier(func(_ sdk.Context, txs []sdk.Tx) { got = len(txs) })
		app.setDeliverState(tmproto.Header{Height: 1})
		app.PrepareDeliverTxBatch([][]byte{{1}, {2}, {3}})
		require.Equal(t, enabled, secp256k1.SigCacheEnabled())
		if enabled {
			require.Equal(t, 3, got)
		} else {
			require.Zero(t, got)
		}
	}
}

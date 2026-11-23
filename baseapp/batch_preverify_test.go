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

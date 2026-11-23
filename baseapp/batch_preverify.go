package baseapp

import (
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// SetBatchSigPreverifier installs the hook that is given all txs of a
// DeliverTx batch before they execute. The hook may only warm caches: it runs
// on a discarded branch of the deliver state with an infinite gas meter, so it
// cannot change state or gas accounting. It only runs when signature
// pre-verification is enabled (SetSignaturePreverify).
func (app *BaseApp) SetBatchSigPreverifier(f func(ctx sdk.Context, txs []sdk.Tx)) {
	app.batchSigPreverifier = f
}

// SetSignaturePreverify enables signature pre-verification ([performance]
// signature-preverify). It also switches the process-wide secp256k1 cache.
func SetSignaturePreverify(enabled bool) func(*BaseApp) {
	return func(app *BaseApp) {
		app.sigPreverify = secp256k1.EnableSigCache(enabled) // never on in the cgo build
	}
}

// PrepareDeliverTxBatch is called by the ABCI server before a batch of
// DeliverTx calls for the current block.
func (app *BaseApp) PrepareDeliverTxBatch(rawTxs [][]byte) {
	if !app.sigPreverify || app.batchSigPreverifier == nil || app.deliverState == nil || len(rawTxs) < 2 {
		return
	}
	// Best effort: never affect execution. The decoder runs inside the recover
	// too, because block txs are untrusted and runTx (not this hook) is where
	// a decoder panic is normally recovered.
	defer func() { _ = recover() }()
	txs := make([]sdk.Tx, 0, len(rawTxs))
	for _, b := range rawTxs {
		if tx, err := app.txDecoder(b); err == nil {
			txs = append(txs, tx)
		}
	}
	ctx, _ := app.deliverState.ctx.CacheContext()
	ctx = ctx.WithGasMeter(sdk.NewInfiniteGasMeter())
	app.batchSigPreverifier(ctx, txs)
}

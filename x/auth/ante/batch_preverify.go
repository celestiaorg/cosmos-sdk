package ante

import (
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
)

// NewBatchSigPreverifier returns a BaseApp batch hook (sync-speedup prototype)
// that predicts each single-key secp256k1 signature's sign bytes from the state
// at the start of the batch and verifies all of them in parallel, recording
// only VALID (pubkey, signBytes, sig) triples in the secp256k1 signature cache.
// SigVerificationDecorator still recomputes sign bytes from the live state and
// calls VerifySignature; a cache hit only skips re-doing a verification that
// already succeeded for exactly the same inputs.
func NewBatchSigPreverifier(ak AccountKeeper, handler authsigning.SignModeHandler) func(sdk.Context, []sdk.Tx) {
	return func(ctx sdk.Context, txs []sdk.Tx) { preverifyBatch(ak, handler, ctx, txs) }
}

// preverifyBatch returns the number of signatures it verified as valid and
// cached.
func preverifyBatch(ak AccountKeeper, handler authsigning.SignModeHandler, ctx sdk.Context, txs []sdk.Tx) int {
	type job struct {
		tx   sdk.Tx
		pk   *secp256k1.PubKey
		sd   authsigning.SignerData
		mode signing.SignMode
		sig  []byte
	}
	if !secp256k1.SigCacheEnabled() {
		return 0
	}
	secp256k1.SigCacheReset()
	jobs := make([]job, 0, len(txs))
	for _, tx := range txs {
		sigTx, ok := tx.(authsigning.SigVerifiableTx)
		if !ok {
			continue
		}
		sigs, err := sigTx.GetSignaturesV2()
		if err != nil {
			continue
		}
		signers := sigTx.GetSigners()
		if len(sigs) != len(signers) {
			continue
		}
		for i, sig := range sigs {
			single, ok := sig.Data.(*signing.SingleSignatureData)
			if !ok {
				continue
			}
			acc := ak.GetAccount(ctx, signers[i])
			if acc == nil {
				continue
			}
			pk := acc.GetPubKey()
			if pk == nil {
				pk = sig.PubKey
			}
			secpPk, ok := pk.(*secp256k1.PubKey)
			if !ok {
				continue
			}
			accNum := acc.GetAccountNumber()
			if ctx.BlockHeight() == 0 {
				accNum = 0
			}
			jobs = append(jobs, job{tx: tx, pk: secpPk, mode: single.SignMode, sig: single.Signature, sd: authsigning.SignerData{
				Address:       acc.GetAddress().String(),
				ChainID:       ctx.ChainID(),
				AccountNumber: accNum,
				Sequence:      sig.Sequence,
				PubKey:        pk,
			}})
		}
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > 16 {
		workers = 16
	}
	ch := make(chan job, len(jobs))
	for _, j := range jobs {
		ch <- j
	}
	close(ch)
	var wg sync.WaitGroup
	var cached atomic.Int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { _ = recover() }()
			for j := range ch {
				msg, err := handler.GetSignBytes(j.mode, j.sd, j.tx)
				if err != nil {
					continue
				}
				if secp256k1.PreverifySignature(j.pk, msg, j.sig) {
					cached.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	return int(cached.Load())
}

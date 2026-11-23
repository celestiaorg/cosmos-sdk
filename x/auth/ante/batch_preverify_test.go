package ante_test

import (
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
)

type anteResult struct {
	gas    uint64
	err    string
	events sdk.Events
}

// TestBatchSigPreverifierEquivalence runs the same block of txs through the
// full ante handler with and without signature pre-verification and requires
// identical gas, errors and events for every tx: valid txs, a multi-signer tx,
// replays, wrong sequence and account number, and a tampered signature.
func (suite *AnteTestSuite) TestBatchSigPreverifierEquivalence() {
	suite.SetupTest(false) // deliver mode
	accs := suite.CreateTestAccounts(3)
	a, b, c := accs[0], accs[1], accs[2]

	mk := func(signers []TestAccount, accNums, seqs []uint64, tamper bool) sdk.Tx {
		suite.txBuilder = suite.clientCtx.TxConfig.NewTxBuilder()
		msgs := make([]sdk.Msg, len(signers))
		privs := make([]cryptotypes.PrivKey, len(signers))
		for i, s := range signers {
			msgs[i] = testdata.NewTestMsg(s.acc.GetAddress())
			privs[i] = s.priv
		}
		suite.Require().NoError(suite.txBuilder.SetMsgs(msgs...))
		suite.txBuilder.SetFeeAmount(testdata.NewTestFeeAmount())
		suite.txBuilder.SetGasLimit(testdata.NewTestGasLimit())
		tx, err := suite.CreateTestTx(privs, accNums, seqs, suite.ctx.ChainID())
		suite.Require().NoError(err)
		if !tamper {
			return tx
		}
		sigs, err := tx.GetSignaturesV2()
		suite.Require().NoError(err)
		bad, err := privs[0].Sign([]byte("unrelated message"))
		suite.Require().NoError(err)
		sigs[0].Data = &signing.SingleSignatureData{
			SignMode:  suite.clientCtx.TxConfig.SignModeHandler().DefaultMode(),
			Signature: bad,
		}
		suite.Require().NoError(suite.txBuilder.SetSignatures(sigs...))
		return suite.txBuilder.GetTx()
	}
	txs := []sdk.Tx{
		mk([]TestAccount{a}, []uint64{0}, []uint64{0}, false),          // valid, sets the pubkey
		mk([]TestAccount{b, c}, []uint64{1, 2}, []uint64{0, 0}, false), // valid multi-signer
		mk([]TestAccount{a}, []uint64{0}, []uint64{0}, false),          // replay: wrong sequence
		mk([]TestAccount{a}, []uint64{0}, []uint64{1}, false),          // valid at sequence 1
		mk([]TestAccount{b}, []uint64{1}, []uint64{1}, true),           // tampered signature
		mk([]TestAccount{c}, []uint64{9}, []uint64{1}, false),          // wrong account number
		mk([]TestAccount{a}, []uint64{0}, []uint64{1}, false),          // duplicate of the 4th
		mk([]TestAccount{b}, []uint64{1}, []uint64{1}, false),          // valid at sequence 1
	}

	run := func(enabled bool) []anteResult {
		secp256k1.EnableSigCache(enabled)
		defer secp256k1.EnableSigCache(false)

		block, _ := suite.ctx.CacheContext() // same starting state for both runs
		if enabled {
			pre, _ := block.CacheContext() // discarded, as in BaseApp.PrepareDeliverTxBatch
			n := ante.PreverifyBatch(suite.app.AccountKeeper, suite.clientCtx.TxConfig.SignModeHandler(), pre.WithGasMeter(sdk.NewInfiniteGasMeter()), txs)
			// every valid signature is predicted from the batch-start state:
			// A@0 (twice), B@0, C@0, A@1 (twice), B@1; the tampered and the
			// wrong-account-number signatures are not cached
			suite.Require().Equal(7, n)
		}
		out := make([]anteResult, len(txs))
		for i, tx := range txs {
			txCtx, write := block.CacheContext()
			txCtx = txCtx.WithGasMeter(sdk.NewGasMeter(testdata.NewTestGasLimit())).WithEventManager(sdk.NewEventManager())
			_, err := suite.anteHandler(txCtx, tx, false)
			out[i] = anteResult{gas: txCtx.GasMeter().GasConsumed(), events: txCtx.EventManager().Events()}
			if err != nil {
				out[i].err = err.Error()
			} else {
				write()
			}
		}
		return out
	}

	off := run(false)
	on := run(true)
	for i := range txs {
		suite.Require().Equal(off[i], on[i], "tx %d", i)
	}
	// sanity: the workload has both accepted and rejected txs
	suite.Require().Empty(off[0].err)
	suite.Require().Empty(off[1].err)
	suite.Require().Empty(off[3].err)
	suite.Require().Empty(off[7].err)
	for _, i := range []int{2, 4, 5, 6} {
		suite.Require().NotEmpty(off[i].err, "tx %d", i)
	}
}

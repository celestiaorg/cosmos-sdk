package baseapp_test

import (
	"bytes"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/baseapp"
	baseapptestutil "github.com/cosmos/cosmos-sdk/baseapp/testutil"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
)

// TestOptimisticExecutionCheckTxSimulateRace runs CheckTx, Simulate and query
// contexts while speculative FinalizeBlocks (served and aborted) write state
// off the consensus goroutine, as in cosmos/cosmos-sdk#18382. Run with -race.
func TestOptimisticExecutionCheckTxSimulateRace(t *testing.T) {
	preBlocker := func(ctx sdk.Context, req *abci.RequestFinalizeBlock) (*sdk.ResponsePreBlock, error) {
		ctx.KVStore(capKey2).Set([]byte(fmt.Sprintf("block-key-%d", req.Height)), bytes.Repeat([]byte("a"), 64))
		return &sdk.ResponsePreBlock{}, nil
	}
	anteHandler := func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		if simulate {
			return ctx, nil
		}
		counter, _ := parseTxMemo(t, tx)
		store := ctx.KVStore(capKey2)
		key := []byte(fmt.Sprintf("checktx-key-%d", counter))
		_ = store.Get(key)
		store.Set(key, bytes.Repeat([]byte("b"), 64))
		return ctx.WithPriority(testTxPriority), nil
	}
	suite := NewBaseAppSuite(t, func(app *baseapp.BaseApp) {
		app.SetPreBlocker(preBlocker)
		app.SetAnteHandler(anteHandler)
	}, baseapp.SetOptimisticExecution())
	baseapptestutil.RegisterKeyValueServer(suite.baseApp.MsgServiceRouter(), ReadWriteKeyValueImpl{})
	_, err := suite.baseApp.InitChain(&abci.RequestInitChain{ConsensusParams: &cmtproto.ConsensusParams{}})
	require.NoError(t, err)
	_, err = suite.baseApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: 1})
	require.NoError(t, err)
	_, err = suite.baseApp.Commit()
	require.NoError(t, err)

	privKey := secp256k1.GenPrivKeyFromSecret([]byte("test"))
	makeTx := func(counter int64) []byte {
		builder := suite.txConfig.NewTxBuilder()
		builder.SetMemo("counter=" + strconv.FormatInt(counter, 10) + "&failOnAnte=false")
		require.NoError(t, builder.SetSignatures(signingtypes.SignatureV2{
			PubKey: privKey.PubKey(), Sequence: uint64(counter), Data: &signingtypes.SingleSignatureData{},
		}))
		bz, err := suite.txConfig.TxEncoder()(builder.GetTx())
		require.NoError(t, err)
		return bz
	}
	_, _, addr := testdata.KeyTestPubAddr()
	simBuilder := suite.txConfig.NewTxBuilder()
	require.NoError(t, simBuilder.SetMsgs(&baseapptestutil.MsgKeyValue{Key: []byte("block-key-1"), Value: []byte("v"), Signer: addr.String()}))
	setTxSignature(t, simBuilder, 0)
	simTx, err := suite.txConfig.TxEncoder()(simBuilder.GetTx())
	require.NoError(t, err)
	// Warm lazy decoder caches outside the concurrent section.
	_, _ = suite.baseApp.CheckTx(&abci.RequestCheckTx{Tx: makeTx(0)})
	_, _, _ = suite.baseApp.Simulate(simTx)

	done := make(chan struct{})
	var wg sync.WaitGroup
	reader := func(f func(i int64)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := int64(1); ; i++ {
				select {
				case <-done:
					return
				default:
					f(i)
				}
			}
		}()
	}
	reader(func(i int64) { _, _ = suite.baseApp.CheckTx(&abci.RequestCheckTx{Tx: makeTx(i)}) })
	reader(func(int64) { _, _, _ = suite.baseApp.Simulate(simTx) })
	reader(func(int64) { _, _ = suite.baseApp.CreateQueryContext(0, false) })

	for h := int64(2); h < 60; h++ {
		hash := []byte(fmt.Sprintf("h%d", h))
		pp, err := suite.baseApp.ProcessProposal(&abci.RequestProcessProposal{Height: h, Hash: hash})
		require.NoError(t, err)
		require.Equal(t, abci.ResponseProcessProposal_ACCEPT, pp.Status)
		time.Sleep(200 * time.Microsecond)
		if h%3 == 0 {
			hash = []byte(fmt.Sprintf("other-%d", h)) // aborted run, synchronous re-execution
		}
		_, err = suite.baseApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: h, Hash: hash})
		require.NoError(t, err)
		_, err = suite.baseApp.Commit()
		require.NoError(t, err)
	}
	close(done)
	wg.Wait()
}

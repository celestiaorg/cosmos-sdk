package baseapp_test

import (
	"fmt"
	"strings"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	storecache "cosmossdk.io/store/cache"

	"github.com/cosmos/cosmos-sdk/baseapp"
	baseapptestutil "github.com/cosmos/cosmos-sdk/baseapp/testutil"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// TestCheckStateWindow: CheckTx and Simulate between FinalizeBlock and Commit read the last
// committed state plus check state's own writes, never the block's writes.
func TestCheckStateWindow(t *testing.T) {
	for _, interBlockCache := range []bool{false, true} {
		t.Run(fmt.Sprintf("interBlockCache=%v", interBlockCache), func(t *testing.T) {
			testCheckStateWindow(t, interBlockCache)
		})
	}
}

func testCheckStateWindow(t *testing.T, interBlockCache bool) {
	blocks := map[int64]string{1: "a=1 b=1 d=1", 2: "a=2 b=2 c=2 d="} // "k=" deletes k
	preBlocker := func(ctx sdk.Context, req *abci.RequestFinalizeBlock) (*sdk.ResponsePreBlock, error) {
		store := ctx.KVStore(capKey2)
		for _, kv := range strings.Fields(blocks[req.Height]) {
			k, v, _ := strings.Cut(kv, "=")
			if v == "" {
				store.Delete([]byte(k))
			} else {
				store.Set([]byte(k), []byte(v))
			}
		}
		return &sdk.ResponsePreBlock{}, nil
	}
	// The ante records point reads of a, b, n and an iteration of the store; CheckTx appends to n.
	var got string
	ante := func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) {
		store := ctx.KVStore(capKey2)
		got = fmt.Sprintf("a=%s b=%s n=%s |", store.Get([]byte("a")), store.Get([]byte("b")), store.Get([]byte("n")))
		it := store.Iterator(nil, nil)
		for ; it.Valid(); it.Next() {
			got += fmt.Sprintf(" %s=%s", it.Key(), it.Value())
		}
		require.NoError(t, it.Close())
		if ctx.ExecMode() == sdk.ExecModeCheck {
			store.Set([]byte("n"), append(store.Get([]byte("n")), 'x'))
		}
		return ctx, nil
	}
	opts := []func(*baseapp.BaseApp){func(app *baseapp.BaseApp) { app.SetPreBlocker(preBlocker); app.SetAnteHandler(ante) }}
	if interBlockCache {
		opts = append(opts, baseapp.SetInterBlockCache(storecache.NewCommitKVStoreCacheManager(storecache.DefaultCommitKVStoreCacheSize)))
	}
	suite := NewBaseAppSuite(t, opts...)
	app := suite.baseApp
	baseapptestutil.RegisterKeyValueServer(app.MsgServiceRouter(), MsgKeyValueImpl{})
	_, err := app.InitChain(&abci.RequestInitChain{ConsensusParams: &cmtproto.ConsensusParams{}})
	require.NoError(t, err)

	_, _, addr := testdata.KeyTestPubAddr()
	b := suite.txConfig.NewTxBuilder()
	require.NoError(t, b.SetMsgs(&baseapptestutil.MsgKeyValue{Key: []byte("k"), Value: []byte("v"), Signer: addr.String()}))
	setTxSignature(t, b, 0)
	tx, err := suite.txConfig.TxEncoder()(b.GetTx())
	require.NoError(t, err)

	finalize := func(h int64) {
		_, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: h})
		require.NoError(t, err)
	}
	commit := func() {
		_, err := app.Commit()
		require.NoError(t, err)
	}
	check := func(want string) {
		r, err := app.CheckTx(&abci.RequestCheckTx{Tx: tx, Type: abci.CheckTxType_New})
		require.NoError(t, err)
		require.True(t, r.IsOK(), r.Log)
		require.Equal(t, want, got, "CheckTx")
	}
	simulate := func(want string) {
		_, _, err := app.Simulate(tx)
		require.NoError(t, err)
		require.Equal(t, want, got, "Simulate")
	}

	finalize(1)
	check("a= b= n= |") // window before the first Commit
	commit()
	check("a=1 b=1 n= | a=1 b=1 d=1") // caches a, b and writes n
	finalize(2)
	check("a=1 b=1 n=x | a=1 b=1 d=1 n=x")
	simulate("a=1 b=1 n=xx | a=1 b=1 d=1 n=xx")
	commit()
	check("a=2 b=2 n= | a=2 b=2 c=2")
}

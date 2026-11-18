package baseapp_test

import (
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/baseapp"
	baseapptestutil "github.com/cosmos/cosmos-sdk/baseapp/testutil"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var feeKey = []byte("fee")

// recheckAnte checks and bumps the sequence of sender s, credits fee f to the
// shared fee key, and optionally reads sender r's sequence (r), iterates the
// marks (it) or fees (itf), or writes mark w with value v without reading it.
func recheckAnte(ctx sdk.Context, tx sdk.Tx, _ bool) (sdk.Context, error) {
	q, err := url.ParseQuery(tx.(sdk.TxWithMemo).GetMemo())
	if err != nil {
		return ctx, err
	}
	ctx = ctx.WithGasMeter(storetypes.NewGasMeter(1_000_000))
	store := ctx.KVStore(capKey1)
	if r := q.Get("r"); r != "" {
		store.Get([]byte("seq/" + r))
	}
	for name, it := range map[string]func() storetypes.Iterator{
		"it":  func() storetypes.Iterator { return store.Iterator([]byte("mark/"), []byte("mark0")) },
		"itf": func() storetypes.Iterator { return ctx.KVStore(capKey2).Iterator(nil, nil) },
	} {
		if q.Has(name) {
			iter := it()
			for ; iter.Valid(); iter.Next() {
			}
			iter.Close()
		}
	}
	if w := q.Get("w"); w != "" {
		store.Set([]byte("mark/"+w), []byte(q.Get("v")+"."))
	}
	seqKey := []byte("seq/" + q.Get("s"))
	seq, fee := sdk.BigEndianToUint64(store.Get(seqKey)), uint64(len(q.Get("f")))
	if fmt.Sprint(seq) != q.Get("n") {
		return ctx, sdkerrors.ErrWrongSequence.Wrapf("expected %d, got %s", seq, q.Get("n"))
	}
	store.Set(seqKey, sdk.Uint64ToBigEndian(seq+1))
	fees := ctx.KVStore(capKey2)
	fees.Set(feeKey, sdk.Uint64ToBigEndian(sdk.BigEndianToUint64(fees.Get(feeKey))+fee))
	return ctx.WithPriority(int64(fee)), nil
}

func isFeeKey(k storetypes.StoreKey, key []byte) bool {
	return k == capKey2 && string(key) == string(feeKey)
}

func newRecheckSuite(t *testing.T, opts ...func(*baseapp.BaseApp)) *BaseAppSuite {
	t.Helper()
	opts = append(opts, func(app *baseapp.BaseApp) { app.SetAnteHandler(recheckAnte) })
	s := NewBaseAppSuite(t, opts...)
	baseapptestutil.RegisterCounterServer(s.baseApp.MsgServiceRouter(), NoopCounterServerImpl{})
	_, err := s.baseApp.InitChain(&abci.RequestInitChain{ConsensusParams: &cmtproto.ConsensusParams{}})
	require.NoError(t, err)
	// a non-empty committed fee, so branches charge the same read gas as a sequential recheck
	commitBlock(t, s.baseApp, 1, s.recheckTx(t, "s=init&n=0&f=x"))
	return s
}

func commitBlock(t *testing.T, app *baseapp.BaseApp, h int64, txs ...[]byte) {
	t.Helper()
	_, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: h, Txs: txs})
	require.NoError(t, err)
	_, err = app.Commit()
	require.NoError(t, err)
}

func (s *BaseAppSuite) recheckTx(t *testing.T, memo string) []byte {
	t.Helper()
	_, _, addr := testdata.KeyTestPubAddr()
	builder := s.txConfig.NewTxBuilder()
	require.NoError(t, builder.SetMsgs(&baseapptestutil.MsgCounter{Signer: addr.String()}))
	builder.SetMemo(memo)
	setTxSignature(t, builder, 0)
	bz, err := s.txConfig.TxEncoder()(builder.GetTx())
	require.NoError(t, err)
	return bz
}

// recheckParallel rechecks groups of txs on workers goroutines and merges the
// fee key additively. It returns nil responses and the error if Validate fails.
func (s *BaseAppSuite) recheckParallel(t *testing.T, txs [][]byte, groups [][]int, workers int) (*baseapp.RecheckExecutor, []*abci.ResponseCheckTx, error) {
	t.Helper()
	// Decode up front: the upstream x/tx v0.13.7 this module pins races in concurrent
	// GetSigners (fixed in the celestia fork's v0.13.9, which apps use).
	decoded := make([]sdk.Tx, len(txs))
	for i, tx := range txs {
		var err error
		decoded[i], err = s.txConfig.TxDecoder()(tx)
		require.NoError(t, err)
		_, err = decoded[i].(interface {
			GetMsgsV2() ([]protov2.Message, error)
		}).GetMsgsV2()
		require.NoError(t, err)
	}
	app := s.baseApp
	defer app.LockCheckState()()
	exec, err := app.NewRecheckExecutor(len(groups), isFeeKey)
	require.NoError(t, err)
	resps := make([]*abci.ResponseCheckTx, len(txs))
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := exec.NewWorker()
			for g := int(next.Add(1)) - 1; g < len(groups); g = int(next.Add(1)) - 1 {
				w.BeginGroup(g)
				for _, i := range groups[g] {
					resps[i] = w.RecheckTx(txs[i], decoded[i])
				}
				w.EndGroup()
			}
		}()
	}
	wg.Wait()
	if err := exec.Validate(workers); err != nil {
		return exec, nil, err
	}
	merged := map[storetypes.StoreKey]map[string][]byte{}
	if aw, ok := exec.AdditiveWrites()[capKey2][string(feeKey)]; ok {
		total := sdk.BigEndianToUint64(aw.Base)
		for _, v := range aw.Finals {
			total += sdk.BigEndianToUint64(v) - sdk.BigEndianToUint64(aw.Base)
		}
		merged[capKey2] = map[string][]byte{string(feeKey): sdk.Uint64ToBigEndian(total)}
	}
	exec.Merge(merged)
	return exec, resps, nil
}

func recheckSequential(t *testing.T, app *baseapp.BaseApp, txs [][]byte) []*abci.ResponseCheckTx {
	t.Helper()
	resps := make([]*abci.ResponseCheckTx, len(txs))
	for i, tx := range txs {
		var err error
		resps[i], err = app.CheckTx(&abci.RequestCheckTx{Tx: tx, Type: abci.CheckTxType_Recheck})
		require.NoError(t, err)
	}
	return resps
}

func checkStateKVs(app *baseapp.BaseApp) map[string]string {
	ctx := getCheckStateCtx(app)
	kvs := map[string]string{}
	for _, k := range []storetypes.StoreKey{capKey1, capKey2} {
		it := ctx.KVStore(k).Iterator(nil, nil)
		for ; it.Valid(); it.Next() {
			kvs[k.Name()+"/"+string(it.Key())] = string(it.Value())
		}
		it.Close()
	}
	return kvs
}

// groupBySender groups tx indices by the s= field of their memos, in order.
func groupBySender(memos []string) [][]int {
	idx := map[string]int{}
	var groups [][]int
	for i, m := range memos {
		q, _ := url.ParseQuery(m)
		g, ok := idx[q.Get("s")]
		if !ok {
			g = len(groups)
			idx[q.Get("s")] = g
			groups = append(groups, nil)
		}
		groups[g] = append(groups[g], i)
	}
	return groups
}

func TestRecheckExecutorMatchesSequential(t *testing.T) {
	seq, par := newRecheckSuite(t), newRecheckSuite(t)
	var memos []string
	for n := range 4 {
		for s := range 8 {
			memos = append(memos, fmt.Sprintf("s=%d&n=%d&f=%s", s, n, string(make([]byte, s+n))))
		}
	}
	memos = append(memos, "s=3&n=9&f=xx", "s=1&n=0&f=x", "s=1&n=4&f=x") // gap, stale, next
	txs := make([][]byte, len(memos))
	for i, m := range memos {
		txs[i] = seq.recheckTx(t, m)
	}

	want := recheckSequential(t, seq.baseApp, txs)
	exec, got, err := par.recheckParallel(t, txs, groupBySender(memos), 4)
	require.NoError(t, err)
	require.True(t, exec.Fresh())
	require.Equal(t, want, got)
	require.Equal(t, checkStateKVs(seq.baseApp), checkStateKVs(par.baseApp))

	// A second batch at the same height reads the first one's writes through checkState.
	next := [][]byte{seq.recheckTx(t, "s=0&n=4&f=x"), seq.recheckTx(t, "s=2&n=4&f=x")}
	want = recheckSequential(t, seq.baseApp, next)
	exec, got, err = par.recheckParallel(t, next, [][]int{{0}, {1}}, 2)
	require.NoError(t, err)
	require.False(t, exec.Fresh())
	require.True(t, got[0].IsOK(), got[0].Log)
	require.Equal(t, want, got)
	require.Equal(t, checkStateKVs(seq.baseApp), checkStateKVs(par.baseApp))
}

func TestRecheckExecutorDirtyCheckState(t *testing.T) {
	s := newRecheckSuite(t)
	// A CheckTx writes checkState outside the executor: branches read through it.
	rsp, err := s.baseApp.CheckTx(&abci.RequestCheckTx{Tx: s.recheckTx(t, "s=0&n=0&f=x")})
	require.NoError(t, err)
	require.True(t, rsp.IsOK(), rsp.Log)
	exec, got, err := s.recheckParallel(t, [][]byte{s.recheckTx(t, "s=0&n=1&f=x")}, [][]int{{0}}, 1)
	require.NoError(t, err)
	require.False(t, exec.Fresh())
	require.True(t, got[0].IsOK(), got[0].Log)
	require.Equal(t, string(sdk.Uint64ToBigEndian(2)), checkStateKVs(s.baseApp)["key1/seq/0"])

	// Commit resets checkState.
	commitBlock(t, s.baseApp, 2)
	exec, _, err = s.recheckParallel(t, nil, nil, 1)
	require.NoError(t, err)
	require.True(t, exec.Fresh())
}

func TestRecheckExecutorPrepareCheckStater(t *testing.T) {
	s := newRecheckSuite(t, func(app *baseapp.BaseApp) {
		app.SetPrepareCheckStater(func(sdk.Context) {})
	})
	exec, _, err := s.recheckParallel(t, nil, nil, 1)
	require.NoError(t, err)
	require.False(t, exec.Fresh())
}

func TestRecheckExecutorConflicts(t *testing.T) {
	for name, tc := range map[string]struct {
		memos  []string
		groups [][]int
		ok     bool
	}{
		"read of another group's write": {[]string{"s=0&n=0&f=x", "s=1&n=0&f=x&r=0"}, [][]int{{0}, {1}}, false},
		"same key in two groups":        {[]string{"s=0&n=0&f=x", "s=0&n=0&f=x"}, [][]int{{0}, {1}}, false},
		"different blind writes":        {[]string{"s=0&n=0&f=x&w=k&v=a", "s=1&n=0&f=x&w=k&v=b"}, [][]int{{0}, {1}}, false},
		"identical blind writes":        {[]string{"s=0&n=0&f=x&w=k&v=a", "s=1&n=0&f=x&w=k&v=a"}, [][]int{{0}, {1}}, true},
		"iterator over own writes":      {[]string{"s=0&n=0&f=x&w=k", "s=0&n=1&f=x&it=1"}, [][]int{{0, 1}}, false},
		"iterator over a key two wrote": {[]string{"s=0&n=0&f=x&it=1&w=k&v=a", "s=1&n=0&f=x&w=k&v=a"}, [][]int{{0}, {1}}, false},
		"iterator over an additive key": {[]string{"s=0&n=0&f=x&itf=1", "s=1&n=0&f=x"}, [][]int{{0}, {1}}, false},
		"iterator on a clean branch":    {[]string{"s=0&n=0&f=x&it=1", "s=1&n=0&f=x"}, [][]int{{0}, {1}}, true},
	} {
		t.Run(name, func(t *testing.T) {
			s := newRecheckSuite(t)
			txs := make([][]byte, len(tc.memos))
			for i, m := range tc.memos {
				txs[i] = s.recheckTx(t, m)
			}
			before := checkStateKVs(s.baseApp)
			_, _, err := s.recheckParallel(t, txs, tc.groups, 1)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, baseapp.ErrRecheckConflict)
			require.Equal(t, before, checkStateKVs(s.baseApp), "nothing merged")
		})
	}
}

package oe

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
)

func testFinalizeBlock(_ context.Context, _ *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	return nil, errors.New("test error")
}

func TestOptimisticExecution(t *testing.T) {
	oe := NewOptimisticExecution(log.NewNopLogger(), testFinalizeBlock)
	assert.True(t, oe.Enabled())
	oe.Execute(&abci.RequestProcessProposal{
		Hash: []byte("test"),
	})
	assert.True(t, oe.Initialized())

	resp, err := oe.WaitResult()
	assert.Nil(t, resp)
	assert.EqualError(t, err, "test error")

	assert.False(t, oe.AbortIfNeeded([]byte("test"), nil))
	assert.True(t, oe.AbortIfNeeded([]byte("wrong_hash"), nil))

	oe.Reset()
}

func TestOptimisticExecutionTxsTransform(t *testing.T) {
	ok := func(context.Context, *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
		return &abci.ResponseFinalizeBlock{}, nil
	}
	oe := NewOptimisticExecution(log.NewNopLogger(), ok, WithTxsTransform(func(txs [][]byte) [][]byte {
		assert.Equal(t, [][]byte{[]byte("a")}, txs)
		return [][]byte{[]byte("x")}
	}))
	oe.Execute(&abci.RequestProcessProposal{Hash: []byte("h"), Txs: [][]byte{[]byte("a")}})
	_, _ = oe.WaitResult()
	assert.False(t, oe.AbortIfNeeded([]byte("h"), [][]byte{[]byte("x")}))
	assert.True(t, oe.AbortIfNeeded([]byte("h"), [][]byte{[]byte("a")}))

	oe = NewOptimisticExecution(log.NewNopLogger(), ok, WithTxsTransform(func([][]byte) [][]byte { panic("boom") }))
	oe.Execute(&abci.RequestProcessProposal{Hash: []byte("h")})
	assert.False(t, oe.Initialized())
}

// A panic in the speculative run is re-raised only when the result is served.
func TestOptimisticExecutionPanic(t *testing.T) {
	fn := func(_ context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
		if string(req.Hash) == "bad" {
			panic("boom")
		}
		return &abci.ResponseFinalizeBlock{}, nil
	}
	oe := NewOptimisticExecution(log.NewNopLogger(), fn)
	oe.Execute(&abci.RequestProcessProposal{Hash: []byte("bad")})
	assert.True(t, oe.AbortIfNeeded([]byte("other"), nil))
	assert.NotPanics(t, oe.Abort)

	oe.Execute(&abci.RequestProcessProposal{Hash: []byte("ok")})
	assert.False(t, oe.AbortIfNeeded([]byte("ok"), nil))
	assert.NotPanics(t, func() { _, _ = oe.WaitResult() })

	oe.Execute(&abci.RequestProcessProposal{Hash: []byte("bad")})
	assert.False(t, oe.AbortIfNeeded([]byte("bad"), nil))
	assert.PanicsWithValue(t, "boom", func() { _, _ = oe.WaitResult() })
}

// Every RequestFinalizeBlock field must be copied from ProcessProposal, so a new
// field cannot be dropped from the speculative request silently.
func TestOptimisticExecutionCopiesAllFields(t *testing.T) {
	pp := &abci.RequestProcessProposal{
		Txs:                [][]byte{[]byte("tx")},
		ProposedLastCommit: abci.CommitInfo{Round: 1},
		Misbehavior:        []abci.Misbehavior{{Height: 1}},
		Hash:               []byte("hash"),
		Height:             2,
		Time:               time.Unix(1, 0),
		NextValidatorsHash: []byte("nvh"),
		ProposerAddress:    []byte("proposer"),
		SquareSize:         4,
		DataRootHash:       []byte("droot"),
		Header:             &cmtproto.Header{Height: 2},
	}
	ppv := reflect.ValueOf(pp).Elem()
	for i := 0; i < ppv.NumField(); i++ {
		if ppv.Type().Field(i).IsExported() {
			require.False(t, ppv.Field(i).IsZero(), "set %s in this test", ppv.Type().Field(i).Name)
		}
	}

	var got *abci.RequestFinalizeBlock
	oe := NewOptimisticExecution(log.NewNopLogger(), func(_ context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
		got = req
		return &abci.ResponseFinalizeBlock{}, nil
	})
	oe.Execute(pp)
	_, err := oe.WaitResult()
	require.NoError(t, err)

	fbv := reflect.ValueOf(got).Elem()
	for i := 0; i < fbv.NumField(); i++ {
		f := fbv.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		src := f.Name
		if src == "DecidedLastCommit" {
			src = "ProposedLastCommit"
		}
		want := ppv.FieldByName(src)
		require.True(t, want.IsValid(), "RequestFinalizeBlock.%s has no ProcessProposal source", f.Name)
		require.Equal(t, want.Interface(), fbv.Field(i).Interface(), f.Name)
	}
}

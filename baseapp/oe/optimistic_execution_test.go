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
	original := [][]byte{[]byte("a"), []byte("b")}
	transformed := [][]byte{[]byte("x")}
	var seen [][]byte
	record := func(_ context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
		seen = req.Txs
		return &abci.ResponseFinalizeBlock{}, nil
	}

	oe := NewOptimisticExecution(log.NewNopLogger(), record, WithTxsTransform(func(txs [][]byte) [][]byte {
		assert.Equal(t, original, txs)
		return transformed
	}))
	req := &abci.RequestProcessProposal{Hash: []byte("test"), Txs: original}
	oe.Execute(req)
	_, err := oe.WaitResult()
	assert.NoError(t, err)
	assert.Equal(t, transformed, seen)
	assert.Equal(t, [][]byte{[]byte("a"), []byte("b")}, req.Txs)

	oe = NewOptimisticExecution(log.NewNopLogger(), record)
	oe.Execute(req)
	_, err = oe.WaitResult()
	assert.NoError(t, err)
	assert.Equal(t, original, seen)
}

func TestOptimisticExecutionTxsMismatchAndPanic(t *testing.T) {
	ok := func(context.Context, *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
		return &abci.ResponseFinalizeBlock{}, nil
	}
	oe := NewOptimisticExecution(log.NewNopLogger(), ok, WithTxsTransform(func([][]byte) [][]byte {
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

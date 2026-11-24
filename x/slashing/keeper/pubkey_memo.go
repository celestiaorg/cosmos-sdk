package keeper

import (
	"bytes"
	"sync"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

const pubkeyMemoMax = 4096

// pubkeyMemo remembers, per address, the last stored pubkey bytes that
// decoded successfully (see checkPubkey).
type pubkeyMemo struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newPubkeyMemo() *pubkeyMemo {
	return &pubkeyMemo{m: make(map[string][]byte)}
}

// checkPubkey returns the error GetPubkey would return for a, with the same
// single store read. With the execution caches on it skips the decode when
// the stored bytes equal bytes that already decoded.
func (k Keeper) checkPubkey(ctx sdk.Context, a cryptotypes.Address) error {
	bz, err := k.getPubkeyBytes(ctx, a)
	if err != nil {
		return err
	}
	memo := k.pubkeyMemo
	if !sdk.ExecutionCachesEnabled() || memo == nil {
		_, err := k.decodePubkey(bz)
		return err
	}
	memo.mu.Lock()
	seen, ok := memo.m[string(a)]
	memo.mu.Unlock()
	if ok && bytes.Equal(seen, bz) {
		return nil
	}
	if _, err := k.decodePubkey(bz); err != nil {
		return err
	}
	memo.mu.Lock()
	if len(memo.m) >= pubkeyMemoMax {
		memo.m = make(map[string][]byte)
	}
	memo.m[string(a)] = append([]byte(nil), bz...)
	memo.mu.Unlock()
	return nil
}

package keeper

import (
	"bytes"
	"fmt"
	"sync"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/slashing/types"
)

const pubkeyMemoMax = 4096

// pubkeyMemo remembers, per store key, the last stored pubkey bytes that
// decoded successfully (see checkPubkey).
type pubkeyMemo struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newPubkeyMemo() *pubkeyMemo {
	return &pubkeyMemo{m: make(map[string][]byte)}
}

// checkPubkey returns the error GetPubkey would return for a, with the same
// single store read (same gas). With the execution caches on it skips the
// decode when the stored bytes equal bytes that already decoded; any other
// bytes take the stock decode and its error.
func (k Keeper) checkPubkey(ctx sdk.Context, a cryptotypes.Address) error {
	memo := k.pubkeyMemo
	if !sdk.ExecutionCachesEnabled() || memo == nil {
		_, err := k.GetPubkey(ctx, a)
		return err
	}
	key := types.AddrPubkeyRelationKey(a)
	bz := ctx.KVStore(k.storeKey).Get(key)
	if bz == nil {
		return fmt.Errorf("address %s not found", sdk.ConsAddress(a))
	}
	memo.mu.Lock()
	seen, ok := memo.m[string(key)]
	memo.mu.Unlock()
	if ok && bytes.Equal(seen, bz) {
		return nil
	}
	var pk cryptotypes.PubKey
	if err := k.cdc.UnmarshalInterface(bz, &pk); err != nil {
		return err
	}
	memo.mu.Lock()
	if len(memo.m) >= pubkeyMemoMax {
		memo.m = make(map[string][]byte)
	}
	memo.m[string(key)] = append([]byte(nil), bz...)
	memo.mu.Unlock()
	return nil
}

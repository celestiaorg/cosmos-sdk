package keeper

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/multisig"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256r1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/types"
)

func newTestAccountCodec() accountCodec {
	ir := codectypes.NewInterfaceRegistry()
	types.RegisterInterfaces(ir)
	cryptocodec.RegisterInterfaces(ir)
	return newAccountCodec(codec.CollInterfaceValue[sdk.AccountI](codec.NewProtoCodec(ir)))
}

func (c accountCodec) cached() (n int) {
	for i := range c.shards {
		n += len(c.shards[i].m)
	}
	return n
}

func TestAccountCodecReturnsIndependentCopies(t *testing.T) {
	ac := newTestAccountCodec()
	pk := secp256k1.GenPrivKey().PubKey()
	base := types.NewBaseAccount(sdk.AccAddress(pk.Address()), pk, 7, 3)
	mod := types.NewModuleAccount(types.NewBaseAccountWithAddress(types.NewModuleAddress("fee")), "fee", types.Minter)

	for _, acc := range []sdk.AccountI{base, mod} {
		bz, err := ac.Encode(acc)
		require.NoError(t, err)
		first, err := ac.Decode(bz)
		require.NoError(t, err)
		second, err := ac.Decode(bz)
		require.NoError(t, err)
		require.NotSame(t, first, second)

		require.NoError(t, first.SetSequence(99))
		switch a := first.(type) {
		case *types.BaseAccount:
			require.NoError(t, a.SetPubKey(secp256k1.GenPrivKey().PubKey()))
		case *types.ModuleAccount:
			a.Permissions = append(a.Permissions, types.Burner)
		}
		third, err := ac.Decode(bz)
		require.NoError(t, err)
		for _, got := range []sdk.AccountI{second, third} {
			require.Equal(t, acc.GetSequence(), got.GetSequence())
			require.Equal(t, acc.GetPubKey(), got.GetPubKey())
			gotBz, err := ac.Encode(got)
			require.NoError(t, err)
			require.Equal(t, bz, gotBz)
		}
	}
	require.Equal(t, 2, ac.cached())
}

func TestAccountCodecSkipsOtherPubKeys(t *testing.T) {
	ac := newTestAccountCodec()
	pks := []cryptotypes.PubKey{secp256k1.GenPrivKey().PubKey(), secp256k1.GenPrivKey().PubKey()}
	r1, err := secp256r1.GenPrivKey()
	require.NoError(t, err)
	for _, pk := range []cryptotypes.PubKey{multisig.NewLegacyAminoPubKey(1, pks), r1.PubKey()} {
		bz, err := ac.Encode(types.NewBaseAccount(sdk.AccAddress(pk.Address()), pk, 1, 0))
		require.NoError(t, err)
		first, err := ac.Decode(bz)
		require.NoError(t, err)
		second, err := ac.Decode(bz)
		require.NoError(t, err)
		require.NotSame(t, first.GetPubKey(), second.GetPubKey())
	}
	require.Zero(t, ac.cached())
}

func TestAccountCodecConcurrentDecode(t *testing.T) {
	ac := newTestAccountCodec()
	pk := secp256k1.GenPrivKey().PubKey()
	bz, err := ac.Encode(types.NewBaseAccount(sdk.AccAddress(pk.Address()), pk, 7, 3))
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			acc, err := ac.Decode(bz)
			require.NoError(t, err)
			require.True(t, acc.GetPubKey().Equals(pk))
			require.NoError(t, acc.SetSequence(acc.GetSequence()+1))
			_, err = ac.Encode(acc)
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	acc, err := ac.Decode(bz)
	require.NoError(t, err)
	require.Equal(t, uint64(3), acc.GetSequence())
}

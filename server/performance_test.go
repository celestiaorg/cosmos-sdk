package server

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/log"
	dbm "github.com/tendermint/tm-db"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
)

// A [performance] key missing from app.toml means its default (on).
func TestSignaturePreverifyOption(t *testing.T) {
	defer secp256k1.EnableSigCache(false)
	for _, tc := range []struct {
		set  interface{}
		want bool
	}{{nil, true}, {true, true}, {false, false}, {"false", false}} {
		v := viper.New()
		v.Set(flags.FlagHome, t.TempDir())
		v.Set(FlagPruning, "everything")
		if tc.set != nil {
			v.Set(FlagSignaturePreverify, tc.set)
		}
		baseapp.NewBaseApp("test", log.NewNopLogger(), dbm.NewMemDB(), nil, DefaultBaseappOptions(v)...)
		require.Equal(t, tc.want, secp256k1.SigCacheEnabled(), "set=%v", tc.set)
	}
}

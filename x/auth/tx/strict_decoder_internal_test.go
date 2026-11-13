package tx

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
)

// TestDecodersNonCriticalFields checks that DefaultTxDecoder still lets a
// non-critical unknown TxBody field through and flags it, while
// StrictTxDecoder rejects it.
func TestDecodersNonCriticalFields(t *testing.T) {
	registry := codectypes.NewInterfaceRegistry()
	testdata.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	msg, err := codectypes.NewAnyWithValue(testdata.NewTestMsg())
	require.NoError(t, err)
	anyBz, err := msg.Marshal()
	require.NoError(t, err)

	field := func(num protowire.Number, v []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(nil, num, protowire.BytesType), v)
	}
	body := append(field(1, anyBz), field(1025, []byte("x"))...)
	raw := append(field(1, body), field(2, nil)...)

	decoded, err := DefaultTxDecoder(cdc)(raw)
	require.NoError(t, err)
	require.True(t, decoded.(*wrapper).txBodyHasUnknownNonCriticals)

	_, err = StrictTxDecoder(cdc)(raw)
	require.ErrorContains(t, err, "errUnknownField")

	// A critical unknown field fails both.
	critical := append(field(1, append(field(1, anyBz), field(20, []byte("x"))...)), field(2, nil)...)
	_, err = DefaultTxDecoder(cdc)(critical)
	require.Error(t, err)
	_, err = StrictTxDecoder(cdc)(critical)
	require.Error(t, err)
}

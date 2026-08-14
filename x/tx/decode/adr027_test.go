package decode

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestRejectNonADR027TxRaw(t *testing.T) {
	// field returns the encoding of a single length-delimited TxRaw field.
	field := func(num protowire.Number, value string) []byte {
		return protowire.AppendBytes(protowire.AppendTag(nil, num, protowire.BytesType), []byte(value))
	}
	concat := func(bzs ...[]byte) []byte {
		out := []byte{}
		for _, bz := range bzs {
			out = append(out, bz...)
		}
		return out
	}

	body := field(1, "body")
	authInfo := field(2, "authInfo")
	sig := field(3, "sig")

	testCases := []struct {
		name      string
		txBytes   []byte
		shouldErr bool
	}{
		{
			name:    "body, authInfo, sig",
			txBytes: concat(body, authInfo, sig),
		},
		{
			name:    "multiple signatures are allowed",
			txBytes: concat(body, authInfo, sig, sig, sig),
		},
		{
			name:      "descending field numbers",
			txBytes:   concat(authInfo, body, sig),
			shouldErr: true,
		},
		{
			name:      "duplicate body",
			txBytes:   concat(body, body, authInfo, sig),
			shouldErr: true,
		},
		{
			name:      "duplicate authInfo",
			txBytes:   concat(body, authInfo, authInfo, sig),
			shouldErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectNonADR027TxRaw(tc.txBytes)
			if tc.shouldErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

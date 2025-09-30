package baseapp_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/cosmos/cosmos-sdk/baseapp"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/signing"
)

// mockTx is a mock transaction that implements signing.SigVerifiableTx
type mockTx struct {
	msgs    []sdk.Msg
	signers [][]byte
}

func (m mockTx) GetMsgs() []sdk.Msg {
	return m.msgs
}

func (m mockTx) ValidateBasic() error {
	return nil
}

func (m mockTx) GetSigners() ([][]byte, error) {
	return m.signers, nil
}

func (m mockTx) GetMsgsV2() ([]proto.Message, error) {
	return nil, nil
}

func (m mockTx) GetPubKeys() ([]cryptotypes.PubKey, error) {
	return nil, nil
}

func (m mockTx) GetSignaturesV2() ([]signingtypes.SignatureV2, error) {
	return nil, nil
}

var _ sdk.Tx = mockTx{}
var _ signing.SigVerifiableTx = mockTx{}

func TestExtractTxSigners(t *testing.T) {
	_, _, addr1 := testdata.KeyTestPubAddr()
	_, _, addr2 := testdata.KeyTestPubAddr()

	testCases := []struct {
		name          string
		tx            sdk.Tx
		expectedLen   int
		expectedAddrs []sdk.AccAddress
		expectError   bool
	}{
		{
			name: "transaction with multiple signers",
			tx: mockTx{
				signers: [][]byte{addr1.Bytes(), addr2.Bytes()},
			},
			expectedLen:   2,
			expectedAddrs: []sdk.AccAddress{addr1, addr2},
			expectError:   false,
		},
		{
			name: "transaction with single signer",
			tx: mockTx{
				signers: [][]byte{addr1.Bytes()},
			},
			expectedLen:   1,
			expectedAddrs: []sdk.AccAddress{addr1},
			expectError:   false,
		},
		{
			name: "transaction with no signers",
			tx: mockTx{
				signers: [][]byte{},
			},
			expectedLen:   0,
			expectedAddrs: []sdk.AccAddress{},
			expectError:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			signers, err := baseapp.ExtractTxSigners(tc.tx)

			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Len(t, signers, tc.expectedLen)

				for i, expectedAddr := range tc.expectedAddrs {
					require.Equal(t, expectedAddr.Bytes(), signers[i])
				}
			}
		})
	}
}
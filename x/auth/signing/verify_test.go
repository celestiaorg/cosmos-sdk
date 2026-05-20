package signing

import (
	"testing"

	signingv1beta1 "cosmossdk.io/api/cosmos/tx/signing/v1beta1"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/stretchr/testify/require"
)

func TestEIP712SignModeConversion(t *testing.T) {
	internal, err := APISignModeToInternal(signingv1beta1.SignMode_SIGN_MODE_EIP_712)
	require.NoError(t, err)
	require.Equal(t, signing.SignMode_SIGN_MODE_EIP_712, internal)

	api, err := internalSignModeToAPI(signing.SignMode_SIGN_MODE_EIP_712)
	require.NoError(t, err)
	require.Equal(t, signingv1beta1.SignMode_SIGN_MODE_EIP_712, api)
}

func TestEthereumTxSignModeConversion(t *testing.T) {
	internal, err := APISignModeToInternal(signingv1beta1.SignMode_SIGN_MODE_ETHEREUM_TX)
	require.NoError(t, err)
	require.Equal(t, signing.SignMode_SIGN_MODE_ETHEREUM_TX, internal)

	api, err := internalSignModeToAPI(signing.SignMode_SIGN_MODE_ETHEREUM_TX)
	require.NoError(t, err)
	require.Equal(t, signingv1beta1.SignMode_SIGN_MODE_ETHEREUM_TX, api)
}

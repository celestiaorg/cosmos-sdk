package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func TestExecutionCachesDefaultOn(t *testing.T) {
	require.True(t, sdk.ExecutionCachesEnabled())
	sdk.SetExecutionCaches(false)
	require.False(t, sdk.ExecutionCachesEnabled())
	sdk.SetExecutionCaches(true)
	require.True(t, sdk.ExecutionCachesEnabled())
}

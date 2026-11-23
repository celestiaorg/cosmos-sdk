package server

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestBoolDefaultTrue(t *testing.T) {
	v := viper.New()
	require.True(t, boolDefaultTrue(v, FlagParallelCommit), "a missing key means true")
	v.Set(FlagParallelCommit, false)
	require.False(t, boolDefaultTrue(v, FlagParallelCommit))
	v.Set(FlagParallelCommit, "true")
	require.True(t, boolDefaultTrue(v, FlagParallelCommit))
}

package server

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestBoolDefaultTrue(t *testing.T) {
	v := viper.New()
	require.True(t, boolDefaultTrue(v, FlagExecutionCaches), "a missing key means true")
	v.Set(FlagExecutionCaches, false)
	require.False(t, boolDefaultTrue(v, FlagExecutionCaches))
	v.Set(FlagExecutionCaches, "true")
	require.True(t, boolDefaultTrue(v, FlagExecutionCaches))
}

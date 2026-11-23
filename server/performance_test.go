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
	v.Set(FlagAsyncPruning, "true")
	require.True(t, boolDefaultTrue(v, FlagAsyncPruning))
}

func TestDeferredPersistenceOpts(t *testing.T) {
	v := viper.New()
	mode, interval := deferredPersistenceOpts(v)
	require.Equal(t, "sync", mode, "a missing key means sync")
	require.Equal(t, int64(100), interval)
	v.Set(FlagDeferredPersistence, "off")
	v.Set(FlagDeferredFlushInterval, 7)
	mode, interval = deferredPersistenceOpts(v)
	require.Equal(t, "off", mode)
	require.Equal(t, int64(7), interval)
}

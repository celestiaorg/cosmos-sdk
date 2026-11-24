package server

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/server/config"
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

func TestDeferredPersistenceConfigValidation(t *testing.T) {
	v := viper.New()
	v.Set(FlagDeferredPersistence, " Always ")
	mode, _ := deferredPersistenceOpts(v)
	require.Equal(t, "always", mode)

	cfg := config.DefaultConfig()
	cfg.MinGasPrices = "0.002utia" // this tag still requires min-gas-prices
	cfg.Performance.DeferredPersistence = "SYNC"
	require.NoError(t, cfg.ValidateBasic())
	cfg.Performance.DeferredPersistence = "fast"
	require.ErrorContains(t, cfg.ValidateBasic(), "deferred-persistence")
	cfg.Performance.DeferredPersistence = "off"
	cfg.Performance.DeferredFlushInterval = -1
	require.ErrorContains(t, cfg.ValidateBasic(), "deferred-flush-interval")
}

func TestBoolDefaultTrueAppOptions(t *testing.T) {
	opts := mapOptions{"a": false, "b": "false", "c": true}
	require.True(t, boolDefaultTrue(opts, "missing"))
	require.False(t, boolDefaultTrue(opts, "a"))
	require.False(t, boolDefaultTrue(opts, "b"))
	require.True(t, boolDefaultTrue(opts, "c"))
}

type mapOptions map[string]interface{}

func (m mapOptions) Get(key string) interface{} { return m[key] }

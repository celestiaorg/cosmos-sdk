package config

import (
	"bytes"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestPerformanceConfigDefaultsAndTemplate(t *testing.T) {
	cfg := DefaultConfig()
	require.True(t, cfg.Performance.ParallelCommit)

	var buf bytes.Buffer
	require.NoError(t, configTemplate.Execute(&buf, cfg))
	rendered := buf.Bytes()
	require.Contains(t, string(rendered), "[performance]")

	v := viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(bytes.NewReader(rendered)))
	read, err := GetConfig(v)
	require.NoError(t, err)
	require.True(t, read.Performance.ParallelCommit)

	off := bytes.Replace(rendered, []byte("parallel-commit = true"), []byte("parallel-commit = false"), 1)
	v = viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(bytes.NewReader(off)))
	read, err = GetConfig(v)
	require.NoError(t, err)
	require.False(t, read.Performance.ParallelCommit)
}

func TestValidateIAVLCacheSize(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinGasPrices = "0stake"
	require.NoError(t, cfg.ValidateBasic())
	cfg.IAVLCacheSize = MaxIAVLCacheSize
	require.NoError(t, cfg.ValidateBasic())
	cfg.IAVLCacheSize = MaxIAVLCacheSize + 1
	require.Error(t, cfg.ValidateBasic())
}

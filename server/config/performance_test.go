package config

import (
	"bytes"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestPerformanceConfigDefaultsAndTemplate(t *testing.T) {
	cfg := DefaultConfig()
	require.True(t, cfg.Performance.ExecutionCaches)
	require.False(t, cfg.Performance.BlocksyncEventsOff)

	var buf bytes.Buffer
	require.NoError(t, configTemplate.Execute(&buf, cfg))
	rendered := buf.Bytes()
	require.Contains(t, string(rendered), "[performance]")

	v := viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(bytes.NewReader(rendered)))
	read, err := GetConfig(v)
	require.NoError(t, err)
	require.True(t, read.Performance.ExecutionCaches)

	off := bytes.Replace(rendered, []byte("execution-caches = true"), []byte("execution-caches = false"), 1)
	v = viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(bytes.NewReader(off)))
	read, err = GetConfig(v)
	require.NoError(t, err)
	require.False(t, read.Performance.ExecutionCaches)
}

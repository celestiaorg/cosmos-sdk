package server

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBoolDefaultTrue(t *testing.T) {
	opts := mapOptions{"a": false, "b": "false", "c": true}
	require.True(t, boolDefaultTrue(opts, "missing"))
	require.False(t, boolDefaultTrue(opts, "a"))
	require.False(t, boolDefaultTrue(opts, "b"))
	require.True(t, boolDefaultTrue(opts, "c"))
}

type mapOptions map[string]interface{}

func (m mapOptions) Get(key string) interface{} { return m[key] }

package parallel

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	var calls atomic.Int64
	out := make([]int, 16)
	Run(len(out), func(i int) { out[i] = i * i; calls.Add(1) })
	require.EqualValues(t, 16, calls.Load())
	for i, v := range out {
		require.Equal(t, i*i, v)
	}

	calls.Store(0)
	require.PanicsWithValue(t, "boom", func() {
		Run(8, func(i int) {
			calls.Add(1)
			if i == 3 {
				panic("boom")
			}
		})
	})
	require.EqualValues(t, 8, calls.Load(), "the other calls still run")
}

package types

import (
	"fmt"
	"sync"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// TestCelestiaParamsCacheConcurrent exercises the memo from many goroutines
// (queries and CheckTx read params while blocks execute); run with -race.
func TestCelestiaParamsCacheConcurrent(t *testing.T) {
	defer sdk.SetExecutionCaches(sdk.ExecutionCachesEnabled())
	sdk.SetExecutionCaches(true)
	s := Subspace{name: []byte("concurrent")}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				key := []byte{byte('a' + (i+g)%5)}
				raw := []byte(fmt.Sprintf(`"%d.%018d"`, (i+g)%5, 0))
				var d sdk.Dec
				if !s.celestiaParamsCacheGet(key, raw, &d) {
					d = sdk.MustNewDecFromStr(fmt.Sprintf("%d", (i+g)%5))
					s.celestiaParamsCachePut(key, raw, &d)
				}
				if !d.Equal(sdk.NewDec(int64((i + g) % 5))) {
					t.Errorf("got %s", d)
					return
				}
				d.MulInt64Mut(3) // mutating a returned value must not reach the memo
			}
		}(g)
	}
	wg.Wait()
}

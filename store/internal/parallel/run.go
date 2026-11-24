// Package parallel runs independent store operations concurrently.
package parallel

import "sync"

// Run calls fn(0) ... fn(n-1) on their own goroutines and waits for all of
// them. If any call panics, Run re-raises the first panic in the caller.
func Run(n int, fn func(i int)) {
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		panicVal any
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					if panicVal == nil {
						panicVal = r
					}
					mu.Unlock()
				}
			}()
			fn(i)
		}(i)
	}
	wg.Wait()
	if panicVal != nil {
		panic(panicVal)
	}
}

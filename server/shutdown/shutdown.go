// Package shutdown runs registered hooks when the server receives SIGINT or
// SIGTERM (server.WaitForQuitSignals), before WaitForQuitSignals returns.
//
// Added for the celestia deferred IAVL persistence prototype
// (CELESTIA_IAVL_FLUSH_EVERY): the start command waits for quit signals in two
// goroutines (wrapCPUProfile and startInProcess/startStandAlone) and the process
// exits as soon as the outer one returns, so the deferred BaseApp.Close() races
// with process exit and is not a reliable place to persist state. Hooks run
// once; concurrent callers of Run block until they have finished.
// It is a leaf package so that store packages can register hooks.
package shutdown

import "sync"

var (
	mu    sync.Mutex
	hooks []func()
	once  sync.Once
)

// Register adds a hook run on SIGINT/SIGTERM.
func Register(fn func()) {
	mu.Lock()
	defer mu.Unlock()
	hooks = append(hooks, fn)
}

// Run runs every registered hook once.
func Run() {
	once.Do(func() {
		mu.Lock()
		hs := append([]func(){}, hooks...)
		mu.Unlock()
		for _, h := range hs {
			h()
		}
	})
}

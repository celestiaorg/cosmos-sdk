// Package shutdown runs registered hooks when the server receives SIGINT or
// SIGTERM, before server.WaitForQuitSignals returns. The start command exits
// as soon as WaitForQuitSignals returns, so a deferred BaseApp.Close races
// with process exit; hooks that must persist state (e.g. the deferred IAVL
// flush) register here instead. It is a leaf package so stores can use it.
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

// Run runs every registered hook once, in registration order. Concurrent
// callers block until the hooks have finished.
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

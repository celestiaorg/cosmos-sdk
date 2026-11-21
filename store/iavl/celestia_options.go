package iavl

import (
	"os"

	"github.com/cosmos/iavl"
)

// Celestia sync-speedup prototype.
//
// CELESTIA_IAVL_ASYNC_PRUNING=1 opens every IAVL tree with
// iavl.AsyncPruningOption(true): DeleteVersionsTo only records the target
// version and a per-tree goroutine deletes the old versions in the
// background, instead of inside Commit. Upstream cosmossdk.io/store v1.1.2
// (used by the celestia-app v4-v6 children) passes this option by default;
// the celestia store fork used by v8-v10 does not. Pruning does not affect
// the app hash.
var celestiaAsyncPruning = os.Getenv("CELESTIA_IAVL_ASYNC_PRUNING") == "1"

func celestiaTreeOptions(opts ...iavl.Option) []iavl.Option {
	if celestiaAsyncPruning {
		opts = append(opts, iavl.AsyncPruningOption(true))
	}
	return opts
}

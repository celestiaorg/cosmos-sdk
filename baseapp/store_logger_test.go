package baseapp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/log"
	dbm "github.com/tendermint/tm-db"

	pruningtypes "github.com/cosmos/cosmos-sdk/pruning/types"
	storetypes "github.com/cosmos/cosmos-sdk/store/types"
)

// lineLogger records Info lines with the key-values of every With call.
type lineLogger struct {
	log.Logger
	kv    []interface{}
	lines *[]string
}

func (l lineLogger) Info(msg string, kv ...interface{}) {
	*l.lines = append(*l.lines, fmt.Sprint(append(append([]interface{}{msg}, l.kv...), kv...)...))
}

func (l lineLogger) With(kv ...interface{}) log.Logger {
	return lineLogger{Logger: l.Logger, kv: append(append([]interface{}{}, l.kv...), kv...), lines: l.lines}
}

// The multistore BaseApp creates logs the deferred persistence decision
// through the app's logger (module=store), not a no-op logger.
func TestStoreLogsDeferredDecisionThroughAppLogger(t *testing.T) {
	var lines []string
	logger := lineLogger{Logger: log.NewNopLogger(), lines: &lines}
	app := NewBaseApp("test", logger, dbm.NewMemDB(), nil,
		SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningEverything)),
		SetDeferredPersistence("always", 0, 0),
	)
	app.MountStores(storetypes.NewKVStoreKey("main"))
	require.NoError(t, app.LoadLatestVersion())

	var enabled, adjusted bool
	for _, l := range lines {
		if strings.Contains(l, "deferred IAVL persistence enabled") {
			enabled = true
			require.Contains(t, l, "modulestore", l)
		}
		adjusted = adjusted || strings.Contains(l, "deferred IAVL persistence setting adjusted")
	}
	require.True(t, enabled, lines)
	require.True(t, adjusted, lines)
}

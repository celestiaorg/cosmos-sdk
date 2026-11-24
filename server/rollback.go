package server

import (
	"fmt"

	cmtcmd "github.com/cometbft/cometbft/cmd/cometbft/commands"
	cmtcfg "github.com/cometbft/cometbft/config"
	cmtstate "github.com/cometbft/cometbft/state"
	cmtstore "github.com/cometbft/cometbft/store"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/server/types"
)

// NewRollbackCmd creates a command to rollback CometBFT and multistore state by one height.
func NewRollbackCmd(appCreator types.AppCreator, defaultNodeHome string) *cobra.Command {
	var removeBlock bool

	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "rollback Cosmos SDK and CometBFT state by one height",
		Long: `
A state rollback is performed to recover from an incorrect application state transition,
when CometBFT has persisted an incorrect app hash and is thus unable to make
progress. Rollback overwrites a state at height n with the state at height n - 1.
The application also rolls back to height n - 1. No blocks are removed, so upon
restarting CometBFT the transactions in block n will be re-executed against the
application.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := GetServerContextFromCmd(cmd)
			cfg := ctx.Config
			home := cfg.RootDir
			db, err := openDB(home, GetAppDBBackend(ctx.Viper))
			if err != nil {
				return err
			}

			// A DB last written with deferred persistence keeps one version.
			lastFlushed, deferred, err := readDeferredMarker(db)
			if err != nil {
				return err
			}
			if deferred {
				return rollbackBehindApp(cfg, lastFlushed, removeBlock)
			}
			// A DB last written by the v0.46 line (the v3 child) uses its own marker.
			if handled, err := rollbackV046Deferred(cfg, db, removeBlock); handled || err != nil {
				return err
			}

			// Never load in deferred mode here: opening the DB must not write.
			ctx.Viper.Set(FlagDeferredPersistence, deferredOff)
			app := appCreator(ctx.Logger, db, nil, ctx.Viper)
			// Check the app can reach the target before CometBFT state changes.
			if err := checkAppRollback(cfg, app.CommitMultiStore()); err != nil {
				return err
			}
			// rollback CometBFT state
			height, hash, err := cmtcmd.RollbackState(ctx.Config, removeBlock)
			if err != nil {
				return fmt.Errorf("failed to rollback CometBFT state: %w", err)
			}
			// rollback the multistore

			if err := app.CommitMultiStore().RollbackToVersion(height); err != nil {
				return fmt.Errorf("failed to rollback to version: %w", err)
			}

			fmt.Printf("Rolled back state to height %d and hash %X", height, hash)
			return nil
		},
	}

	cmd.Flags().String(flags.FlagHome, defaultNodeHome, "The application home directory")
	cmd.Flags().BoolVar(&removeBlock, "hard", false, "remove last block as well as state")
	return cmd
}

// rollbackBehindApp rolls back CometBFT state only, for an app DB last written
// with deferred persistence. The app must be at or below the target height;
// on start the handshake replays the blocks above the app's version.
func rollbackBehindApp(cfg *cmtcfg.Config, lastFlushed int64, removeBlock bool) error {
	cometHeight, err := cometLastBlockHeight(cfg)
	if err != nil {
		return err
	}
	if target := cometHeight - 1; lastFlushed > target {
		return errDeferredRollback{LastFlushed: lastFlushed}
	}
	height, hash, err := cmtcmd.RollbackState(cfg, removeBlock)
	if err != nil {
		return fmt.Errorf("failed to rollback CometBFT state: %w", err)
	}
	fmt.Printf("Rolled back CometBFT state to height %d and hash %X. The app state stays at height %d "+
		"(deferred persistence); the node replays the blocks above it on start.\n", height, hash, lastFlushed)
	return nil
}

// cometLastBlockHeight reads the last block height of the CometBFT state.
func cometLastBlockHeight(cfg *cmtcfg.Config) (int64, error) {
	stateDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "state", Config: cfg})
	if err != nil {
		return 0, err
	}
	st, err := cmtstate.NewStore(stateDB, cmtstate.StoreOptions{}).Load()
	_ = stateDB.Close()
	if err != nil {
		return 0, fmt.Errorf("failed to load CometBFT state: %w", err)
	}
	return st.LastBlockHeight, nil
}

// rollbackV046Deferred handles an app DB carrying the v0.46 deferred marker
// before anything is written: it refuses a rollback below the oldest app
// version on disk, and rolls back CometBFT state only for a DB that holds just
// its last deferred flush. handled is false when the regular rollback may run.
func rollbackV046Deferred(cfg *cmtcfg.Config, db dbm.DB, removeBlock bool) (handled bool, err error) {
	deferred, lastFlushed, found, err := readV046DeferredMarker(db)
	if err != nil || !found {
		return false, err
	}
	cometHeight, err := cometLastBlockHeight(cfg)
	if err != nil {
		return false, err
	}
	cometOnly, err := v046RollbackPlan(db, deferred, lastFlushed, cometHeight-1)
	if err != nil || !cometOnly {
		return false, err
	}
	return true, rollbackBehindApp(cfg, lastFlushed, removeBlock)
}

// rollbackChecker is a multistore that can check a rollback without writing
// (the celestia store; upstream store has no such method).
type rollbackChecker interface {
	CheckRollbackToVersion(target int64) ([]byte, error)
}

// checkAppRollback refuses, before anything is written, a rollback the app
// store cannot complete, such as one to a version pruned away when deferred
// persistence turned off.
func checkAppRollback(cfg *cmtcfg.Config, cms any) error {
	checker, ok := cms.(rollbackChecker)
	if !ok {
		return nil
	}
	target, err := cometRollbackTarget(cfg)
	if err != nil {
		return err
	}
	if _, err := checker.CheckRollbackToVersion(target); err != nil {
		return fmt.Errorf("cannot roll back the app state to height %d, nothing was changed: %w", target, err)
	}
	return nil
}

// cometRollbackTarget returns the height cmtcmd.RollbackState rolls back to:
// the state height when the block store is one block ahead, else one below it.
func cometRollbackTarget(cfg *cmtcfg.Config) (int64, error) {
	stateHeight, err := cometLastBlockHeight(cfg)
	if err != nil {
		return 0, err
	}
	bsDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "blockstore", Config: cfg})
	if err != nil {
		return 0, err
	}
	blockHeight := cmtstore.NewBlockStore(bsDB).Height()
	_ = bsDB.Close()
	if blockHeight == stateHeight+1 {
		return stateHeight, nil
	}
	return stateHeight - 1, nil
}

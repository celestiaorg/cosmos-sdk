package server

import (
	"fmt"
	"os"
	"path/filepath"

	cmtcmd "github.com/cometbft/cometbft/cmd/cometbft/commands"
	cmtcfg "github.com/cometbft/cometbft/config"
	cmtstate "github.com/cometbft/cometbft/state"
	cmtstore "github.com/cometbft/cometbft/store"
	"github.com/spf13/cobra"

	"cosmossdk.io/store/rootmulti"
	storetypes "cosmossdk.io/store/types"

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
			lastFlushed, deferred, err := rootmulti.ReadDeferredMarker(db)
			if err != nil {
				return err
			}
			if deferred {
				return rollbackBehindApp(cfg, lastFlushed, removeBlock)
			}

			// Never load in deferred mode here: opening the DB must not write.
			ctx.Viper.Set(FlagDeferredPersistence, rootmulti.DeferredOff)
			app := appCreator(ctx.Logger, db, nil, ctx.Viper)
			defer app.Close()
			// Check the app can roll back before CometBFT state is touched.
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
	stateDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "state", Config: cfg})
	if err != nil {
		return err
	}
	st, err := cmtstate.NewStore(stateDB, cmtstate.StoreOptions{}).Load()
	_ = stateDB.Close()
	if err != nil {
		return fmt.Errorf("failed to load CometBFT state: %w", err)
	}
	if target := st.LastBlockHeight - 1; lastFlushed > target {
		return rootmulti.ErrDeferredRollback{LastFlushed: lastFlushed}
	}
	height, hash, err := cmtcmd.RollbackState(cfg, removeBlock)
	if err != nil {
		return fmt.Errorf("failed to rollback CometBFT state: %w", err)
	}
	fmt.Printf("Rolled back CometBFT state to height %d and hash %X. The app state stays at height %d "+
		"(deferred persistence); the node replays the blocks above it on start.\n", height, hash, lastFlushed)
	return nil
}

// rollbackCheckStore is a multistore that can check a rollback without writing.
type rollbackCheckStore interface {
	CheckRollbackToVersion(target int64) ([]byte, error)
}

// checkAppRollback refuses, without writing, a rollback whose app version
// CometBFT's rollback would target is missing (for example pruned).
func checkAppRollback(cfg *cmtcfg.Config, cms storetypes.CommitMultiStore) error {
	rs, ok := cms.(rollbackCheckStore)
	if !ok {
		return nil
	}
	target, err := cometRollbackTarget(cfg)
	if err != nil {
		return err
	}
	if _, err := rs.CheckRollbackToVersion(target); err != nil {
		return fmt.Errorf("app state cannot roll back to height %d; CometBFT state is untouched: %w", target, err)
	}
	return nil
}

// cometRollbackTarget returns the height cmtcmd.RollbackState rolls back to,
// without writing: the state height when the block store holds one block more
// (an interrupted commit), else the state height minus one.
func cometRollbackTarget(cfg *cmtcfg.Config) (int64, error) {
	for _, name := range []string{"blockstore.db", "state.db"} {
		if _, err := os.Stat(filepath.Join(cfg.DBDir(), name)); err != nil {
			return 0, fmt.Errorf("no CometBFT %s in %s: %w", name, cfg.DBDir(), err)
		}
	}
	stateDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "state", Config: cfg})
	if err != nil {
		return 0, err
	}
	st, err := cmtstate.NewStore(stateDB, cmtstate.StoreOptions{}).Load()
	_ = stateDB.Close()
	if err != nil {
		return 0, fmt.Errorf("failed to load CometBFT state: %w", err)
	}
	blockDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "blockstore", Config: cfg})
	if err != nil {
		return 0, err
	}
	bs := cmtstore.NewBlockStore(blockDB)
	height := bs.Height()
	_ = bs.Close()
	if height == st.LastBlockHeight+1 {
		return st.LastBlockHeight, nil
	}
	return st.LastBlockHeight - 1, nil
}

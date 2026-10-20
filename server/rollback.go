package server

import (
	"bytes"
	"fmt"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtcmd "github.com/cometbft/cometbft/cmd/cometbft/commands"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/spf13/cobra"

	"cosmossdk.io/store/rootmulti"

	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/server/types"
)

// keyAllowPendingRollback is an app option set only by the rollback command.
const keyAllowPendingRollback = "allow-pending-rollback"

// allowPendingRollback is the value of keyAllowPendingRollback; config files and env cannot produce it.
type allowPendingRollback struct{}

// NewRollbackCmd creates a command to roll back CometBFT and multistore state to a height.
func NewRollbackCmd(appCreator types.AppCreator, defaultNodeHome string) *cobra.Command {
	var (
		height      int64
		removeBlock bool
		force       bool
		dryRun      bool
	)

	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Roll back CometBFT and app state to a height",
		Long: `Roll back CometBFT and app state to --height (default: current height - 1).
Blocks above --height+1 are deleted. --hard also deletes block --height+1.
Re-executed blocks must reproduce the committed app hashes; a bad app hash
committed by the network needs surgery or a new chain.
An interrupted app rollback of 2+ heights may need state sync or a snapshot.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := GetServerContextFromCmd(cmd)
			cfg := ctx.Config

			t, err := cmtcmd.LoadRollbackTarget(cfg, height)
			if err != nil {
				return fmt.Errorf("check CometBFT state: %w", err)
			}
			cmd.Println(cmtcmd.RollbackSummary(t, removeBlock))
			if err := cmtcmd.CheckRollbackDepth(t, force); err != nil {
				return err
			}
			target := t.TargetHeight

			info, err := cmtcmd.CheckRollbackState(cfg, target, removeBlock)
			if err != nil {
				return fmt.Errorf("check CometBFT state: %w", err)
			}
			if info.TargetAppVersion != info.CurrentAppVersion {
				return errAcrossUpgrade(info.TargetAppVersion, target, info.CurrentAppVersion)
			}

			db, err := openDB(cfg.RootDir, GetAppDBBackend(ctx.Viper))
			if err != nil {
				return err
			}
			app, err := createRollbackApp(ctx, appCreator, db)
			if err != nil {
				return err
			}
			defer app.Close()

			rs, ok := app.CommitMultiStore().(*rootmulti.Store)
			if !ok {
				return fmt.Errorf("rollback needs *rootmulti.Store, got %T", app.CommitMultiStore())
			}
			appInfo, err := app.Info(&abci.RequestInfo{})
			if err != nil {
				return fmt.Errorf("check app version: %w", err)
			}
			if appInfo.AppVersion != info.TargetAppVersion {
				return errAcrossUpgrade(info.TargetAppVersion, target, appInfo.AppVersion)
			}
			if depth := rs.LatestVersion() - target; depth > cmtcmd.MaxRollbackDepth && !force {
				return fmt.Errorf("app rollback depth %d exceeds %d; use --force", depth, cmtcmd.MaxRollbackDepth)
			}
			hash, err := rs.CheckRollbackToVersion(target)
			if err != nil {
				return fmt.Errorf("check app state: %w", err)
			}
			if !bytes.Equal(hash, info.TargetAppHash) {
				return fmt.Errorf("app hash at height %d is %X; CometBFT expects %X", target, hash, info.TargetAppHash)
			}
			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "dry run: rollback to height %d is possible\n", target)
				return nil
			}

			h, coreHash, err := cmtcmd.RollbackStateToHeight(cfg, target, removeBlock)
			if err != nil {
				return fmt.Errorf("rollback CometBFT state: %w", err)
			}
			if err := rs.RollbackToVersion(target); err != nil {
				return fmt.Errorf("rollback app state: %w", err)
			}
			if appHash := rs.LastCommitID().Hash; !bytes.Equal(appHash, coreHash) {
				return fmt.Errorf("app hash after rollback is %X; CometBFT has %X", appHash, coreHash)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Rolled back to height %d, app hash %X\n", h, coreHash)
			return nil
		},
	}

	cmd.Flags().String(flags.FlagHome, defaultNodeHome, "The application home directory")
	cmd.Flags().Int64Var(&height, "height", 0, "height to roll back to (0 = current height - 1)")
	cmd.Flags().BoolVar(&removeBlock, "hard", false, "also delete block --height+1; one height only")
	cmd.Flags().BoolVar(&force, "force", false, fmt.Sprintf("allow rolling back more than %d heights", cmtcmd.MaxRollbackDepth))
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "run all checks without writing")
	return cmd
}

// createRollbackApp creates the app with pending rollbacks allowed and returns a panic as an error.
func createRollbackApp(ctx *Context, appCreator types.AppCreator, db dbm.DB) (app types.Application, err error) {
	ctx.Viper.Set(keyAllowPendingRollback, allowPendingRollback{})
	defer func() {
		ctx.Viper.Set(keyAllowPendingRollback, nil)
		if r := recover(); r != nil {
			_ = db.Close()
			if e, ok := r.(error); ok {
				err = fmt.Errorf("load app: %w", e)
			} else {
				err = fmt.Errorf("load app: %v", r)
			}
		}
	}()
	return appCreator(ctx.Logger, db, nil, ctx.Viper), nil
}

func errAcrossUpgrade(targetVersion uint64, target int64, currentVersion uint64) error {
	return fmt.Errorf("rollback across an upgrade is not supported (app version %d at height %d, %d now); use surgery or a new chain",
		targetVersion, target, currentVersion)
}

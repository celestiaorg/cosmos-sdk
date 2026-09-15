package cosmovisor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	upgradetypes "cosmossdk.io/x/upgrade/types"
)

func TestParseUpgradeInfoFile(t *testing.T) {
	cases := []struct {
		filename      string
		expectUpgrade upgradetypes.Plan
		expectErr     bool
	}{
		{
			filename:      "f1-good.json",
			expectUpgrade: upgradetypes.Plan{Name: "upgrade1", Info: "some info", Height: 123},
			expectErr:     false,
		},
		{
			filename:      "f2-normalized-name.json",
			expectUpgrade: upgradetypes.Plan{Name: "upgrade2", Info: "some info", Height: 125},
			expectErr:     false,
		},
		{
			filename:      "f2-bad-type.json",
			expectUpgrade: upgradetypes.Plan{},
			expectErr:     true,
		},
		{
			filename:      "f2-bad-type-2.json",
			expectUpgrade: upgradetypes.Plan{},
			expectErr:     true,
		},
		{
			filename:      "f3-empty.json",
			expectUpgrade: upgradetypes.Plan{},
			expectErr:     true,
		},
		{
			filename:      "f4-empty-obj.json",
			expectUpgrade: upgradetypes.Plan{},
			expectErr:     true,
		},
		{
			filename:      "f5-partial-obj-1.json",
			expectUpgrade: upgradetypes.Plan{},
			expectErr:     true,
		},
		{
			filename:      "f5-partial-obj-2.json",
			expectUpgrade: upgradetypes.Plan{},
			expectErr:     true,
		},
		{
			filename:      "unknown.json",
			expectUpgrade: upgradetypes.Plan{},
			expectErr:     true,
		},
	}

	for i := range cases {
		tc := cases[i]
		t.Run(tc.filename, func(t *testing.T) {
			require := require.New(t)
			ui, err := parseUpgradeInfoFile(filepath.Join(".", "testdata", "upgrade-files", tc.filename))
			if tc.expectErr {
				require.Error(err)
			} else {
				require.NoError(err)
				require.Equal(tc.expectUpgrade, ui)
			}
		})
	}
}

// TestCheckUpdateEmptyFile covers the race in which the watcher stats
// upgrade-info.json between the app creating it and writing its contents. The
// watcher must treat a zero-length file as "no update yet" rather than a parse
// error; see https://github.com/cosmos/cosmos-sdk/issues/21086.
func TestCheckUpdateEmptyFile(t *testing.T) {
	require := require.New(t)

	filename := filepath.Join(t.TempDir(), "upgrade-info.json")
	f, err := os.Create(filename)
	require.NoError(err)
	require.NoError(f.Close())

	fw, err := newUpgradeFileWatcher(log.NewNopLogger(), filename, time.Millisecond)
	require.NoError(err)

	require.False(fw.CheckUpdate(upgradetypes.Plan{}))
}

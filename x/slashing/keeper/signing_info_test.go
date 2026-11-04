package keeper_test

import (
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/slashing/testutil"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
)

func (s *KeeperTestSuite) TestValidatorSigningInfo() {
	ctx, keeper := s.ctx, s.slashingKeeper
	require := s.Require()

	signingInfo := slashingtypes.NewValidatorSigningInfo(
		consAddr,
		ctx.BlockHeight(),
		int64(3),
		time.Unix(2, 0),
		false,
		int64(10),
	)

	// set the validator signing information
	keeper.SetValidatorSigningInfo(ctx, consAddr, signingInfo)

	require.True(keeper.HasValidatorSigningInfo(ctx, consAddr))
	info, err := keeper.GetValidatorSigningInfo(ctx, consAddr)
	require.NoError(err)
	require.Equal(info.StartHeight, ctx.BlockHeight())
	require.Equal(info.IndexOffset, int64(3))
	require.Equal(info.JailedUntil, time.Unix(2, 0).UTC())
	require.Equal(info.MissedBlocksCounter, int64(10))

	var signingInfos []slashingtypes.ValidatorSigningInfo

	keeper.IterateValidatorSigningInfos(ctx, func(consAddr sdk.ConsAddress, info slashingtypes.ValidatorSigningInfo) (stop bool) {
		signingInfos = append(signingInfos, info)
		return false
	})

	require.Equal(signingInfos[0].Address, signingInfo.Address)

	// test Tombstone
	err = keeper.Tombstone(ctx, consAddr)
	require.NoError(err)
	require.True(keeper.IsTombstoned(ctx, consAddr))

	// test JailUntil
	jailTime := time.Now().Add(time.Hour).UTC()
	keeper.JailUntil(ctx, consAddr, jailTime)
	sInfo, _ := keeper.GetValidatorSigningInfo(ctx, consAddr)
	require.Equal(sInfo.JailedUntil, jailTime)
}

func (s *KeeperTestSuite) TestValidatorMissedBlocksKeepIndexWithMissingChunks() {
	ctx, keeper := s.ctx, s.slashingKeeper
	require := s.Require()

	params := testutil.TestParams()
	params.SignedBlocksWindow = 10_000
	require.NoError(keeper.SetParams(ctx, params))

	// Only chunks 1 and 4 exist: the validator never missed a block in chunk 0.
	missedIdx := []int64{1500, 1501, 4200}
	for _, idx := range missedIdx {
		require.NoError(keeper.SetMissedBlockBitmapValue(ctx, consAddr, idx, true))
	}

	missedBlocks, err := keeper.GetValidatorMissedBlocks(ctx, consAddr)
	require.NoError(err)
	got := make([]int64, 0, len(missedBlocks))
	for _, mb := range missedBlocks {
		got = append(got, mb.Index)
	}
	require.Equal(missedIdx, got)

	// Stopping in the first chunk must not visit the next one.
	var visited []int64
	require.NoError(keeper.IterateMissedBlockBitmap(ctx, consAddr, func(index int64, missed bool) bool {
		if missed {
			visited = append(visited, index)
		}
		return missed
	}))
	require.Equal([]int64{1500}, visited)

	// Chunk keys are little-endian, so chunk 256 sorts before chunk 255.
	params.SignedBlocksWindow = 300 * slashingtypes.MissedBlockBitmapChunkSize
	require.NoError(keeper.SetParams(ctx, params))
	require.NoError(keeper.DeleteMissedBlockBitmap(ctx, consAddr))
	boundaryIdx := []int64{255*slashingtypes.MissedBlockBitmapChunkSize + 5, 256*slashingtypes.MissedBlockBitmapChunkSize + 7}
	for _, idx := range boundaryIdx {
		require.NoError(keeper.SetMissedBlockBitmapValue(ctx, consAddr, idx, true))
	}
	missedBlocks, err = keeper.GetValidatorMissedBlocks(ctx, consAddr)
	require.NoError(err)
	got = got[:0]
	for _, mb := range missedBlocks {
		got = append(got, mb.Index)
	}
	require.ElementsMatch(boundaryIdx, got)
}

func (s *KeeperTestSuite) TestValidatorMissedBlockBitmap_SmallWindow() {
	ctx, keeper := s.ctx, s.slashingKeeper
	require := s.Require()

	for _, window := range []int64{100, 32_000} {
		params := testutil.TestParams()
		params.SignedBlocksWindow = window
		require.NoError(keeper.SetParams(ctx, params))

		// validator misses all blocks in the window
		var valIdxOffset int64
		for valIdxOffset < params.SignedBlocksWindow {
			idx := valIdxOffset % params.SignedBlocksWindow
			err := keeper.SetMissedBlockBitmapValue(ctx, consAddr, idx, true)
			require.NoError(err)

			missed, err := keeper.GetMissedBlockBitmapValue(ctx, consAddr, idx)
			require.NoError(err)
			require.True(missed)

			valIdxOffset++
		}

		// validator should have missed all blocks
		missedBlocks, err := keeper.GetValidatorMissedBlocks(ctx, consAddr)
		require.NoError(err)
		require.Len(missedBlocks, int(params.SignedBlocksWindow))

		// sign next block, which rolls the missed block bitmap
		idx := valIdxOffset % params.SignedBlocksWindow
		err = keeper.SetMissedBlockBitmapValue(ctx, consAddr, idx, false)
		require.NoError(err)

		missed, err := keeper.GetMissedBlockBitmapValue(ctx, consAddr, idx)
		require.NoError(err)
		require.False(missed)

		// validator should have missed all blocks except the last one
		missedBlocks, err = keeper.GetValidatorMissedBlocks(ctx, consAddr)
		require.NoError(err)
		require.Len(missedBlocks, int(params.SignedBlocksWindow)-1)
	}
}

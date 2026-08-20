package rpc_test

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/stretchr/testify/suite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/cosmos/cosmos-sdk/testutil/network"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	"github.com/cosmos/cosmos-sdk/types/address"
	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

type IntegrationTestSuite struct {
	suite.Suite

	network *network.Network
}

func (s *IntegrationTestSuite) SetupSuite() {
	s.T().Log("setting up integration test suite")

	cfg, err := network.DefaultConfigWithAppConfig(network.MinimumAppConfig())

	s.NoError(err)

	s.network, err = network.New(s.T(), s.T().TempDir(), cfg)
	s.Require().NoError(err)

	s.Require().NoError(s.network.WaitForNextBlock())
}

func (s *IntegrationTestSuite) TearDownSuite() {
	s.T().Log("tearing down integration test suite")
	s.network.Cleanup()
}

func (s *IntegrationTestSuite) TestCLIQueryConn() {
	s.T().Skip("data race in comet is causing this to fail")
	var header metadata.MD

	testClient := testdata.NewQueryClient(s.network.Validators[0].ClientCtx)
	res, err := testClient.Echo(context.Background(), &testdata.EchoRequest{Message: "hello"}, grpc.Header(&header))
	s.NoError(err)

	blockHeight := header.Get(grpctypes.GRPCBlockHeightHeader)
	height, err := strconv.Atoi(blockHeight[0])
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(height, 1) // at least the 1st block

	s.Equal("hello", res.Message)
}

func (s *IntegrationTestSuite) TestQueryABCIHeight() {
	testCases := []struct {
		name      string
		reqHeight int64
		ctxHeight int64
		expHeight int64
	}{
		{
			name:      "non zero request height",
			reqHeight: 3,
			ctxHeight: 1, // query at height 1 or 2 would cause an error
			expHeight: 3,
		},
		{
			name:      "empty request height - use context height",
			reqHeight: 0,
			ctxHeight: 3,
			expHeight: 3,
		},
		{
			name:      "empty request height and context height - use latest height",
			reqHeight: 0,
			ctxHeight: 0,
			expHeight: 4,
		},
	}

	for _, tc := range testCases {
		s.Run(tc.name, func() {
			s.network.WaitForHeight(tc.expHeight)

			val := s.network.Validators[0]

			clientCtx := val.ClientCtx
			clientCtx = clientCtx.WithHeight(tc.ctxHeight)

			req := abci.RequestQuery{
				Path:   fmt.Sprintf("store/%s/key", banktypes.StoreKey),
				Height: tc.reqHeight,
				Data:   address.MustLengthPrefix(val.Address),
				Prove:  true,
			}

			// The app's committed state can briefly lag behind the block header
			// height reported by WaitForHeight, since CometBFT makes the new
			// block header visible slightly before the app finishes Commit().
			// Retry a few times to absorb that lag instead of racing it.
			var res abci.ResponseQuery
			var err error
			for attempt := 0; attempt < 5; attempt++ {
				res, err = clientCtx.QueryABCI(req)
				if err == nil && res.Height == tc.expHeight {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			s.Require().NoError(err)
			s.Require().Equal(tc.expHeight, res.Height)
		})
	}
}

func TestIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(IntegrationTestSuite))
}

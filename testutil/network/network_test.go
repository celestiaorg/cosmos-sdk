//go:build norace
// +build norace

package network_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"google.golang.org/grpc/metadata"

	"github.com/cosmos/cosmos-sdk/testutil/network"
	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

type IntegrationTestSuite struct {
	suite.Suite

	network *network.Network
}

func (s *IntegrationTestSuite) SetupSuite() {
	s.T().Log("setting up integration test suite")

	var err error
	s.network, err = network.New(s.T(), s.T().TempDir(), network.DefaultConfig())
	s.Require().NoError(err)

	h, err := s.network.WaitForHeight(1)
	s.Require().NoError(err, "stalled at height %d", h)
}

func (s *IntegrationTestSuite) TearDownSuite() {
	s.T().Log("tearing down integration test suite")
	s.network.Cleanup()
}

func (s *IntegrationTestSuite) TestNetwork_Liveness() {
	h, err := s.network.WaitForHeightWithTimeout(10, time.Minute)
	s.Require().NoError(err, "expected to reach 10 blocks; got %d", h)
}

// TestNetwork_WaitForHeightIsCommitted asserts that once WaitForHeight returns,
// the application has already committed that height, so a query pinned to it
// succeeds instead of failing because the state is not yet available.
func (s *IntegrationTestSuite) TestNetwork_WaitForHeightIsCommitted() {
	val := s.network.Validators[0]
	queryClient := banktypes.NewQueryClient(val.ClientCtx)

	for i := 0; i < 3; i++ {
		latest, err := s.network.LatestHeight()
		s.Require().NoError(err)

		h, err := s.network.WaitForHeight(latest + 1)
		s.Require().NoError(err)

		ctx := metadata.AppendToOutgoingContext(context.Background(), grpctypes.GRPCBlockHeightHeader, strconv.FormatInt(h, 10))
		_, err = queryClient.Balance(ctx, &banktypes.QueryBalanceRequest{
			Address: val.Address.String(),
			Denom:   s.network.Config.BondDenom,
		})
		s.Require().NoError(err, "query at height %d, which WaitForHeight reported as reached, failed", h)
	}
}

func TestIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(IntegrationTestSuite))
}

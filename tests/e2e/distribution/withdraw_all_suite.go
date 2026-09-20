package distribution

import (
	"fmt"
	"strings"
	"time"

	"github.com/stretchr/testify/suite"

	"cosmossdk.io/math"
	"cosmossdk.io/simapp"

	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/testutil"
	clitestutil "github.com/cosmos/cosmos-sdk/testutil/cli"
	"github.com/cosmos/cosmos-sdk/testutil/network"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	"github.com/cosmos/cosmos-sdk/x/distribution/client/cli"
	stakingcli "github.com/cosmos/cosmos-sdk/x/staking/client/cli"
)

type WithdrawAllTestSuite struct {
	suite.Suite

	cfg     network.Config
	network *network.Network
}

func (s *WithdrawAllTestSuite) SetupSuite() {
	cfg := network.DefaultConfig(simapp.NewTestNetworkFixture)
	cfg.NumValidators = 2
	s.cfg = cfg

	s.T().Log("setting up e2e test suite")
	network, err := network.New(s.T(), s.T().TempDir(), s.cfg)
	s.Require().NoError(err)
	s.network = network

	s.Require().NoError(s.network.WaitForNextBlock())
}

// TearDownSuite cleans up the curret test network after _each_ test.
func (s *WithdrawAllTestSuite) TearDownSuite() {
	s.T().Log("tearing down e2e test suite")
	s.network.Cleanup()
}

// This test requires multiple validators, if I add this test to `E2ETestSuite` by increasing
// `NumValidators` the existing tests are leading to non-determnism so created new suite for this test.
func (s *WithdrawAllTestSuite) TestNewWithdrawAllRewardsGenerateOnly() {
	require := s.Require()
	val := s.network.Validators[0]
	val1 := s.network.Validators[1]
	clientCtx := val.ClientCtx

	info, _, err := val.ClientCtx.Keyring.NewMnemonic("newAccount", keyring.English, sdk.FullFundraiserPath, keyring.DefaultBIP39Passphrase, hd.Secp256k1)
	require.NoError(err)

	pubkey, err := info.GetPubKey()
	require.NoError(err)

	newAddr := sdk.AccAddress(pubkey.Address())
	out, err := clitestutil.MsgSendExec(
		val.ClientCtx,
		val.Address,
		newAddr,
		sdk.NewCoins(sdk.NewCoin(s.cfg.BondDenom, math.NewInt(2000))), address.NewBech32Codec("cosmos"), fmt.Sprintf("--%s=true", flags.FlagSkipConfirmation),
		fmt.Sprintf("--%s=%s", flags.FlagBroadcastMode, flags.BroadcastSync),
		fmt.Sprintf("--%s=%s", flags.FlagFees, sdk.NewCoins(sdk.NewCoin(s.cfg.BondDenom, math.NewInt(10))).String()),
	)
	require.NoError(err)
	// Every tx below is broadcast in sync mode, which only guarantees mempool
	// acceptance. Confirm each one actually landed before signing the next,
	// instead of assuming a single block is enough.
	require.NoError(s.confirmTxCommitted(out))

	// delegate 500 tokens to validator1
	args := []string{
		val.ValAddress.String(),
		sdk.NewCoin(s.cfg.BondDenom, math.NewInt(500)).String(),
		fmt.Sprintf("--%s=%s", flags.FlagFrom, newAddr.String()),
		fmt.Sprintf("--%s=true", flags.FlagSkipConfirmation),
		fmt.Sprintf("--%s=%s", flags.FlagBroadcastMode, flags.BroadcastSync),
		fmt.Sprintf("--%s=%s", flags.FlagFees, sdk.NewCoins(sdk.NewCoin(s.cfg.BondDenom, math.NewInt(10))).String()),
	}
	cmd := stakingcli.NewDelegateCmd(clientCtx.InterfaceRegistry.SigningContext().ValidatorAddressCodec(), clientCtx.InterfaceRegistry.SigningContext().AddressCodec())
	out, err = clitestutil.ExecTestCLICmd(clientCtx, cmd, args)
	require.NoError(err)
	require.NoError(s.confirmTxCommitted(out))

	// delegate 500 tokens to validator2
	args = []string{
		val1.ValAddress.String(),
		sdk.NewCoin(s.cfg.BondDenom, math.NewInt(500)).String(),
		fmt.Sprintf("--%s=%s", flags.FlagFrom, newAddr.String()),
		fmt.Sprintf("--%s=true", flags.FlagSkipConfirmation),
		fmt.Sprintf("--%s=%s", flags.FlagBroadcastMode, flags.BroadcastSync),
		fmt.Sprintf("--%s=%s", flags.FlagFees, sdk.NewCoins(sdk.NewCoin(s.cfg.BondDenom, math.NewInt(10))).String()),
	}
	out, err = clitestutil.ExecTestCLICmd(clientCtx, cmd, args)
	require.NoError(err)
	require.NoError(s.confirmTxCommitted(out))

	// withdraw-all-rewards emits one message per delegation the delegator
	// has, so both delegations must be visible to the query. Retry until
	// they are rather than racing a fixed block count - a genuine bug still
	// surfaces via the timeout.
	err = s.network.RetryWithTimeout(func() error {
		args = []string{
			fmt.Sprintf("--%s=%s", flags.FlagFrom, newAddr.String()),
			fmt.Sprintf("--%s=true", flags.FlagSkipConfirmation),
			fmt.Sprintf("--%s=true", flags.FlagGenerateOnly),
			fmt.Sprintf("--%s=1", cli.FlagMaxMessagesPerTx),
			fmt.Sprintf("--%s=%s", flags.FlagBroadcastMode, flags.BroadcastSync),
			fmt.Sprintf("--%s=%s", flags.FlagFees, sdk.NewCoins(sdk.NewCoin(s.cfg.BondDenom, math.NewInt(10))).String()),
		}
		cmd = cli.NewWithdrawAllRewardsCmd(address.NewBech32Codec("cosmosvaloper"), address.NewBech32Codec("cosmos"))
		out, err := clitestutil.ExecTestCLICmd(clientCtx, cmd, args)
		if err != nil {
			return err
		}

		// expect 2 transactions in the generated file when --max-msgs in a tx set 1.
		txLen := len(strings.Split(strings.Trim(out.String(), "\n"), "\n"))
		if txLen != 2 {
			return fmt.Errorf("expected 2 transactions in the generated file, got %d", txLen)
		}
		return nil
	}, 30*time.Second, 500*time.Millisecond)
	require.NoError(err)

	args = []string{
		fmt.Sprintf("--%s=%s", flags.FlagFrom, newAddr.String()),
		fmt.Sprintf("--%s=true", flags.FlagSkipConfirmation),
		fmt.Sprintf("--%s=true", flags.FlagGenerateOnly),
		fmt.Sprintf("--%s=2", cli.FlagMaxMessagesPerTx),
		fmt.Sprintf("--%s=%s", flags.FlagBroadcastMode, flags.BroadcastSync),
		fmt.Sprintf("--%s=%s", flags.FlagFees, sdk.NewCoins(sdk.NewCoin(s.cfg.BondDenom, math.NewInt(10))).String()),
	}
	cmd = cli.NewWithdrawAllRewardsCmd(address.NewBech32Codec("cosmosvaloper"), address.NewBech32Codec("cosmos"))
	out, err = clitestutil.ExecTestCLICmd(clientCtx, cmd, args)
	require.NoError(err)
	// expect 1 transaction in the generated file when --max-msgs in a tx set 2, since there are only delegations.
	s.Require().Equal(1, len(strings.Split(strings.Trim(out.String(), "\n"), "\n")))
}

// confirmTxCommitted parses a sync-broadcast tx response and polls until the
// tx is committed with code 0, or the timeout elapses.
func (s *WithdrawAllTestSuite) confirmTxCommitted(out testutil.BufferWriter) error {
	clientCtx := s.network.Validators[0].ClientCtx

	var txRes sdk.TxResponse
	if err := clientCtx.Codec.UnmarshalJSON(out.Bytes(), &txRes); err != nil {
		return fmt.Errorf("failed to unmarshal tx response %q: %w", out.String(), err)
	}
	if txRes.Code != 0 {
		return fmt.Errorf("tx rejected on broadcast with code %d: %s", txRes.Code, txRes.RawLog)
	}

	return s.network.RetryWithTimeout(func() error {
		res, err := authtx.QueryTx(clientCtx, txRes.TxHash)
		if err != nil {
			return err
		}
		if res.Code != 0 {
			return fmt.Errorf("tx %s committed with code %d: %s", txRes.TxHash, res.Code, res.RawLog)
		}
		return nil
	}, 30*time.Second, 500*time.Millisecond)
}

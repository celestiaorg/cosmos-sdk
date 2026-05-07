package grpc

import (
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/server/config"
	"github.com/cosmos/cosmos-sdk/server/grpc/gogoreflection"
	reflection "github.com/cosmos/cosmos-sdk/server/grpc/reflection/v2alpha1"
	"github.com/cosmos/cosmos-sdk/server/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// GRPCServerOption configures the gRPC server constructed by StartGRPCServer.
type GRPCServerOption func(*grpcServerConfig)

type grpcServerConfig struct {
	grpcOpts []grpc.ServerOption
}

// WithGRPCServerOptions appends extra grpc.ServerOptions (e.g. interceptors,
// keepalive policies, custom credentials) to the ones the SDK applies by
// default. The user-provided options are appended after the defaults, so they
// can override defaults where grpc-go's last-write-wins semantics allow.
func WithGRPCServerOptions(opts ...grpc.ServerOption) GRPCServerOption {
	return func(c *grpcServerConfig) {
		c.grpcOpts = append(c.grpcOpts, opts...)
	}
}

// StartGRPCServer starts a gRPC server on the given address.
func StartGRPCServer(clientCtx client.Context, app types.Application, cfg config.GRPCConfig, opts ...GRPCServerOption) (*grpc.Server, error) {
	maxSendMsgSize := cfg.MaxSendMsgSize
	if maxSendMsgSize == 0 {
		maxSendMsgSize = config.DefaultGRPCMaxSendMsgSize
	}

	maxRecvMsgSize := cfg.MaxRecvMsgSize
	if maxRecvMsgSize == 0 {
		maxRecvMsgSize = config.DefaultGRPCMaxRecvMsgSize
	}

	serverConfig := &grpcServerConfig{}
	for _, opt := range opts {
		opt(serverConfig)
	}

	serverOpts := []grpc.ServerOption{
		grpc.ForceServerCodec(codec.NewProtoCodec(clientCtx.InterfaceRegistry).GRPCCodec()),
		grpc.MaxSendMsgSize(maxSendMsgSize),
		grpc.MaxRecvMsgSize(maxRecvMsgSize),
	}
	serverOpts = append(serverOpts, serverConfig.grpcOpts...)

	grpcSrv := grpc.NewServer(serverOpts...)

	app.RegisterGRPCServer(grpcSrv)

	// Reflection allows consumers to build dynamic clients that can write to any
	// Cosmos SDK application without relying on application packages at compile
	// time.
	err := reflection.Register(grpcSrv, reflection.Config{
		SigningModes: func() map[string]int32 {
			modes := make(map[string]int32, len(clientCtx.TxConfig.SignModeHandler().Modes()))
			for _, m := range clientCtx.TxConfig.SignModeHandler().Modes() {
				modes[m.String()] = (int32)(m)
			}
			return modes
		}(),
		ChainID:           clientCtx.ChainID,
		SdkConfig:         sdk.GetConfig(),
		InterfaceRegistry: clientCtx.InterfaceRegistry,
	})
	if err != nil {
		return nil, err
	}

	// Reflection allows external clients to see what services and methods
	// the gRPC server exposes.
	gogoreflection.Register(grpcSrv)

	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return nil, err
	}

	errCh := make(chan error)
	go func() {
		err = grpcSrv.Serve(listener)
		if err != nil {
			errCh <- fmt.Errorf("failed to serve: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return nil, err

	case <-time.After(types.ServerStartTime):
		// assume server started successfully
		return grpcSrv, nil
	}
}

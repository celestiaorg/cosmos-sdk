// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.2.0
// - protoc             (unknown)
// source: cosmos/tx/v1beta1/service.proto

package txv1beta1

import (
	context "context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.32.0 or later.
const _ = grpc.SupportPackageIsVersion7

// ServiceClient is the client API for Service service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type ServiceClient interface {
	// Simulate simulates executing a transaction for estimating gas usage.
	Simulate(ctx context.Context, in *SimulateRequest, opts ...grpc.CallOption) (*SimulateResponse, error)
	// GetTx fetches a tx by hash.
	GetTx(ctx context.Context, in *GetTxRequest, opts ...grpc.CallOption) (*GetTxResponse, error)
	// BroadcastTx broadcast transaction.
	BroadcastTx(ctx context.Context, in *BroadcastTxRequest, opts ...grpc.CallOption) (*BroadcastTxResponse, error)
	// GetTxsEvent fetches txs by event.
	GetTxsEvent(ctx context.Context, in *GetTxsEventRequest, opts ...grpc.CallOption) (*GetTxsEventResponse, error)
	// GetBlockWithTxs fetches a block with decoded txs.
	//
	// Since: cosmos-sdk 0.45.2
	GetBlockWithTxs(ctx context.Context, in *GetBlockWithTxsRequest, opts ...grpc.CallOption) (*GetBlockWithTxsResponse, error)
}

type serviceClient struct {
	cc grpc.ClientConnInterface
}

func NewServiceClient(cc grpc.ClientConnInterface) ServiceClient {
	return &serviceClient{cc}
}

func (c *serviceClient) Simulate(ctx context.Context, in *SimulateRequest, opts ...grpc.CallOption) (*SimulateResponse, error) {
	out := new(SimulateResponse)
	err := c.cc.Invoke(ctx, "/cosmos.tx.v1beta1.Service/Simulate", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *serviceClient) GetTx(ctx context.Context, in *GetTxRequest, opts ...grpc.CallOption) (*GetTxResponse, error) {
	out := new(GetTxResponse)
	err := c.cc.Invoke(ctx, "/cosmos.tx.v1beta1.Service/GetTx", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *serviceClient) BroadcastTx(ctx context.Context, in *BroadcastTxRequest, opts ...grpc.CallOption) (*BroadcastTxResponse, error) {
	out := new(BroadcastTxResponse)
	err := c.cc.Invoke(ctx, "/cosmos.tx.v1beta1.Service/BroadcastTx", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *serviceClient) GetTxsEvent(ctx context.Context, in *GetTxsEventRequest, opts ...grpc.CallOption) (*GetTxsEventResponse, error) {
	out := new(GetTxsEventResponse)
	err := c.cc.Invoke(ctx, "/cosmos.tx.v1beta1.Service/GetTxsEvent", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *serviceClient) GetBlockWithTxs(ctx context.Context, in *GetBlockWithTxsRequest, opts ...grpc.CallOption) (*GetBlockWithTxsResponse, error) {
	out := new(GetBlockWithTxsResponse)
	err := c.cc.Invoke(ctx, "/cosmos.tx.v1beta1.Service/GetBlockWithTxs", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ServiceServer is the server API for Service service.
// All implementations must embed UnimplementedServiceServer
// for forward compatibility
type ServiceServer interface {
	// Simulate simulates executing a transaction for estimating gas usage.
	Simulate(context.Context, *SimulateRequest) (*SimulateResponse, error)
	// GetTx fetches a tx by hash.
	GetTx(context.Context, *GetTxRequest) (*GetTxResponse, error)
	// BroadcastTx broadcast transaction.
	BroadcastTx(context.Context, *BroadcastTxRequest) (*BroadcastTxResponse, error)
	// GetTxsEvent fetches txs by event.
	GetTxsEvent(context.Context, *GetTxsEventRequest) (*GetTxsEventResponse, error)
	// GetBlockWithTxs fetches a block with decoded txs.
	//
	// Since: cosmos-sdk 0.45.2
	GetBlockWithTxs(context.Context, *GetBlockWithTxsRequest) (*GetBlockWithTxsResponse, error)
	mustEmbedUnimplementedServiceServer()
}

// UnimplementedServiceServer must be embedded to have forward compatible implementations.
type UnimplementedServiceServer struct {
}

func (UnimplementedServiceServer) Simulate(context.Context, *SimulateRequest) (*SimulateResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Simulate not implemented")
}
func (UnimplementedServiceServer) GetTx(context.Context, *GetTxRequest) (*GetTxResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetTx not implemented")
}
func (UnimplementedServiceServer) BroadcastTx(context.Context, *BroadcastTxRequest) (*BroadcastTxResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method BroadcastTx not implemented")
}
func (UnimplementedServiceServer) GetTxsEvent(context.Context, *GetTxsEventRequest) (*GetTxsEventResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetTxsEvent not implemented")
}
func (UnimplementedServiceServer) GetBlockWithTxs(context.Context, *GetBlockWithTxsRequest) (*GetBlockWithTxsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetBlockWithTxs not implemented")
}
func (UnimplementedServiceServer) mustEmbedUnimplementedServiceServer() {}

// UnsafeServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to ServiceServer will
// result in compilation errors.
type UnsafeServiceServer interface {
	mustEmbedUnimplementedServiceServer()
}

func RegisterServiceServer(s grpc.ServiceRegistrar, srv ServiceServer) {
	s.RegisterService(&Service_ServiceDesc, srv)
}

func _Service_Simulate_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(SimulateRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ServiceServer).Simulate(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/cosmos.tx.v1beta1.Service/Simulate",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ServiceServer).Simulate(ctx, req.(*SimulateRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _Service_GetTx_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetTxRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ServiceServer).GetTx(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/cosmos.tx.v1beta1.Service/GetTx",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ServiceServer).GetTx(ctx, req.(*GetTxRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _Service_BroadcastTx_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(BroadcastTxRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ServiceServer).BroadcastTx(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/cosmos.tx.v1beta1.Service/BroadcastTx",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ServiceServer).BroadcastTx(ctx, req.(*BroadcastTxRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _Service_GetTxsEvent_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetTxsEventRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ServiceServer).GetTxsEvent(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/cosmos.tx.v1beta1.Service/GetTxsEvent",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ServiceServer).GetTxsEvent(ctx, req.(*GetTxsEventRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _Service_GetBlockWithTxs_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetBlockWithTxsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ServiceServer).GetBlockWithTxs(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/cosmos.tx.v1beta1.Service/GetBlockWithTxs",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ServiceServer).GetBlockWithTxs(ctx, req.(*GetBlockWithTxsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// Service_ServiceDesc is the grpc.ServiceDesc for Service service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var Service_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "cosmos.tx.v1beta1.Service",
	HandlerType: (*ServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Simulate",
			Handler:    _Service_Simulate_Handler,
		},
		{
			MethodName: "GetTx",
			Handler:    _Service_GetTx_Handler,
		},
		{
			MethodName: "BroadcastTx",
			Handler:    _Service_BroadcastTx_Handler,
		},
		{
			MethodName: "GetTxsEvent",
			Handler:    _Service_GetTxsEvent_Handler,
		},
		{
			MethodName: "GetBlockWithTxs",
			Handler:    _Service_GetBlockWithTxs_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "cosmos/tx/v1beta1/service.proto",
}
// Code generated by protoc-gen-gogo. DO NOT EDIT.
// source: pmem-registry.proto

package registry

import proto "github.com/gogo/protobuf/proto"
import fmt "fmt"
import math "math"

import (
	context "golang.org/x/net/context"
	grpc "google.golang.org/grpc"
)

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

// This is a compile-time assertion to ensure that this generated file
// is compatible with the proto package it is being compiled against.
// A compilation error at this line likely means your copy of the
// proto package needs to be updated.
const _ = proto.GoGoProtoPackageIsVersion2 // please upgrade the proto package

type RegisterControllerRequest struct {
	// unique node id, usually id of the compute node in the cluster
	// which has the nvdimm installed
	NodeId string `protobuf:"bytes,1,opt,name=node_id,json=nodeId,proto3" json:"node_id,omitempty"`
	// Node controller's address that can be used for grpc.Dial to
	// connect to the controller
	Endpoint string `protobuf:"bytes,2,opt,name=endpoint,proto3" json:"endpoint,omitempty"`
	// Available capacity of the node.
	Capacity             uint64   `protobuf:"varint,3,opt,name=capacity,proto3" json:"capacity,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *RegisterControllerRequest) Reset()         { *m = RegisterControllerRequest{} }
func (m *RegisterControllerRequest) String() string { return proto.CompactTextString(m) }
func (*RegisterControllerRequest) ProtoMessage()    {}
func (*RegisterControllerRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_pmem_registry_8ff709e7052f3a8b, []int{0}
}
func (m *RegisterControllerRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_RegisterControllerRequest.Unmarshal(m, b)
}
func (m *RegisterControllerRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_RegisterControllerRequest.Marshal(b, m, deterministic)
}
func (dst *RegisterControllerRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_RegisterControllerRequest.Merge(dst, src)
}
func (m *RegisterControllerRequest) XXX_Size() int {
	return xxx_messageInfo_RegisterControllerRequest.Size(m)
}
func (m *RegisterControllerRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_RegisterControllerRequest.DiscardUnknown(m)
}

var xxx_messageInfo_RegisterControllerRequest proto.InternalMessageInfo

func (m *RegisterControllerRequest) GetNodeId() string {
	if m != nil {
		return m.NodeId
	}
	return ""
}

func (m *RegisterControllerRequest) GetEndpoint() string {
	if m != nil {
		return m.Endpoint
	}
	return ""
}

func (m *RegisterControllerRequest) GetCapacity() uint64 {
	if m != nil {
		return m.Capacity
	}
	return 0
}

type RegisterControllerReply struct {
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *RegisterControllerReply) Reset()         { *m = RegisterControllerReply{} }
func (m *RegisterControllerReply) String() string { return proto.CompactTextString(m) }
func (*RegisterControllerReply) ProtoMessage()    {}
func (*RegisterControllerReply) Descriptor() ([]byte, []int) {
	return fileDescriptor_pmem_registry_8ff709e7052f3a8b, []int{1}
}
func (m *RegisterControllerReply) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_RegisterControllerReply.Unmarshal(m, b)
}
func (m *RegisterControllerReply) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_RegisterControllerReply.Marshal(b, m, deterministic)
}
func (dst *RegisterControllerReply) XXX_Merge(src proto.Message) {
	xxx_messageInfo_RegisterControllerReply.Merge(dst, src)
}
func (m *RegisterControllerReply) XXX_Size() int {
	return xxx_messageInfo_RegisterControllerReply.Size(m)
}
func (m *RegisterControllerReply) XXX_DiscardUnknown() {
	xxx_messageInfo_RegisterControllerReply.DiscardUnknown(m)
}

var xxx_messageInfo_RegisterControllerReply proto.InternalMessageInfo

type UnregisterControllerRequest struct {
	// Id of the node controller to unregister from ControllerRegistry
	NodeId               string   `protobuf:"bytes,1,opt,name=node_id,json=nodeId,proto3" json:"node_id,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *UnregisterControllerRequest) Reset()         { *m = UnregisterControllerRequest{} }
func (m *UnregisterControllerRequest) String() string { return proto.CompactTextString(m) }
func (*UnregisterControllerRequest) ProtoMessage()    {}
func (*UnregisterControllerRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_pmem_registry_8ff709e7052f3a8b, []int{2}
}
func (m *UnregisterControllerRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_UnregisterControllerRequest.Unmarshal(m, b)
}
func (m *UnregisterControllerRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_UnregisterControllerRequest.Marshal(b, m, deterministic)
}
func (dst *UnregisterControllerRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_UnregisterControllerRequest.Merge(dst, src)
}
func (m *UnregisterControllerRequest) XXX_Size() int {
	return xxx_messageInfo_UnregisterControllerRequest.Size(m)
}
func (m *UnregisterControllerRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_UnregisterControllerRequest.DiscardUnknown(m)
}

var xxx_messageInfo_UnregisterControllerRequest proto.InternalMessageInfo

func (m *UnregisterControllerRequest) GetNodeId() string {
	if m != nil {
		return m.NodeId
	}
	return ""
}

type UnregisterControllerReply struct {
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *UnregisterControllerReply) Reset()         { *m = UnregisterControllerReply{} }
func (m *UnregisterControllerReply) String() string { return proto.CompactTextString(m) }
func (*UnregisterControllerReply) ProtoMessage()    {}
func (*UnregisterControllerReply) Descriptor() ([]byte, []int) {
	return fileDescriptor_pmem_registry_8ff709e7052f3a8b, []int{3}
}
func (m *UnregisterControllerReply) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_UnregisterControllerReply.Unmarshal(m, b)
}
func (m *UnregisterControllerReply) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_UnregisterControllerReply.Marshal(b, m, deterministic)
}
func (dst *UnregisterControllerReply) XXX_Merge(src proto.Message) {
	xxx_messageInfo_UnregisterControllerReply.Merge(dst, src)
}
func (m *UnregisterControllerReply) XXX_Size() int {
	return xxx_messageInfo_UnregisterControllerReply.Size(m)
}
func (m *UnregisterControllerReply) XXX_DiscardUnknown() {
	xxx_messageInfo_UnregisterControllerReply.DiscardUnknown(m)
}

var xxx_messageInfo_UnregisterControllerReply proto.InternalMessageInfo

type GetCapacityRequest struct {
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *GetCapacityRequest) Reset()         { *m = GetCapacityRequest{} }
func (m *GetCapacityRequest) String() string { return proto.CompactTextString(m) }
func (*GetCapacityRequest) ProtoMessage()    {}
func (*GetCapacityRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_pmem_registry_8ff709e7052f3a8b, []int{4}
}
func (m *GetCapacityRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_GetCapacityRequest.Unmarshal(m, b)
}
func (m *GetCapacityRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_GetCapacityRequest.Marshal(b, m, deterministic)
}
func (dst *GetCapacityRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_GetCapacityRequest.Merge(dst, src)
}
func (m *GetCapacityRequest) XXX_Size() int {
	return xxx_messageInfo_GetCapacityRequest.Size(m)
}
func (m *GetCapacityRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_GetCapacityRequest.DiscardUnknown(m)
}

var xxx_messageInfo_GetCapacityRequest proto.InternalMessageInfo

type GetCapacityReply struct {
	Capacity             uint64   `protobuf:"varint,1,opt,name=capacity,proto3" json:"capacity,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *GetCapacityReply) Reset()         { *m = GetCapacityReply{} }
func (m *GetCapacityReply) String() string { return proto.CompactTextString(m) }
func (*GetCapacityReply) ProtoMessage()    {}
func (*GetCapacityReply) Descriptor() ([]byte, []int) {
	return fileDescriptor_pmem_registry_8ff709e7052f3a8b, []int{5}
}
func (m *GetCapacityReply) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_GetCapacityReply.Unmarshal(m, b)
}
func (m *GetCapacityReply) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_GetCapacityReply.Marshal(b, m, deterministic)
}
func (dst *GetCapacityReply) XXX_Merge(src proto.Message) {
	xxx_messageInfo_GetCapacityReply.Merge(dst, src)
}
func (m *GetCapacityReply) XXX_Size() int {
	return xxx_messageInfo_GetCapacityReply.Size(m)
}
func (m *GetCapacityReply) XXX_DiscardUnknown() {
	xxx_messageInfo_GetCapacityReply.DiscardUnknown(m)
}

var xxx_messageInfo_GetCapacityReply proto.InternalMessageInfo

func (m *GetCapacityReply) GetCapacity() uint64 {
	if m != nil {
		return m.Capacity
	}
	return 0
}

func init() {
	proto.RegisterType((*RegisterControllerRequest)(nil), "registry.v0.RegisterControllerRequest")
	proto.RegisterType((*RegisterControllerReply)(nil), "registry.v0.RegisterControllerReply")
	proto.RegisterType((*UnregisterControllerRequest)(nil), "registry.v0.UnregisterControllerRequest")
	proto.RegisterType((*UnregisterControllerReply)(nil), "registry.v0.UnregisterControllerReply")
	proto.RegisterType((*GetCapacityRequest)(nil), "registry.v0.GetCapacityRequest")
	proto.RegisterType((*GetCapacityReply)(nil), "registry.v0.GetCapacityReply")
}

// Reference imports to suppress errors if they are not otherwise used.
var _ context.Context
var _ grpc.ClientConn

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
const _ = grpc.SupportPackageIsVersion4

// RegistryClient is the client API for Registry service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://godoc.org/google.golang.org/grpc#ClientConn.NewStream.
type RegistryClient interface {
	RegisterController(ctx context.Context, in *RegisterControllerRequest, opts ...grpc.CallOption) (*RegisterControllerReply, error)
	UnregisterController(ctx context.Context, in *UnregisterControllerRequest, opts ...grpc.CallOption) (*UnregisterControllerReply, error)
}

type registryClient struct {
	cc *grpc.ClientConn
}

func NewRegistryClient(cc *grpc.ClientConn) RegistryClient {
	return &registryClient{cc}
}

func (c *registryClient) RegisterController(ctx context.Context, in *RegisterControllerRequest, opts ...grpc.CallOption) (*RegisterControllerReply, error) {
	out := new(RegisterControllerReply)
	err := c.cc.Invoke(ctx, "/registry.v0.Registry/RegisterController", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *registryClient) UnregisterController(ctx context.Context, in *UnregisterControllerRequest, opts ...grpc.CallOption) (*UnregisterControllerReply, error) {
	out := new(UnregisterControllerReply)
	err := c.cc.Invoke(ctx, "/registry.v0.Registry/UnregisterController", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RegistryServer is the server API for Registry service.
type RegistryServer interface {
	RegisterController(context.Context, *RegisterControllerRequest) (*RegisterControllerReply, error)
	UnregisterController(context.Context, *UnregisterControllerRequest) (*UnregisterControllerReply, error)
}

func RegisterRegistryServer(s *grpc.Server, srv RegistryServer) {
	s.RegisterService(&_Registry_serviceDesc, srv)
}

func _Registry_RegisterController_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(RegisterControllerRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(RegistryServer).RegisterController(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/registry.v0.Registry/RegisterController",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(RegistryServer).RegisterController(ctx, req.(*RegisterControllerRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _Registry_UnregisterController_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(UnregisterControllerRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(RegistryServer).UnregisterController(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/registry.v0.Registry/UnregisterController",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(RegistryServer).UnregisterController(ctx, req.(*UnregisterControllerRequest))
	}
	return interceptor(ctx, in, info, handler)
}

var _Registry_serviceDesc = grpc.ServiceDesc{
	ServiceName: "registry.v0.Registry",
	HandlerType: (*RegistryServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "RegisterController",
			Handler:    _Registry_RegisterController_Handler,
		},
		{
			MethodName: "UnregisterController",
			Handler:    _Registry_UnregisterController_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "pmem-registry.proto",
}

// NodeControllerClient is the client API for NodeController service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://godoc.org/google.golang.org/grpc#ClientConn.NewStream.
type NodeControllerClient interface {
	GetCapacity(ctx context.Context, in *GetCapacityRequest, opts ...grpc.CallOption) (*GetCapacityReply, error)
}

type nodeControllerClient struct {
	cc *grpc.ClientConn
}

func NewNodeControllerClient(cc *grpc.ClientConn) NodeControllerClient {
	return &nodeControllerClient{cc}
}

func (c *nodeControllerClient) GetCapacity(ctx context.Context, in *GetCapacityRequest, opts ...grpc.CallOption) (*GetCapacityReply, error) {
	out := new(GetCapacityReply)
	err := c.cc.Invoke(ctx, "/registry.v0.NodeController/GetCapacity", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// NodeControllerServer is the server API for NodeController service.
type NodeControllerServer interface {
	GetCapacity(context.Context, *GetCapacityRequest) (*GetCapacityReply, error)
}

func RegisterNodeControllerServer(s *grpc.Server, srv NodeControllerServer) {
	s.RegisterService(&_NodeController_serviceDesc, srv)
}

func _NodeController_GetCapacity_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetCapacityRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(NodeControllerServer).GetCapacity(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/registry.v0.NodeController/GetCapacity",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(NodeControllerServer).GetCapacity(ctx, req.(*GetCapacityRequest))
	}
	return interceptor(ctx, in, info, handler)
}

var _NodeController_serviceDesc = grpc.ServiceDesc{
	ServiceName: "registry.v0.NodeController",
	HandlerType: (*NodeControllerServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "GetCapacity",
			Handler:    _NodeController_GetCapacity_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "pmem-registry.proto",
}

func init() { proto.RegisterFile("pmem-registry.proto", fileDescriptor_pmem_registry_8ff709e7052f3a8b) }

var fileDescriptor_pmem_registry_8ff709e7052f3a8b = []byte{
	// 281 bytes of a gzipped FileDescriptorProto
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff, 0xe2, 0x12, 0x2e, 0xc8, 0x4d, 0xcd,
	0xd5, 0x2d, 0x4a, 0x4d, 0xcf, 0x2c, 0x2e, 0x29, 0xaa, 0xd4, 0x2b, 0x28, 0xca, 0x2f, 0xc9, 0x17,
	0xe2, 0x86, 0xf3, 0xcb, 0x0c, 0x94, 0x72, 0xb8, 0x24, 0x83, 0xc0, 0xdc, 0xd4, 0x22, 0xe7, 0xfc,
	0xbc, 0x92, 0xa2, 0xfc, 0x9c, 0x9c, 0xd4, 0xa2, 0xa0, 0xd4, 0xc2, 0xd2, 0xd4, 0xe2, 0x12, 0x21,
	0x71, 0x2e, 0xf6, 0xbc, 0xfc, 0x94, 0xd4, 0xf8, 0xcc, 0x14, 0x09, 0x46, 0x05, 0x46, 0x0d, 0xce,
	0x20, 0x36, 0x10, 0xd7, 0x33, 0x45, 0x48, 0x8a, 0x8b, 0x23, 0x35, 0x2f, 0xa5, 0x20, 0x3f, 0x33,
	0xaf, 0x44, 0x82, 0x09, 0x2c, 0x03, 0xe7, 0x83, 0xe4, 0x92, 0x13, 0x0b, 0x12, 0x93, 0x33, 0x4b,
	0x2a, 0x25, 0x98, 0x15, 0x18, 0x35, 0x58, 0x82, 0xe0, 0x7c, 0x25, 0x49, 0x2e, 0x71, 0x6c, 0xb6,
	0x15, 0xe4, 0x54, 0x2a, 0x99, 0x71, 0x49, 0x87, 0xe6, 0x15, 0x91, 0xec, 0x14, 0x25, 0x69, 0x2e,
	0x49, 0xec, 0xfa, 0x40, 0x86, 0x8a, 0x70, 0x09, 0xb9, 0xa7, 0x96, 0x38, 0x43, 0xad, 0x87, 0x9a,
	0xa5, 0xa4, 0xc7, 0x25, 0x80, 0x22, 0x5a, 0x90, 0x53, 0x89, 0xe2, 0x6a, 0x46, 0x54, 0x57, 0x1b,
	0xdd, 0x61, 0xe4, 0xe2, 0x08, 0x82, 0x86, 0x99, 0x50, 0x0a, 0x97, 0x10, 0xa6, 0x17, 0x84, 0xd4,
	0xf4, 0x90, 0x02, 0x55, 0x0f, 0x67, 0x88, 0x4a, 0xa9, 0x10, 0x54, 0x07, 0x72, 0x36, 0x83, 0x50,
	0x16, 0x97, 0x08, 0x36, 0x5f, 0x09, 0x69, 0xa0, 0xe8, 0xc7, 0x13, 0x60, 0x52, 0x6a, 0x44, 0xa8,
	0x04, 0xdb, 0x65, 0x94, 0xc8, 0xc5, 0xe7, 0x97, 0x9f, 0x92, 0x8a, 0x64, 0x8b, 0x3f, 0x17, 0x37,
	0x52, 0x00, 0x09, 0xc9, 0xa3, 0x18, 0x85, 0x19, 0xa0, 0x52, 0xb2, 0xb8, 0x15, 0x80, 0xad, 0x70,
	0xe2, 0x8a, 0xe2, 0x80, 0xa9, 0x48, 0x62, 0x03, 0xa7, 0x42, 0x63, 0x40, 0x00, 0x00, 0x00, 0xff,
	0xff, 0xa8, 0xde, 0x3b, 0xff, 0x9c, 0x02, 0x00, 0x00,
}

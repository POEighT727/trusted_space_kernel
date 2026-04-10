package operator_peer

import (
	"encoding/binary"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/proto"
	pb "github.com/trusted-space/kernel/proto/kernel/v1"
)

// P2P协议版本
const ProtocolVersion = "1.0.0"

// 包头魔数，用于验证数据包
const MagicNumber uint32 = 0x50503250 // "PP2P"

const (
	// 最大数据包大小 (8MB)
	MaxPacketSize = 8 * 1024 * 1024
)

// PacketType P2P消息类型
type PacketType uint8

const (
	PacketTypeHandshake       PacketType = 0
	PacketTypeHandshakeAck    PacketType = 1
	PacketTypeSyncConnectors  PacketType = 10
	PacketTypeSyncAck         PacketType = 11
	PacketTypeRelayMessage    PacketType = 20
	PacketTypeRelayMessageAck PacketType = 21
	PacketTypeHeartbeat       PacketType = 30
	PacketTypeHeartbeatAck    PacketType = 31
	PacketTypeDisconnect      PacketType = 40
)

// Packet 原始数据包结构（TCP传输层）
type Packet struct {
	Magic   uint32    // 魔数 4字节
	Version uint8     // 版本号 1字节
	Type    PacketType // 消息类型 1字节
	Flags   uint8     // 标志位 1字节
	Length  uint32    // 载荷长度 4字节
	TraceID [16]byte  // 追踪ID 16字节
	_       [2]byte   // 对齐填充 2字节
	Payload []byte    // 载荷
}

// Encode 将Packet编码为二进制格式
func (p *Packet) Encode() ([]byte, error) {
	if len(p.Payload) > int(MaxPacketSize) {
		return nil, fmt.Errorf("payload too large: %d > %d", len(p.Payload), MaxPacketSize)
	}

	// 分配空间：固定头28字节 + 载荷
	data := make([]byte, 28+len(p.Payload))

	// 魔数 (大端序)
	binary.BigEndian.PutUint32(data[0:4], MagicNumber)

	// 版本
	data[4] = p.Version

	// 类型
	data[5] = uint8(p.Type)

	// 标志位
	data[6] = p.Flags

	// 保留
	data[7] = 0

	// 载荷长度 (大端序)
	binary.BigEndian.PutUint32(data[8:12], uint32(len(p.Payload)))

	// 追踪ID
	copy(data[12:28], p.TraceID[:])

	// 载荷
	copy(data[28:], p.Payload)

	return data, nil
}

// DecodeHeader 从二进制数据解码包头
func DecodePacket(data []byte) (*Packet, error) {
	if len(data) < 28 {
		return nil, fmt.Errorf("data too short for header: %d < 28", len(data))
	}

	// 检查魔数
	magic := binary.BigEndian.Uint32(data[0:4])
	if magic != MagicNumber {
		return nil, fmt.Errorf("invalid magic number: 0x%X", magic)
	}

	// 版本
	version := data[4]

	// 类型
	msgType := data[5]

	// 标志位
	flags := data[6]

	// 载荷长度
	length := binary.BigEndian.Uint32(data[8:12])

	// 追踪ID
	var traceID [16]byte
	copy(traceID[:], data[12:28])

	// 检查数据是否完整
	if len(data) < 28+int(length) {
		return nil, fmt.Errorf("data truncated: expected %d, got %d", 28+int(length), len(data))
	}

	// 载荷
	payload := make([]byte, length)
	copy(payload, data[28:28+length])

	return &Packet{
		Magic:   magic,
		Version: version,
		Type:    PacketType(msgType),
		Flags:   flags,
		Length:  length,
		TraceID: traceID,
		Payload: payload,
	}, nil
}

// NewPacket 创建一个新的Packet
func NewPacket(msgType PacketType, traceID string, payload []byte) *Packet {
	var tid [16]byte
	copy(tid[:], []byte(traceID))

	return &Packet{
		Magic:   MagicNumber,
		Version: 1,
		Type:    msgType,
		Flags:   0,
		Length:  uint32(len(payload)),
		TraceID: tid,
		Payload: payload,
	}
}

// MessageToPayload 将protobuf消息转换为字节载荷
func MessageToPayload(msg proto.Message) ([]byte, error) {
	return proto.Marshal(msg)
}

// PayloadToMessage 将字节载荷转换为protobuf消息
func PayloadToMessage(data []byte, msg proto.Message) error {
	return proto.Unmarshal(data, msg)
}

// ConnectorOnlineInfo P2P同步用的连接器信息（内部使用，不需要proto）
type ConnectorOnlineInfo struct {
	ConnectorId    string `json:"connector_id"`
	KernelId       string `json:"kernel_id"`
	Address        string `json:"address"`
	Port           int32  `json:"port"`
	EntityType     string `json:"entity_type"`
	SessionId      string `json:"session_id"`
	LastHeartbeat  int64  `json:"last_heartbeat"`
	RegisteredAt   int64  `json:"registered_at"`
	ExposeToOthers bool   `json:"expose_to_others"`
}

// FromProto 从proto消息转换
func (c *ConnectorOnlineInfo) FromProto(info *pb.ConnectorOnlineInfo) *ConnectorOnlineInfo {
	if info == nil {
		return nil
	}
	return &ConnectorOnlineInfo{
		ConnectorId:    info.ConnectorId,
		KernelId:       info.KernelId,
		Address:        info.Address,
		Port:           info.Port,
		EntityType:     info.EntityType,
		SessionId:      info.SessionId,
		LastHeartbeat:  info.LastHeartbeat,
		RegisteredAt:   info.RegisteredAt,
		ExposeToOthers: info.ExposeToOthers,
	}
}

// ToProto 转换为proto消息
func (c *ConnectorOnlineInfo) ToProto() *pb.ConnectorOnlineInfo {
	if c == nil {
		return nil
	}
	return &pb.ConnectorOnlineInfo{
		ConnectorId:    c.ConnectorId,
		KernelId:       c.KernelId,
		Address:        c.Address,
		Port:           c.Port,
		EntityType:     c.EntityType,
		SessionId:      c.SessionId,
		LastHeartbeat:  c.LastHeartbeat,
		RegisteredAt:   c.RegisteredAt,
		ExposeToOthers: c.ExposeToOthers,
	}
}

// HandshakePayload 握手载荷
type HandshakePayload struct {
	KernelID   string `json:"kernel_id"`
	KernelAddr string `json:"kernel_address"`
	KernelPort int32  `json:"kernel_port"`
	PeerPort   int32  `json:"peer_port"`
	Version    string `json:"version"`
	Timestamp  int64  `json:"timestamp"`
}

// HandshakeAckPayload 握手响应载荷
type HandshakeAckPayload struct {
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	KernelID  string `json:"kernel_id"`
	Version   string `json:"version"`
	Timestamp int64  `json:"timestamp"`
}

// SyncConnectorsPayload 连接器同步载荷
type SyncConnectorsPayload struct {
	SourceKernelID string                  `json:"source_kernel_id"`
	Connectors     []*ConnectorOnlineInfo `json:"connectors"`
	Timestamp      int64                  `json:"timestamp"`
}

// SyncConnectorsAckPayload 连接器同步响应载荷
type SyncConnectorsAckPayload struct {
	Success          bool                   `json:"success"`
	Message          string                 `json:"message"`
	RemoteConnectors []*ConnectorOnlineInfo `json:"remote_connectors"`
	Timestamp        int64                 `json:"timestamp"`
}

// RelayMessagePayload 消息转发载荷
type RelayMessagePayload struct {
	MessageID      string `json:"message_id"`
	SenderID      string `json:"sender_id"`
	SenderKernelID string `json:"sender_kernel_id"`
	ReceiverID    string `json:"receiver_id"`
	Payload       []byte `json:"payload"`
	Timestamp     int64  `json:"timestamp"`
	FlowID        string `json:"flow_id"`
}

// RelayMessageAckPayload 消息转发响应载荷
type RelayMessageAckPayload struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	MessageID   string `json:"message_id"`
	DeliveredAt int64  `json:"delivered_at"`
}

// HeartbeatPayload 心跳载荷
type HeartbeatPayload struct {
	KernelID            string `json:"kernel_id"`
	Timestamp           int64  `json:"timestamp"`
	ConnectedConnectors int32  `json:"connected_connectors"`
	ActivePeers        int32  `json:"active_peers"`
}

// HeartbeatAckPayload 心跳响应载荷
type HeartbeatAckPayload struct {
	Success             bool   `json:"success"`
	KernelID            string `json:"kernel_id"`
	Timestamp           int64  `json:"timestamp"`
	ConnectedConnectors int32  `json:"connected_connectors"`
	ActivePeers        int32  `json:"active_peers"`
}

// JSONMarshal JSON序列化
func JSONMarshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

// JSONUnmarshal JSON反序列化
func JSONUnmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

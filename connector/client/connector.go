package client

import (
	"context"
	"crypto/tls"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "github.com/trusted-space/kernel/proto/kernel/v1"
)

// ConnectorStatus 连接器状态
type ConnectorStatus string

const (
	ConnectorStatusActive   ConnectorStatus = "active"
	ConnectorStatusInactive ConnectorStatus = "inactive"
	ConnectorStatusClosed   ConnectorStatus = "closed"
)

// LocalChannelInfo 本地记录的频道信息（从创建或通知中感知到）
type LocalChannelInfo struct {
	ChannelID    string
	DataTopic    string
	CreatorID    string
	SenderID     string
	ReceiverID   string
	Encrypted    bool
	Participants []string // 保留兼容性
	CreatedAt    int64
	IsEvidenceChannel bool // 是否为存证频道
}

// Connector 连接器客户端
type Connector struct {
	connectorID  string
	entityType   string
	publicKey    string
	serverAddr   string
	status       ConnectorStatus // 连接器状态，默认为active
	
	conn         *grpc.ClientConn
	identitySvc  pb.IdentityServiceClient
	channelSvc   pb.ChannelServiceClient
	evidenceSvc  pb.EvidenceServiceClient
	
	sessionToken string
	ctx          context.Context
	cancel       context.CancelFunc

	channelsMu sync.RWMutex
	channels   map[string]*LocalChannelInfo

	// 订阅跟踪，防止重复订阅
	subscriptionsMu sync.RWMutex
	subscriptions   map[string]bool // key: channelID, value: 是否正在订阅

	// 通知去重，防止重复处理同一个频道的通知
	processedNotificationsMu sync.RWMutex
	processedNotifications   map[string]bool // key: channelID, value: 是否已处理过通知
	skippedNotifications     map[string]int  // key: channelID, value: 跳过次数

	// 存证存储配置
	evidenceLocalStorage bool   // 是否启用本地存证存储
	evidenceStoragePath  string // 本地存证存储路径
}

// Config 连接器配置
type Config struct {
	ConnectorID    string
	EntityType     string
	PublicKey      string
	ServerAddr     string
	CACertPath     string
	ClientCertPath string
	ClientKeyPath  string
	ServerName     string

	// 存证存储配置
	EvidenceLocalStorage bool   // 是否启用本地存证存储
	EvidenceStoragePath  string // 本地存证存储路径
}

// NewConnector 创建新的连接器
func NewConnector(config *Config) (*Connector, error) {
	// 加载 TLS 凭证
	tlsConfig, err := loadTLSConfig(config.CACertPath, config.ClientCertPath, config.ClientKeyPath, config.ServerName)
	if err != nil {
		return nil, fmt.Errorf("failed to load TLS config: %w", err)
	}

	creds := credentials.NewTLS(tlsConfig)

	// 连接到内核
	// 使用30秒超时以适应跨主机网络延迟
	conn, err := grpc.Dial(
		config.ServerAddr,
		grpc.WithTransportCredentials(creds),
		grpc.WithBlock(),
		grpc.WithTimeout(30*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to kernel: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	connector := &Connector{
		connectorID: config.ConnectorID,
		entityType:  config.EntityType,
		publicKey:   config.PublicKey,
		serverAddr:  config.ServerAddr,
		status:      ConnectorStatusActive, // 默认状态为active
		conn:        conn,
		identitySvc: pb.NewIdentityServiceClient(conn),
		channelSvc:  pb.NewChannelServiceClient(conn),
		evidenceSvc: pb.NewEvidenceServiceClient(conn),
		ctx:         ctx,
		cancel:      cancel,
		channels:     make(map[string]*LocalChannelInfo),
		subscriptions: make(map[string]bool),
		processedNotifications: make(map[string]bool),
		skippedNotifications: make(map[string]int),
		evidenceLocalStorage: config.EvidenceLocalStorage,
		evidenceStoragePath:  config.EvidenceStoragePath,
	}

	return connector, nil
}

// Connect 连接到内核并完成握手
func (c *Connector) Connect() error {
	// 发送握手请求
	resp, err := c.identitySvc.Handshake(c.ctx, &pb.HandshakeRequest{
		ConnectorId: c.connectorID,
		EntityType:  c.entityType,
		PublicKey:   c.publicKey,
		Timestamp:   time.Now().Unix(),
	})
	if err != nil {
		return fmt.Errorf("handshake failed: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("handshake rejected: %s", resp.Message)
	}

	c.sessionToken = resp.SessionToken
	log.Printf("✓ Connected to kernel (version: %s)", resp.KernelVersion)
	log.Printf("✓ Session token: %s", c.sessionToken)

	// 启动心跳
	go c.startHeartbeat()

	return nil
}

// startHeartbeat 启动心跳协程
func (c *Connector) startHeartbeat() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			resp, err := c.identitySvc.Heartbeat(c.ctx, &pb.HeartbeatRequest{
				ConnectorId: c.connectorID,
				Timestamp:   time.Now().Unix(),
			})
			if err != nil {
				log.Printf("⚠ Heartbeat failed: %v", err)
			} else if !resp.Acknowledged {
				log.Printf("⚠ Heartbeat not acknowledged")
			}
		}
	}
}

// CreateChannel 创建数据传输频道（支持多对多模式）
// senderIDs: 发送方ID列表（至少指定一个，不能为空）
// receiverIDs: 接收方ID列表（至少指定一个，不能为空）
// dataTopic: 数据主题
// createRelatedChannels: 是否创建关联频道（数据频道会自动创建控制和日志频道）
// CreateChannel method removed - channels must be created through proposal process

// addOrUpdateLocalChannel 记录或更新本地频道信息
func (c *Connector) addOrUpdateLocalChannel(info *LocalChannelInfo) {
	c.channelsMu.Lock()
	defer c.channelsMu.Unlock()

	if existing, ok := c.channels[info.ChannelID]; ok {
		// 更新已知字段，但尽量保留已有信息
		if info.DataTopic != "" {
			existing.DataTopic = info.DataTopic
		}
		if info.CreatorID != "" {
			existing.CreatorID = info.CreatorID
		}
		if info.SenderID != "" {
			existing.SenderID = info.SenderID
		}
		if info.ReceiverID != "" {
			existing.ReceiverID = info.ReceiverID
		}
		// 统一频道不再需要ChannelType和RelatedChannelIDs
		if len(info.Participants) > 0 {
			existing.Participants = info.Participants
		}
		if info.CreatedAt != 0 {
			existing.CreatedAt = info.CreatedAt
		}
		existing.Encrypted = info.Encrypted // 总是更新加密状态
	} else {
		c.channels[info.ChannelID] = info
	}
}

// removeLocalChannel 从本地记录中移除频道
func (c *Connector) removeLocalChannel(channelID string) {
	c.channelsMu.Lock()
	defer c.channelsMu.Unlock()
	delete(c.channels, channelID)
	}

// RecordChannelFromNotification 根据频道创建通知记录本地频道信息
func (c *Connector) RecordChannelFromNotification(notification *pb.ChannelNotification) {
	// 构建参与者列表（支持多对多模式）
	var participants []string
	participants = append(participants, notification.SenderIds...)
	participants = append(participants, notification.ReceiverIds...)

	// 如果没有参与者信息，使用创建者
	if len(participants) == 0 {
		participants = []string{notification.CreatorId}
	}

	// 检查是否为存证频道（主题以"-evidence"结尾）
	isEvidenceChannel := strings.HasSuffix(notification.DataTopic, "-evidence")

	c.addOrUpdateLocalChannel(&LocalChannelInfo{
		ChannelID:         notification.ChannelId,
		DataTopic:         notification.DataTopic,
		CreatorID:         notification.CreatorId,
		Participants:      participants,
		CreatedAt:         notification.CreatedAt,
		IsEvidenceChannel: isEvidenceChannel,
	})
	}

// ListLocalChannels 列出本地已知的频道信息
func (c *Connector) ListLocalChannels() []*LocalChannelInfo {
	c.channelsMu.RLock()
	defer c.channelsMu.RUnlock()

	result := make([]*LocalChannelInfo, 0, len(c.channels))
	for _, ch := range c.channels {
		result = append(result, ch)
	}
	return result
}

// SendData 发送数据到频道
func (c *Connector) SendData(channelID string, data [][]byte) error {
	stream, err := c.channelSvc.StreamData(c.ctx)
	if err != nil {
		return fmt.Errorf("failed to create stream: %w", err)
	}

	// 发送数据包
	for i, chunk := range data {
		// 计算签名（这里简化为数据哈希）
		hash := sha256.Sum256(chunk)
		signature := hex.EncodeToString(hash[:])

		packet := &pb.DataPacket{
			ChannelId:      channelID,
			SequenceNumber: int64(i + 1),
			Payload:        chunk,
			Signature:      signature,
			Timestamp:      time.Now().Unix(),
			SenderId:       c.connectorID,
			TargetIds:      []string{}, // 空列表表示广播给所有订阅者
		}

		if err := stream.Send(packet); err != nil {
			return fmt.Errorf("failed to send packet %d: %w", i, err)
		}

		// 接收确认
		status, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("failed to receive status for packet %d: %w", i, err)
		}

		if !status.Success {
			return fmt.Errorf("packet %d failed: %s", i, status.Message)
		}

		log.Printf("✓ Packet %d sent successfully", i+1)
	}

	// 关闭发送流
	if err := stream.CloseSend(); err != nil {
		return fmt.Errorf("failed to close send: %w", err)
	}

	log.Printf("✓ All data sent successfully")
	return nil
}

// RealtimeSender 实时发送器，用于逐行发送数据
type RealtimeSender struct {
	stream     pb.ChannelService_StreamDataClient
	channelID  string
	sequence   int64
	connector  *Connector
}

// StartRealtimeSend 开始实时发送模式
func (c *Connector) StartRealtimeSend(channelID string) (*RealtimeSender, error) {
	stream, err := c.channelSvc.StreamData(c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create stream: %w", err)
	}

	return &RealtimeSender{
		stream:    stream,
		channelID: channelID,
		sequence:  0,
		connector: c,
	}, nil
}

// SendLine 发送一行数据（实时，广播给所有订阅者）
func (rs *RealtimeSender) SendLine(data []byte) error {
	return rs.SendLineTo(data, []string{}) // 空列表表示广播
}

// SendLineTo 发送一行数据到指定接收者（实时）
func (rs *RealtimeSender) SendLineTo(data []byte, targetIDs []string) error {
	rs.sequence++
	
	// 计算签名
	hash := sha256.Sum256(data)
	signature := hex.EncodeToString(hash[:])

	packet := &pb.DataPacket{
		ChannelId:      rs.channelID,
		SequenceNumber: rs.sequence,
		Payload:        data,
		Signature:      signature,
		Timestamp:      time.Now().Unix(),
		SenderId:       rs.connector.connectorID,
		TargetIds:      targetIDs, // 指定目标接收者，空列表表示广播
	}

	if err := rs.stream.Send(packet); err != nil {
		return fmt.Errorf("failed to send packet: %w", err)
	}

	// 接收确认（异步，不阻塞）
	go func() {
		status, err := rs.stream.Recv()
		if err != nil {
			log.Printf("⚠ 确认接收失败: %v", err)
			return
		}
		if !status.Success {
			log.Printf("⚠ 数据包发送失败: %s", status.Message)
		}
	}()

	return nil
}

// Close 关闭实时发送流
func (rs *RealtimeSender) Close() error {
	return rs.stream.CloseSend()
}

// ReceiveData 从频道接收数据
func (c *Connector) ReceiveData(channelID string, handler func(*pb.DataPacket) error) error {
	// 检查是否已经订阅了该频道
	c.subscriptionsMu.Lock()
	if c.subscriptions[channelID] {
		c.subscriptionsMu.Unlock()
		return fmt.Errorf("already subscribed to channel: %s", channelID)
	}
	// 标记开始订阅
	c.subscriptions[channelID] = true
	c.subscriptionsMu.Unlock()

	// 确保在函数结束时清理订阅标记
	defer func() {
		c.subscriptionsMu.Lock()
		delete(c.subscriptions, channelID)
		c.subscriptionsMu.Unlock()
	}()

	stream, err := c.channelSvc.SubscribeData(c.ctx, &pb.SubscribeRequest{
		ConnectorId: c.connectorID,
		ChannelId:   channelID,
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe: %w", err)
	}

	log.Printf("✓ Subscribed to channel: %s", channelID)
	log.Println("Waiting for data...")

	for {
		packet, err := stream.Recv()
		if err == io.EOF {
			log.Println("✓ Stream closed")
			return nil
		}
		if err != nil {
			return fmt.Errorf("receive error: %w", err)
		}

		log.Printf("✓ Received packet #%d (%d bytes)", packet.SequenceNumber, len(packet.Payload))

		// 在统一频道模式下，根据payload内容判断消息类型
		if c.isEvidenceData(packet.Payload) {
			// 存证数据：继续使用原有逻辑
			if err := c.processEvidencePacket(packet); err != nil {
				log.Printf("⚠ Failed to process evidence packet: %v", err)
			}
		} else if c.isControlMessage(packet.Payload) {
			// 控制消息：处理控制消息
			if err := c.processControlPacket(packet); err != nil {
				log.Printf("⚠ Failed to process control packet: %v", err)
			}
		} else {
			// 业务数据：调用处理函数
			if handler != nil {
				if err := handler(packet); err != nil {
					log.Printf("⚠ Handler error: %v", err)
				}
			}
		}
	}
}

// ReceiveEvidenceData 接收存证频道数据并本地存储
func (c *Connector) ReceiveEvidenceData(channelID string) error {
	// 检查是否已经在本地标记为订阅状态（避免重复调用）
	c.subscriptionsMu.Lock()
	if c.subscriptions[channelID] {
		c.subscriptionsMu.Unlock()
		return fmt.Errorf("already subscribed to evidence channel: %s", channelID)
	}
	// 标记开始订阅（即使内核已经记录了订阅，也要防止客户端重复调用）
	c.subscriptions[channelID] = true
	c.subscriptionsMu.Unlock()

	// 确保在函数结束时清理订阅标记
	defer func() {
		c.subscriptionsMu.Lock()
		delete(c.subscriptions, channelID)
		c.subscriptionsMu.Unlock()
	}()

	stream, err := c.channelSvc.SubscribeData(c.ctx, &pb.SubscribeRequest{
		ConnectorId: c.connectorID,
		ChannelId:   channelID,
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to evidence channel: %w", err)
	}

	log.Printf("✓ Subscribed to evidence channel: %s", channelID)

	// 初始化本地存证存储
	if err := c.initializeEvidenceStorage(); err != nil {
		log.Printf("⚠ Failed to initialize evidence storage: %v", err)
		// 继续运行，但不存储数据
	}

	for {
		packet, err := stream.Recv()
		if err == io.EOF {
			log.Println("✓ Evidence channel closed")
			return nil
		}
		if err != nil {
			return fmt.Errorf("evidence channel receive error: %w", err)
		}

		// 只处理证据数据，跳过其他类型的数据
		if c.isEvidenceData(packet.Payload) {
			// 处理存证数据
			if err := c.processEvidencePacket(packet); err != nil {
				log.Printf("⚠ Failed to process evidence packet: %v", err)
			}
		} else {
			// 跳过非证据数据
			// log.Printf("⏭️ Skipped non-evidence packet #%d in evidence channel %s", packet.SequenceNumber, channelID)
		}
	}
}

// ReceiveControlData 接收控制频道数据并处理控制消息
func (c *Connector) ReceiveControlData(channelID string) error {
	// 检查是否已经在本地标记为订阅状态（避免重复调用）
	c.subscriptionsMu.Lock()
	if c.subscriptions[channelID] {
		c.subscriptionsMu.Unlock()
		return fmt.Errorf("already subscribed to control channel: %s", channelID)
	}
	// 标记开始订阅（即使内核已经记录了订阅，也要防止客户端重复调用）
	c.subscriptions[channelID] = true
	c.subscriptionsMu.Unlock()

	// 确保在函数结束时清理订阅标记
	defer func() {
		c.subscriptionsMu.Lock()
		delete(c.subscriptions, channelID)
		c.subscriptionsMu.Unlock()
	}()

	stream, err := c.channelSvc.SubscribeData(c.ctx, &pb.SubscribeRequest{
		ConnectorId: c.connectorID,
		ChannelId:   channelID,
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to control channel: %w", err)
	}

	log.Printf("✓ Subscribed to control channel: %s", channelID)
	log.Println("Waiting for control messages...")

	for {
		packet, err := stream.Recv()
		if err == io.EOF {
			log.Println("✓ Control channel closed")
			return nil
		}
		if err != nil {
			return fmt.Errorf("control channel receive error: %w", err)
		}

		// 只处理控制消息，跳过其他类型的数据
		if c.isControlMessage(packet.Payload) {
			// 处理控制消息
			if err := c.processControlPacket(packet); err != nil {
				log.Printf("⚠ Failed to process control packet: %v", err)
			}
		} else {
			// 跳过非控制消息
			// log.Printf("⏭️ Skipped non-control packet #%d in control channel %s", packet.SequenceNumber, channelID)
		}
	}
}

// processControlPacket 处理接收到的控制消息数据包
func (c *Connector) processControlPacket(packet *pb.DataPacket) error {
	// 控制消息由connector/cmd/main.go中的handleControlMessage处理
	// 这里只需要记录日志，不需要特殊处理
	log.Printf("📢 [控制频道: %s, 序列号: %d] 收到控制消息 (%d bytes)",
		packet.ChannelId, packet.SequenceNumber, len(packet.Payload))

	// 控制消息的具体处理逻辑在UI层（main.go）实现
	return nil
}

// processEvidencePacket 处理接收到的存证数据包
func (c *Connector) processEvidencePacket(packet *pb.DataPacket) error {
	// 反序列化存证记录
	var record struct {
		TxID        string            `json:"tx_id"`
		ConnectorID string            `json:"connector_id"`
		EventType   string            `json:"event_type"`
		ChannelID   string            `json:"channel_id"`
		DataHash    string            `json:"data_hash"`
		Signature   string            `json:"signature"`
		Timestamp   string            `json:"timestamp"`
		Metadata    map[string]string `json:"metadata"`
		Hash        string            `json:"hash"`
		EventID     string            `json:"event_id"`
	}

	if err := json.Unmarshal(packet.Payload, &record); err != nil {
		return fmt.Errorf("failed to unmarshal evidence record: %w", err)
	}

	// 验证存证数据的完整性
	if err := c.verifyEvidenceRecord(&record); err != nil {
		log.Printf("⚠ Evidence record verification failed: %v", err)
		// 继续处理，但记录警告
	} else {
		// 减少证据验证成功的日志输出
	}

	// 本地存储存证数据
	if err := c.storeEvidenceRecord(&record); err != nil {
		return fmt.Errorf("failed to store evidence record: %w", err)
	}

	return nil
}

// verifyEvidenceRecord 验证存证记录的完整性
func (c *Connector) verifyEvidenceRecord(record *struct {
	TxID        string            `json:"tx_id"`
	ConnectorID string            `json:"connector_id"`
	EventType   string            `json:"event_type"`
	ChannelID   string            `json:"channel_id"`
	DataHash    string            `json:"data_hash"`
	Signature   string            `json:"signature"`
	Timestamp   string            `json:"timestamp"`
	Metadata    map[string]string `json:"metadata"`
	Hash        string            `json:"hash"`
	EventID     string            `json:"event_id"`
}) error {
	// 解析时间戳
	timestamp, err := time.Parse(time.RFC3339Nano, record.Timestamp)
	if err != nil {
		return fmt.Errorf("failed to parse timestamp: %w", err)
	}

	// 重新计算哈希值（与kernel端保持一致）
	data := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d|%s",
		record.TxID,
		record.ConnectorID,
		record.EventType,
		record.ChannelID,
		record.DataHash,
		record.Signature,
		timestamp.Unix(),
		record.EventID,
	)

	// 如果有metadata，包含在哈希中
	if len(record.Metadata) > 0 {
		metadataJSON, _ := json.Marshal(record.Metadata)
		data += "|" + string(metadataJSON)
	}

	hash := sha256.Sum256([]byte(data))
	calculatedHash := hex.EncodeToString(hash[:])

	// 比较哈希值
	if calculatedHash != record.Hash {
		return fmt.Errorf("hash mismatch: expected %s, got %s", record.Hash, calculatedHash)
	}

	return nil
}

// initializeEvidenceStorage 初始化本地存证存储
func (c *Connector) initializeEvidenceStorage() error {
	// 检查是否启用本地存证存储
	if !c.evidenceLocalStorage {
		return nil // 不启用本地存储
	}

	// 确定存储目录
	evidenceDir := c.evidenceStoragePath
	if evidenceDir == "" {
		evidenceDir = "./evidence" // 默认路径
	}

	if err := os.MkdirAll(evidenceDir, 0755); err != nil {
		return fmt.Errorf("failed to create evidence directory: %w", err)
	}

	// 检查存证文件是否存在，如果不存在则创建
	evidenceFile := filepath.Join(evidenceDir, "evidence.log")
	if _, err := os.Stat(evidenceFile); os.IsNotExist(err) {
		file, err := os.OpenFile(evidenceFile, os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("failed to create evidence file: %w", err)
		}
		file.Close()
		log.Printf("✓ Created evidence storage file: %s", evidenceFile)
	}

	return nil
}

// storeEvidenceRecord 本地存储存证记录
func (c *Connector) storeEvidenceRecord(record *struct {
	TxID        string            `json:"tx_id"`
	ConnectorID string            `json:"connector_id"`
	EventType   string            `json:"event_type"`
	ChannelID   string            `json:"channel_id"`
	DataHash    string            `json:"data_hash"`
	Signature   string            `json:"signature"`
	Timestamp   string            `json:"timestamp"`
	Metadata    map[string]string `json:"metadata"`
	Hash        string            `json:"hash"`
	EventID     string            `json:"event_id"`
}) error {
	// 检查是否启用本地存证存储
	if !c.evidenceLocalStorage {
		return nil // 不启用本地存储
	}

	// 验证存证记录的数字签名（可选，但推荐）
	if record.Signature != "" {
		timestamp, err := time.Parse(time.RFC3339Nano, record.Timestamp)
		if err != nil {
			log.Printf("⚠️  无法解析存证时间戳: %v", err)
		} else {
			// 调用内核验证签名
			valid, message, err := c.verifyEvidenceSignature(record.ConnectorID, record.EventType, record.ChannelID, record.DataHash, record.Signature, timestamp.Unix())
			if err != nil {
				log.Printf("⚠️  签名验证调用失败: %v", err)
			} else if !valid {
				log.Printf("⚠️  存证记录签名验证失败: %s", message)
				// 可以选择不存储无效签名的记录
				return fmt.Errorf("evidence signature verification failed: %s", message)
			} else {
				// 减少签名验证成功的详细日志
			}
		}
	}

	// 确定存储目录
	evidenceDir := c.evidenceStoragePath
	if evidenceDir == "" {
		evidenceDir = "./evidence" // 默认路径
	}

	evidenceFile := filepath.Join(evidenceDir, "evidence.log")

	file, err := os.OpenFile(evidenceFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open evidence file: %w", err)
	}
	defer file.Close()

	// 将记录序列化为JSON并写入文件
	recordData, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to marshal evidence record: %w", err)
	}

	if _, err := file.Write(append(recordData, '\n')); err != nil {
		return fmt.Errorf("failed to write evidence record: %w", err)
	}

	// 减少本地存储成功的日志输出
	return nil
}

// CloseChannel 关闭频道
func (c *Connector) CloseChannel(channelID string) error {
	resp, err := c.channelSvc.CloseChannel(c.ctx, &pb.CloseChannelRequest{
		ChannelId:   channelID,
		RequesterId: c.connectorID,
	})
	if err != nil {
		return fmt.Errorf("failed to close channel: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("close channel failed: %s", resp.Message)
	}

	log.Printf("✓ Channel closed: %s", channelID)
	return nil
}

// SubmitEvidence 提交存证
func (c *Connector) SubmitEvidence(eventType, channelID, dataHash, signature string, metadata map[string]string) (string, error) {
	resp, err := c.evidenceSvc.SubmitEvidence(c.ctx, &pb.EvidenceRequest{
		ConnectorId: c.connectorID,
		EventType:   eventType,
		ChannelId:   channelID,
		DataHash:    dataHash,
		Timestamp:   time.Now().Format(time.RFC3339),
		Signature:   signature,
		Metadata:    metadata,
	})
	if err != nil {
		return "", fmt.Errorf("failed to submit evidence: %w", err)
	}

	if !resp.Committed {
		return "", fmt.Errorf("evidence not committed: %s", resp.Message)
	}

	log.Printf("✓ Evidence committed: %s", resp.EvidenceTxId)
	return resp.EvidenceTxId, nil
}

// QueryEvidence 查询存证记录
func (c *Connector) QueryEvidence(channelID string, connectorID string, limit int32) ([]*pb.EvidenceRecord, error) {
	req := &pb.QueryRequest{
		Limit: limit,
	}

	if channelID != "" {
		req.ChannelId = channelID
	}
	if connectorID != "" {
		req.ConnectorId = connectorID
	}

	return c.queryEvidenceWithRequest(req)
}

// QueryEvidenceByFlowID 通过业务流程ID查询完整的业务流程
func (c *Connector) QueryEvidenceByFlowID(flowID string) ([]*pb.EvidenceRecord, error) {
	req := &pb.QueryRequest{
		FlowId: flowID,
		Limit:  1000, // 默认获取较多记录以包含完整流程
	}

	return c.queryEvidenceWithRequest(req)
}

// queryEvidenceWithRequest 通用查询方法
func (c *Connector) queryEvidenceWithRequest(req *pb.QueryRequest) ([]*pb.EvidenceRecord, error) {
	resp, err := c.evidenceSvc.QueryEvidence(c.ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to query evidence: %w", err)
	}

	log.Printf("✓ Found %d evidence records", resp.TotalCount)
	return resp.Logs, nil
}

// Close 关闭连接器
func (c *Connector) Close() error {
	c.cancel()

	// 清理订阅标记
	c.subscriptionsMu.Lock()
	c.subscriptions = make(map[string]bool)
	c.subscriptionsMu.Unlock()

	// 清理通知处理缓存
	c.processedNotificationsMu.Lock()
	c.processedNotifications = make(map[string]bool)
	c.skippedNotifications = make(map[string]int)
	c.processedNotificationsMu.Unlock()

	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// GetID 获取连接器 ID
func (c *Connector) GetID() string {
	return c.connectorID
}

// GetStatus 获取连接器状态
func (c *Connector) GetStatus() ConnectorStatus {
	return c.status
}

// SetStatus 设置连接器状态
func (c *Connector) SetStatus(status ConnectorStatus) error {
	// 验证状态值
	validStatuses := map[ConnectorStatus]bool{
		ConnectorStatusActive:   true,
		ConnectorStatusInactive: true,
		ConnectorStatusClosed:   true,
	}
	if !validStatuses[status] {
		return fmt.Errorf("invalid status: %s", status)
	}

	c.status = status

	// 如果设置了状态，同步到内核
	if c.identitySvc != nil {
		_, err := c.identitySvc.SetConnectorStatus(c.ctx, &pb.SetConnectorStatusRequest{
			RequesterId: c.connectorID,
			ConnectorId: c.connectorID,
			Status:      string(status),
		})
		if err != nil {
			log.Printf("⚠ 同步状态到内核失败: %v", err)
		}
	}

	return nil
}

// StartAutoNotificationListener 启动自动通知监听（连接器启动时调用）
// 所有连接器都会自动等待频道创建通知
// 收到通知后，如果连接器处于active状态，会自动订阅
func (c *Connector) StartAutoNotificationListener(onNotification func(*pb.ChannelNotification)) error {
	go func() {
		for {
			// 持续等待通知
			notifyChan, err := c.WaitForChannelNotification()
			if err != nil {
				log.Printf("⚠ 等待通知失败: %v，5秒后重试...", err)
				time.Sleep(5 * time.Second)
				continue
			}

			// 处理通知
			for notification := range notifyChan {
				// 去重检查：防止重复处理同一个频道的通知
				c.processedNotificationsMu.Lock()
				if notification.NegotiationStatus == pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED {
					if c.processedNotifications[notification.ChannelId] {
						c.processedNotificationsMu.Unlock()
						// 只在第一次跳过时显示消息，避免重复日志
						c.skippedNotifications[notification.ChannelId]++
						continue
					}
					// 标记为已处理
					c.processedNotifications[notification.ChannelId] = true
				}
				c.processedNotificationsMu.Unlock()

				// 记录本地频道信息
				c.RecordChannelFromNotification(notification)

				// 检查当前连接器是否是接收方或发送方（支持多对多模式）
				isReceiver := false
				isSender := false

				// 检查是否是发送方
				for _, senderID := range notification.SenderIds {
					if senderID == c.connectorID {
						isSender = true
						break
					}
				}

				// 检查是否是接收方
				for _, receiverID := range notification.ReceiverIds {
					if receiverID == c.connectorID {
						isReceiver = true
						break
					}
				}

				// 如果是发送方，根据协商状态显示不同信息
				if isSender && !isReceiver {
					// 调用回调函数显示详细信息
					if onNotification != nil {
						onNotification(notification)
					}

					if notification.NegotiationStatus == pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_PROPOSED {
						// 检查当前连接器是否是创建者
						isCreator := notification.CreatorId == c.connectorID

						if isCreator {
							// 创建者应该已经在提议时自动接受，这里不应该收到提议通知
							// 如果意外收到，说明已经自动接受了
							log.Printf("📋 频道提议已创建: %s (您是创建者，已自动接受)", notification.ChannelId)
							log.Printf("ℹ 等待其他参与方接受提议...")
						} else {
							// 收到提议通知，需要接受提议
							// 格式化参与者列表显示
							senderInfo := fmt.Sprintf("%v", notification.SenderIds)
							receiverInfo := fmt.Sprintf("%v", notification.ReceiverIds)

							log.Printf("📋 收到频道提议: %s (创建者: %s, 发送方: %s, 接收方: %s)",
								notification.ChannelId, notification.CreatorId, senderInfo, receiverInfo)
							log.Printf("ℹ 频道提议需要确认。作为发送方，您需要接受此提议")
							if notification.ProposalId != "" {
								log.Printf("ℹ 请使用 'accept %s %s' 接受频道提议", notification.ChannelId, notification.ProposalId)
							}
							log.Printf("ℹ 所有参与方接受后，频道将被激活，您可以开始发送数据")
						}
					} else if notification.NegotiationStatus == pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED {
						// 频道已正式创建
						// 格式化参与者列表显示
						senderInfo := fmt.Sprintf("%v", notification.SenderIds)
						receiverInfo := fmt.Sprintf("%v", notification.ReceiverIds)

						log.Printf("📢 频道已正式创建: %s (创建者: %s, 发送方: %s, 接收方: %s)",
							notification.ChannelId, notification.CreatorId, senderInfo, receiverInfo)
						log.Printf("✓ 您可以开始发送数据: 'sendto %s <data>'", notification.ChannelId)
					} else if notification.NegotiationStatus == pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_REJECTED {
						// 频道提议已被拒绝
						log.Printf("❌ 频道提议已被拒绝: %s (创建者: %s)",
							notification.ChannelId, notification.CreatorId)
						log.Printf("ℹ 频道协商已终止，无法使用此频道")
					}
					continue
				}

				// 如果当前连接器不是接收方，跳过自动订阅
				if !isReceiver {
					log.Printf("ℹ 收到频道创建通知，但当前连接器既不是发送方也不是接收方，跳过")
					continue
				}

				// 根据协商状态显示不同信息
				if onNotification != nil {
					onNotification(notification)
				}

				if notification.NegotiationStatus == pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_PROPOSED {
					// 检查当前连接器是否是创建者
					isCreator := notification.CreatorId == c.connectorID

					if isCreator {
						// 创建者已经自动接受，不需要显示额外信息
						// onNotification回调已经显示了通知，这里不再重复显示
						log.Printf("ℹ 等待其他参与方接受提议...")
					} else {
						// 收到提议通知，需要接受提议
						// 格式化参与者列表显示
						senderInfo := fmt.Sprintf("%v", notification.SenderIds)
						receiverInfo := fmt.Sprintf("%v", notification.ReceiverIds)

						log.Printf("📋 收到频道提议: %s (创建者: %s, 发送方: %s, 接收方: %s)",
							notification.ChannelId, notification.CreatorId, senderInfo, receiverInfo)
						log.Printf("ℹ 频道提议需要确认。作为接收方，您需要接受此提议")
						if notification.ProposalId != "" {
							log.Printf("ℹ 请使用 'accept %s %s' 接受频道提议", notification.ChannelId, notification.ProposalId)
						}
						log.Printf("ℹ 所有参与方接受后，频道将被激活并自动开始接收数据")
					}
				} else if notification.NegotiationStatus == pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED {

					// 频道已正式创建，可以自动订阅
					// 格式化参与者列表显示
					senderInfo := fmt.Sprintf("%v", notification.SenderIds)
					receiverInfo := fmt.Sprintf("%v", notification.ReceiverIds)

					// 统一频道模式
					log.Printf("📢 统一频道已正式创建: %s (创建者: %s, 发送方: %s, 接收方: %s)",
							notification.ChannelId, notification.CreatorId, senderInfo, receiverInfo)

					// 自动订阅逻辑
						go func(chID string, isEvidence bool, isControl bool) {
							if isEvidence {
								// 存证频道的特殊处理
								err := c.ReceiveEvidenceData(chID)
								if err != nil {
									log.Printf("❌ 存证频道自动订阅失败: %v", err)
								} else {
									log.Printf("✓ 存证频道 %s 已关闭", chID)
								}
							} else if isControl {
								// 控制频道的特殊处理
								err := c.ReceiveControlData(chID)
								if err != nil {
									log.Printf("❌ 控制频道自动订阅失败: %v", err)
								} else {
									log.Printf("✓ 控制频道 %s 已关闭", chID)
								}
							} else {
								// 普通数据频道的处理
								// 创建文件接收器（用于自动接收文件）
								outputDir := "./received"
								if err := os.MkdirAll(outputDir, 0755); err != nil {
									log.Printf("⚠ 创建接收目录失败: %v", err)
								}
								fileReceiver := NewFileReceiver(outputDir, func(filePath, fileHash string) {
									log.Printf("✓ 文件自动接收并保存成功: %s (哈希: %s)", filePath, fileHash)
									fmt.Printf("\n✓ 文件自动接收并保存成功:\n")
									fmt.Printf("  文件路径: %s\n", filePath)
									fmt.Printf("  文件哈希: %s\n", fileHash)
								})

								// 自动订阅并接收数据
								err := c.ReceiveData(chID, func(packet *pb.DataPacket) error {
									// 检查是否是文件传输数据包
									if IsFileTransferPacket(packet.Payload) {
										// 处理文件传输数据包
										if err := fileReceiver.HandleFilePacket(packet); err != nil {
											log.Printf("⚠ 处理文件数据包失败: %v", err)
										}
									} else {
										// 普通数据包，显示文本
										payloadStr := string(packet.Payload)

										// 检查是否是存证数据（JSON格式且包含特定字段）
										isEvidenceData := strings.Contains(payloadStr, `"event_type"`) &&
											strings.Contains(payloadStr, `"tx_id"`) &&
											strings.Contains(payloadStr, `"signature"`)

										if isEvidenceData {
											// 存证数据，简化显示
											var evidenceBrief struct {
												EventType   string `json:"event_type"`
												ConnectorID string `json:"connector_id"`
												TxID        string `json:"tx_id"`
											}
											if err := json.Unmarshal(packet.Payload, &evidenceBrief); err == nil {
												log.Printf("📋 [频道: %s, 序列号: %d] 存证记录: %s (%s) - TxID: %s",
													chID, packet.SequenceNumber, evidenceBrief.EventType,
													evidenceBrief.ConnectorID, evidenceBrief.TxID[:8]+"...")
												fmt.Printf("📋 [序列号: %d] 存证记录: %s (%s)\n",
													packet.SequenceNumber, evidenceBrief.EventType, evidenceBrief.ConnectorID)
											} else {
												log.Printf("📋 [频道: %s, 序列号: %d] 存证数据 (%d bytes)", chID, packet.SequenceNumber, len(packet.Payload))
												fmt.Printf("📋 [序列号: %d] 存证数据 (%d bytes)\n", packet.SequenceNumber, len(packet.Payload))
											}
										} else {
											// 普通数据，显示完整内容
											log.Printf("📦 [频道: %s, 序列号: %d] 数据: %s", chID, packet.SequenceNumber, payloadStr)
											fmt.Printf("📦 [序列号: %d] 数据: %s\n", packet.SequenceNumber, payloadStr)
										}
									}
									return nil
								})
								if err != nil {
									log.Printf("❌ 自动订阅失败: %v", err)
								} else {
									log.Printf("✓ 频道 %s 已关闭", chID)
								}
							}
						}(notification.ChannelId, false, false)
					} else if notification.NegotiationStatus == pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_REJECTED {
						// 频道提议已被拒绝
						log.Printf("❌ 频道提议已被拒绝: %s (创建者: %s)",
							notification.ChannelId, notification.CreatorId)
						log.Printf("ℹ 频道协商已终止，无法使用此频道")
					} else {
						// inactive 或其他状态：不显示详细通知，只记录简单日志
						log.Printf("ℹ 收到频道创建通知 (频道: %s, 创建者: %s)，但连接器状态为 %s，不会自动订阅。请手动使用 'receive %s' 订阅",
							notification.ChannelId, notification.CreatorId, c.status, notification.ChannelId)
					}
				}

			// 如果通道关闭，等待后重试
			time.Sleep(1 * time.Second)
		}
	}()

	return nil
}

// DiscoverConnectors 发现空间中的其他连接器
func (c *Connector) DiscoverConnectors(filterType string) ([]*pb.ConnectorInfo, error) {
	resp, err := c.identitySvc.DiscoverConnectors(c.ctx, &pb.DiscoverRequest{
		RequesterId: c.connectorID,
		FilterType:  filterType,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to discover connectors: %w", err)
	}

	return resp.Connectors, nil
}

// GetConnectorInfo 获取指定连接器的详细信息
func (c *Connector) GetConnectorInfo(connectorID string) (*pb.ConnectorInfo, error) {
	resp, err := c.identitySvc.GetConnectorInfo(c.ctx, &pb.GetConnectorInfoRequest{
		RequesterId: c.connectorID,
		ConnectorId: connectorID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get connector info: %w", err)
	}

	if !resp.Found {
		return nil, fmt.Errorf("connector not found: %s", resp.Message)
	}

	return resp.Connector, nil
}

// GetChannelInfo 获取频道信息
func (c *Connector) GetChannelInfo(channelID string) (*pb.GetChannelInfoResponse, error) {
	resp, err := c.channelSvc.GetChannelInfo(c.ctx, &pb.GetChannelInfoRequest{
		RequesterId: c.connectorID,
		ChannelId:   channelID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get channel info: %w", err)
	}

	return resp, nil
}

// WaitForChannelNotification 等待频道创建通知（接收方使用）
func (c *Connector) WaitForChannelNotification() (<-chan *pb.ChannelNotification, error) {
	stream, err := c.channelSvc.WaitForChannelNotification(c.ctx, &pb.WaitNotificationRequest{
		ReceiverId: c.connectorID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to wait for notification: %w", err)
	}

	notifyChan := make(chan *pb.ChannelNotification, 10)

	// 在goroutine中接收通知
	go func() {
		defer close(notifyChan)
		for {
			notification, err := stream.Recv()
			if err != nil {
				log.Printf("⚠ 通知接收错误: %v", err)
				return
			}
			notifyChan <- notification
		}
	}()

	return notifyChan, nil
}

// RegisterConnector 首次注册连接器并获取证书（使用引导端点，不需要证书）
func RegisterConnector(bootstrapAddr string, connectorID, entityType, publicKey, configYAML string) (certPEM, keyPEM, caCertPEM []byte, err error) {
	// 使用普通TLS连接到引导端点（不需要客户端证书）
	// 注意：首次注册时连接器还没有CA证书，所以暂时跳过证书验证
	// 注册成功后，连接器会获得CA证书，后续连接将使用mTLS进行验证
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true, // 首次注册时跳过验证，因为还没有CA证书
		MinVersion:         tls.VersionTLS13,
	}
	
	creds := credentials.NewTLS(tlsConfig)
	
	conn, err := grpc.Dial(
		bootstrapAddr,
		grpc.WithTransportCredentials(creds),
		grpc.WithBlock(),
		grpc.WithTimeout(30*time.Second), // 增加超时时间以支持跨主机连接
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to connect to bootstrap server %s: %w (请确认内核服务器已启动并监听引导端口)", bootstrapAddr, err)
	}
	defer conn.Close()
	
	identitySvc := pb.NewIdentityServiceClient(conn)
	
	// 发送注册请求
	resp, err := identitySvc.RegisterConnector(context.Background(), &pb.RegisterConnectorRequest{
		ConnectorId: connectorID,
		EntityType:  entityType,
		PublicKey:   publicKey,
		ConfigYaml:  configYAML,
		Timestamp:   time.Now().Unix(),
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to register connector: %w", err)
	}
	
	if !resp.Success {
		return nil, nil, nil, fmt.Errorf("registration failed: %s", resp.Message)
	}
	
	return resp.Certificate, resp.PrivateKey, resp.CaCertificate, nil
}

// GetConnectorID 获取连接器ID（别名）
func (c *Connector) GetConnectorID() string {
	return c.GetID()
}

// RequestPermissionChange 申请权限变更
func (c *Connector) RequestPermissionChange(channelID, changeType, targetID, reason string) (*pb.RequestPermissionChangeResponse, error) {
	resp, err := c.channelSvc.RequestPermissionChange(c.ctx, &pb.RequestPermissionChangeRequest{
		RequesterId: c.connectorID,
		ChannelId:   channelID,
		ChangeType:  changeType,
		TargetId:    targetID,
		Reason:      reason,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to request permission change: %w", err)
	}

	log.Printf("✓ Permission change request submitted: %s", resp.RequestId)
	return resp, nil
}

// ApprovePermissionChange 批准权限变更
func (c *Connector) ApprovePermissionChange(channelID, requestID string) (*pb.ApprovePermissionChangeResponse, error) {
	resp, err := c.channelSvc.ApprovePermissionChange(c.ctx, &pb.ApprovePermissionChangeRequest{
		ApproverId: c.connectorID,
		ChannelId:  channelID,
		RequestId:  requestID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to approve permission change: %w", err)
	}

	log.Printf("✓ Permission change approved: %s", requestID)
	return resp, nil
}

// RejectPermissionChange 拒绝权限变更
func (c *Connector) RejectPermissionChange(channelID, requestID, reason string) (*pb.RejectPermissionChangeResponse, error) {
	resp, err := c.channelSvc.RejectPermissionChange(c.ctx, &pb.RejectPermissionChangeRequest{
		ApproverId: c.connectorID,
		ChannelId:  channelID,
		RequestId:  requestID,
		Reason:     reason,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to reject permission change: %w", err)
	}

	log.Printf("✓ Permission change rejected: %s", requestID)
	return resp, nil
}

// GetPermissionRequests 获取权限变更请求列表
func (c *Connector) GetPermissionRequests(channelID string) (*pb.GetPermissionRequestsResponse, error) {
	resp, err := c.channelSvc.GetPermissionRequests(c.ctx, &pb.GetPermissionRequestsRequest{
		RequesterId: c.connectorID,
		ChannelId:   channelID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get permission requests: %w", err)
	}

	log.Printf("✓ Retrieved %d permission requests", len(resp.Requests))
	return resp, nil
}

// ChannelConfigFile 频道配置文件结构（与kernel中的定义保持一致）
type ChannelConfigFile struct {
	ChannelID       string          `json:"channel_id"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	CreatorID       string          `json:"creator_id"`
	ApproverID      string          `json:"approver_id"`
	SenderIDs       []string        `json:"sender_ids"`
	ReceiverIDs     []string        `json:"receiver_ids"`
	DataTopic       string          `json:"data_topic"`
	Encrypted       bool            `json:"encrypted"`
	EvidenceConfig  *EvidenceConfig `json:"evidence_config"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Version         int             `json:"version"`
}

// EvidenceConfig 存证配置（与kernel中的定义保持一致）
type EvidenceConfig struct {
	Mode           string            `json:"mode"`
	Strategy       string            `json:"strategy"`
	ConnectorID    string            `json:"connector_id"`
	BackupEnabled  bool              `json:"backup_enabled"`
	RetentionDays  int               `json:"retention_days"`
	CompressData   bool              `json:"compress_data"`
	CustomSettings map[string]string `json:"custom_settings"`
}

// CreateChannelFromConfig 从配置文件创建频道
func (c *Connector) CreateChannelFromConfig(configFilePath string) (*ChannelConfigFile, error) {
	// 读取配置文件
	data, err := os.ReadFile(configFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", configFilePath, err)
	}

	// 解析配置文件
	var config ChannelConfigFile
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// 验证配置
	if config.ChannelID == "" {
		return nil, fmt.Errorf("channel_id is required in config file")
	}
	if config.CreatorID == "" {
		return nil, fmt.Errorf("creator_id is required in config file")
	}
	if len(config.SenderIDs) == 0 {
		return nil, fmt.Errorf("at least one sender_id is required in config file")
	}
	if len(config.ReceiverIDs) == 0 {
		return nil, fmt.Errorf("at least one receiver_id is required in config file")
	}
	if config.DataTopic == "" {
		return nil, fmt.Errorf("data_topic is required in config file")
	}

	// 验证当前连接器是否是创建者
	if config.CreatorID != c.connectorID {
		return nil, fmt.Errorf("current connector (%s) is not the creator (%s) specified in config file", c.connectorID, config.CreatorID)
	}

	// 转换存证配置（如果有）
	var evidenceConfig *pb.EvidenceConfig
	if config.EvidenceConfig != nil {
		evidenceConfig = &pb.EvidenceConfig{
			Mode:           config.EvidenceConfig.Mode,
			Strategy:       config.EvidenceConfig.Strategy,
			ConnectorId:    config.EvidenceConfig.ConnectorID,
			BackupEnabled:  config.EvidenceConfig.BackupEnabled,
			RetentionDays:  int32(config.EvidenceConfig.RetentionDays),
			CompressData:   config.EvidenceConfig.CompressData,
			CustomSettings: config.EvidenceConfig.CustomSettings,
		}
	}

	// 调用gRPC API提议创建频道
	req := &pb.ProposeChannelRequest{
		CreatorId:      config.CreatorID,
		SenderIds:      config.SenderIDs,
		ReceiverIds:    config.ReceiverIDs,
		DataTopic:      config.DataTopic,
		Encrypted:      config.Encrypted,
		ApproverId:     config.ApproverID,
		Reason:         fmt.Sprintf("Channel created from config file: %s", configFilePath),
		ConfigFilePath: configFilePath, // 传递配置文件路径
		EvidenceConfig: evidenceConfig, // 传递存证配置
	}

	resp, err := c.channelSvc.ProposeChannel(context.Background(), req)
	if err != nil {
		return nil, fmt.Errorf("failed to propose channel: %w", err)
	}

	if !resp.Success {
		return nil, fmt.Errorf("failed to propose channel: %s", resp.Message)
	}

	log.Printf("✓ Channel created from config file: %s (%s)", config.ChannelID, configFilePath)
	return &config, nil
}

// ProposeChannel 提议创建频道（支持多对多模式）
func (c *Connector) ProposeChannel(senderIDs []string, receiverIDs []string, dataTopic, approverID, reason string) (string, string, error) {
	// 创建者固定为当前连接器
	creatorID := c.connectorID

	if len(senderIDs) == 0 {
		return "", "", fmt.Errorf("at least one senderID is required")
	}
	if len(receiverIDs) == 0 {
		return "", "", fmt.Errorf("at least one receiverID is required")
	}

	// 验证ID不为空
	for _, id := range senderIDs {
		if id == "" {
			return "", "", fmt.Errorf("senderID cannot be empty")
		}
	}
	for _, id := range receiverIDs {
		if id == "" {
			return "", "", fmt.Errorf("receiverID cannot be empty")
		}
	}

	resp, err := c.channelSvc.ProposeChannel(c.ctx, &pb.ProposeChannelRequest{
		CreatorId:      creatorID,
		SenderIds:      senderIDs,
		ReceiverIds:    receiverIDs,
		DataTopic:      dataTopic,
		Encrypted:      true, // 统一频道默认加密
		ApproverId:     approverID, // 权限变更批准者
		Reason:         reason,
		TimeoutSeconds: 300, // 默认5分钟超时
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to propose channel: %w", err)
	}

	if !resp.Success {
		return "", "", fmt.Errorf("channel proposal failed: %s", resp.Message)
	}

	log.Printf("✓ Channel proposed: %s (proposal: %s)", resp.ChannelId, resp.ProposalId)

	// 创建者自动接受提议
	if resp.ProposalId != "" {
		if err := c.AcceptChannelProposal(resp.ChannelId, resp.ProposalId); err != nil {
			log.Printf("⚠ 创建者自动接受提议失败: %v", err)
			// 不返回错误，继续执行，因为提议已经创建成功
		} else {
			log.Printf("✓ 创建者已自动接受频道提议: %s", resp.ChannelId)
		}
	}

	// 本地记录频道信息（提议状态）
	participants := make([]string, 0, len(senderIDs)+len(receiverIDs))
	participants = append(participants, senderIDs...)
	participants = append(participants, receiverIDs...)
	c.addOrUpdateLocalChannel(&LocalChannelInfo{
		ChannelID:    resp.ChannelId,
		DataTopic:    dataTopic,
		CreatorID:    creatorID,
		Participants: participants,
		Encrypted:    true,
		CreatedAt:    time.Now().Unix(),
	})

	return resp.ChannelId, resp.ProposalId, nil
}

// AcceptChannelProposal 接受频道提议
func (c *Connector) AcceptChannelProposal(channelID, proposalID string) error {
	// 设置30秒超时，避免无限期阻塞
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()

	resp, err := c.channelSvc.AcceptChannelProposal(ctx, &pb.AcceptChannelProposalRequest{
		AccepterId: c.connectorID,
		ChannelId:  channelID,
		ProposalId: proposalID,
	})
	if err != nil {
		return fmt.Errorf("failed to accept channel proposal: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("accept channel proposal failed: %s", resp.Message)
	}

	log.Printf("✓ Channel proposal accepted: %s", channelID)

	// 注意：频道可能还没有完全激活（需要所有参与方确认）
	// 状态更新将通过通知机制异步处理

	return nil
}

// RejectChannelProposal 拒绝频道提议
func (c *Connector) RejectChannelProposal(channelID, proposalID, reason string) error {
	resp, err := c.channelSvc.RejectChannelProposal(c.ctx, &pb.RejectChannelProposalRequest{
		RejecterId: c.connectorID,
		ChannelId:  channelID,
		ProposalId: proposalID,
		Reason:     reason,
	})
	if err != nil {
		return fmt.Errorf("failed to reject channel proposal: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("reject channel proposal failed: %s", resp.Message)
	}

	log.Printf("✓ Channel proposal rejected: %s", channelID)

	// 从本地记录中移除频道
	c.removeLocalChannel(channelID)

	return nil
}

// 文件传输协议常量
const (
	FileTransferPrefix = "__FILE_TRANSFER__"
	FileHeaderType     = "FILE_HEADER"
	FileChunkType      = "FILE_CHUNK"
	FileEndType        = "FILE_END"
)

// FileHeader 文件头信息
type FileHeader struct {
	Type      string `json:"type"`       // FILE_HEADER
	FileName  string `json:"file_name"`  // 文件名
	FileSize  int64  `json:"file_size"`  // 文件大小（字节）
	TotalChunks int  `json:"total_chunks"` // 总块数
	FileHash  string `json:"file_hash"`  // 文件哈希（SHA256）
	TransferID string `json:"transfer_id"` // 传输ID（用于标识同一个文件传输）
}

// FileChunk 文件块信息
type FileChunk struct {
	Type       string `json:"type"`        // FILE_CHUNK
	TransferID string `json:"transfer_id"` // 传输ID
	ChunkIndex int    `json:"chunk_index"` // 块索引（从0开始）
	ChunkSize  int    `json:"chunk_size"` // 块大小
	Data       []byte `json:"data"`       // 块数据（在JSON序列化时使用base64）
}

// FileEnd 文件尾信息
type FileEnd struct {
	Type       string `json:"type"`        // FILE_END
	TransferID string `json:"transfer_id"` // 传输ID
	Success    bool   `json:"success"`     // 传输是否成功
}

// SendFile 发送文件到频道
func (c *Connector) SendFile(channelID string, filePath string, targetIDs []string) error {
	// 打开文件
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// 获取文件信息
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	fileSize := fileInfo.Size()
	fileName := fileInfo.Name()

	// 计算文件哈希
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("failed to calculate file hash: %w", err)
	}
	fileHash := hex.EncodeToString(hasher.Sum(nil))

	// 重新打开文件（因为已经读取完了）
	file, err = os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to reopen file: %w", err)
	}
	defer file.Close()

	// 创建发送流
	stream, err := c.channelSvc.StreamData(c.ctx)
	if err != nil {
		return fmt.Errorf("failed to create stream: %w", err)
	}

	// 生成传输ID
	transferID := fmt.Sprintf("%s_%d", c.connectorID, time.Now().UnixNano())

	// 分块大小（1MB）
	chunkSize := 1024 * 1024
	totalChunks := int((fileSize + int64(chunkSize) - 1) / int64(chunkSize))

	// 发送文件头
	header := FileHeader{
		Type:        FileHeaderType,
		FileName:    fileName,
		FileSize:    fileSize,
		TotalChunks: totalChunks,
		FileHash:    fileHash,
		TransferID:  transferID,
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("failed to marshal file header: %w", err)
	}

	headerPayload := append([]byte(FileTransferPrefix), headerJSON...)
	headerHash := sha256.Sum256(headerPayload)
	headerSignature := hex.EncodeToString(headerHash[:])

	headerPacket := &pb.DataPacket{
		ChannelId:      channelID,
		SequenceNumber: 0,
		Payload:        headerPayload,
		Signature:      headerSignature,
		Timestamp:      time.Now().Unix(),
		SenderId:       c.connectorID,
		TargetIds:      targetIDs,
	}

	if err := stream.Send(headerPacket); err != nil {
		return fmt.Errorf("failed to send file header: %w", err)
	}

	log.Printf("📤 发送文件: %s (大小: %d 字节, %d 块)", fileName, fileSize, totalChunks)

	// 分块发送文件
	sequence := int64(1)
	buffer := make([]byte, chunkSize)

	for chunkIndex := 0; chunkIndex < totalChunks; chunkIndex++ {
		// 读取一块数据
		n, err := file.Read(buffer)
		if err != nil && err != io.EOF {
			return fmt.Errorf("failed to read file chunk: %w", err)
		}
		if n == 0 {
			break
		}

		chunkData := buffer[:n]

		// 创建文件块信息
		chunk := FileChunk{
			Type:       FileChunkType,
			TransferID: transferID,
			ChunkIndex: chunkIndex,
			ChunkSize:  n,
			Data:       chunkData,
		}

		chunkJSON, err := json.Marshal(chunk)
		if err != nil {
			return fmt.Errorf("failed to marshal file chunk: %w", err)
		}

		chunkPayload := append([]byte(FileTransferPrefix), chunkJSON...)
		chunkHash := sha256.Sum256(chunkPayload)
		chunkSignature := hex.EncodeToString(chunkHash[:])

		chunkPacket := &pb.DataPacket{
			ChannelId:      channelID,
			SequenceNumber: sequence,
			Payload:        chunkPayload,
			Signature:      chunkSignature,
			Timestamp:      time.Now().Unix(),
			SenderId:       c.connectorID,
			TargetIds:      targetIDs,
		}

		if err := stream.Send(chunkPacket); err != nil {
			return fmt.Errorf("failed to send file chunk %d: %w", chunkIndex, err)
		}

		sequence++
		log.Printf("  ✓ 已发送块 %d/%d", chunkIndex+1, totalChunks)
	}

	// 发送文件尾
	end := FileEnd{
		Type:       FileEndType,
		TransferID: transferID,
		Success:    true,
	}

	endJSON, err := json.Marshal(end)
	if err != nil {
		return fmt.Errorf("failed to marshal file end: %w", err)
	}

	endPayload := append([]byte(FileTransferPrefix), endJSON...)
	endHash := sha256.Sum256(endPayload)
	endSignature := hex.EncodeToString(endHash[:])

	endPacket := &pb.DataPacket{
		ChannelId:      channelID,
		SequenceNumber: sequence,
		Payload:        endPayload,
		Signature:      endSignature,
		Timestamp:      time.Now().Unix(),
		SenderId:       c.connectorID,
		TargetIds:      targetIDs,
	}

	if err := stream.Send(endPacket); err != nil {
		return fmt.Errorf("failed to send file end: %w", err)
	}

	// 关闭发送流
	if err := stream.CloseSend(); err != nil {
		return fmt.Errorf("failed to close send: %w", err)
	}

	log.Printf("✓ 文件发送完成: %s", fileName)
	return nil
}

// FileReceiver 文件接收器（支持同时接收多个文件）
type FileReceiver struct {
	transfers    map[string]*fileTransfer // key: transferID
	transfersMu  sync.Mutex
	outputPath   string
	onComplete   func(string, string) // 回调函数：文件路径, 文件哈希
}

// fileTransfer 单个文件传输状态
type fileTransfer struct {
	transferID     string
	fileName       string
	fileSize       int64
	totalChunks    int
	fileHash       string
	chunks         map[int][]byte
	receivedChunks int
}

// NewFileReceiver 创建文件接收器
func NewFileReceiver(outputDir string, onComplete func(string, string)) *FileReceiver {
	return &FileReceiver{
		transfers:  make(map[string]*fileTransfer),
		outputPath: outputDir,
		onComplete: onComplete,
	}
}

// HandleFilePacket 处理文件传输数据包
func (fr *FileReceiver) HandleFilePacket(packet *pb.DataPacket) error {
	payload := packet.Payload

	// 检查是否是文件传输数据包
	if !IsFileTransferPacket(payload) {
		return fmt.Errorf("not a file transfer packet")
	}

	// 移除前缀
	jsonData := payload[len(FileTransferPrefix):]

	// 解析JSON（先判断类型）
	var typeCheck struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(jsonData, &typeCheck); err != nil {
		return fmt.Errorf("failed to parse packet type: %w", err)
	}

	switch typeCheck.Type {
	case FileHeaderType:
		return fr.handleFileHeader(jsonData)
	case FileChunkType:
		return fr.handleFileChunk(jsonData)
	case FileEndType:
		return fr.handleFileEnd(jsonData)
	default:
		return fmt.Errorf("unknown file transfer packet type: %s", typeCheck.Type)
	}
}

func (fr *FileReceiver) handleFileHeader(jsonData []byte) error {
	var header FileHeader
	if err := json.Unmarshal(jsonData, &header); err != nil {
		return fmt.Errorf("failed to parse file header: %w", err)
	}

	fr.transfersMu.Lock()
	defer fr.transfersMu.Unlock()

	// 创建新的文件传输记录
	fr.transfers[header.TransferID] = &fileTransfer{
		transferID:     header.TransferID,
		fileName:       header.FileName,
		fileSize:       header.FileSize,
		totalChunks:    header.TotalChunks,
		fileHash:       header.FileHash,
		chunks:         make(map[int][]byte),
		receivedChunks: 0,
	}

	log.Printf("📥 开始接收文件: %s (大小: %d 字节, %d 块, 传输ID: %s)", 
		header.FileName, header.FileSize, header.TotalChunks, header.TransferID)
	return nil
}

func (fr *FileReceiver) handleFileChunk(jsonData []byte) error {
	var chunk FileChunk
	if err := json.Unmarshal(jsonData, &chunk); err != nil {
		return fmt.Errorf("failed to parse file chunk: %w", err)
	}

	fr.transfersMu.Lock()
	transfer, exists := fr.transfers[chunk.TransferID]
	if !exists {
		fr.transfersMu.Unlock()
		return fmt.Errorf("file header not received for transfer ID: %s", chunk.TransferID)
	}

	// 存储块数据
	transfer.chunks[chunk.ChunkIndex] = chunk.Data
	transfer.receivedChunks++

	receivedChunks := transfer.receivedChunks
	totalChunks := transfer.totalChunks
	fileName := transfer.fileName
	transferID := transfer.transferID

	fr.transfersMu.Unlock()

	log.Printf("  ✓ 已接收块 %d/%d (文件: %s)", chunk.ChunkIndex+1, totalChunks, fileName)

	// 检查是否接收完所有块
	if receivedChunks >= totalChunks {
		return fr.assembleFile(transferID)
	}

	return nil
}

func (fr *FileReceiver) handleFileEnd(jsonData []byte) error {
	var end FileEnd
	if err := json.Unmarshal(jsonData, &end); err != nil {
		return fmt.Errorf("failed to parse file end: %w", err)
	}

	if !end.Success {
		return fmt.Errorf("file transfer failed for transfer ID: %s", end.TransferID)
	}

	fr.transfersMu.Lock()
	transfer, exists := fr.transfers[end.TransferID]
	if !exists {
		fr.transfersMu.Unlock()
		return fmt.Errorf("file header not received for transfer ID: %s", end.TransferID)
	}

	receivedChunks := transfer.receivedChunks
	totalChunks := transfer.totalChunks
	transferID := end.TransferID

	if receivedChunks < totalChunks {
		log.Printf("⚠ 文件传输结束，但只接收到 %d/%d 块 (传输ID: %s)", receivedChunks, totalChunks, transferID)
	}

	fr.transfersMu.Unlock()

	// 尝试组装文件
	return fr.assembleFile(transferID)
}

func (fr *FileReceiver) assembleFile(transferID string) error {
	fr.transfersMu.Lock()
	transfer, exists := fr.transfers[transferID]
	if !exists {
		fr.transfersMu.Unlock()
		return fmt.Errorf("transfer not found: %s", transferID)
	}

	// 检查是否所有块都已接收
	if transfer.receivedChunks < transfer.totalChunks {
		fr.transfersMu.Unlock()
		return fmt.Errorf("incomplete file: received %d/%d chunks", transfer.receivedChunks, transfer.totalChunks)
	}

	// 复制传输信息
	fileName := transfer.fileName
	fileSize := transfer.fileSize
	totalChunks := transfer.totalChunks
	fileHash := transfer.fileHash
	chunks := make(map[int][]byte)
	for k, v := range transfer.chunks {
		chunks[k] = v
	}

	// 从传输列表中移除（组装完成后）
	defer func() {
		fr.transfersMu.Lock()
		delete(fr.transfers, transferID)
		fr.transfersMu.Unlock()
	}()

	fr.transfersMu.Unlock()

	// 组装文件
	outputPath := filepath.Join(fr.outputPath, fileName)
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()

	// 按顺序写入块
	for i := 0; i < totalChunks; i++ {
		chunk, exists := chunks[i]
		if !exists {
			return fmt.Errorf("missing chunk %d", i)
		}
		if _, err := file.Write(chunk); err != nil {
			return fmt.Errorf("failed to write chunk %d: %w", i, err)
		}
	}

	// 验证文件哈希
	file.Seek(0, 0)
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("failed to calculate received file hash: %w", err)
	}
	receivedHash := hex.EncodeToString(hasher.Sum(nil))

	if receivedHash != fileHash {
		os.Remove(outputPath)
		return fmt.Errorf("file hash mismatch: expected %s, got %s", fileHash, receivedHash)
	}

	log.Printf("✓ 文件接收完成: %s (大小: %d 字节, 哈希验证通过)", outputPath, fileSize)

	// 调用完成回调
	if fr.onComplete != nil {
		fr.onComplete(outputPath, receivedHash)
	}

	return nil
}

// IsFileTransferPacket 检查是否是文件传输数据包（导出函数）
func IsFileTransferPacket(payload []byte) bool {
	return len(payload) > len(FileTransferPrefix) && string(payload[:len(FileTransferPrefix)]) == FileTransferPrefix
}

// IsControlMessage 检查是否是控制消息
func IsControlMessage(payload []byte) bool {
	payloadStr := string(payload)

	// 检查是否包含控制消息的特征字段
	return strings.Contains(payloadStr, `"message_type":`) &&
		strings.Contains(payloadStr, `"timestamp":`)
}

// verifyEvidenceSignature 验证存证记录的数字签名
func (c *Connector) verifyEvidenceSignature(connectorID, eventType, channelID, dataHash, signature string, timestamp int64) (bool, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 调用内核的签名验证服务
	resp, err := c.evidenceSvc.VerifyEvidenceSignature(ctx, &pb.VerifySignatureRequest{
		RequesterId: c.connectorID, // 使用当前连接器的ID作为请求者
		ConnectorId: connectorID,
		EventType:   eventType,
		ChannelId:   channelID,
		DataHash:    dataHash,
		Signature:   signature,
		Timestamp:   timestamp,
	})

	if err != nil {
		return false, "", fmt.Errorf("failed to verify signature: %w", err)
	}

	return resp.Valid, resp.Message, nil
}

// IsChannelParticipant 检查当前连接器是否是指定频道的参与者
func (c *Connector) IsChannelParticipant(channelID string) (bool, error) {
	c.channelsMu.RLock()
	defer c.channelsMu.RUnlock()

	channel, exists := c.channels[channelID]
	if !exists {
		return false, nil
	}

	// 检查当前连接器是否是发送方或接收方
	if channel.SenderID == c.connectorID || channel.ReceiverID == c.connectorID {
		return true, nil
	}

	return false, nil
}

// isEvidenceChannel 检查是否为存证频道
func (c *Connector) isEvidenceChannel(channelID string) bool {
	c.channelsMu.RLock()
	defer c.channelsMu.RUnlock()

	channel, exists := c.channels[channelID]
	if !exists {
		return false
	}

	return channel.IsEvidenceChannel
}

// isControlChannel 函数已移除，在统一频道模式下不再需要

// isEvidenceData 检查数据包是否为证据数据
func (c *Connector) isEvidenceData(payload []byte) bool {
	payloadStr := string(payload)

	// 检查是否包含证据数据的特征字段
	return strings.Contains(payloadStr, `"event_type"`) &&
		strings.Contains(payloadStr, `"tx_id"`) &&
		strings.Contains(payloadStr, `"signature"`)
}

// isControlMessage 检查是否为控制消息
func (c *Connector) isControlMessage(payload []byte) bool {
	payloadStr := string(payload)

	// 检查是否包含控制消息的特征字段
	return strings.Contains(payloadStr, `"message_type"`) &&
		strings.Contains(payloadStr, `"timestamp"`)
}

// RequestChannelAccess 申请订阅频道指定角色（频道外连接器使用）
func (c *Connector) RequestChannelAccess(channelID, role, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 验证角色
	if role != "sender" && role != "receiver" {
		return fmt.Errorf("invalid role: %s", role)
	}

	// 调用频道订阅申请RPC（专门为频道外连接器设计）
	resp, err := c.channelSvc.RequestChannelSubscription(ctx, &pb.RequestChannelSubscriptionRequest{
		SubscriberId: c.connectorID, // 订阅者ID（自己）
		ChannelId:    channelID,
		Role:         role,
		Reason:       reason,
	})

	if err != nil {
		return fmt.Errorf("failed to request channel subscription: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("subscription request failed: %s", resp.Message)
	}

	log.Printf("✓ Subscription request submitted for channel %s (request ID: %s)", channelID, resp.RequestId)
	return nil
}

// ApproveChannelSubscription 批准频道订阅申请
func (c *Connector) ApproveChannelSubscription(channelID, requestID string) (*pb.ApproveChannelSubscriptionResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := c.channelSvc.ApproveChannelSubscription(ctx, &pb.ApproveChannelSubscriptionRequest{
		ApproverId: c.connectorID,
		ChannelId:  channelID,
		RequestId:  requestID,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to approve channel subscription: %w", err)
	}

	return resp, nil
}

// RejectChannelSubscription 拒绝频道订阅申请
func (c *Connector) RejectChannelSubscription(channelID, requestID, reason string) (*pb.RejectChannelSubscriptionResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := c.channelSvc.RejectChannelSubscription(ctx, &pb.RejectChannelSubscriptionRequest{
		ApproverId: c.connectorID,
		ChannelId:  channelID,
		RequestId:  requestID,
		Reason:     reason,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to reject channel subscription: %w", err)
	}

	return resp, nil
}


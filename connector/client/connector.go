package client

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
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

	conn_db "github.com/trusted-space/kernel/connector/database"
	pb "github.com/trusted-space/kernel/proto/kernel/v1"
)

// ConnectorStatus 连接器状态
type ConnectorStatus string

const (
	ConnectorStatusActive   ConnectorStatus = "active"
	ConnectorStatusInactive ConnectorStatus = "inactive"
	ConnectorStatusClosed   ConnectorStatus = "closed"
)

// DataCatalogItem 数据目录项
type DataCatalogItem struct {
	Id      string // 数据项唯一标识
	Name    string // 数据项名称
	Type    string // 数据类型
	Exposed bool   // 是否向外部内核展示，默认为 true
}

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
}

// Connector 连接器客户端
type Connector struct {
	connectorID        string
	entityType         string
	publicKey          string
	serverAddr         string
	status             ConnectorStatus // 连接器状态，默认为active
	kernelID           string          // 可选：连接器所在内核ID（用于跨内核发现请求）
	exposed            *bool           // 是否向其他内核公开自己的信息，默认为true（使用指针以支持可选字段）
	dataCatalog        []string        // 数据类型/分类目录（旧版，字符串列表）
	dataCatalogItems   []*DataCatalogItem // 结构化数据目录（新版，支持 exposed 字段）
	dataCatalogFile    string          // 数据目录文件路径（用于保存变更）
	clientKeyPath      string          // connector 私钥路径（用于签名）

	conn         *grpc.ClientConn
	identitySvc  pb.IdentityServiceClient
	channelSvc   pb.ChannelServiceClient
	evidenceSvc  pb.EvidenceServiceClient
	kernelSvc    pb.KernelServiceClient

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

	// 业务数据哈希链管理器
	businessChainManager *conn_db.BusinessChainManager
	// 业务哈希链服务客户端（用于与内核通信）
	businessChainSvc pb.BusinessChainServiceClient
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
	// 可选：当前连接器所属的内核ID（用于跨内核发现）
	KernelID string
	// 是否向其他内核公开自己的信息，默认为true（使用指针以支持可选字段）
	Exposed *bool
	// 数据类型/分类目录（旧版，字符串列表）
	DataCatalog []string
	// 结构化数据目录（新版，支持 exposed 字段）
	DataCatalogItems []*DataCatalogItem
	// 数据目录文件路径（用于保存变更）
	DataCatalogFile string
	// 数据库配置
	DatabaseEnabled  bool
	DatabaseHost     string
	DatabasePort     int
	DatabaseUser     string
	DatabasePassword string
	DatabaseName     string
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
		kernelID:    config.KernelID,
		exposed:     config.Exposed, // 是否向其他内核公开自己的信息（传递指针）
		dataCatalog: config.DataCatalog,
		dataCatalogItems: config.DataCatalogItems, // 结构化数据目录
		dataCatalogFile: config.DataCatalogFile, // 数据目录文件路径
		clientKeyPath: config.ClientKeyPath, // 用于签名
		status:      ConnectorStatusActive, // 默认状态为active
		conn:        conn,
		identitySvc: pb.NewIdentityServiceClient(conn),
		channelSvc:  pb.NewChannelServiceClient(conn),
		evidenceSvc: pb.NewEvidenceServiceClient(conn),
		kernelSvc:   pb.NewKernelServiceClient(conn),
		businessChainSvc: pb.NewBusinessChainServiceClient(conn),
		ctx:         ctx,
		cancel:      cancel,
		channels:     make(map[string]*LocalChannelInfo),
		subscriptions: make(map[string]bool),
		processedNotifications: make(map[string]bool),
		skippedNotifications: make(map[string]int),
	}

	// 同步旧版 DataCatalog 从结构化数据目录（如果在配置中指定了数据目录文件）
	if connector.dataCatalogItems != nil && len(connector.dataCatalogItems) > 0 {
		connector.syncDataCatalog()
	}

	// 初始化业务数据哈希链管理器
	if config.DatabaseEnabled {
		dbConfig := conn_db.MySQLConfig{
			Host:     config.DatabaseHost,
			Port:     config.DatabasePort,
			User:     config.DatabaseUser,
			Password: config.DatabasePassword,
			Database: config.DatabaseName,
		}
		dbManager, err := conn_db.NewDBManager(dbConfig)
		if err != nil {
			cancel()
			conn.Close()
			return nil, fmt.Errorf("failed to initialize connector database: %w", err)
		}
		store := conn_db.NewMySQLStore(dbManager.GetDB())
		connector.businessChainManager = conn_db.NewBusinessChainManager(store)
		log.Println("[OK] Connector business chain manager initialized")
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
		Exposed:     c.exposed, // 是否向其他内核公开自己的信息（已经是*bool类型）
		DataCatalog: c.dataCatalog, // 旧版数据目录（字符串列表）
		DataCatalogItems: c.convertDataCatalogItems(), // 新版结构化数据目录
	})
	if err != nil {
		return fmt.Errorf("handshake failed: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("handshake rejected: %s", resp.Message)
	}

	c.sessionToken = resp.SessionToken
	log.Printf("[OK] Connected to kernel (version: %s)", resp.KernelVersion)
	log.Printf("[OK] Session token: %s", c.sessionToken)

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
				log.Printf("[WARN] Heartbeat failed: %v", err)
			} else if !resp.Acknowledged {
				log.Printf("[WARN] Heartbeat not acknowledged")
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

	c.addOrUpdateLocalChannel(&LocalChannelInfo{
		ChannelID:    notification.ChannelId,
		DataTopic:    notification.DataTopic,
		CreatorID:    notification.CreatorId,
		Participants: participants,
		CreatedAt:   notification.CreatedAt,
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
		// 如果启用了业务哈希链，计算 data_hash 并签名
		var dataHash, connectorSig string
		if c.HasBusinessChainManager() {
			dataHash, connectorSig, err = c.connectorRecordBusinessData(channelID, chunk)
			if err != nil {
				log.Printf("[WARN] Failed to record business data, continuing without hash chain: %v", err)
			}
		}

		packet := &pb.DataPacket{
			ChannelId:      channelID,
			SequenceNumber: int64(i + 1),
			Payload:        chunk,
			Timestamp:      time.Now().Unix(),
			SenderId:       c.connectorID,
			TargetIds:      []string{}, // 空列表表示广播给所有订阅者
		}

		// 业务哈希链信息放入 DataPacket
		if dataHash != "" && connectorSig != "" {
			packet.DataHash = dataHash
			packet.Signature = connectorSig // connector 的 RSA 签名
		} else {
			// 兼容：无业务哈希链时，使用简单的 hash 作为 signature
			hash := sha256.Sum256(chunk)
			packet.Signature = hex.EncodeToString(hash[:])
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

		log.Printf("[OK] Packet %d sent successfully", i+1)
	}

	// 关闭发送流
	if err := stream.CloseSend(); err != nil {
		return fmt.Errorf("failed to close send: %w", err)
	}

	log.Printf("[OK] All data sent successfully")
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

	// 如果启用了业务哈希链，计算 data_hash 并签名
	var dataHash, connectorSig string
	var err error
	if rs.connector.HasBusinessChainManager() {
		dataHash, connectorSig, err = rs.connector.connectorRecordBusinessData(rs.channelID, data)
		if err != nil {
			log.Printf("[WARN] Failed to record business data, continuing without hash chain: %v", err)
		}
	}

	packet := &pb.DataPacket{
		ChannelId:      rs.channelID,
		SequenceNumber: rs.sequence,
		Payload:        data,
		Timestamp:      time.Now().Unix(),
		SenderId:       rs.connector.connectorID,
		TargetIds:      targetIDs, // 指定目标接收者，空列表表示广播
	}

	// 业务哈希链信息放入 DataPacket
	if dataHash != "" && connectorSig != "" {
		packet.DataHash = dataHash
		packet.Signature = connectorSig // connector 的 RSA 签名
	} else {
		// 兼容：无业务哈希链时，使用简单的 hash 作为 signature
		hash := sha256.Sum256(data)
		packet.Signature = hex.EncodeToString(hash[:])
	}

	if err := rs.stream.Send(packet); err != nil {
		return fmt.Errorf("failed to send packet: %w", err)
	}

	// 接收确认（异步，不阻塞）
	go func() {
		status, err := rs.stream.Recv()
		if err != nil {
			log.Printf("[WARN] 确认接收失败: %v", err)
			return
		}
		if !status.Success {
			log.Printf("[WARN] 数据包发送失败: %s", status.Message)
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

	log.Printf("[OK] Subscribed to channel: %s", channelID)
	log.Println("Waiting for data...")

	for {
		packet, err := stream.Recv()
		if err == io.EOF {
			log.Println("[OK] Stream closed")
			return nil
		}
		if err != nil {
			return fmt.Errorf("receive error: %w", err)
		}

		log.Printf("[OK] Received packet #%d (%d bytes)", packet.SequenceNumber, len(packet.Payload))

		// connector-B 接收时：如果启用了业务哈希链，提取 data_hash 和上一跳签名，写入本地 business_data_chain
		// 注意：必须先写本地 business_data_chain，再发 ACK（确保内核 ACK 记录在数据记录之后）
		// 跳过流转结束包（seq=-1），该包只在内核间传递协调信号，不需要在业务链中记录
		if c.HasBusinessChainManager() && packet.DataHash != "" && !packet.IsAck && packet.SequenceNumber > 0 {
			prevSignatureHex := packet.Signature
			connectorSig, err := c.connectorReceiveBusinessData(channelID, packet.DataHash, prevSignatureHex)
			if err != nil {
				log.Printf("[WARN] Failed to record received business data: %v", err)
			}
			// 同步发送 ACK（不等同 goroutine），确保数据记录在 ACK 记录之前
			// 复用 connectorReceiveBusinessData 中已计算的签名，避免重复计算
			c.sendAckForData(channelID, packet.DataHash, packet.Signature, packet.FlowId, connectorSig)
		}

		// ACK 数据包：写入本地 business_data_chain
		// ACK 数据包：写入本地 business_data_chain
		// kernelSignature 来自 packet.Signature（kernel 对 connector 签名的签名）
		if c.HasBusinessChainManager() && packet.IsAck && packet.DataHash != "" {
			if err := c.recordAckFromKernel(channelID, packet.DataHash, packet.Signature); err != nil {
				log.Printf("[WARN] Failed to record ACK: %v", err)
			}
		}

		// 在统一频道模式下，根据payload内容判断消息类型
		if c.isControlMessage(packet.Payload) {
			// 控制消息：处理控制消息
			if err := c.processControlPacket(packet); err != nil {
				log.Printf("[WARN] Failed to process control packet: %v", err)
			}
		} else {
			// 业务数据：调用处理函数
			if handler != nil {
				if err := handler(packet); err != nil {
					log.Printf("[WARN] Handler error: %v", err)
				}
			}
		}
	}
}

// receiveDataSilent 发送方静默订阅频道数据（仅用于接收 ACK，不打印任何日志）
func (c *Connector) receiveDataSilent(channelID string) error {
	stream, err := c.channelSvc.SubscribeData(c.ctx, &pb.SubscribeRequest{
		ConnectorId: c.connectorID,
		ChannelId:   channelID,
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe: %w", err)
	}

	for {
		packet, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("receive error: %w", err)
		}

		// 收到 ACK 数据包：写入本地 business_data_chain
		// kernelSignature 来自 packet.Signature（kernel 对 connector 签名的签名）
		if c.HasBusinessChainManager() && packet.IsAck && packet.DataHash != "" {
			if err := c.recordAckFromKernel(channelID, packet.DataHash, packet.Signature); err != nil {
				log.Printf("[WARN] Failed to record ACK: %v", err)
			}
		}
	}
}

// processControlPacket 处理接收到的控制消息数据包
func (c *Connector) processControlPacket(packet *pb.DataPacket) error {
	// 控制消息由connector/cmd/main.go中的handleControlMessage处理
	// 这里只需要记录日志，不需要特殊处理
	log.Printf("[INFO] [控制频道: %s, 序列号: %d] 收到控制消息 (%d bytes)",
		packet.ChannelId, packet.SequenceNumber, len(packet.Payload))

	// 控制消息的具体处理逻辑在UI层（main.go）实现
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

	log.Printf("[OK] Channel closed: %s", channelID)
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

	log.Printf("[OK] Evidence committed: %s", resp.EvidenceTxId)
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

	log.Printf("[OK] Found %d evidence records", resp.TotalCount)
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
			log.Printf("[WARN] 同步状态到内核失败: %v", err)
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
				log.Printf("[WARN] 等待通知失败: %v，5秒后重试...", err)
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
					// 支持 kernel:connector 格式
					if idx := strings.LastIndex(senderID, ":"); idx != -1 {
						if senderID[idx+1:] == c.connectorID {
							isSender = true
							break
						}
					}
				}

				// 检查是否是接收方
				for _, receiverID := range notification.ReceiverIds {
					if receiverID == c.connectorID {
						isReceiver = true
						break
					}
					// 支持 kernel:connector 格式
					if idx := strings.LastIndex(receiverID, ":"); idx != -1 {
						if receiverID[idx+1:] == c.connectorID {
							isReceiver = true
							break
						}
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
						log.Printf("[INFO] 频道提议已创建: %s (您是创建者，已自动接受)", notification.ChannelId)
						log.Printf("[INFO] 等待其他参与方接受提议...")
						} else {
							// 收到提议通知，需要接受提议
							// 格式化参与者列表显示
							senderInfo := fmt.Sprintf("%v", notification.SenderIds)
							receiverInfo := fmt.Sprintf("%v", notification.ReceiverIds)

						log.Printf("[INFO] 收到频道提议: %s (创建者: %s, 发送方: %s, 接收方: %s)",
							notification.ChannelId, notification.CreatorId, senderInfo, receiverInfo)
						log.Printf("[INFO] 频道提议需要确认。作为发送方，您需要接受此提议")
						if notification.ProposalId != "" {
							log.Printf("[INFO] 请使用 'accept %s %s' 接受频道提议", notification.ChannelId, notification.ProposalId)
						}
						log.Printf("[INFO] 所有参与方接受后，频道将被激活，您可以开始发送数据")
						}
				} else if notification.NegotiationStatus == pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED {
					// 频道已正式创建
					// 格式化参与者列表显示
					senderInfo := fmt.Sprintf("%v", notification.SenderIds)
					receiverInfo := fmt.Sprintf("%v", notification.ReceiverIds)

					log.Printf("[INFO] 频道已正式创建: %s (创建者: %s, 发送方: %s, 接收方: %s)",
						notification.ChannelId, notification.CreatorId, senderInfo, receiverInfo)
					log.Printf("[OK] 您可以开始发送数据: 'sendto %s <data>'", notification.ChannelId)

					// 作为发送方订阅频道，用于接收接收方的 ACK
					go func(chID string) {
						_ = c.receiveDataSilent(chID)
					}(notification.ChannelId)
					} else if notification.NegotiationStatus == pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_REJECTED {
						// 频道提议已被拒绝
						log.Printf("[ERROR] 频道提议已被拒绝: %s (创建者: %s)",
							notification.ChannelId, notification.CreatorId)
						log.Printf("[INFO] 频道协商已终止，无法使用此频道")
					}
					continue
				}

				// 如果当前连接器不是接收方，跳过自动订阅
				if !isReceiver {
					log.Printf("[INFO] 收到频道创建通知，但当前连接器既不是发送方也不是接收方，跳过")
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
						log.Printf("[INFO] 等待其他参与方接受提议...")
					} else {
						// 收到提议通知，需要接受提议
						// 格式化参与者列表显示
						senderInfo := fmt.Sprintf("%v", notification.SenderIds)
						receiverInfo := fmt.Sprintf("%v", notification.ReceiverIds)

						log.Printf("[INFO] 收到频道提议: %s (创建者: %s, 发送方: %s, 接收方: %s)",
							notification.ChannelId, notification.CreatorId, senderInfo, receiverInfo)
						log.Printf("[INFO] 频道提议需要确认。作为接收方，您需要接受此提议")
						if notification.ProposalId != "" {
							log.Printf("[INFO] 请使用 'accept %s %s' 接受频道提议", notification.ChannelId, notification.ProposalId)
						}
						log.Printf("[INFO] 所有参与方接受后，频道将被激活并自动开始接收数据")
					}
				} else if notification.NegotiationStatus == pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED {

					// 频道已正式创建，可以自动订阅
					// 格式化参与者列表显示
					senderInfo := fmt.Sprintf("%v", notification.SenderIds)
					receiverInfo := fmt.Sprintf("%v", notification.ReceiverIds)

					// 统一频道模式
					log.Printf("[INFO] 统一频道已正式创建: %s (创建者: %s, 发送方: %s, 接收方: %s)",
							notification.ChannelId, notification.CreatorId, senderInfo, receiverInfo)

					// 自动订阅逻辑
					go func(chID string) {
						// 创建文件接收器（用于自动接收文件）
						outputDir := "./received"
						if err := os.MkdirAll(outputDir, 0755); err != nil {
							log.Printf("[WARN] 创建接收目录失败: %v", err)
						}
						fileReceiver := NewFileReceiver(outputDir, func(filePath, fileHash string) {
							log.Printf("[OK] 文件自动接收并保存成功: %s (哈希: %s)", filePath, fileHash)
							fmt.Printf("\n[OK] 文件自动接收并保存成功:\n")
							fmt.Printf("  文件路径: %s\n", filePath)
							fmt.Printf("  文件哈希: %s\n", fileHash)
						})

						// 自动订阅并接收数据
						err := c.ReceiveData(chID, func(packet *pb.DataPacket) error {
							// 检查是否是文件传输数据包
							if IsFileTransferPacket(packet.Payload) {
								// 处理文件传输数据包
								if err := fileReceiver.HandleFilePacket(packet); err != nil {
									log.Printf("[WARN] 处理文件数据包失败: %v", err)
								}
							} else {
								// 普通数据包，显示文本
							log.Printf("[DATA] [频道: %s, 序列号: %d] 数据: %s", chID, packet.SequenceNumber, string(packet.Payload))
							fmt.Printf("[DATA] [序列号: %d] 数据: %s\n", packet.SequenceNumber, string(packet.Payload))
							}
							return nil
						})
						if err != nil {
							log.Printf("[ERROR] 自动订阅失败: %v", err)
						} else {
							log.Printf("[OK] 频道 %s 已关闭", chID)
						}
					}(notification.ChannelId)
					} else if notification.NegotiationStatus == pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_REJECTED {
						// 频道提议已被拒绝
						log.Printf("[ERROR] 频道提议已被拒绝: %s (创建者: %s)",
							notification.ChannelId, notification.CreatorId)
						log.Printf("[INFO] 频道协商已终止，无法使用此频道")
					} else {
						// inactive 或其他状态：不显示详细通知，只记录简单日志
						log.Printf("[INFO] 收到频道创建通知 (频道: %s, 创建者: %s)，但连接器状态为 %s，不会自动订阅。请手动使用 'receive %s' 订阅",
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

// DiscoverCrossKernelConnectors 发现跨内核的连接器
func (c *Connector) DiscoverCrossKernelConnectors(targetKernelID, filterType string) ([]*pb.ConnectorInfo, []*pb.KernelInfo, error) {
	resp, err := c.identitySvc.DiscoverCrossKernelConnectors(c.ctx, &pb.CrossKernelDiscoverRequest{
		RequesterId:      c.connectorID,
		RequesterKernelId: c.kernelID, // 当前连接器所在内核ID（可为空）
		FilterType:       filterType,
		TargetKernelId:   targetKernelID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to discover cross-kernel connectors: %w", err)
	}

	return resp.Connectors, resp.Kernels, nil
}

// CreateCrossKernelChannel 发起跨内核频道创建（由连接器调用，代理到本地内核的 KernelService.CreateCrossKernelChannel）
func (c *Connector) CreateCrossKernelChannel(senderIDs []string, receiverIDs []string, dataTopic string, encrypted bool, reason string, evidenceConfig *pb.EvidenceConfig) (string, error) {
	// 构建请求中的 CrossKernelParticipant 列表
	buildParticipants := func(ids []string) []*pb.CrossKernelParticipant {
		out := make([]*pb.CrossKernelParticipant, 0, len(ids))
		for _, id := range ids {
			if strings.Contains(id, ":") {
				parts := strings.SplitN(id, ":", 2)
				out = append(out, &pb.CrossKernelParticipant{
					KernelId:    parts[0],
					ConnectorId: parts[1],
				})
			} else {
				out = append(out, &pb.CrossKernelParticipant{
					KernelId:    "",
					ConnectorId: id,
				})
			}
		}
		return out
	}

	req := &pb.CreateCrossKernelChannelRequest{
		CreatorKernelId:    c.kernelID,
		CreatorConnectorId: c.connectorID,
		SenderIds:          buildParticipants(senderIDs),
		ReceiverIds:        buildParticipants(receiverIDs),
		DataTopic:          dataTopic,
		Encrypted:          encrypted,
		Reason:             reason,
		EvidenceConfig:     evidenceConfig,
	}

	resp, err := c.kernelSvc.CreateCrossKernelChannel(c.ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to create cross-kernel channel: %w", err)
	}
	if !resp.Success {
		return "", fmt.Errorf("create cross-kernel channel failed: %s", resp.Message)
	}
	return resp.ChannelId, nil
}

// GetConnectorInfo 获取指定连接器的详细信息（支持跨内核查询）
// connectorID 支持格式: "connector-A" (本内核) 或 "kernel-1:connector-A" (跨内核)
func (c *Connector) GetConnectorInfo(connectorID string) (*pb.ConnectorInfo, error) {
	// 解析 connectorID 是否包含 kernel: 前缀
	targetKernelID := ""
	actualConnectorID := connectorID

	if strings.Contains(connectorID, ":") {
		parts := strings.SplitN(connectorID, ":", 2)
		if len(parts) == 2 {
			targetKernelID = parts[0]
			actualConnectorID = parts[1]
		}
	}

	resp, err := c.identitySvc.GetConnectorInfo(c.ctx, &pb.GetConnectorInfoRequest{
		RequesterId:     c.connectorID,
		ConnectorId:   actualConnectorID,
		TargetKernelId: targetKernelID,
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
				log.Printf("[WARN] 通知接收错误: %v", err)
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

	log.Printf("[OK] Permission change request submitted: %s", resp.RequestId)
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

	// 只有在服务器返回成功时才打印成功日志
	if resp.Success {
		log.Printf("[OK] Permission change approved: %s", requestID)
	} else {
		log.Printf("[FAILED] Permission change approval failed: %s", resp.Message)
	}
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

	log.Printf("[OK] Permission change rejected: %s", requestID)
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

	log.Printf("[OK] Retrieved %d permission requests", len(resp.Requests))
	return resp, nil
}

// ChannelConfigFile 频道配置文件结构（与kernel中的定义保持一致）
type ChannelConfigFile struct {
	ChannelName     string          `json:"channel_name"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	CreatorID       string          `json:"creator_id"`
	ApproverID      string          `json:"approver_id"`
	SenderIDs       []string        `json:"sender_ids"`
	ReceiverIDs     []string        `json:"receiver_ids"`
	DataTopic       string          `json:"data_topic"`
	Encrypted       bool            `json:"encrypted"`
	EvidenceConfig  *EvidenceConfig `json:"evidence_config"`
	CreatedAt       *time.Time      `json:"created_at,omitempty"`
	UpdatedAt       *time.Time      `json:"updated_at,omitempty"`
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
	if config.ChannelName == "" {
		return nil, fmt.Errorf("channel_name is required in config file")
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

	// 如果配置了外部存证，自动将外部存证连接器添加到接收方列表
	if config.EvidenceConfig != nil && config.EvidenceConfig.Mode == "external" && config.EvidenceConfig.ConnectorID != "" {
		externalConnectorID := config.EvidenceConfig.ConnectorID
		// 检查是否已经在接收方列表中
		alreadyInReceivers := false
		for _, receiverID := range config.ReceiverIDs {
			if receiverID == externalConnectorID {
				alreadyInReceivers = true
				break
			}
		}
		if !alreadyInReceivers {
			config.ReceiverIDs = append(config.ReceiverIDs, externalConnectorID)
			log.Printf("[OK] 外部存证连接器 %s 已自动添加到接收方列表", externalConnectorID)
		}
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

	log.Printf("[OK] Channel created from config file: %s (%s)", config.ChannelName, configFilePath)
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

	log.Printf("[OK] Channel proposed: %s (proposal: %s)", resp.ChannelId, resp.ProposalId)

	// 创建者自动接受提议
	if resp.ProposalId != "" {
		if err := c.AcceptChannelProposal(resp.ChannelId, resp.ProposalId); err != nil {
			log.Printf("[WARN] 创建者自动接受提议失败: %v", err)
			// 不返回错误，继续执行，因为提议已经创建成功
		} else {
			log.Printf("[OK] 创建者已自动接受频道提议: %s", resp.ChannelId)
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

	log.Printf("[OK] Channel proposal accepted: %s (evidence hash: %s)", channelID, proposalID)

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

	log.Printf("[OK] Channel proposal rejected: %s", channelID)

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
		log.Printf("  [OK] 已发送块 %d/%d", chunkIndex+1, totalChunks)
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

	log.Printf("[OK] 文件发送完成: %s", fileName)
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

	log.Printf("  [OK] 已接收块 %d/%d (文件: %s)", chunk.ChunkIndex+1, totalChunks, fileName)

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
		// 检查是否能找到对应的文件（通过文件名模式匹配）
		// 由于FileEnd中没有文件名，我们需要查找可能的已完成文件
		files, err := os.ReadDir(fr.outputPath)
		if err == nil {
			for _, file := range files {
				if !file.IsDir() {
					// 检查文件是否刚刚创建（在最近几秒内）
					info, err := file.Info()
					if err == nil && time.Since(info.ModTime()) < 30*time.Second {
						fr.transfersMu.Unlock()
						log.Printf("[OK] 检测到最近完成的文件传输，忽略重复的FileEnd包 (传输ID: %s, 文件: %s)", end.TransferID, file.Name())
						return nil
					}
				}
			}
		}
		fr.transfersMu.Unlock()
		return fmt.Errorf("file header not received for transfer ID: %s", end.TransferID)
	}

	receivedChunks := transfer.receivedChunks
	totalChunks := transfer.totalChunks
	transferID := end.TransferID

	if receivedChunks < totalChunks {
		log.Printf("[WARN] 文件传输结束，但只接收到 %d/%d 块 (传输ID: %s)", receivedChunks, totalChunks, transferID)
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

	log.Printf("[OK] 文件接收完成: %s (大小: %d 字节, 哈希验证通过)", outputPath, fileSize)

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

// isControlChannel 函数已移除，在统一频道模式下不再需要

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

	log.Printf("[OK] Subscription request submitted for channel %s (request ID: %s)", channelID, resp.RequestId)
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

// =============================================================================
// 业务数据哈希链相关方法
// =============================================================================

// HasBusinessChainManager 检查是否启用了业务哈希链
func (c *Connector) HasBusinessChainManager() bool {
	return c.businessChainManager != nil
}

// GetBusinessChainManager 获取业务哈希链管理器
func (c *Connector) GetBusinessChainManager() *conn_db.BusinessChainManager {
	return c.businessChainManager
}

// GetLastDataHashFromKernel 从内核获取指定频道的最新data_hash
func (c *Connector) GetLastDataHashFromKernel(channelID string) (string, error) {
	if c.businessChainSvc == nil {
		return "", fmt.Errorf("business chain service not available")
	}

	resp, err := c.businessChainSvc.GetLastDataHash(c.ctx, &pb.GetLastDataHashRequest{
		ConnectorId: c.connectorID,
		ChannelId:   channelID,
	})
	if err != nil {
		return "", fmt.Errorf("failed to get last data hash from kernel: %w", err)
	}
	if !resp.Success {
		return "", fmt.Errorf("failed to get last data hash: %s", resp.Message)
	}

	return resp.LastDataHash, nil
}

// SyncDataHashFromKernel 从内核同步最新data_hash到本地
// 用于连接器重启后或本地无记录时同步状态
func (c *Connector) SyncDataHashFromKernel(channelID string) error {
	if c.businessChainManager == nil {
		return fmt.Errorf("business chain manager not initialized")
	}

	lastHash, err := c.GetLastDataHashFromKernel(channelID)
	if err != nil {
		return fmt.Errorf("failed to get last data hash from kernel: %w", err)
	}

	// 如果内核也没有记录，则不需要同步
	if lastHash == "" {
		log.Printf("[OK] No data hash to sync from kernel for channel %s", channelID)
		return nil
	}

	// 检查是否已有同步记录
	existingRecord, err := c.businessChainManager.GetLastDataHash(channelID)
	if err != nil {
		return fmt.Errorf("failed to check existing record: %w", err)
	}

	// 如果本地已有记录且哈希相同，不需要重复同步
	if existingRecord == lastHash {
		log.Printf("[OK] Local hash already synced with kernel for channel %s", channelID)
		return nil
	}

	// 记录同步信息（由后续数据包更新 prev_hash）
	log.Printf("[OK] Synced data hash from kernel: channel=%s, last_hash=%s", channelID, lastHash)
	return nil
}

// QueryBusinessChain 查询业务哈希链
func (c *Connector) QueryBusinessChain(channelID string, limit int32) ([]*conn_db.BusinessChainRecord, error) {
	if c.businessChainManager == nil {
		return nil, fmt.Errorf("business chain manager not initialized")
	}

	records, err := c.businessChainManager.GetChainRecords(channelID)
	if err != nil {
		return nil, fmt.Errorf("failed to query chain: %w", err)
	}

	return records, nil
}

// GetBusinessChainRecordsFromKernel 从内核查询业务哈希链
func (c *Connector) GetBusinessChainRecordsFromKernel(channelID string, limit int32) ([]*pb.ChainRecord, error) {
	if c.businessChainSvc == nil {
		return nil, fmt.Errorf("business chain service not available")
	}

	resp, err := c.businessChainSvc.QueryChain(c.ctx, &pb.QueryChainRequest{
		ConnectorId: c.connectorID,
		ChannelId:   channelID,
		Limit:       limit,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query chain from kernel: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("failed to query chain: %s", resp.Message)
	}

	return resp.Records, nil
}

// =============================================================================
// 文件传输协议常量
// =============================================================================

// =============================================================================
// 签名相关辅助方法
// =============================================================================

// loadPrivateKey 加载 RSA 私钥
func (c *Connector) loadPrivateKey(keyPath string) (*rsa.PrivateKey, error) {
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key file: %w", err)
	}

	block, _ := pem.Decode(keyData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	rsaKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		key, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("failed to parse private key: PKCS1=%v, PKCS8=%v", err, err2)
		}
		var ok bool
		rsaKey, ok = key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("private key is not RSA")
		}
	}

	return rsaKey, nil
}

// signData 使用 RSA 私钥对数据进行签名（需传入哈希值）
func (c *Connector) signData(data []byte, privateKey *rsa.PrivateKey) ([]byte, error) {
	hash := sha256.Sum256(data)
	return rsa.SignPSS(rand.Reader, privateKey, crypto.SHA256, hash[:], nil)
}

// connectorSignDataHash connector 对 dataHash 进行签名
// 输入: dataHash（原始字节）+ prevSignature（上一跳签名，可为空）
// 签名输入: dataHash + prevSignature（原始字节拼接）
func (c *Connector) connectorSignDataHash(dataHash []byte, prevSignature []byte) ([]byte, error) {
	privateKey, err := c.loadPrivateKey(c.clientKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load connector private key: %w", err)
	}

	// 签名输入: dataHash + prevSignature（原始字节拼接，不哈希）
	signInput := append(dataHash, prevSignature...)
	return c.signData(signInput, privateKey)
}

// connectorSignDataHashHex connector 对 dataHash 进行签名，返回 hex 编码
func (c *Connector) connectorSignDataHashHex(dataHash string, prevSignatureHex string) (string, error) {
	privateKey, err := c.loadPrivateKey(c.clientKeyPath)
	if err != nil {
		return "", fmt.Errorf("failed to load connector private key: %w", err)
	}

	// 签名输入: dataHash + prevSignature（原始字节拼接）
	signInput := append([]byte(dataHash), []byte(prevSignatureHex)...)
	hash := sha256.Sum256(signInput)
	signature, err := rsa.SignPSS(rand.Reader, privateKey, crypto.SHA256, hash[:], nil)
	if err != nil {
		return "", fmt.Errorf("failed to sign: %w", err)
	}
	return hex.EncodeToString(signature), nil
}

// connectorRecordBusinessData connector 记录业务数据哈希链（connector-A 发送时调用）
// 返回: dataHash（hex），signature（hex）
func (c *Connector) connectorRecordBusinessData(channelID string, data []byte) (dataHash, signature string, err error) {
	if c.businessChainManager == nil {
		return "", "", fmt.Errorf("business chain manager not initialized")
	}

	// 1. 获取频道上一条记录（prevHash 排除 ACK，prevSignature 取最后一条的签名）
	prevNonAckRecord, err := c.businessChainManager.GetLastNonAckRecord(channelID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get last non-ack record: %w", err)
	}
	lastRecord, err := c.businessChainManager.GetLastRecord(channelID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get last record: %w", err)
	}

	prevHash := ""
	prevSignature := ""
	if prevNonAckRecord != nil {
		prevHash = prevNonAckRecord.DataHash
	}
	if lastRecord != nil {
		prevSignature = lastRecord.Signature
	}

	// 2. 计算 data_hash = SHA256(data + prevHash)
	var hashInput []byte
	if prevHash != "" {
		hashInput = append(data, []byte(prevHash)...)
	} else {
		hashInput = data
	}
	hash := sha256.Sum256(hashInput)
	dataHash = hex.EncodeToString(hash[:])

	// 3. 签名: RSA_Sign(dataHash + prevHash)
	var signInput []byte
	if prevHash != "" {
		signInput = append([]byte(dataHash), []byte(prevHash)...)
	} else {
		signInput = []byte(dataHash)
	}
	hash2 := sha256.Sum256(signInput)
	privateKey, err := c.loadPrivateKey(c.clientKeyPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to load connector private key: %w", err)
	}
	sigBytes, err := rsa.SignPSS(rand.Reader, privateKey, crypto.SHA256, hash2[:], nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to sign: %w", err)
	}
	signature = hex.EncodeToString(sigBytes)

	// 4. 写入本地 business_data_chain（带重试，防止 MySQL 连接临时断开）
	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		err = c.businessChainManager.RecordChainData(channelID, dataHash, prevHash, prevSignature, signature)
		if err == nil {
			break
		}
		// 检查是否是连接错误，连接错误才重试
		if attempt < maxRetries-1 && (strings.Contains(err.Error(), "connection") ||
			strings.Contains(err.Error(), "closed") ||
			strings.Contains(err.Error(), "bad connection") ||
			strings.Contains(err.Error(), "invalid connection") ||
			strings.Contains(err.Error(), "EOF")) {
			log.Printf("[INFO] connectorRecordBusinessData: MySQL connection error (attempt %d/%d), retrying: %v", attempt+1, maxRetries, err)
			time.Sleep(500 * time.Millisecond)
			continue
		}
	}
	if err != nil {
		return "", "", fmt.Errorf("failed to record chain data after %d attempts: %w", maxRetries, err)
	}

	return dataHash, signature, nil
}

// connectorReceiveBusinessData connector 接收业务数据时写入本地 business_data_chain
// 从 packet.Signature 提取上一跳（kernel）的签名，记录 data_hash 和 prev_signature
// 同时计算并存储 connector 自己的签名（用于后续 ACK 链）
// 返回 connectorSig，供 sendAckForData 复用（避免重复计算签名）
func (c *Connector) connectorReceiveBusinessData(channelID, dataHash, prevSignatureHex string) (connectorSig string, err error) {
	if c.businessChainManager == nil {
		return "", fmt.Errorf("business chain manager not initialized")
	}

	// 1. 获取频道上一条记录的 dataHash（用于 prev_hash）
	prevHash, err := c.businessChainManager.GetLastDataHash(channelID)
	if err != nil {
		return "", fmt.Errorf("failed to get last data hash: %w", err)
	}

	// 2. 计算 connector 自己的签名（用于后续 ACK 链的 prev_signature）
	// 签名输入: dataHash + prevSignatureHex
	if prevSignatureHex != "" {
		// 有上一跳签名时，使用 dataHash + prevSignatureHex 计算签名
		connectorSig, err = c.connectorSignDataHashHex(dataHash, prevSignatureHex)
		if err != nil {
			log.Printf("[WARN] Failed to sign received data: %v", err)
		}
	} else {
		// 没有上一跳签名时，仅使用 dataHash 计算签名
		connectorSig, err = c.connectorSignDataHashHex(dataHash, "")
		if err != nil {
			log.Printf("[WARN] Failed to sign received data: %v", err)
		}
	}

	// 3. 写入本地 business_data_chain（带重试，防止 MySQL 连接临时断开）
	// prevSignature 是上一跳 kernel 的签名（来自 packet.Signature）
	// signature 是 connector 自己对 dataHash 的签名
	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		err = c.businessChainManager.RecordChainData(channelID, dataHash, prevHash, prevSignatureHex, connectorSig)
		if err == nil {
			break
		}
		if attempt < maxRetries-1 && (strings.Contains(err.Error(), "connection") ||
			strings.Contains(err.Error(), "closed") ||
			strings.Contains(err.Error(), "bad connection") ||
			strings.Contains(err.Error(), "invalid connection") ||
			strings.Contains(err.Error(), "EOF")) {
			log.Printf("[INFO] connectorReceiveBusinessData: MySQL connection error (attempt %d/%d), retrying: %v", attempt+1, maxRetries, err)
			time.Sleep(500 * time.Millisecond)
			continue
		}
	}
	if err != nil {
		return "", fmt.Errorf("failed to record chain data after %d attempts: %w", maxRetries, err)
	}

	return connectorSig, nil
}

// sendAckForData 接收方（connector）对收到的数据包生成并发送 ACK 给内核
// dataHash: 被确认的数据包的 data_hash
// kernelSignature: 上一跳（kernel）的签名，作为 ACK 记录的 prev_signature
// flowID: 业务流程ID
// connectorSig: connector 在 connectorReceiveBusinessData 中已计算好的签名（直接复用，避免重复计算）
func (c *Connector) sendAckForData(channelID, dataHash, kernelSignature, flowID, connectorSig string) {
	if c.channelSvc == nil {
		log.Printf("[WARN] Channel service not available, cannot send ACK")
		return
	}

	// 复用 connectorReceiveBusinessData 中已计算的签名（方案 A）
	// ACK 记录的 prev_signature = kernelSignature（保持不变）
	// ACK 记录的 signature = connectorSig（复用，不复算）
	// ACK 包的 Signature = connectorSig（复用，不复算）

	// 记录 ACK 到本地 business_data_chain（带重试，防止 MySQL 连接临时断开）
	if c.businessChainManager != nil {
		ackErr := c.businessChainManager.HandleAck(channelID, kernelSignature, connectorSig)
		if ackErr != nil {
			// 重试
			for attempt := 1; attempt <= 3; attempt++ {
				log.Printf("[INFO] sendAckForData HandleAck: MySQL connection error (attempt %d/3), retrying: %v", attempt, ackErr)
				time.Sleep(500 * time.Millisecond)
				ackErr = c.businessChainManager.HandleAck(channelID, kernelSignature, connectorSig)
				if ackErr == nil {
					break
				}
			}
			if ackErr != nil {
				log.Printf("[WARN] Failed to record ACK locally after retries: %v", ackErr)
			}
		}
	}

	// 发送 ACK 给内核
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := c.channelSvc.SendAck(ctx, &pb.AckPacket{
		ChannelId: channelID,
		DataHash:  dataHash,
		SenderId:  c.connectorID,
		Signature: connectorSig,
		FlowId:    flowID,
	})
	if err != nil {
		log.Printf("[WARN] Failed to send ACK for data_hash=%s: %v", dataHash, err)
		return
	}
	if !resp.Success {
		log.Printf("[WARN] ACK rejected by kernel: %s", resp.Message)
		return
	}
	log.Printf("[OK] ACK sent")
}

// recordAckFromKernel 接收方（connector）从内核收到转发的 ACK，写入本地链
// dataHash: 被确认的 data_hash
// kernelSignature: kernel 对 connectorSignature 的签名（来自 packet.Signature）
func (c *Connector) recordAckFromKernel(channelID, dataHash, kernelSignature string) error {
	if c.businessChainManager == nil {
		return fmt.Errorf("business chain manager not initialized")
	}

	// 计算 connector 自己的签名：RSA_Sign(dataHash + kernelSignature)
	// prev_signature = kernel 的签名，signature = connector 自己的签名
	connectorSig, err := c.connectorSignDataHashHex(dataHash, kernelSignature)
	if err != nil {
		log.Printf("[WARN] Failed to sign ACK: %v", err)
		return err
	}

	// prev_signature = kernel 的签名，signature = connector 的签名
	return c.businessChainManager.HandleAck(channelID, kernelSignature, connectorSig)
}

// ReconnectWithCatalog 重新连接到内核并上报数据目录
// 用于在更新数据目录后重新上报
func (c *Connector) ReconnectWithCatalog() error {
	// 首先保存数据目录到本地文件（如果有配置数据目录文件）
	if c.dataCatalogFile != "" {
		if err := c.saveDataCatalogToFile(); err != nil {
			log.Printf("[WARN] Failed to save data catalog to file: %v", err)
		}
	}

	// 发送握手请求，重新注册数据目录
	resp, err := c.identitySvc.Handshake(c.ctx, &pb.HandshakeRequest{
		ConnectorId: c.connectorID,
		EntityType:  c.entityType,
		PublicKey:   c.publicKey,
		Timestamp:   time.Now().Unix(),
		Exposed:     c.exposed,
		DataCatalog: c.dataCatalog, // 旧版字符串列表（已在 SetDataCatalogItems 时同步更新）
		DataCatalogItems: c.convertDataCatalogItems(), // 新版结构化数据目录（包含所有项，含 exposed 状态）
	})
	if err != nil {
		return fmt.Errorf("handshake failed: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("handshake rejected: %s", resp.Message)
	}

	c.sessionToken = resp.SessionToken
	log.Printf("[OK] Data catalog updated and synced to kernel (version: %s)", resp.KernelVersion)
	return nil
}

// syncDataCatalog 同步旧版 DataCatalog（字符串列表）从新版 DataCatalogItems
func (c *Connector) syncDataCatalog() {
	if c.dataCatalogItems == nil {
		return
	}
	c.dataCatalog = make([]string, 0, len(c.dataCatalogItems))
	for _, item := range c.dataCatalogItems {
		c.dataCatalog = append(c.dataCatalog, item.Name)
	}
}

// saveDataCatalogToFile 保存数据目录到本地 JSON 文件
func (c *Connector) saveDataCatalogToFile() error {
	if c.dataCatalogFile == "" {
		return nil
	}

	if c.dataCatalogItems == nil {
		return nil
	}

	// 构造保存的数据结构
	catalogFile := struct {
		DataItems []DataCatalogItem `json:"data_items"`
	}{
		DataItems: make([]DataCatalogItem, 0, len(c.dataCatalogItems)),
	}

	for _, item := range c.dataCatalogItems {
		catalogFile.DataItems = append(catalogFile.DataItems, DataCatalogItem{
			Id:      item.Id,
			Name:    item.Name,
			Type:    item.Type,
			Exposed: item.Exposed,
		})
	}

	// 序列化并保存到文件
	data, err := json.MarshalIndent(catalogFile, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal data catalog: %w", err)
	}

	if err := os.WriteFile(c.dataCatalogFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write data catalog file: %w", err)
	}

	log.Printf("[OK] Data catalog saved to %s", c.dataCatalogFile)
	return nil
}

// UpdateDataCatalogItem 更新指定数据项的暴露状态
func (c *Connector) UpdateDataCatalogItem(dataID string, exposed bool) error {
	if c.dataCatalogItems == nil {
		return fmt.Errorf("data catalog items not initialized")
	}

	found := false
	for _, item := range c.dataCatalogItems {
		if item.Id == dataID {
			item.Exposed = exposed
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("data item not found: %s", dataID)
	}

	return nil
}

// convertDataCatalogItems 将连接器内部的 DataCatalogItem 格式转换为 protobuf 格式
func (c *Connector) convertDataCatalogItems() []*pb.DataCatalogItem {
	if len(c.dataCatalogItems) == 0 {
		return nil
	}

	result := make([]*pb.DataCatalogItem, 0, len(c.dataCatalogItems))
	for _, item := range c.dataCatalogItems {
		result = append(result, &pb.DataCatalogItem{
			Id:      item.Id,
			Name:    item.Name,
			Type:    item.Type,
			Exposed: item.Exposed,
		})
	}
	return result
}

// GetDataCatalogItems 获取连接器的结构化数据目录
func (c *Connector) GetDataCatalogItems() []*DataCatalogItem {
	return c.dataCatalogItems
}

// SetDataCatalogItems 设置连接器的结构化数据目录
func (c *Connector) SetDataCatalogItems(items []*DataCatalogItem) {
	c.dataCatalogItems = items
	// 同步更新旧版 DataCatalog（字符串列表）- 用于兼容旧版显示
	c.syncDataCatalog()
}

// LoadDataCatalogFromFile 从 JSON 文件加载结构化数据目录
func (c *Connector) LoadDataCatalogFromFile(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read data catalog file: %w", err)
	}

	var catalogFile struct {
		DataItems []struct {
			Id      string `json:"id"`
			Name    string `json:"name"`
			Type    string `json:"type"`
			Exposed bool   `json:"exposed"`
		} `json:"data_items"`
	}

	if err := json.Unmarshal(data, &catalogFile); err != nil {
		return fmt.Errorf("failed to parse data catalog file: %w", err)
	}

	c.dataCatalogItems = make([]*DataCatalogItem, 0, len(catalogFile.DataItems))
	for _, item := range catalogFile.DataItems {
		c.dataCatalogItems = append(c.dataCatalogItems, &DataCatalogItem{
			Id:      item.Id,
			Name:    item.Name,
			Type:    item.Type,
			Exposed: item.Exposed,
		})
	}

	// 同步更新旧版 DataCatalog（字符串列表）
	c.syncDataCatalog()

	return nil
}
package operator_peer

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// PeerClient TCP客户端，用于主动连接到其他运维方
type PeerClient struct {
	localKernelID string
	localAddr     string
	localPort     int
	peerPort      int

	// 连接配置
	connectTimeout time.Duration
	heartbeatInterval time.Duration

	// 持久连接
	conn     net.Conn
	connMu   sync.RWMutex
	reader   *PacketReaderJSON
	writer   *PacketWriterJSON

	// 状态
	peerKernelID string
	peerAddr     string
	connected    int32
	status       string // "disconnected", "connecting", "handshaking", "connected"

	// 回调
	onConnected    func(kernelID string)
	onDisconnected func(kernelID string, reason error)
	onMessage      func(msgType PacketType, payload []byte)
	onSyncAck      func(remoteConnectors []*ConnectorOnlineInfo)

	// 停止信号
	stopped     chan struct{}
	stopOnce    sync.Once
}

// PeerClientConfig 客户端配置
type PeerClientConfig struct {
	LocalKernelID     string
	LocalAddr         string
	LocalPort         int
	PeerAddr          string        // 完整地址，包含端口
	ConnectTimeout    time.Duration
	HeartbeatInterval time.Duration
}

// DefaultPeerClientConfig 默认配置
func DefaultPeerClientConfig() *PeerClientConfig {
	return &PeerClientConfig{
		ConnectTimeout:    10 * time.Second,
		HeartbeatInterval: 15 * time.Second,
	}
}

// NewPeerClient 创建P2P客户端
func NewPeerClient(config *PeerClientConfig) *PeerClient {
	if config == nil {
		config = DefaultPeerClientConfig()
	}

	// 从 PeerAddr 解析端口号
	var peerPort int
	if config.PeerAddr != "" {
		if _, portStr, err := net.SplitHostPort(config.PeerAddr); err == nil {
			if p, err := strconv.Atoi(portStr); err == nil {
				peerPort = p
			}
		}
	}

	return &PeerClient{
		localKernelID:    config.LocalKernelID,
		localAddr:        config.LocalAddr,
		localPort:        config.LocalPort,
		peerPort:        peerPort,
		connectTimeout:   config.ConnectTimeout,
		heartbeatInterval: config.HeartbeatInterval,
		stopped:          make(chan struct{}),
		status:           "disconnected",
	}
}

// SetOnConnected 设置连接成功回调
func (c *PeerClient) SetOnConnected(f func(kernelID string)) {
	c.onConnected = f
}

// SetOnDisconnected 设置断开连接回调
func (c *PeerClient) SetOnDisconnected(f func(kernelID string, reason error)) {
	c.onDisconnected = f
}

// SetOnMessage 设置消息回调
func (c *PeerClient) SetOnMessage(f func(msgType PacketType, payload []byte)) {
	c.onMessage = f
}

// SetOnSyncAck 设置同步确认回调
func (c *PeerClient) SetOnSyncAck(f func(remoteConnectors []*ConnectorOnlineInfo)) {
	c.onSyncAck = f
}

// newPeerClientFromConn 从已建立的连接创建 PeerClient（用于被动接受的连接）
// 与 Connect() 不同，这里不需要建立 TCP 连接、握手
// 注意：不启动读循环和心跳——因为 TCP 连接上的读循环已由 P2PServer 的
// handleConnection 提供，两套读循环竞争同一连接会导致数据丢失
func newPeerClientFromConn(config *PeerClientConfig, conn net.Conn, peerKernelID string) *PeerClient {
	c := &PeerClient{
		localKernelID:     config.LocalKernelID,
		localAddr:         config.LocalAddr,
		localPort:         config.LocalPort,
		peerPort:          0,
		connectTimeout:    config.ConnectTimeout,
		heartbeatInterval: config.HeartbeatInterval,
		conn:              conn,
		peerAddr:          conn.RemoteAddr().String(),
		peerKernelID:      peerKernelID,
		status:            "connected",
		stopped:           make(chan struct{}),
	}

	c.reader = NewPacketReaderJSON(conn)
	c.writer = NewPacketWriterJSON(conn)

	atomic.StoreInt32(&c.connected, 1)

	log.Printf("[P2P] Passive peer client ready for %s (from %s)", peerKernelID, conn.RemoteAddr().String())
	return c
}

// Connect 连接到远程运维方
func (c *PeerClient) Connect(targetAddr string) error {
	// 检查是否已连接
	if atomic.LoadInt32(&c.connected) == 1 {
		return fmt.Errorf("already connected to %s", c.peerAddr)
	}

	c.status = "connecting"
	// targetAddr 已包含完整地址和端口
	peerAddr := targetAddr

	log.Printf("[P2P] Connecting to %s...", peerAddr)

	// 建立TCP连接
	conn, err := net.DialTimeout("tcp", peerAddr, c.connectTimeout)
	if err != nil {
		c.status = "disconnected"
		return fmt.Errorf("failed to connect to %s: %w", peerAddr, err)
	}

	c.connMu.Lock()
	c.conn = conn
	c.peerAddr = peerAddr
	c.reader = NewPacketReaderJSON(conn)
	c.writer = NewPacketWriterJSON(conn)
	c.connMu.Unlock()

	// 发送握手请求
	if err := c.sendHandshake(); err != nil {
		c.Close()
		return fmt.Errorf("handshake failed: %w", err)
	}

	// 启动读取循环和心跳
	go c.readLoop()
	go c.heartbeatLoop()

	atomic.StoreInt32(&c.connected, 1)
	c.status = "connected"

	log.Printf("[P2P] Connected to %s (peer kernel: %s)", peerAddr, c.peerKernelID)

	if c.onConnected != nil {
		c.onConnected(c.peerKernelID)
	}

	return nil
}

// sendHandshake 发送握手请求
func (c *PeerClient) sendHandshake() error {
	c.status = "handshaking"

	payload := HandshakePayload{
		KernelID:   c.localKernelID,
		KernelAddr: c.localAddr,
		KernelPort: int32(c.localPort),
		PeerPort:   int32(c.peerPort),
		Version:    ProtocolVersion,
		Timestamp:  time.Now().Unix(),
	}

	payloadData, err := JSONMarshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal handshake: %w", err)
	}

	packet := NewPacket(PacketTypeHandshake, fmt.Sprintf("%d", time.Now().UnixNano()), payloadData)
	if err := c.writer.WritePacket(packet); err != nil {
		return fmt.Errorf("failed to send handshake: %w", err)
	}

	// 等待握手响应
	c.conn.SetReadDeadline(time.Now().Add(c.connectTimeout))
	respPacket, err := c.reader.ReadPacket()
	if err != nil {
		return fmt.Errorf("failed to read handshake response: %w", err)
	}

	if respPacket.Type != PacketTypeHandshakeAck {
		return fmt.Errorf("unexpected packet type: %d", respPacket.Type)
	}

	var ackPayload HandshakeAckPayload
	if err := JSONUnmarshal(respPacket.Payload, &ackPayload); err != nil {
		return fmt.Errorf("failed to parse handshake ack: %w", err)
	}

	if !ackPayload.Success {
		return fmt.Errorf("handshake rejected: %s", ackPayload.Message)
	}

	c.peerKernelID = ackPayload.KernelID
	log.Printf("[P2P] Handshake successful with %s", c.peerKernelID)

	return nil
}

// readLoop 读取循环
func (c *PeerClient) readLoop() {
	for {
		select {
		case <-c.stopped:
			return
		default:
			// 设置读超时
			c.connMu.RLock()
			conn := c.conn
			c.connMu.RUnlock()

			if conn == nil {
				return
			}

			conn.SetReadDeadline(time.Now().Add(60 * time.Second))

			packet, err := c.reader.ReadPacket()
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				log.Printf("[P2P] Read error: %v", err)
				c.handleDisconnect(err)
				return
			}

			// 处理消息
			c.handlePacket(packet)
		}
	}
}

// handlePacket 处理数据包
func (c *PeerClient) handlePacket(packet *Packet) {
	switch packet.Type {
	case PacketTypeHandshakeAck:
		// 握手已完成，忽略
	case PacketTypeSyncConnectors:
		if c.onMessage != nil {
			c.onMessage(packet.Type, packet.Payload)
		}
	case PacketTypeRelayMessage:
		if c.onMessage != nil {
			c.onMessage(packet.Type, packet.Payload)
		}
	case PacketTypeHeartbeatAck:
		// 心跳响应（静默，不打印日志避免刷屏）
	case PacketTypeRelayMessageAck:
		// 消息转发确认
		var ack RelayMessageAckPayload
		if err := JSONUnmarshal(packet.Payload, &ack); err == nil {
			log.Printf("[P2P] Message %s delivered: %v", ack.MessageID, ack.Success)
		}
	case PacketTypeSyncAck:
		// 同步确认
		var ack SyncConnectorsAckPayload
		if err := JSONUnmarshal(packet.Payload, &ack); err == nil {
			// 同步日志降为 DEBUG，避免刷屏
			log.Printf("[P2P-DEBUG] Sync completed: %v, received %d remote connectors",
				ack.Success, len(ack.RemoteConnectors))
			if c.onSyncAck != nil {
				c.onSyncAck(ack.RemoteConnectors)
			}
		}
	default:
		log.Printf("[P2P] Unknown packet type: %d", packet.Type)
	}
}

// heartbeatLoop 心跳循环
func (c *PeerClient) heartbeatLoop() {
	ticker := time.NewTicker(c.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopped:
			return
		case <-ticker.C:
			if err := c.sendHeartbeat(); err != nil {
				log.Printf("[P2P] Heartbeat failed: %v", err)
				c.handleDisconnect(err)
				return
			}
		}
	}
}

// sendHeartbeat 发送心跳
func (c *PeerClient) sendHeartbeat() error {
	payload := HeartbeatPayload{
		KernelID: c.localKernelID,
		Timestamp: time.Now().Unix(),
		ConnectedConnectors: 0, // TODO: 从TempChatManager获取
		ActivePeers: 0,
	}

	payloadData, _ := JSONMarshal(payload)
	packet := NewPacket(PacketTypeHeartbeat, fmt.Sprintf("%d", time.Now().UnixNano()), payloadData)

	return c.writer.WritePacket(packet)
}

// handleDisconnect 处理断开连接
func (c *PeerClient) handleDisconnect(reason error) {
	if atomic.CompareAndSwapInt32(&c.connected, 1, 0) {
		c.status = "disconnected"

		c.connMu.Lock()
		if c.conn != nil {
			c.conn.Close()
			c.conn = nil
		}
		c.connMu.Unlock()

		log.Printf("[P2P] Disconnected from %s: %v", c.peerKernelID, reason)

		if c.onDisconnected != nil {
			c.onDisconnected(c.peerKernelID, reason)
		}
	}
}

// Close 关闭连接
func (c *PeerClient) Close() {
	c.stopOnce.Do(func() {
		close(c.stopped)

		c.connMu.Lock()
		if c.conn != nil {
			// 发送断开通知
			disconnectPayload, _ := JSONMarshal(map[string]interface{}{
				"kernel_id": c.localKernelID,
				"reason":    "normal_close",
			})
			disconnectPacket := NewPacket(PacketTypeDisconnect,
				fmt.Sprintf("%d", time.Now().UnixNano()), disconnectPayload)
			c.writer.WritePacket(disconnectPacket)

			c.conn.Close()
			c.conn = nil
		}
		c.connMu.Unlock()

		atomic.StoreInt32(&c.connected, 0)
		c.status = "disconnected"
	})
}

// IsConnected 检查是否已连接
func (c *PeerClient) IsConnected() bool {
	return atomic.LoadInt32(&c.connected) == 1
}

// GetPeerKernelID 获取对端内核ID
func (c *PeerClient) GetPeerKernelID() string {
	return c.peerKernelID
}

// GetPeerAddr 获取对端地址
func (c *PeerClient) GetPeerAddr() string {
	return c.peerAddr
}

// GetStatus 获取连接状态
func (c *PeerClient) GetStatus() string {
	return c.status
}

// SendSyncConnectors 发送连接器同步请求
func (c *PeerClient) SendSyncConnectors(connectors []*ConnectorOnlineInfo) error {
	if !c.IsConnected() {
		return fmt.Errorf("not connected")
	}

	payload := SyncConnectorsPayload{
		SourceKernelID: c.localKernelID,
		Connectors:     connectors,
		Timestamp:      time.Now().Unix(),
	}

	payloadData, err := JSONMarshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal sync payload: %w", err)
	}

	packet := NewPacket(PacketTypeSyncConnectors, fmt.Sprintf("%d", time.Now().UnixNano()), payloadData)
	return c.writer.WritePacket(packet)
}

// SendRelayMessage 发送消息转发请求
func (c *PeerClient) SendRelayMessage(msg *RelayMessagePayload) error {
	if !c.IsConnected() {
		return fmt.Errorf("not connected")
	}

	payloadData, err := JSONMarshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal relay payload: %w", err)
	}

	packet := NewPacket(PacketTypeRelayMessage, fmt.Sprintf("%d", time.Now().UnixNano()), payloadData)
	return c.writer.WritePacket(packet)
}

// PeerClientWithReconnect 带自动重连的客户端
type PeerClientWithReconnect struct {
	config        *PeerClientConfig
	targetAddr    string
	client        *PeerClient
	reconnectMu   sync.Mutex
	reconnecting  bool
	maxRetries    int
	retryInterval time.Duration
	stopped       chan struct{}
}

// NewPeerClientWithReconnect 创建带自动重连的客户端
func NewPeerClientWithReconnect(config *PeerClientConfig, targetAddr string) *PeerClientWithReconnect {
	return &PeerClientWithReconnect{
		config:        config,
		targetAddr:    targetAddr,
		maxRetries:    5,
		retryInterval: 5 * time.Second,
		stopped:       make(chan struct{}),
	}
}

// Connect 连接并启动重连循环
func (c *PeerClientWithReconnect) Connect() error {
	c.client = NewPeerClient(c.config)

	c.client.SetOnConnected(func(kernelID string) {
		log.Printf("[P2P] Peer connected: %s", kernelID)
	})

	c.client.SetOnDisconnected(func(kernelID string, reason error) {
		log.Printf("[P2P] Peer disconnected: %s, reason: %v", kernelID, reason)
		// 触发自动重连
		go c.reconnectLoop()
	})

	return c.client.Connect(c.targetAddr)
}

// reconnectLoop 重连循环
func (c *PeerClientWithReconnect) reconnectLoop() {
	c.reconnectMu.Lock()
	if c.reconnecting {
		c.reconnectMu.Unlock()
		return
	}
	c.reconnecting = true
	c.reconnectMu.Unlock()

	defer func() {
		c.reconnectMu.Lock()
		c.reconnecting = false
		c.reconnectMu.Unlock()
	}()

	for i := 0; i < c.maxRetries; i++ {
		select {
		case <-c.stopped:
			return
		case <-time.After(c.retryInterval):
			log.Printf("[P2P] Reconnect attempt %d/%d to %s...", i+1, c.maxRetries, c.targetAddr)

			client := NewPeerClient(c.config)
			client.SetOnConnected(func(kernelID string) {
				c.client = client
				log.Printf("[P2P] Reconnected to %s", kernelID)
			})

			if err := client.Connect(c.targetAddr); err != nil {
				log.Printf("[P2P] Reconnect failed: %v", err)
				continue
			}
			return
		}
	}

	log.Printf("[P2P] Max retries reached for %s", c.targetAddr)
}

// Close 关闭连接
func (c *PeerClientWithReconnect) Close() {
	close(c.stopped)
	if c.client != nil {
		c.client.Close()
	}
}

// IsConnected 检查是否已连接
func (c *PeerClientWithReconnect) IsConnected() bool {
	return c.client != nil && c.client.IsConnected()
}

// GetClient 获取底层客户端
func (c *PeerClientWithReconnect) GetClient() *PeerClient {
	return c.client
}

// SendSyncConnectors 发送连接器同步
func (c *PeerClientWithReconnect) SendSyncConnectors(connectors []*ConnectorOnlineInfo) error {
	if c.client == nil {
		return fmt.Errorf("client not initialized")
	}
	return c.client.SendSyncConnectors(connectors)
}

// SendRelayMessage 发送消息转发
func (c *PeerClientWithReconnect) SendRelayMessage(msg *RelayMessagePayload) error {
	if c.client == nil {
		return fmt.Errorf("client not initialized")
	}
	return c.client.SendRelayMessage(msg)
}

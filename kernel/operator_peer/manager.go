package operator_peer

import (
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	pb "github.com/trusted-space/kernel/proto/kernel/v1"
	tempchat "github.com/trusted-space/kernel/kernel/tempchat"
)

// P2PManagerConfig P2P管理器配置
type P2PManagerConfig struct {
	KernelID          string
	ListenAddr       string // P2P监听地址，如 "0.0.0.0:50054"
	LocalKernelAddr   string
	LocalKernelPort   int
	HeartbeatInterval int    // 心跳间隔（秒）
	SyncInterval      int    // 同步间隔（秒）
}

// DefaultP2PManagerConfig 默认配置
func DefaultP2PManagerConfig() *P2PManagerConfig {
	return &P2PManagerConfig{
		HeartbeatInterval: 15,
		SyncInterval:      30,
	}
}

// PeerInfo 对等方信息
type PeerInfo struct {
	KernelID      string
	Address       string
	Port          int
	Status        string // "connecting", "handshaking", "connected", "disconnected"
	ConnectedAt   time.Time
	LastHeartbeat int64
	Client        *PeerClient // 仅主动连接（主动方）时设置
	PassConn      *PeerConnection // 仅被动连接（被动方）时设置
}

// P2PManager P2P连接管理器
// 负责：
// 1. 启动TCP服务器接收运维方连接
// 2. 主动连接其他运维方
// 3. 管理对等方连接和消息路由
// 4. 与TempChatManager集成实现消息转发
type P2PManager struct {
	config    *P2PManagerConfig
	server    *P2PServer
	peers     map[string]*PeerInfo // key: kernelID
	peersMu   sync.RWMutex
	running   int32

	// 回调：消息投递到本地连接器
	onMessageDelivered func(senderKernelID, senderID, receiverID string, payload []byte, flowID string) error

	// 回调：消息转发到远程内核
	onRelay func(receiverKernelID, receiverID string, payload []byte) error

	// TempChatManager引用（用于获取本地连接器信息）
	tempChatManager TempChatManagerReference

	// 内核信息提供者（用于按需连接时获取目标内核地址）
	kernelInfoProvider KernelInfoProvider

	// 心跳和同步
	stopSync chan struct{}
}

// TempChatManagerReference TempChatManager的P2P相关接口
type TempChatManagerReference interface {
	GetLocalConnectors() []*pb.ConnectorOnlineInfo
	GetSessionByConnector(connectorID string) (*tempchat.ConnectorSession, error)
	DeliverMessage(receiverID string, msg *pb.TempMessage) error
	SetRemoteConnectors(kernelID string, connectors []*pb.ConnectorOnlineInfo)
}

// KernelInfoProvider 内核信息提供者接口（由 MultiKernelManager 实现）
// P2PManager 按需连接时通过此接口获取目标内核的地址信息
type KernelInfoProvider interface {
	// GetKernelAddress 根据内核ID获取内核地址信息
	// 返回值：address, p2pPort, found
	GetKernelAddress(kernelID string) (address string, p2pPort int, found bool)

	// GetKnownRemoteKernelIDs 获取所有已知远程内核的 ID 列表
	// 排除本内核自身
	GetKnownRemoteKernelIDs() []string
}

// NewP2PManager 创建P2P管理器
func NewP2PManager(config *P2PManagerConfig) (*P2PManager, error) {
	if config == nil {
		config = DefaultP2PManagerConfig()
	}

	// 设置默认监听地址
	if config.ListenAddr == "" {
		// 从LocalKernelPort推断P2P端口（约定为kernel_port+3）
		config.ListenAddr = fmt.Sprintf("0.0.0.0:%d", config.LocalKernelPort+3)
	}

	mgr := &P2PManager{
		config:  config,
		peers:   make(map[string]*PeerInfo),
		stopSync: make(chan struct{}),
	}

	// 创建P2P服务器
	mgr.server = NewP2PServer(config.ListenAddr)

	// 设置回调
	mgr.server.SetPeerConnectedHandler(func(kernelID string, conn *PeerConnection) {
		mgr.onPeerConnected(kernelID, conn.GetConn())
	})
	mgr.server.SetPeerDisconnectedHandler(mgr.onPeerDisconnected)
	mgr.server.SetHandshakeHandler(mgr.handleHandshake)
	mgr.server.SetSyncConnectorsHandler(mgr.handleSyncConnectors)
	mgr.server.SetRelayMessageHandler(mgr.handleRelayMessage)

	return mgr, nil
}

// SetTempChatManager 设置TempChatManager引用
func (m *P2PManager) SetTempChatManager(tcm TempChatManagerReference) {
	m.tempChatManager = tcm
}

// SetKernelInfoProvider 设置内核信息提供者（由 MultiKernelManager 实现）
// 用于按需连接时获取目标内核的地址和 P2P 端口
func (m *P2PManager) SetKernelInfoProvider(provider KernelInfoProvider) {
	m.kernelInfoProvider = provider
}

// SetOnMessageDelivered 设置消息投递回调
func (m *P2PManager) SetOnMessageDelivered(f func(senderKernelID, senderID, receiverID string, payload []byte, flowID string) error) {
	m.onMessageDelivered = f
}

// SetRelayHandler 设置消息转发回调
func (m *P2PManager) SetRelayHandler(f func(receiverKernelID, receiverID string, payload []byte) error) {
	m.onRelay = f
}

// Start 启动P2P管理器
func (m *P2PManager) Start() error {
	if !atomic.CompareAndSwapInt32(&m.running, 0, 1) {
		return fmt.Errorf("P2P manager already running")
	}

	// 启动TCP服务器
	if err := m.server.Start(); err != nil {
		return fmt.Errorf("failed to start P2P server: %w", err)
	}

	// 启动同步循环
	go m.syncLoop()

	log.Printf("[P2P] Manager started, listening on %s", m.config.ListenAddr)
	return nil
}

// Stop 停止P2P管理器
func (m *P2PManager) Stop() {
	if !atomic.CompareAndSwapInt32(&m.running, 1, 0) {
		return
	}

	close(m.stopSync)
	m.server.Close()

	m.peersMu.Lock()
	for _, peer := range m.peers {
		if peer.Client != nil {
			peer.Client.Close()
		}
	}
	m.peers = make(map[string]*PeerInfo)
	m.peersMu.Unlock()

	log.Printf("[P2P] Manager stopped")
}

	// ConnectToPeer 连接到指定运维方
func (m *P2PManager) ConnectToPeer(kernelID, address string, port int) error {
	// 先在锁外检查是否已连接（避免不必要的 I/O）
	m.peersMu.RLock()
	if peer, exists := m.peers[kernelID]; exists && peer.Client != nil && peer.Client.IsConnected() {
		m.peersMu.RUnlock()
		return fmt.Errorf("already connected to %s", kernelID)
	}
	m.peersMu.RUnlock()

	// 创建客户端（I/O 操作在锁外进行）
	config := &PeerClientConfig{
		LocalKernelID:      m.config.KernelID,
		LocalAddr:          m.config.LocalKernelAddr,
		LocalPort:          m.config.LocalKernelPort,
		PeerAddr:          address, // address 已包含端口
		ConnectTimeout:    5 * time.Second,
		HeartbeatInterval: time.Duration(m.config.HeartbeatInterval) * time.Second,
	}

	client := NewPeerClient(config)
	client.SetOnConnected(func(peerKernelID string) {
		m.peersMu.Lock()
		if peer, exists := m.peers[peerKernelID]; exists {
			peer.Status = "connected"
			peer.ConnectedAt = time.Now()
		}
		m.peersMu.Unlock()
		log.Printf("[P2P] Connected to peer %s", peerKernelID)

		// 连接成功后立即同步本地连接器给对等方
		m.syncConnectorsToPeer(peerKernelID)
	})

	client.SetOnDisconnected(func(peerKernelID string, reason error) {
		m.peersMu.Lock()
		if peer, exists := m.peers[peerKernelID]; exists {
			peer.Status = "disconnected"
		}
		m.peersMu.Unlock()
		log.Printf("[P2P] Disconnected from peer %s: %v", peerKernelID, reason)
	})

	// 设置消息回调
	client.SetOnMessage(func(msgType PacketType, payload []byte) {
		m.handleIncomingMessage(msgType, payload)
	})

	// 设置同步确认回调（ack 中带回的远程连接器需要缓存到 TempChatManager）
	// 但此时还不知道对方的 kernelID，需要从 peer 信息中获取
	client.SetOnSyncAck(func(remoteConnectors []*ConnectorOnlineInfo) {
		m.peersMu.RLock()
		peer := m.peers[kernelID]
		m.peersMu.RUnlock()
		if peer == nil {
			return
		}
		if m.tempChatManager != nil && len(remoteConnectors) > 0 {
			protoConnectors := make([]*pb.ConnectorOnlineInfo, len(remoteConnectors))
			for i, c := range remoteConnectors {
				protoConnectors[i] = &pb.ConnectorOnlineInfo{
					ConnectorId:    c.ConnectorId,
					KernelId:      c.KernelId,
					Address:       c.Address,
					Port:          c.Port,
					EntityType:    c.EntityType,
					SessionId:     c.SessionId,
					LastHeartbeat: c.LastHeartbeat,
					RegisteredAt:  c.RegisteredAt,
					ExposeToOthers: c.ExposeToOthers,
				}
			}
			m.tempChatManager.SetRemoteConnectors(kernelID, protoConnectors)
		}
	})

	// 在锁内注册 peer
	m.peersMu.Lock()
	peer := &PeerInfo{
		KernelID: kernelID,
		Address:  address,
		Port:    port,
		Status:  "connecting",
		Client:  client,
	}
	m.peers[kernelID] = peer
	m.peersMu.Unlock()

	// TCP 连接（I/O 在锁外）
	targetAddr := fmt.Sprintf("%s:%d", address, port)
	if err := client.Connect(targetAddr); err != nil {
		// 连接失败时清理 peer 记录
		m.peersMu.Lock()
		delete(m.peers, kernelID)
		m.peersMu.Unlock()
		return fmt.Errorf("failed to connect to %s: %w", kernelID, err)
	}

	return nil
}

// DisconnectFromPeer 断开与指定运维方的连接
func (m *P2PManager) DisconnectFromPeer(kernelID string) error {
	m.peersMu.Lock()
	defer m.peersMu.Unlock()

	peer, exists := m.peers[kernelID]
	if !exists {
		return fmt.Errorf("peer %s not found", kernelID)
	}

	if peer.Client != nil {
		peer.Client.Close()
	}

	delete(m.peers, kernelID)
	return nil
}

	// EnsurePeerConnected 按需建立 P2P 连接
// 如果已连接则直接返回成功；如果未连接则尝试建立连接
// caller 参数用于日志记录调用者身份
func (m *P2PManager) EnsurePeerConnected(kernelID, caller string) error {
	// 快速路径：已连接则直接返回
	if m.IsPeerConnected(kernelID) {
		return nil
	}

	// 需要内核信息提供者才能按需连接
	if m.kernelInfoProvider == nil {
		return fmt.Errorf("kernel info provider not set, cannot connect to %s", kernelID)
	}

	// 获取目标内核的地址信息
	address, p2pPort, found := m.kernelInfoProvider.GetKernelAddress(kernelID)
	if !found {
		return fmt.Errorf("kernel %s not found in kernel info provider", kernelID)
	}

	log.Printf("[P2P] On-demand connecting to peer %s (caller: %s) at %s:%d", kernelID, caller, address, p2pPort)

	// 调用已有的 ConnectToPeer 建立连接（内部会加锁）
	if err := m.ConnectToPeer(kernelID, address, p2pPort); err != nil {
		return fmt.Errorf("failed to connect to peer %s: %w", kernelID, err)
	}

	log.Printf("[P2P] On-demand connected to peer %s (caller: %s)", kernelID, caller)
	return nil
}

// onPeerConnected 对等方连接回调（被动连接）
func (m *P2PManager) onPeerConnected(kernelID string, conn net.Conn) {
	m.peersMu.Lock()
	defer m.peersMu.Unlock()

	peer, exists := m.peers[kernelID]
	if !exists {
		peer = &PeerInfo{
			KernelID:    kernelID,
			Address:     conn.RemoteAddr().String(),
			Port:        0,
			Status:      "connected",
			ConnectedAt: time.Now(),
		}
		m.peers[kernelID] = peer
	} else {
		peer.Status = "connected"
		peer.ConnectedAt = time.Now()
	}

	// 将被动接受的连接包装为 PeerClient 并存入 PeerInfo.Client
	// 这样 RelayMessage 就能统一使用 peer.Client 发送消息
	config := &PeerClientConfig{
		LocalKernelID:     m.config.KernelID,
		LocalAddr:         m.config.LocalKernelAddr,
		LocalPort:         m.config.LocalKernelPort,
		ConnectTimeout:    5 * time.Second,
		HeartbeatInterval: 30 * time.Second,
	}
	peer.Client = newPeerClientFromConn(config, conn, kernelID)

	// 设置同步确认回调
	peer.Client.SetOnSyncAck(func(remoteConnectors []*ConnectorOnlineInfo) {
		if m.tempChatManager != nil && len(remoteConnectors) > 0 {
			protoConnectors := make([]*pb.ConnectorOnlineInfo, len(remoteConnectors))
			for i, c := range remoteConnectors {
				protoConnectors[i] = &pb.ConnectorOnlineInfo{
					ConnectorId:    c.ConnectorId,
					KernelId:      c.KernelId,
					Address:       c.Address,
					Port:          c.Port,
					EntityType:    c.EntityType,
					SessionId:     c.SessionId,
					LastHeartbeat: c.LastHeartbeat,
					RegisteredAt:  c.RegisteredAt,
					ExposeToOthers: c.ExposeToOthers,
				}
			}
			m.tempChatManager.SetRemoteConnectors(kernelID, protoConnectors)
		}
	})

	log.Printf("[P2P] Peer connected (passive): %s from %s", kernelID, conn.RemoteAddr().String())

	// 在独立的 goroutine 中同步连接器，避免持有锁时阻塞握手机制
	go m.syncConnectorsToPeer(kernelID)
}

// onPeerDisconnected 对等方断开回调
func (m *P2PManager) onPeerDisconnected(kernelID string) {
	m.peersMu.Lock()
	defer m.peersMu.Unlock()

	if peer, exists := m.peers[kernelID]; exists {
		peer.Status = "disconnected"
	}

	log.Printf("[P2P] Peer disconnected: %s", kernelID)
}

// handleHandshake 处理握手请求
func (m *P2PManager) handleHandshake(conn net.Conn, payload *HandshakePayload) (*HandshakeAckPayload, error) {
	log.Printf("[P2P] Handling handshake from %s", payload.KernelID)

	// 更新对等方信息
	m.peersMu.Lock()
	peer, exists := m.peers[payload.KernelID]
	if !exists {
		peer = &PeerInfo{
			KernelID: payload.KernelID,
			Address:  payload.KernelAddr,
			Port:     int(payload.PeerPort),
			Status:   "connected",
		}
		m.peers[payload.KernelID] = peer
	} else {
		peer.Status = "connected"
		peer.Address = payload.KernelAddr
		peer.Port = int(payload.PeerPort)
	}
	m.peersMu.Unlock()

	return &HandshakeAckPayload{
		Success:   true,
		Message:   "OK",
		KernelID:  m.config.KernelID,
		Version:   ProtocolVersion,
		Timestamp: time.Now().Unix(),
	}, nil
}

// handleSyncConnectors 处理连接器同步请求
func (m *P2PManager) handleSyncConnectors(payload *SyncConnectorsPayload) (*SyncConnectorsAckPayload, error) {
	log.Printf("[P2P] Handling sync from %s: %d connectors",
		payload.SourceKernelID, len(payload.Connectors))

	// 缓存发送方（远程）的连接器
	if len(payload.Connectors) > 0 {
		protoConnectors := make([]*pb.ConnectorOnlineInfo, len(payload.Connectors))
		for i, c := range payload.Connectors {
			protoConnectors[i] = &pb.ConnectorOnlineInfo{
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
		if m.tempChatManager != nil {
			m.tempChatManager.SetRemoteConnectors(payload.SourceKernelID, protoConnectors)
		}
	}

	// 将本方的连接器带回给请求方（对方缓存）
	var localConnectors []*ConnectorOnlineInfo
	if m.tempChatManager != nil {
		for _, conn := range m.tempChatManager.GetLocalConnectors() {
			localConnectors = append(localConnectors, &ConnectorOnlineInfo{
				ConnectorId:    conn.ConnectorId,
				KernelId:       conn.KernelId,
				Address:        conn.Address,
				Port:           conn.Port,
				EntityType:     conn.EntityType,
				SessionId:      conn.SessionId,
				LastHeartbeat:  conn.LastHeartbeat,
				RegisteredAt:   conn.RegisteredAt,
				ExposeToOthers: conn.ExposeToOthers,
			})
		}
	}

	log.Printf("[P2P] handleSyncConnectors: 返回 %d 个本地连接器给 %s", len(localConnectors), payload.SourceKernelID)
	return &SyncConnectorsAckPayload{
		Success:          true,
		Message:          "OK",
		RemoteConnectors: localConnectors,
		Timestamp:        time.Now().Unix(),
	}, nil
}

// handleRelayMessage 处理消息转发请求
func (m *P2PManager) handleRelayMessage(payload *RelayMessagePayload) (*RelayMessageAckPayload, error) {
	// 解析真正的连接器ID（格式为 "kernelID:connectorID"，或直接是 "connectorID"）
	connectorID := payload.ReceiverID
	if idx := strings.LastIndex(payload.ReceiverID, ":"); idx >= 0 {
		connectorID = payload.ReceiverID[idx+1:]
	}

	// 检查接收方是否在本地
	if m.tempChatManager != nil {
		session, err := m.tempChatManager.GetSessionByConnector(connectorID)
		if err == nil && session != nil {
			// 投递到本地连接器
			tempMsg := &pb.TempMessage{
				MessageId:      payload.MessageID,
				SenderId:       payload.SenderID,
				ReceiverId:     connectorID,
				Payload:       payload.Payload,
				Timestamp:      payload.Timestamp,
				FlowId:        payload.FlowID,
				SourceKernelId: payload.SenderKernelID,
			}

			// 投递消息
			if err := m.tempChatManager.DeliverMessage(connectorID, tempMsg); err != nil {
				return &RelayMessageAckPayload{
					Success:     false,
					Message:     err.Error(),
					MessageID:   payload.MessageID,
					DeliveredAt: 0,
				}, nil
			}

			log.Printf("[P2P] Message delivered to connector %s (from %s)", connectorID, payload.SenderID)
			return &RelayMessageAckPayload{
				Success:     true,
				Message:     "OK",
				MessageID:   payload.MessageID,
				DeliveredAt: time.Now().Unix(),
			}, nil
		}
	}

	// 接收方不在本地
	return &RelayMessageAckPayload{
		Success:     false,
		Message:     fmt.Sprintf("receiver %s (connector: %s) not found locally", payload.ReceiverID, connectorID),
		MessageID:   payload.MessageID,
		DeliveredAt: 0,
	}, nil
}

// handleIncomingMessage 处理来自对等方的消息
func (m *P2PManager) handleIncomingMessage(msgType PacketType, payload []byte) {
	switch msgType {
	case PacketTypeSyncConnectors:
		var syncPayload SyncConnectorsPayload
		if err := JSONUnmarshal(payload, &syncPayload); err != nil {
			log.Printf("[P2P] Failed to parse sync payload: %v", err)
			return
		}
	case PacketTypeRelayMessage:
		var relayPayload RelayMessagePayload
		if err := JSONUnmarshal(payload, &relayPayload); err != nil {
			log.Printf("[P2P] Failed to parse relay payload: %v", err)
			return
		}
		log.Printf("[P2P] Received relay: %s -> %s",
			relayPayload.SenderID, relayPayload.ReceiverID)

		// 处理消息转发（投递到本地连接器）
		if _, err := m.handleRelayMessage(&relayPayload); err != nil {
			log.Printf("[P2P] Failed to handle relay: %v", err)
		}
	}
}

// syncLoop 定时同步连接器列表
func (m *P2PManager) syncLoop() {
	ticker := time.NewTicker(time.Duration(m.config.SyncInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopSync:
			return
		case <-ticker.C:
			m.syncConnectors()
		}
	}
}

// syncConnectors 同步连接器列表到所有对等方
// 对于已连接的对等方直接同步数据，对于已知但未连接的对等方按需建立连接后再同步
func (m *P2PManager) syncConnectors() {
	// 获取本地连接器列表
	var connectors []*ConnectorOnlineInfo
	if m.tempChatManager != nil {
		for _, conn := range m.tempChatManager.GetLocalConnectors() {
			connectors = append(connectors, &ConnectorOnlineInfo{
				ConnectorId:    conn.ConnectorId,
				KernelId:       conn.KernelId,
				Address:        conn.Address,
				Port:           conn.Port,
				EntityType:     conn.EntityType,
				SessionId:      conn.SessionId,
				LastHeartbeat:  conn.LastHeartbeat,
				RegisteredAt:   conn.RegisteredAt,
				ExposeToOthers: conn.ExposeToOthers,
			})
		}
	}

	// 获取已知远程内核列表（来自 kernelInfoProvider）
	var knownRemoteKernels []string
	if m.kernelInfoProvider != nil {
		knownRemoteKernels = m.kernelInfoProvider.GetKnownRemoteKernelIDs()
	}

	// 对每个已知但未连接的内核按需建立 P2P 连接
	for _, kernelID := range knownRemoteKernels {
		// 检查是否已连接
		if m.IsPeerConnected(kernelID) {
			continue
		}
		// 按需建立连接
		if err := m.EnsurePeerConnected(kernelID, "TempChat-sync"); err != nil {
			log.Printf("[P2P] Failed to connect for sync to %s: %v", kernelID, err)
		}
	}

	// 同步数据到所有已连接的对等方
	m.peersMu.RLock()
	for _, peer := range m.peers {
		if peer.Client != nil && peer.Client.IsConnected() {
			if err := peer.Client.SendSyncConnectors(connectors); err != nil {
				log.Printf("[P2P] Failed to sync to %s: %v", peer.KernelID, err)
			}
		}
	}
	m.peersMu.RUnlock()
}

// syncConnectorsToPeer 同步本地连接器列表到指定的对等方
func (m *P2PManager) syncConnectorsToPeer(targetKernelID string) {
	// 获取本地连接器列表
	var connectors []*ConnectorOnlineInfo
	if m.tempChatManager != nil {
		for _, conn := range m.tempChatManager.GetLocalConnectors() {
			connectors = append(connectors, &ConnectorOnlineInfo{
				ConnectorId:    conn.ConnectorId,
				KernelId:       conn.KernelId,
				Address:        conn.Address,
				Port:           conn.Port,
				EntityType:     conn.EntityType,
				SessionId:      conn.SessionId,
				LastHeartbeat:  conn.LastHeartbeat,
				RegisteredAt:   conn.RegisteredAt,
				ExposeToOthers: conn.ExposeToOthers,
			})
		}
	}

	// 在锁内获取 peer 引用并发送，避免并发问题
	m.peersMu.RLock()
	peer, exists := m.peers[targetKernelID]
	if !exists {
		log.Printf("[P2P] Peer %s not found in peers map", targetKernelID)
		m.peersMu.RUnlock()
		return
	}
	if peer.Client == nil {
		log.Printf("[P2P] Peer %s has no Client", targetKernelID)
		m.peersMu.RUnlock()
		return
	}
	if !peer.Client.IsConnected() {
		log.Printf("[P2P] Peer %s Client not connected", targetKernelID)
		m.peersMu.RUnlock()
		return
	}
	// 保持锁，直接发送
	log.Printf("[P2P] Calling SendSyncConnectors for %s...", targetKernelID)
	if err := peer.Client.SendSyncConnectors(connectors); err != nil {
		log.Printf("[P2P] Failed to sync to %s: %v", targetKernelID, err)
	} else {
		log.Printf("[P2P] Synced %d local connectors to peer %s", len(connectors), targetKernelID)
	}
	m.peersMu.RUnlock()
}

// RelayMessage 转发消息到远程运维方
func (m *P2PManager) RelayMessage(receiverKernelID string, msg *RelayMessagePayload) error {
	m.peersMu.RLock()
	peer, exists := m.peers[receiverKernelID]
	m.peersMu.RUnlock()

	if !exists || peer.Client == nil {
		return fmt.Errorf("peer %s not connected", receiverKernelID)
	}
	if !peer.Client.IsConnected() {
		return fmt.Errorf("peer %s not connected", receiverKernelID)
	}

	// 设置追踪ID
	msg.MessageID = uuid.New().String()

	if err := peer.Client.SendRelayMessage(msg); err != nil {
		return fmt.Errorf("failed to relay message: %w", err)
	}

	log.Printf("[P2P] Message relayed to %s: %s -> %s", receiverKernelID, msg.SenderID, msg.ReceiverID)
	return nil
}

// ListPeers 列出所有对等方
func (m *P2PManager) ListPeers() []*PeerInfo {
	m.peersMu.RLock()
	defer m.peersMu.RUnlock()

	peers := make([]*PeerInfo, 0, len(m.peers))
	for _, p := range m.peers {
		peers = append(peers, p)
	}
	return peers
}

// GetPeer 获取指定对等方
func (m *P2PManager) GetPeer(kernelID string) *PeerInfo {
	m.peersMu.RLock()
	defer m.peersMu.RUnlock()
	return m.peers[kernelID]
}

// GetPeerCount 获取对等方数量
func (m *P2PManager) GetPeerCount() int {
	m.peersMu.RLock()
	defer m.peersMu.RUnlock()
	return len(m.peers)
}

// IsPeerConnected 检查对等方是否已连接
func (m *P2PManager) IsPeerConnected(kernelID string) bool {
	m.peersMu.RLock()
	defer m.peersMu.RUnlock()

	peer, exists := m.peers[kernelID]
	if !exists {
		return false
	}
	return peer.Status == "connected"
}

// GetKernelID 获取本内核ID
func (m *P2PManager) GetKernelID() string {
	return m.config.KernelID
}

// GetListenAddr 获取监听地址
func (m *P2PManager) GetListenAddr() string {
	return m.config.ListenAddr
}

// IsRunning 检查P2P管理器是否正在运行
func (m *P2PManager) IsRunning() bool {
	return atomic.LoadInt32(&m.running) == 1
}

// ConnectPeer 连接到P2P对等方（简化版本，使用 ConnectToPeer）
func (m *P2PManager) ConnectPeer(kernelID, address string, port int) error {
	return m.ConnectToPeer(kernelID, address, port)
}

// DisconnectPeer 断开P2P对等方连接（简化版本，使用 DisconnectFromPeer）
func (m *P2PManager) DisconnectPeer(kernelID string) error {
	return m.DisconnectFromPeer(kernelID)
}

package tempchat

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	pb "github.com/trusted-space/kernel/proto/kernel/v1"
)

// TempChatConfig 临时会话配置
type TempChatConfig struct {
	HeartbeatInterval int // 心跳间隔（秒）
	SessionTimeout   int // 会话超时时间（秒）
	SyncInterval     int // 运维方同步间隔（秒）
}

// DefaultConfig 默认配置
var DefaultConfig = &TempChatConfig{
	HeartbeatInterval: 15, // 与连接器心跳保持一致
	SessionTimeout:     60, // 60秒无心跳则认为离线
	SyncInterval:       30, // 每30秒同步一次在线连接器列表
}

// ConnectorSession 连接器会话信息
type ConnectorSession struct {
	SessionID      string    // 会话ID
	ConnectorID    string    // 连接器ID
	KernelID       string    // 连接器所属内核ID
	Address        string    // 连接器IP地址
	Port           int32     // 连接器监听端口
	EntityType     string    // 连接器实体类型
	ExposeToOthers bool      // 是否向其他内核暴露
	RegisteredAt   time.Time // 注册时间
	LastHeartbeat  time.Time // 最后心跳时间
	MessageChan    chan *pb.TempMessage // 消息通道（用于流式推送）
}

// TempChatManager 临时会话管理器
// 负责管理连接器的临时会话注册、心跳、消息路由等功能
type TempChatManager struct {
	mu         sync.RWMutex
	sessions   map[string]*ConnectorSession // key: sessionID
	byConnector map[string]string           // key: connectorID -> sessionID

	config     *TempChatConfig
	kernelID   string

	// 远程运维方连接（用于跨内核通信）
	remoteOpsMu sync.RWMutex
	remoteOps   map[string]*RemoteOpsConnection // key: remoteKernelID

	// 远程内核的在线连接器缓存（key: remoteKernelID, value: 连接器列表）
	remoteConnectorsMu sync.RWMutex
	remoteConnectors   map[string][]*pb.ConnectorOnlineInfo // key: remoteKernelID

	// 消息处理回调
	messageHandler func(senderID, receiverID string, payload []byte) error

	// 跨内核消息转发回调（由 P2P Manager 设置）
	relayHandler func(receiverKernelID, receiverID string, payload []byte) error

	// 清理定时器
	cleanupTicker *time.Ticker
	stopCleanup   chan struct{}
}

// RemoteOpsConnection 远程运维方连接
type RemoteOpsConnection struct {
	KernelID   string
	Address    string
	Port       int
	Connected  bool
	LastSync   time.Time
}

// NewTempChatManager 创建临时会话管理器
func NewTempChatManager(kernelID string, config *TempChatConfig) *TempChatManager {
	if config == nil {
		config = DefaultConfig
	}

	mgr := &TempChatManager{
		sessions:      make(map[string]*ConnectorSession),
		byConnector:   make(map[string]string),
		config:       config,
		kernelID:     kernelID,
		remoteOps:    make(map[string]*RemoteOpsConnection),
		remoteConnectors: make(map[string][]*pb.ConnectorOnlineInfo),
		cleanupTicker: time.NewTicker(10 * time.Second), // 每10秒检查一次超时
		stopCleanup:  make(chan struct{}),
	}

	// 启动清理协程
	go mgr.cleanupLoop()

	return mgr
}

// cleanupLoop 定期清理超时会话
func (m *TempChatManager) cleanupLoop() {
	for {
		select {
		case <-m.cleanupTicker.C:
			m.cleanupExpiredSessions()
		case <-m.stopCleanup:
			return
		}
	}
}

// cleanupExpiredSessions 清理超时会话
func (m *TempChatManager) cleanupExpiredSessions() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	timeout := time.Duration(m.config.SessionTimeout) * time.Second

	for sessionID, session := range m.sessions {
		if now.Sub(session.LastHeartbeat) > timeout {
			log.Printf("[INFO] TempChat: 清理超时会话 %s (connector: %s)", sessionID, session.ConnectorID)
			delete(m.sessions, sessionID)
			delete(m.byConnector, session.ConnectorID)

			// 关闭消息通道
			if session.MessageChan != nil {
				close(session.MessageChan)
			}
		}
	}
}

// Stop 停止管理器
func (m *TempChatManager) Stop() {
	close(m.stopCleanup)
	m.cleanupTicker.Stop()
}

// RegisterSession 注册临时会话
func (m *TempChatManager) RegisterSession(req *pb.RegisterTempSessionRequest) (*ConnectorSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 检查是否已存在该连接器的会话
	if existingSessionID, ok := m.byConnector[req.ConnectorId]; ok {
		// 更新已存在的会话
		if existingSession, ok := m.sessions[existingSessionID]; ok {
			existingSession.Address = req.Address
			existingSession.Port = req.Port
			existingSession.EntityType = req.EntityType
			existingSession.ExposeToOthers = req.ExposeToOthers
			existingSession.LastHeartbeat = time.Now()
			log.Printf("[INFO] TempChat: 更新已存在会话 %s (connector: %s)", existingSessionID, req.ConnectorId)
			return existingSession, nil
		}
	}

	// 创建新会话
	sessionID := uuid.New().String()
	session := &ConnectorSession{
		SessionID:      sessionID,
		ConnectorID:    req.ConnectorId,
		KernelID:       req.KernelId,
		Address:        req.Address,
		Port:           req.Port,
		EntityType:     req.EntityType,
		ExposeToOthers: req.ExposeToOthers,
		RegisteredAt:   time.Now(),
		LastHeartbeat:  time.Now(),
		MessageChan:    make(chan *pb.TempMessage, 100), // 缓冲通道
	}

	m.sessions[sessionID] = session
	m.byConnector[req.ConnectorId] = sessionID

	log.Printf("[INFO] TempChat: 注册新会话 %s (connector: %s, address: %s:%d)",
		sessionID, req.ConnectorId, req.Address, req.Port)

	return session, nil
}

// Heartbeat 更新心跳
func (m *TempChatManager) Heartbeat(sessionID, connectorID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("会话不存在: %s", sessionID)
	}

	if session.ConnectorID != connectorID {
		return fmt.Errorf("连接器ID不匹配")
	}

	session.LastHeartbeat = time.Now()
	return nil
}

// UnregisterSession 注销会话
func (m *TempChatManager) UnregisterSession(sessionID, connectorID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("会话不存在: %s", sessionID)
	}

	if session.ConnectorID != connectorID {
		return fmt.Errorf("连接器ID不匹配")
	}

	// 关闭消息通道
	if session.MessageChan != nil {
		close(session.MessageChan)
	}

	delete(m.sessions, sessionID)
	delete(m.byConnector, connectorID)

	log.Printf("[INFO] TempChat: 注销会话 %s (connector: %s)", sessionID, connectorID)
	return nil
}

// GetSession 获取会话
func (m *TempChatManager) GetSession(sessionID string) (*ConnectorSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("会话不存在: %s", sessionID)
	}

	return session, nil
}

// GetSessionByConnector 根据连接器ID获取会话
func (m *TempChatManager) GetSessionByConnector(connectorID string) (*ConnectorSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessionID, ok := m.byConnector[connectorID]
	if !ok {
		return nil, fmt.Errorf("连接器未注册: %s", connectorID)
	}

	session, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("会话不存在: %s", sessionID)
	}

	return session, nil
}

// ListOnlineConnectors 列出在线连接器
func (m *TempChatManager) ListOnlineConnectors(kernelID string) []*pb.ConnectorOnlineInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var connectors []*pb.ConnectorOnlineInfo
	now := time.Now()
	timeout := time.Duration(m.config.SessionTimeout) * time.Second

	for _, session := range m.sessions {
		// 检查是否超时
		if now.Sub(session.LastHeartbeat) > timeout {
			continue
		}

		// 过滤不向其他内核暴露的连接器
		if !session.ExposeToOthers {
			continue
		}

		// 如果指定了内核ID，只返回该内核的连接器
		if kernelID != "" && session.KernelID != kernelID {
			continue
		}

		connectors = append(connectors, &pb.ConnectorOnlineInfo{
			ConnectorId:    session.ConnectorID,
			KernelId:       session.KernelID,
			Address:        session.Address,
			Port:           session.Port,
			EntityType:     session.EntityType,
			SessionId:      session.SessionID,
			LastHeartbeat:  session.LastHeartbeat.Unix(),
			RegisteredAt:   session.RegisteredAt.Unix(),
			ExposeToOthers: session.ExposeToOthers,
		})
	}

	return connectors
}

// SendMessage 发送消息给连接器
func (m *TempChatManager) SendMessage(receiverID string, msg *pb.TempMessage) error {
	m.mu.RLock()
	session, err := m.GetSessionByConnector(receiverID)
	m.mu.RUnlock()

	if err != nil {
		return err
	}

	select {
	case session.MessageChan <- msg:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("发送超时，接收方缓冲区满")
	}
}

// DeliverMessage 投递消息到连接器（由P2P管理器调用，用于跨内核消息投递）
func (m *TempChatManager) DeliverMessage(receiverID string, msg *pb.TempMessage) error {
	return m.SendMessage(receiverID, msg)
}

// GetSessionForP2P 获取会话的MessageChan（供P2P使用）
func (m *TempChatManager) GetSessionForP2P(receiverID string) (chan *pb.TempMessage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessionID, ok := m.byConnector[receiverID]
	if !ok {
		return nil, fmt.Errorf("连接器未注册: %s", receiverID)
	}

	session, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("会话不存在: %s", sessionID)
	}

	return session.MessageChan, nil
}

// ReceiveMessages 获取消息通道（用于流式接收）
func (m *TempChatManager) ReceiveMessages(sessionID, connectorID string) (<-chan *pb.TempMessage, error) {
	session, err := m.GetSession(sessionID)
	if err != nil {
		return nil, err
	}

	if session.ConnectorID != connectorID {
		return nil, fmt.Errorf("连接器ID不匹配")
	}

	return session.MessageChan, nil
}

// GetLocalConnectors 获取本地在线连接器
func (m *TempChatManager) GetLocalConnectors() []*pb.ConnectorOnlineInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.listOnlineConnectorsLocked("")
}

// listOnlineConnectorsLocked 在已持有读锁的情况下列出在线连接器（内部使用）
func (m *TempChatManager) listOnlineConnectorsLocked(kernelID string) []*pb.ConnectorOnlineInfo {
	var connectors []*pb.ConnectorOnlineInfo
	now := time.Now()
	timeout := time.Duration(m.config.SessionTimeout) * time.Second

	for _, session := range m.sessions {
		// 检查是否超时
		if now.Sub(session.LastHeartbeat) > timeout {
			continue
		}

		// 过滤不向其他内核暴露的连接器
		if !session.ExposeToOthers {
			continue
		}

		// 如果指定了内核ID，只返回该内核的连接器
		if kernelID != "" && session.KernelID != kernelID {
			continue
		}

		connectors = append(connectors, &pb.ConnectorOnlineInfo{
			ConnectorId:    session.ConnectorID,
			KernelId:       session.KernelID,
			Address:        session.Address,
			Port:           session.Port,
			EntityType:     session.EntityType,
			SessionId:      session.SessionID,
			LastHeartbeat:  session.LastHeartbeat.Unix(),
			RegisteredAt:   session.RegisteredAt.Unix(),
			ExposeToOthers: session.ExposeToOthers,
		})
	}

	return connectors
}

// GetRemoteConnectors 获取远程内核的在线连接器（只返回 expose_to_others=true 的连接器）
func (m *TempChatManager) GetRemoteConnectors(kernelID string) []*pb.ConnectorOnlineInfo {
	m.remoteConnectorsMu.RLock()
	defer m.remoteConnectorsMu.RUnlock()

	var source map[string][]*pb.ConnectorOnlineInfo
	if kernelID != "" {
		source = map[string][]*pb.ConnectorOnlineInfo{kernelID: m.remoteConnectors[kernelID]}
	} else {
		source = m.remoteConnectors
	}

	var all []*pb.ConnectorOnlineInfo
	for _, connectors := range source {
		for _, c := range connectors {
			if c.ExposeToOthers {
				all = append(all, c)
			}
		}
	}
	return all
}

// SetRemoteConnectors 更新远程内核的在线连接器缓存
func (m *TempChatManager) SetRemoteConnectors(kernelID string, connectors []*pb.ConnectorOnlineInfo) {
	m.remoteConnectorsMu.Lock()
	defer m.remoteConnectorsMu.Unlock()

	oldCount := 0
	if old, ok := m.remoteConnectors[kernelID]; ok {
		oldCount = len(old)
	}
	m.remoteConnectors[kernelID] = connectors

	// 仅在数量变化时打印日志，避免刷屏
	if oldCount != len(connectors) {
		log.Printf("[INFO] TempChat: 更新远程内核 %s 的连接器缓存，当前 %d 个", kernelID, len(connectors))
	}
}

// SetRemoteOpsConnection 设置远程运维方连接
func (m *TempChatManager) SetRemoteOpsConnection(kernelID, address string, port int) {
	m.remoteOpsMu.Lock()
	defer m.remoteOpsMu.Unlock()

	m.remoteOps[kernelID] = &RemoteOpsConnection{
		KernelID:  kernelID,
		Address:   address,
		Port:      port,
		Connected: true,
		LastSync:  time.Now(),
	}

	log.Printf("[INFO] TempChat: 设置远程运维方连接 kernel=%s, address=%s:%d", kernelID, address, port)
}

// RemoveRemoteOpsConnection 移除远程运维方连接
func (m *TempChatManager) RemoveRemoteOpsConnection(kernelID string) {
	m.remoteOpsMu.Lock()
	defer m.remoteOpsMu.Unlock()

	delete(m.remoteOps, kernelID)
	log.Printf("[INFO] TempChat: 移除远程运维方连接 kernel=%s", kernelID)
}

// IsRemoteConnected 检查远程运维方是否已连接
func (m *TempChatManager) IsRemoteConnected(kernelID string) bool {
	m.remoteOpsMu.RLock()
	defer m.remoteOpsMu.Unlock()

	conn, ok := m.remoteOps[kernelID]
	return ok && conn.Connected
}

// GetRemoteKernels 获取已连接的远程内核列表
func (m *TempChatManager) GetRemoteKernels() []string {
	m.remoteOpsMu.RLock()
	defer m.remoteOpsMu.RUnlock()

	var kernels []string
	for kernelID := range m.remoteOps {
		kernels = append(kernels, kernelID)
	}
	return kernels
}

// GetKernelID 获取当前内核ID
func (m *TempChatManager) GetKernelID() string {
	return m.kernelID
}

// SetMessageHandler 设置消息处理回调
func (m *TempChatManager) SetMessageHandler(handler func(senderID, receiverID string, payload []byte) error) {
	m.messageHandler = handler
}

// SetRelayHandler 设置跨内核消息转发回调（由 P2P Manager 设置）
func (m *TempChatManager) SetRelayHandler(handler func(receiverKernelID, receiverID string, payload []byte) error) {
	m.relayHandler = handler
}

// RouteMessage 路由消息（根据receiverID判断本地还是远程）
func (m *TempChatManager) RouteMessage(senderID, senderKernelID, receiverID string, payload []byte, flowID string) (string, error) {
	// 生成本地消息ID
	messageID := uuid.New().String()

	// 检查接收方是否在本地
	session, err := m.GetSessionByConnector(receiverID)
	if err == nil && session != nil {
		// 本地接收
		msg := &pb.TempMessage{
			MessageId:      messageID,
			SenderId:       senderID,
			ReceiverId:     receiverID,
			Payload:        payload,
			Timestamp:      time.Now().Unix(),
			FlowId:         flowID,
			SourceKernelId: senderKernelID,
		}

		if err := m.SendMessage(receiverID, msg); err != nil {
			return "", fmt.Errorf("发送消息失败: %w", err)
		}

		log.Printf("[INFO] TempChat: 消息路由到本地 kernel=%s, connector=%s, messageID=%s",
			m.kernelID, receiverID, messageID)
		return messageID, nil
	}

	// 接收方不在本地，尝试跨内核转发
	// receiverID 格式: "kernelID:connectorID"
	var targetKernelID, targetConnectorID string
	if len(receiverID) > 0 && strings.Contains(receiverID, ":") {
		parts := strings.SplitN(receiverID, ":", 2)
		targetKernelID = parts[0]
		targetConnectorID = parts[1]
	} else {
		targetKernelID = receiverID
		targetConnectorID = receiverID
	}

	// 检查是否需要转发到其他内核
	if targetKernelID != "" && targetKernelID != m.kernelID {
		if m.relayHandler != nil {
			log.Printf("[INFO] TempChat: 转发消息到远程 kernel=%s, connector=%s, messageID=%s",
				targetKernelID, targetConnectorID, messageID)
			if err := m.relayHandler(targetKernelID, targetConnectorID, payload); err != nil {
				return "", fmt.Errorf("跨内核转发失败: %w", err)
			}
			return messageID, nil
		}
		log.Printf("[WARN] TempChat: 无法转发消息，relayHandler 未设置")
		return "", fmt.Errorf("跨内核转发未配置")
	}

	log.Printf("[WARN] TempChat: 接收方 %s 不在本地 kernel=%s，且无有效目标内核",
		receiverID, m.kernelID)
	return "", fmt.Errorf("接收方不在本地且无有效转发目标")
}

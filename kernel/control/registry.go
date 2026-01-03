package control

import (
	"fmt"
	"sync"
	"time"
)

// ConnectorStatus 连接器状态类型
type ConnectorStatus string

const (
	// ConnectorStatusActive 活跃状态：连接器在线且可以自动订阅频道
	ConnectorStatusActive ConnectorStatus = "active"
	// ConnectorStatusInactive 非活跃状态：连接器在线但不自动订阅，需要手动订阅
	ConnectorStatusInactive ConnectorStatus = "inactive"
	// ConnectorStatusOffline 离线状态：连接器离线
	ConnectorStatusOffline ConnectorStatus = "offline"
	// ConnectorStatusClosed 关闭状态：连接器已关闭
	ConnectorStatusClosed ConnectorStatus = "closed"
)

// ConnectorInfo 连接器信息
type ConnectorInfo struct {
	ConnectorID   string
	EntityType    string
	PublicKey     string
	Status        ConnectorStatus // 连接器状态：active, inactive, offline, closed
	LastHeartbeat time.Time
	RegisteredAt  time.Time
	SessionToken  string
}

// Registry 身份注册表，维护连接器信息
type Registry struct {
	mu         sync.RWMutex
	connectors map[string]*ConnectorInfo
}

// NewRegistry 创建新的注册表
func NewRegistry() *Registry {
	return &Registry{
		connectors: make(map[string]*ConnectorInfo),
	}
}

// Register 注册连接器
func (r *Registry) Register(connectorID, entityType, publicKey, sessionToken string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if connectorID == "" {
		return fmt.Errorf("connector ID cannot be empty")
	}

	now := time.Now()

	// 检查是否已注册
	if info, exists := r.connectors[connectorID]; exists {
		// 更新现有连接器信息
		info.EntityType = entityType
		info.PublicKey = publicKey
		// 如果之前是关闭状态，重新注册时设为活跃状态
		if info.Status == ConnectorStatusClosed {
			info.Status = ConnectorStatusActive
		} else if info.Status == ConnectorStatusOffline {
			info.Status = ConnectorStatusActive
		}
		info.LastHeartbeat = now
		info.SessionToken = sessionToken
		return nil
	}

	// 新注册，默认为活跃状态
	r.connectors[connectorID] = &ConnectorInfo{
		ConnectorID:   connectorID,
		EntityType:    entityType,
		PublicKey:     publicKey,
		Status:        ConnectorStatusActive,
		LastHeartbeat: now,
		RegisteredAt:  now,
		SessionToken:  sessionToken,
	}

	return nil
}

// UpdateHeartbeat 更新心跳时间
func (r *Registry) UpdateHeartbeat(connectorID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, exists := r.connectors[connectorID]
	if !exists {
		return fmt.Errorf("connector %s not registered", connectorID)
	}

	info.LastHeartbeat = time.Now()
	// 更新心跳时，如果之前是离线状态，恢复为活跃状态
	if info.Status == ConnectorStatusOffline {
		info.Status = ConnectorStatusActive
	}

	return nil
}

// GetConnector 获取连接器信息
func (r *Registry) GetConnector(connectorID string) (*ConnectorInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, exists := r.connectors[connectorID]
	if !exists {
		return nil, fmt.Errorf("connector %s not found", connectorID)
	}

	return info, nil
}

// IsOnline 检查连接器是否在线（最近30秒内有心跳）
func (r *Registry) IsOnline(connectorID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, exists := r.connectors[connectorID]
	if !exists {
		return false
	}

	return time.Since(info.LastHeartbeat) < 30*time.Second
}

// ListConnectors 列出所有连接器
func (r *Registry) ListConnectors() []*ConnectorInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]*ConnectorInfo, 0, len(r.connectors))
	for _, info := range r.connectors {
		list = append(list, info)
	}

	return list
}

// Unregister 注销连接器
func (r *Registry) Unregister(connectorID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.connectors[connectorID]; !exists {
		return fmt.Errorf("connector %s not found", connectorID)
	}

	delete(r.connectors, connectorID)
	return nil
}

// CheckOfflineConnectors 检查并标记离线连接器（超过1分钟没有心跳）
func (r *Registry) CheckOfflineConnectors() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	for _, info := range r.connectors {
		if now.Sub(info.LastHeartbeat) > time.Minute {
			// 只有活跃或非活跃状态才会变为离线，关闭状态保持不变
			if info.Status == ConnectorStatusActive || info.Status == ConnectorStatusInactive {
				info.Status = ConnectorStatusOffline
			}
		}
	}
}

// SetStatus 设置连接器状态
func (r *Registry) SetStatus(connectorID string, status ConnectorStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, exists := r.connectors[connectorID]
	if !exists {
		return fmt.Errorf("connector %s not found", connectorID)
	}

	info.Status = status
	return nil
}

// GetStatus 获取连接器状态
func (r *Registry) GetStatus(connectorID string) (ConnectorStatus, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, exists := r.connectors[connectorID]
	if !exists {
		return "", fmt.Errorf("connector %s not found", connectorID)
	}

	return info.Status, nil
}

// IsActive 检查连接器是否处于活跃状态
func (r *Registry) IsActive(connectorID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, exists := r.connectors[connectorID]
	if !exists {
		return false
	}

	return info.Status == ConnectorStatusActive
}

// StartHealthCheck 启动健康检查协程
func (r *Registry) StartHealthCheck() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			r.CheckOfflineConnectors()
		}
	}()
}


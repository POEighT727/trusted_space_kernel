package circulation

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ChannelStatus 频道状态
type ChannelStatus string

const (
	ChannelStatusProposed ChannelStatus = "proposed" // 已提议，等待协商
	ChannelStatusActive   ChannelStatus = "active"   // 活跃状态，可使用
	ChannelStatusClosed   ChannelStatus = "closed"   // 已关闭
)

// NegotiationStatus 协商状态
type NegotiationStatus int

const (
	NegotiationStatusProposed NegotiationStatus = 1 // 已提议
	NegotiationStatusAccepted NegotiationStatus = 2 // 已接受
	NegotiationStatusRejected NegotiationStatus = 3 // 已拒绝
)

// MessageType 消息类型
type MessageType string

const (
	MessageTypeData     MessageType = "data"     // 业务数据消息
	MessageTypeControl  MessageType = "control"  // 控制消息
	MessageTypeEvidence MessageType = "evidence" // 存证数据消息
)

// ChannelProposal 频道提议信息
type ChannelProposal struct {
	ProposalID     string            // 提议ID
	Status         NegotiationStatus // 协商状态
	Reason         string            // 创建理由
	TimeoutSeconds int32             // 超时时间（秒）
	CreatedAt      time.Time         // 提议创建时间

	// 参与方信息
	SenderIDs   []string // 发送方ID列表
	ReceiverIDs []string // 接收方ID列表
	ApproverID  string   // 权限变更批准者ID（默认是创建者）

	// 参与方确认状态（所有参与方都确认后频道才激活）
	SenderApprovals   map[string]bool // key: senderID, value: 是否已确认
	ReceiverApprovals map[string]bool // key: receiverID, value: 是否已确认
}

// EvidenceMode 存证方式
type EvidenceMode string

const (
	EvidenceModeNone         EvidenceMode = "none"         // 不进行存证
	EvidenceModeInternal     EvidenceMode = "internal"     // 使用内核内置存证
	EvidenceModeExternal     EvidenceMode = "external"     // 使用外部存证连接器
	EvidenceModeHybrid       EvidenceMode = "hybrid"       // 同时使用内置和外部存证
)

// EvidenceStrategy 存证策略
type EvidenceStrategy string

const (
	EvidenceStrategyAll       EvidenceStrategy = "all"       // 存证所有消息
	EvidenceStrategyData      EvidenceStrategy = "data"      // 只存证数据消息
	EvidenceStrategyControl   EvidenceStrategy = "control"   // 只存证控制消息
	EvidenceStrategyImportant EvidenceStrategy = "important" // 只存证重要消息
)

// EvidenceConfig 存证配置
type EvidenceConfig struct {
	Mode           EvidenceMode     // 存证方式
	Strategy       EvidenceStrategy // 存证策略
	ConnectorID    string           // 外部存证连接器ID（当Mode为external或hybrid时使用）
	BackupEnabled  bool             // 是否启用备份存证
	RetentionDays  int              // 存证数据保留天数
	CompressData   bool             // 是否压缩存证数据
	CustomSettings map[string]string // 自定义存证设置
}

// Channel 数据传输频道（统一频道模式，支持多种消息类型）
type Channel struct {
	ChannelID     string        // 系统生成的唯一ID
	ChannelName   string        // 配置文件中的频道名称
	CreatorID     string        // 创建者ID（不一定是发送方）
	ApproverID    string        // 权限变更批准者ID（默认是创建者）
	SenderIDs     []string      // 发送方ID列表
	ReceiverIDs   []string      // 接收方ID列表
	Encrypted     bool          // 是否加密传输
	DataTopic     string
	Status        ChannelStatus
	CreatedAt     time.Time
	ClosedAt      *time.Time
	LastActivity  time.Time

	// 存证配置
	EvidenceConfig *EvidenceConfig // 存证配置信息

	// 配置文件路径（可选，由创建者指定）
	ConfigFilePath string // 频道配置文件路径，如果为空则不使用配置文件

	// 频道协商信息（提议阶段）
	ChannelProposal *ChannelProposal // 协商提议信息

	// 数据流控制
	dataQueue     chan *DataPacket
	subscribers   map[string]chan *DataPacket // key: subscriber ID
	mu            sync.RWMutex
	participantsMu sync.RWMutex // 参与者集合的锁（保留用于兼容）

	// 数据暂存（在接收方订阅前暂存数据）
	buffer        []*DataPacket  // 暂存的数据包
	bufferMu      sync.RWMutex   // 暂存缓冲区的锁
	maxBufferSize int           // 最大暂存数量

	// 权限变更管理
	permissionRequests []*PermissionChangeRequest // 权限变更请求列表
	permissionMu       sync.RWMutex               // 权限变更锁

	// 连接器状态管理（重启恢复）
	manager *ChannelManager // 指向ChannelManager的引用，用于访问连接器状态
}

// DataPacket 数据包
type DataPacket struct {
	ChannelID      string
	SequenceNumber int64
	Payload        []byte
	Signature      string
	Timestamp      int64
	SenderID       string     // 发送方ID
	TargetIDs      []string   // 目标接收者ID列表（为空则广播给所有订阅者）
	MessageType    MessageType // 消息类型（数据/控制/存证）
}

// PermissionChangeRequest 权限变更请求
type PermissionChangeRequest struct {
	RequestID       string            // 请求ID
	RequesterID     string            // 请求者ID
	ChannelID       string            // 频道ID
	ChangeType      string            // 变更类型: "add_sender", "remove_sender", "add_receiver", "remove_receiver"
	TargetID        string            // 目标连接器ID
	Reason          string            // 变更理由
	Status          string            // 请求状态: "pending", "approved", "rejected"
	CreatedAt       time.Time         // 创建时间
	ApprovedAt      *time.Time        // 批准时间
	ApprovedBy      string            // 批准者ID
	RejectedAt      *time.Time        // 拒绝时间
	RejectedBy      string            // 拒绝者ID
	RejectReason    string            // 拒绝理由
}

// ConnectorStatus 连接器状态
type ConnectorStatus int

const (
	ConnectorStatusUnknown ConnectorStatus = iota
	ConnectorStatusOnline  // 在线
	ConnectorStatusOffline // 离线
)

// EvidenceConnector 外部存证连接器信息
type EvidenceConnector struct {
	ConnectorID   string            // 连接器ID
	Name          string            // 连接器名称
	Description   string            // 连接器描述
	Capabilities  []string          // 支持的存证能力
	Status        ConnectorStatus   // 连接器状态
	RegisteredAt  time.Time         // 注册时间
	LastHeartbeat time.Time         // 最后心跳时间
	Config        map[string]string // 连接器配置
}

// ChannelManager 频道管理器
type ChannelManager struct {
	mu                   sync.RWMutex
	channels             map[string]*Channel
	notifyChannelCreated func(*Channel) // 频道创建通知回调

	// 连接器状态跟踪（用于重启恢复）
	connectorStatus  map[string]ConnectorStatus // 连接器状态
	connectorBuffers map[string][]*DataPacket   // 离线连接器的个人缓冲区
	lastActivity     map[string]time.Time       // 连接器最后活动时间
	connectorMu      sync.RWMutex               // 连接器状态的锁

	// 外部存证连接器管理
	evidenceConnectors map[string]*EvidenceConnector // 已注册的存证连接器
	evidenceMu         sync.RWMutex                   // 存证连接器的锁

	// 频道配置管理（可选，由创建者指定配置文件路径时使用）
	configManager *ChannelConfigManager // 频道配置管理器
	// forwardToKernel 回调，用于将 DataPacket 转发到远端内核
	forwardToKernel func(kernelID string, packet *DataPacket) error
}

// NewChannelManager 创建新的频道管理器
func NewChannelManager() *ChannelManager {
	return &ChannelManager{
		channels:            make(map[string]*Channel),
		connectorStatus:     make(map[string]ConnectorStatus),
		connectorBuffers:    make(map[string][]*DataPacket),
		lastActivity:        make(map[string]time.Time),
		evidenceConnectors:  make(map[string]*EvidenceConnector),
	}
}

// SetChannelCreatedCallback 设置频道创建通知回调
func (cm *ChannelManager) SetChannelCreatedCallback(callback func(*Channel)) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.notifyChannelCreated = callback
	log.Printf("✓ Channel creation callback set in ChannelManager")
}

// SetConfigManager 设置频道配置管理器
func (cm *ChannelManager) SetConfigManager(configManager *ChannelConfigManager) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.configManager = configManager
	log.Printf("✓ Channel config manager set")
}

// SetForwardToKernel 设置将要用于跨内核转发数据的回调函数（由上层多内核管理器设置）
func (cm *ChannelManager) SetForwardToKernel(fn func(kernelID string, packet *DataPacket) error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.forwardToKernel = fn
}

// GetConfigManager 获取频道配置管理器
func (cm *ChannelManager) GetConfigManager() *ChannelConfigManager {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.configManager
}

// SetDefaultEvidenceConfig 设置默认存证配置
func (cm *ChannelManager) SetDefaultEvidenceConfig(config *EvidenceConfig) error {
	if cm.configManager == nil {
		return fmt.Errorf("config manager not set")
	}

	cm.configManager.SetDefaultEvidenceConfig(config)
	log.Printf("✓ Default evidence config updated in ChannelManager")
	return nil
}

// GetDefaultEvidenceConfig 获取默认存证配置
func (cm *ChannelManager) GetDefaultEvidenceConfig() *EvidenceConfig {
	if cm.configManager == nil {
		return &EvidenceConfig{
			Mode:           EvidenceModeNone,
			Strategy:       EvidenceStrategyAll,
			BackupEnabled:  false,
			RetentionDays:  30,
			CompressData:   true,
			CustomSettings: make(map[string]string),
		}
	}

	return cm.configManager.GetDefaultEvidenceConfig()
}

// ProposeChannel 提议创建频道（协商第一阶段）
func (cm *ChannelManager) ProposeChannel(creatorID, approverID string, senderIDs, receiverIDs []string, dataTopic string, encrypted bool, evidenceConfig *EvidenceConfig, configFilePath string, reason string, timeoutSeconds int32) (*Channel, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if creatorID == "" {
		return nil, fmt.Errorf("creator ID cannot be empty")
	}
	if len(senderIDs) == 0 {
		return nil, fmt.Errorf("at least one sender ID is required")
	}
	if len(receiverIDs) == 0 {
		return nil, fmt.Errorf("at least one receiver ID is required")
	}

	// 检查是否有重复的ID
	allIDs := make(map[string]bool)
	for _, id := range senderIDs {
		if id == "" {
			return nil, fmt.Errorf("sender ID cannot be empty")
		}
		if allIDs[id] {
			return nil, fmt.Errorf("duplicate sender ID: %s", id)
		}
		allIDs[id] = true
	}
	for _, id := range receiverIDs {
		if id == "" {
			return nil, fmt.Errorf("receiver ID cannot be empty")
		}
		if allIDs[id] {
			return nil, fmt.Errorf("receiver ID %s conflicts with sender", id)
		}
		allIDs[id] = true
	}

	// 生成唯一频道 ID 和提议 ID
	channelID := uuid.New().String()
	proposalID := uuid.New().String()

	if timeoutSeconds <= 0 {
		timeoutSeconds = 300 // 默认5分钟超时
	}

	// 初始化确认状态映射
	senderApprovals := make(map[string]bool)
	receiverApprovals := make(map[string]bool)
	for _, id := range senderIDs {
		senderApprovals[id] = false
	}
	for _, id := range receiverIDs {
		receiverApprovals[id] = false
	}

	// 创建者自动批准自己的提议
	for _, id := range senderIDs {
		if id == creatorID {
			senderApprovals[id] = true
			break
		}
	}
	for _, id := range receiverIDs {
		if id == creatorID {
			receiverApprovals[id] = true
			break
		}
	}

	channel := &Channel{
		ChannelID:         channelID,
		CreatorID:         creatorID,
		ApproverID:        approverID,
		SenderIDs:         senderIDs,
		ReceiverIDs:       receiverIDs,
		Encrypted:         encrypted,
		DataTopic:         dataTopic,
		Status:            ChannelStatusProposed,
		CreatedAt:         time.Now(),
		LastActivity:      time.Now(),
		EvidenceConfig:    evidenceConfig, // 设置存证配置
		ConfigFilePath:    configFilePath, // 设置配置文件路径
		manager:           cm, // 设置ChannelManager引用
		ChannelProposal: &ChannelProposal{
			ProposalID:        proposalID,
			Status:            NegotiationStatusProposed,
			Reason:            reason,
			TimeoutSeconds:    timeoutSeconds,
			CreatedAt:         time.Now(),
			SenderIDs:         senderIDs,
			ReceiverIDs:       receiverIDs,
			ApproverID:        approverID,
			SenderApprovals:   senderApprovals,
			ReceiverApprovals: receiverApprovals,
		},
		dataQueue:           make(chan *DataPacket, 1000), // 缓冲队列
		subscribers:         make(map[string]chan *DataPacket),
		buffer:              make([]*DataPacket, 0),
		maxBufferSize:       10000, // 最多暂存10000个数据包
		permissionRequests:  make([]*PermissionChangeRequest, 0),
	}

	cm.channels[channelID] = channel

	return channel, nil
}

// AcceptChannelProposal 接受频道提议（协商第二阶段）
func (cm *ChannelManager) AcceptChannelProposal(channelID, accepterID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	channel, exists := cm.channels[channelID]
	if !exists {
		return fmt.Errorf("channel not found")
	}

	// 检查频道状态
	if channel.Status != ChannelStatusProposed {
		return fmt.Errorf("channel is not in proposed state")
	}

	// 检查提议是否存在
	if channel.ChannelProposal == nil {
		return fmt.Errorf("channel proposal not found")
	}

	// 检查提议是否已超时
	if time.Since(channel.ChannelProposal.CreatedAt) > time.Duration(channel.ChannelProposal.TimeoutSeconds)*time.Second {
		return fmt.Errorf("channel proposal has expired")
	}

	// 根据接受者身份更新确认状态
	isSender := false
	for _, senderID := range channel.SenderIDs {
		if accepterID == senderID {
			channel.ChannelProposal.SenderApprovals[accepterID] = true
			isSender = true
			break
		}
	}

	isReceiver := false
	if !isSender {
		for _, receiverID := range channel.ReceiverIDs {
			if accepterID == receiverID {
				channel.ChannelProposal.ReceiverApprovals[accepterID] = true
				isReceiver = true
				break
			}
		}
	}

	if !isSender && !isReceiver {
		return fmt.Errorf("only channel participants can accept channel proposal")
	}

	// 检查是否所有参与方都已确认
	allApproved := true
	for _, approved := range channel.ChannelProposal.SenderApprovals {
		if !approved {
			allApproved = false
			break
		}
	}
	if allApproved {
		for _, approved := range channel.ChannelProposal.ReceiverApprovals {
			if !approved {
				allApproved = false
				break
			}
		}
	}

	log.Printf("🔍 Channel %s approval status - SenderApprovals: %v, ReceiverApprovals: %v", channelID, channel.ChannelProposal.SenderApprovals, channel.ChannelProposal.ReceiverApprovals)

	if allApproved {
		log.Printf("✅ All participants approved for channel %s, activating...", channelID)
		// 所有参与方都确认了，激活频道
		channel.Status = ChannelStatusActive
		channel.ChannelProposal.Status = NegotiationStatusAccepted
		channel.LastActivity = time.Now()

		// 启动数据分发协程（确保数据能够被分发到订阅者）
		go channel.startDataDistribution()

	} else {
		log.Printf("⏳ Channel %s still waiting for approvals", channelID)
	}

	return nil
}

// RejectChannelProposal 拒绝频道提议
func (cm *ChannelManager) RejectChannelProposal(channelID, rejecterID, reason string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	channel, exists := cm.channels[channelID]
	if !exists {
		return fmt.Errorf("channel not found")
	}

	// 检查频道状态
	if channel.Status != ChannelStatusProposed {
		return fmt.Errorf("channel is not in proposed state")
	}

	// 检查提议是否存在
	if channel.ChannelProposal == nil {
		return fmt.Errorf("channel proposal not found")
	}

	// 检查拒绝者是否是发送方或接收方
	isParticipant := false
	for _, senderID := range channel.SenderIDs {
		if rejecterID == senderID {
			isParticipant = true
			break
		}
	}
	if !isParticipant {
		for _, receiverID := range channel.ReceiverIDs {
			if rejecterID == receiverID {
				isParticipant = true
				break
			}
		}
	}
	if !isParticipant {
		return fmt.Errorf("only channel participants can reject channel proposal")
	}

	// 检查提议是否已超时
	if time.Since(channel.ChannelProposal.CreatedAt) > time.Duration(channel.ChannelProposal.TimeoutSeconds)*time.Second {
		return fmt.Errorf("channel proposal has expired")
	}

	// 更新频道状态为关闭
	channel.Status = ChannelStatusClosed
	channel.ChannelProposal.Status = NegotiationStatusRejected
	channel.ClosedAt = &time.Time{}
	*channel.ClosedAt = time.Now()
	channel.LastActivity = time.Now()

	return nil
}

// CreateChannel 创建新的数据传输频道（统一频道模式）
func (cm *ChannelManager) CreateChannel(creatorID, approverID string, senderIDs, receiverIDs []string, dataTopic string, encrypted bool, evidenceConfig *EvidenceConfig, configFilePath string) (*Channel, error) {
	// 如果没有提供存证配置，使用默认配置
	if evidenceConfig == nil && cm.configManager != nil {
		evidenceConfig = cm.configManager.GetDefaultEvidenceConfig()
	}

	// 创建主频道
	channel, err := cm.createChannelInternal(creatorID, approverID, senderIDs, receiverIDs, dataTopic, encrypted, evidenceConfig, configFilePath)
	if err != nil {
		return nil, err
	}

	// 注意：存证频道不在创建时同步创建，而是在频道激活时异步创建
	// 这样可以避免协商模式下的重复创建问题

	return channel, nil
}

// CreateChannelFromConfig 从配置文件创建频道
func (cm *ChannelManager) CreateChannelFromConfig(configFilePath string) (*Channel, error) {
	// 直接从指定文件路径加载配置
	data, err := ioutil.ReadFile(configFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %v", configFilePath, err)
	}

	var config ChannelConfigFile
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %v", err)
	}

	// 验证配置
	if config.ChannelName == "" {
		return nil, fmt.Errorf("channel name is required in config file")
	}

	// 如果配置了外部存证，自动将外部存证连接器添加到接收方列表
	if config.EvidenceConfig != nil && config.EvidenceConfig.Mode == EvidenceModeExternal && config.EvidenceConfig.ConnectorID != "" {
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
			log.Printf("✓ 外部存证连接器 %s 已自动添加到接收方列表", externalConnectorID)
		}
	}

	// 处理创建时间
	createdAt := time.Now()
	if config.CreatedAt != nil {
		createdAt = *config.CreatedAt
	}

	// 创建频道
	channel := &Channel{
		ChannelID:       uuid.New().String(),
		ChannelName:     config.ChannelName,
		CreatorID:       config.CreatorID,
		ApproverID:      config.ApproverID,
		SenderIDs:       make([]string, len(config.SenderIDs)),
		ReceiverIDs:     make([]string, len(config.ReceiverIDs)),
		Encrypted:       config.Encrypted,
		DataTopic:       config.DataTopic,
		Status:          ChannelStatusActive,
		CreatedAt:       createdAt,
		LastActivity:    time.Now(),
		EvidenceConfig:  config.EvidenceConfig,
		ConfigFilePath:  configFilePath, // 设置配置文件路径
		dataQueue:       make(chan *DataPacket, 1000),
		subscribers:     make(map[string]chan *DataPacket),
		buffer:          make([]*DataPacket, 0),
		maxBufferSize:   10000,
		permissionRequests: make([]*PermissionChangeRequest, 0),
		manager:         cm, // 设置ChannelManager引用
	}

	// 复制切片
	copy(channel.SenderIDs, config.SenderIDs)
	copy(channel.ReceiverIDs, config.ReceiverIDs)

	// 注册到管理器
	cm.mu.Lock()
	cm.channels[config.ChannelName] = channel
	cm.mu.Unlock()

	// 启动数据分发协程
	go channel.startDataDistribution()

	// 调用创建通知回调
	if cm.notifyChannelCreated != nil {
		go cm.notifyChannelCreated(channel)
	}

	log.Printf("✓ Channel created from config file: %s", configFilePath)
	return channel, nil
}

// SaveChannelConfig 保存频道配置到文件
func (cm *ChannelManager) SaveChannelConfig(channelID, name, description string) error {
	now := time.Now()
	cm.mu.RLock()
	channel, exists := cm.channels[channelID]
	cm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("channel %s not found", channelID)
	}

	// 如果频道指定了配置文件路径，直接保存到该文件
	if channel.ConfigFilePath != "" {
		config := &ChannelConfigFile{
			ChannelName:    channel.ChannelName,
			Name:           name,
			Description:    description,
			CreatorID:      channel.CreatorID,
			ApproverID:     channel.ApproverID,
			SenderIDs:      make([]string, len(channel.SenderIDs)),
			ReceiverIDs:    make([]string, len(channel.ReceiverIDs)),
			DataTopic:      channel.DataTopic,
			Encrypted:      channel.Encrypted,
			EvidenceConfig: channel.EvidenceConfig,
			CreatedAt:      &channel.CreatedAt,
			UpdatedAt:      &now,
			Version:        1,
		}

		copy(config.SenderIDs, channel.SenderIDs)
		copy(config.ReceiverIDs, channel.ReceiverIDs)

		data, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal config: %v", err)
		}

		if err := ioutil.WriteFile(channel.ConfigFilePath, data, 0644); err != nil {
			return fmt.Errorf("failed to write config file: %v", err)
		}

		log.Printf("✓ Channel config saved: %s", channel.ConfigFilePath)
		return nil
	}

	// 如果没有指定配置文件路径，使用全局配置管理器（向后兼容）
	if cm.configManager == nil {
		return fmt.Errorf("config manager not set and no config file path specified")
	}

	config := cm.configManager.CreateConfigFromChannel(channel, name, description)
	return cm.configManager.SaveConfig(config)
}

// LoadChannelConfig 加载频道配置
func (cm *ChannelManager) LoadChannelConfig(channelID string) (*ChannelConfigFile, error) {
	if cm.configManager == nil {
		return nil, fmt.Errorf("config manager not set")
	}

	return cm.configManager.LoadConfig(channelID)
}

// createChannelInternal 创建频道的核心逻辑
func (cm *ChannelManager) createChannelInternal(creatorID, approverID string, senderIDs, receiverIDs []string, dataTopic string, encrypted bool, evidenceConfig *EvidenceConfig, configFilePath string) (*Channel, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if creatorID == "" {
		return nil, fmt.Errorf("creator ID cannot be empty")
	}
	if len(senderIDs) == 0 {
		return nil, fmt.Errorf("at least one sender ID is required")
	}
	if len(receiverIDs) == 0 {
		return nil, fmt.Errorf("at least one receiver ID is required")
	}

	// 检查是否有重复的ID
	allIDs := make(map[string]bool)
	for _, id := range senderIDs {
		if id == "" {
			return nil, fmt.Errorf("sender ID cannot be empty")
		}
		if allIDs[id] {
			return nil, fmt.Errorf("duplicate sender ID: %s", id)
		}
		allIDs[id] = true
	}
	for _, id := range receiverIDs {
		if id == "" {
			return nil, fmt.Errorf("receiver ID cannot be empty")
		}
		if allIDs[id] {
			return nil, fmt.Errorf("receiver ID %s conflicts with sender", id)
		}
		allIDs[id] = true
	}

	// 生成唯一频道 ID
	channelID := uuid.New().String()

	channel := &Channel{
		ChannelID:          channelID,
		CreatorID:          creatorID,
		ApproverID:         approverID,
		SenderIDs:          senderIDs,
		ReceiverIDs:        receiverIDs,
		Encrypted:          encrypted,
		DataTopic:          dataTopic,
		Status:             ChannelStatusActive,
		CreatedAt:          time.Now(),
		LastActivity:       time.Now(),
		EvidenceConfig:     evidenceConfig, // 设置存证配置
		ConfigFilePath:     configFilePath, // 设置配置文件路径
		dataQueue:          make(chan *DataPacket, 1000), // 缓冲队列
		subscribers:        make(map[string]chan *DataPacket),
		buffer:             make([]*DataPacket, 0),
		maxBufferSize:      10000, // 最多暂存10000个数据包
		permissionRequests: make([]*PermissionChangeRequest, 0),
		manager:            cm, // 设置ChannelManager引用
	}

	cm.channels[channelID] = channel

	// 启动数据分发协程
	go channel.startDataDistribution()

	return channel, nil
}


// AddParticipant 添加参与者到频道（已废弃，单对单模式下发送方和接收方在创建时已确定）
// 保留此方法以保持兼容性，但不会实际添加参与者
func (c *Channel) AddParticipant(connectorID string) error {
	// 单对单模式下，发送方和接收方在创建时已确定，不能动态添加
	// 此方法保留以保持兼容性，但不执行任何操作
	return nil
}

// IsParticipant 检查连接器是否是频道参与者（发送方或接收方）
func (c *Channel) IsParticipant(connectorID string) bool {
	for _, senderID := range c.SenderIDs {
		if connectorID == senderID {
			return true
		}
	}
	for _, receiverID := range c.ReceiverIDs {
		if connectorID == receiverID {
			return true
		}
	}
	return false
}

// GetParticipants 获取所有参与者ID列表（发送方和接收方）
func (c *Channel) GetParticipants() []string {
	participants := make([]string, 0, len(c.SenderIDs)+len(c.ReceiverIDs))
	participants = append(participants, c.SenderIDs...)
	participants = append(participants, c.ReceiverIDs...)
	return participants
}

// CanSend 检查连接器是否可以在此频道发送数据
func (c *Channel) CanSend(connectorID string) bool {
	for _, senderID := range c.SenderIDs {
		if connectorID == senderID {
			return true
		}
	}
	return false
}

// CanReceive 检查连接器是否可以在此频道接收数据
func (c *Channel) CanReceive(connectorID string) bool {
	for _, receiverID := range c.ReceiverIDs {
		if connectorID == receiverID {
			return true
		}
	}
	return false
}

// GetChannel 获取频道
func (cm *ChannelManager) GetChannel(channelID string) (*Channel, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	channel, exists := cm.channels[channelID]
	if !exists {
		return nil, fmt.Errorf("channel %s not found", channelID)
	}

	return channel, nil
}

// CloseChannel 关闭频道
func (cm *ChannelManager) CloseChannel(channelID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	channel, exists := cm.channels[channelID]
	if !exists {
		return fmt.Errorf("channel %s not found", channelID)
	}

	if channel.Status == ChannelStatusClosed {
		return fmt.Errorf("channel %s already closed", channelID)
	}

	now := time.Now()
	channel.Status = ChannelStatusClosed
	channel.ClosedAt = &now

	// 关闭数据队列
	close(channel.dataQueue)

	// 关闭所有订阅者通道
	channel.mu.Lock()
	for _, subChan := range channel.subscribers {
		close(subChan)
	}
	channel.subscribers = make(map[string]chan *DataPacket)
	channel.mu.Unlock()

	return nil
}

// ListChannels 列出所有频道
func (cm *ChannelManager) ListChannels() []*Channel {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	channels := make([]*Channel, 0, len(cm.channels))
	for _, ch := range cm.channels {
		channels = append(channels, ch)
	}

	return channels
}

// ListActiveChannels 列出活跃频道
func (cm *ChannelManager) ListActiveChannels() []*Channel {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	channels := make([]*Channel, 0)
	for _, ch := range cm.channels {
		if ch.Status == ChannelStatusActive {
			channels = append(channels, ch)
		}
	}

	return channels
}

// ListChannelsByParticipant 列出参与者的所有频道
func (cm *ChannelManager) ListChannelsByParticipant(connectorID string) []*Channel {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	channels := make([]*Channel, 0)
	for _, ch := range cm.channels {
		if ch.IsParticipant(connectorID) {
			channels = append(channels, ch)
		}
	}

	return channels
}

// PushData 向频道推送数据
func (c *Channel) PushData(packet *DataPacket) error {
	if c.Status != ChannelStatusActive {
		return fmt.Errorf("channel is not active")
	}

	// 验证发送方是否有权限发送
	if !c.CanSend(packet.SenderID) {
		return fmt.Errorf("sender %s is not authorized to send data in this channel", packet.SenderID)
	}

	c.LastActivity = time.Now()

	// 检查目标接收者是否都已订阅
	c.mu.RLock()
	hasSubscribers := len(c.subscribers) > 0
	
	// 确定需要接收此数据的目标接收者（多对多模式，支持所有接收方）
	targetReceivers := make([]string, 0, len(c.ReceiverIDs))
	targetReceivers = append(targetReceivers, c.ReceiverIDs...)

	// 处理目标列表，支持远端目标格式 kernelID:connectorID
	remoteTargetsByKernel := make(map[string][]string)
	localTargets := make([]string, 0)
	// offline 本地 targets
	offlineTargets := make([]string, 0)

	if len(packet.TargetIDs) > 0 {
		for _, targetID := range packet.TargetIDs {
			if strings.Contains(targetID, ":") {
				parts := strings.SplitN(targetID, ":", 2)
				kernelPart := parts[0]
				connectorPart := parts[1]
				remoteTargetsByKernel[kernelPart] = append(remoteTargetsByKernel[kernelPart], connectorPart)
			} else {
				if c.CanReceive(targetID) {
					localTargets = append(localTargets, targetID)
					if _, subscribed := c.subscribers[targetID]; !subscribed {
						if c.manager != nil && !c.manager.IsConnectorOnline(targetID) {
							offlineTargets = append(offlineTargets, targetID)
						}
					}
				}
			}
		}
		if len(localTargets) == 0 && len(remoteTargetsByKernel) == 0 {
			// 没有有效目标
			return nil
		}
		targetReceivers = localTargets
	} else {
		// 广播模式：所有本地接收者
		for _, receiverID := range c.ReceiverIDs {
			if _, subscribed := c.subscribers[receiverID]; !subscribed {
				if c.manager != nil && !c.manager.IsConnectorOnline(receiverID) {
					offlineTargets = append(offlineTargets, receiverID)
				}
			}
		}
	}
	
	// 为离线本地连接器缓冲数据
	for _, offlineTarget := range offlineTargets {
		if c.manager != nil {
			c.manager.BufferDataForOfflineConnector(offlineTarget, packet)
			log.Printf("📦 Buffered data for offline connector %s in channel %s", offlineTarget, c.ChannelID)
		}
	}

	if len(offlineTargets) > 0 {
		log.Printf("🔍 Found %d offline targets for packet in channel %s", len(offlineTargets), c.ChannelID)
	}

	// 决定是否需要频道级别的缓冲
	shouldBuffer := false
	if len(packet.TargetIDs) > 0 {
		// 检查指定的目标接收者是否有未订阅但在线的
		for _, targetID := range packet.TargetIDs {
			if c.CanReceive(targetID) {
			if _, subscribed := c.subscribers[targetID]; !subscribed {
					// 只有在线但未订阅的才需要频道级别缓冲
					if c.manager == nil || c.manager.IsConnectorOnline(targetID) {
						shouldBuffer = true
				break
			}
		}
	}
		}
	}

	c.mu.RUnlock()

	if shouldBuffer {
		// 有指定的目标接收者未订阅（但在线），暂存数据等待他们订阅
		c.bufferMu.Lock()
		if len(c.buffer) >= c.maxBufferSize {
			c.bufferMu.Unlock()
			return fmt.Errorf("buffer is full, max size: %d", c.maxBufferSize)
		}
		// 复制数据包以避免并发问题
		bufferedPacket := &DataPacket{
			ChannelID:      packet.ChannelID,
			SequenceNumber: packet.SequenceNumber,
			Payload:        make([]byte, len(packet.Payload)),
			Signature:      packet.Signature,
			Timestamp:      packet.Timestamp,
			SenderID:       packet.SenderID,
			TargetIDs:      make([]string, len(packet.TargetIDs)),
		}
		copy(bufferedPacket.Payload, packet.Payload)
		copy(bufferedPacket.TargetIDs, packet.TargetIDs)
		c.buffer = append(c.buffer, bufferedPacket)
		c.bufferMu.Unlock()
		return nil
	}

	// 将数据推送到本地队列（如果有本地订阅者）
	if hasSubscribers {
		select {
		case c.dataQueue <- packet:
			// pushed locally
		case <-time.After(5 * time.Second):
			return fmt.Errorf("timeout pushing data to channel")
		}
	}

	// 转发到远端内核（如果有远端目标）
	if len(remoteTargetsByKernel) > 0 {
		if c.manager == nil || c.manager.forwardToKernel == nil {
			return fmt.Errorf("forwardToKernel callback not configured")
		}
		for rk, connectorIDs := range remoteTargetsByKernel {
			outPacket := &DataPacket{
				ChannelID:      packet.ChannelID,
				SequenceNumber: packet.SequenceNumber,
				Payload:        make([]byte, len(packet.Payload)),
				Signature:      packet.Signature,
				Timestamp:      packet.Timestamp,
				SenderID:       packet.SenderID,
				TargetIDs:      make([]string, len(connectorIDs)),
				MessageType:    packet.MessageType,
			}
			copy(outPacket.Payload, packet.Payload)
			copy(outPacket.TargetIDs, connectorIDs)
			if err := c.manager.forwardToKernel(rk, outPacket); err != nil {
				log.Printf("⚠ Failed to forward packet to kernel %s: %v", rk, err)
			}
		}
	}

	// 没有订阅者且没有目标接收者（包括远端），数据丢失（正常情况返回 nil）
	return nil
}

// Subscribe 订阅频道数据
func (c *Channel) Subscribe(subscriberID string) (chan *DataPacket, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Status != ChannelStatusActive {
		return nil, fmt.Errorf("channel is not active")
	}

	// 验证订阅者是否是接收方
	if !c.CanReceive(subscriberID) {
		return nil, fmt.Errorf("subscriber %s is not authorized to receive data from this channel", subscriberID)
	}

	// 检查是否已订阅
	if _, exists := c.subscribers[subscriberID]; exists {
		return nil, fmt.Errorf("already subscribed")
	}

	// 创建订阅通道
	subChan := make(chan *DataPacket, 100)
	c.subscribers[subscriberID] = subChan

	// 先发送暂存的数据（频道级别缓冲）
	c.bufferMu.Lock()
	bufferedPackets := make([]*DataPacket, len(c.buffer))
	copy(bufferedPackets, c.buffer)
	c.buffer = c.buffer[:0] // 清空缓冲区
	c.bufferMu.Unlock()

	// 获取连接器级别的离线缓冲数据
	connectorBufferedPackets := []*DataPacket{}
	if c.manager != nil {
		connectorBufferedPackets = c.manager.GetBufferedDataForConnector(subscriberID)
		log.Printf("🔍 Connector %s has %d buffered packets", subscriberID, len(connectorBufferedPackets))
	}

	// 合并所有缓冲数据
	allBufferedPackets := append(bufferedPackets, connectorBufferedPackets...)

	log.Printf("📊 Total buffered packets for %s: %d (channel: %d, connector: %d)",
		subscriberID, len(allBufferedPackets), len(bufferedPackets), len(connectorBufferedPackets))

	// 在goroutine中发送所有暂存的数据，避免阻塞
	go func() {
		if len(allBufferedPackets) > 0 {
			log.Printf("📤 Sending %d buffered packets to recovered connector %s", len(allBufferedPackets), subscriberID)
		}

		for _, packet := range allBufferedPackets {
			// 检查是否应该发送给此订阅者
			if shouldSendToSubscriber(packet, subscriberID) {
				select {
				case subChan <- packet:
					// 成功发送暂存数据
				case <-time.After(1 * time.Second):
					// 超时，跳过
					log.Printf("⚠️ Timeout sending buffered packet to %s", subscriberID)
				}
			}
		}
	}()

	return subChan, nil
}

// shouldSendToSubscriber 判断是否应该将数据包发送给订阅者
func shouldSendToSubscriber(packet *DataPacket, subscriberID string) bool {
	// 如果目标列表为空，广播给所有订阅者
	if len(packet.TargetIDs) == 0 {
		return true
	}
	// 检查订阅者是否在目标列表中
	for _, targetID := range packet.TargetIDs {
		if targetID == subscriberID {
			return true
		}
	}
	return false
}

// Unsubscribe 取消订阅
func (c *Channel) Unsubscribe(subscriberID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if subChan, exists := c.subscribers[subscriberID]; exists {
		close(subChan)
		delete(c.subscribers, subscriberID)
	}
}

// startDataDistribution 启动数据分发（将数据从队列分发到订阅者）
func (c *Channel) startDataDistribution() {
	for packet := range c.dataQueue {
		c.mu.RLock()
		subscribers := make(map[string]chan *DataPacket)
		for id, ch := range c.subscribers {
			subscribers[id] = ch
		}
		c.mu.RUnlock()

		// 分发到订阅者（根据目标列表）
		for subscriberID, subChan := range subscribers {
			// 检查是否应该发送给此订阅者
			if shouldSendToSubscriber(packet, subscriberID) {
				select {
				case subChan <- packet:
					// 成功发送
				case <-time.After(1 * time.Second):
					// 超时，跳过此订阅者
				}
			}
		}
	}
}

// GetSubscriberCount 获取订阅者数量
func (c *Channel) GetSubscriberCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.subscribers)
}

// IsSubscribed 检查连接器是否已订阅频道
func (c *Channel) IsSubscribed(subscriberID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, exists := c.subscribers[subscriberID]
	return exists
}

// SubscribeWithRecovery 订阅频道，支持重启恢复
func (c *Channel) SubscribeWithRecovery(subscriberID string, isRestartRecovery bool) (chan *DataPacket, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Status != ChannelStatusActive {
		return nil, fmt.Errorf("channel is not active")
	}

	// 验证订阅者是否是接收方
	if !c.CanReceive(subscriberID) {
		return nil, fmt.Errorf("subscriber %s is not authorized to receive data from this channel", subscriberID)
	}

	// 检查是否已订阅
	if _, exists := c.subscribers[subscriberID]; exists {
		return nil, fmt.Errorf("already subscribed")
	}

	// 创建订阅通道
	subChan := make(chan *DataPacket, 100)
	c.subscribers[subscriberID] = subChan

	// 先发送暂存的数据（频道级别缓冲）
	c.bufferMu.Lock()
	bufferedPackets := make([]*DataPacket, len(c.buffer))
	copy(bufferedPackets, c.buffer)
	c.buffer = c.buffer[:0] // 清空缓冲区
	c.bufferMu.Unlock()

	// 如果是重启恢复，获取连接器级别的离线缓冲数据
	connectorBufferedPackets := []*DataPacket{}
	if isRestartRecovery && c.manager != nil {
		connectorBufferedPackets = c.manager.GetBufferedDataForConnector(subscriberID)
		log.Printf("🔍 Connector %s has %d buffered packets (restart recovery)", subscriberID, len(connectorBufferedPackets))
	}

	// 合并所有缓冲数据
	allBufferedPackets := append(bufferedPackets, connectorBufferedPackets...)

	log.Printf("📊 Total buffered packets for %s: %d (channel: %d, connector: %d)",
		subscriberID, len(allBufferedPackets), len(bufferedPackets), len(connectorBufferedPackets))

	// 在goroutine中发送所有暂存的数据，避免阻塞
	go func() {
		if len(allBufferedPackets) > 0 {
			log.Printf("📤 Sending %d buffered packets to connector %s", len(allBufferedPackets), subscriberID)
		}
		for _, packet := range allBufferedPackets {
			select {
			case subChan <- packet:
			case <-time.After(5 * time.Second):
				log.Printf("⚠️ Timeout sending buffered packet to %s", subscriberID)
				return
			}
		}
	}()

	return subChan, nil
}


// CleanupInactiveChannels 清理不活跃的频道（超过1小时没有活动）
func (cm *ChannelManager) CleanupInactiveChannels(inactiveThreshold time.Duration) int {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	now := time.Now()
	cleaned := 0

	for channelID, channel := range cm.channels {
		if channel.Status == ChannelStatusClosed && now.Sub(channel.LastActivity) > inactiveThreshold {
			delete(cm.channels, channelID)
			cleaned++
		}
	}

	return cleaned
}

// ------------------------------------------------------------
// 连接器状态管理（重启恢复）
// ------------------------------------------------------------

// MarkConnectorOnline 标记连接器在线
func (cm *ChannelManager) MarkConnectorOnline(connectorID string) {
	cm.connectorMu.Lock()
	defer cm.connectorMu.Unlock()

	oldStatus := cm.connectorStatus[connectorID]
	cm.connectorStatus[connectorID] = ConnectorStatusOnline
	cm.lastActivity[connectorID] = time.Now()

	// 如果是从离线状态恢复，记录恢复事件
	if oldStatus == ConnectorStatusOffline {
		log.Printf("🔄 Connector %s recovered from offline state", connectorID)
	}
}

// MarkConnectorOffline 标记连接器离线
func (cm *ChannelManager) MarkConnectorOffline(connectorID string) {
	cm.connectorMu.Lock()
	defer cm.connectorMu.Unlock()

	cm.connectorStatus[connectorID] = ConnectorStatusOffline
	log.Printf("📴 Connector %s marked as offline", connectorID)
}

// IsConnectorOnline 检查连接器是否在线
func (cm *ChannelManager) IsConnectorOnline(connectorID string) bool {
	cm.connectorMu.RLock()
	defer cm.connectorMu.RUnlock()
	return cm.connectorStatus[connectorID] == ConnectorStatusOnline
}

// IsConnectorRestarting 检查连接器是否正在重启恢复
// 如果连接器最近（5秒内）没有活动，则认为是重启恢复
func (cm *ChannelManager) IsConnectorRestarting(connectorID string) bool {
	cm.connectorMu.RLock()
	defer cm.connectorMu.RUnlock()

	lastActivity, exists := cm.lastActivity[connectorID]
	if !exists {
		// 从来没有连接过，认为是新连接
		return false
	}

	// 如果最后活动时间超过5秒，认为是重启恢复
	return time.Since(lastActivity) > 5*time.Second
}

// BufferDataForOfflineConnector 为离线连接器缓冲数据
func (cm *ChannelManager) BufferDataForOfflineConnector(connectorID string, packet *DataPacket) {
	cm.connectorMu.Lock()
	defer cm.connectorMu.Unlock()

	// 检查缓冲区大小限制（每个连接器最多缓冲1000个数据包）
	if len(cm.connectorBuffers[connectorID]) >= 1000 {
		// 如果缓冲区满了，移除最旧的数据包
		cm.connectorBuffers[connectorID] = cm.connectorBuffers[connectorID][1:]
	}

	// 复制数据包
	bufferedPacket := &DataPacket{
		ChannelID:      packet.ChannelID,
		SequenceNumber: packet.SequenceNumber,
		Payload:        make([]byte, len(packet.Payload)),
		Signature:      packet.Signature,
		Timestamp:      packet.Timestamp,
		SenderID:       packet.SenderID,
		TargetIDs:      make([]string, len(packet.TargetIDs)),
	}
	copy(bufferedPacket.Payload, packet.Payload)
	copy(bufferedPacket.TargetIDs, packet.TargetIDs)

	// 添加到缓冲区
	cm.connectorBuffers[connectorID] = append(cm.connectorBuffers[connectorID], bufferedPacket)
}

// GetBufferedDataForConnector 获取连接器的缓冲数据
func (cm *ChannelManager) GetBufferedDataForConnector(connectorID string) []*DataPacket {
	cm.connectorMu.Lock()
	defer cm.connectorMu.Unlock()

	bufferedPackets := make([]*DataPacket, len(cm.connectorBuffers[connectorID]))
	copy(bufferedPackets, cm.connectorBuffers[connectorID])

	// 清空缓冲区
	cm.connectorBuffers[connectorID] = nil

	return bufferedPackets
}

// CleanupExpiredConnectorBuffers 清理过期的连接器缓冲数据
func (cm *ChannelManager) CleanupExpiredConnectorBuffers(maxAge time.Duration) int {
	cm.connectorMu.Lock()
	defer cm.connectorMu.Unlock()

	cleanupCount := 0
	cutoffTime := time.Now().Add(-maxAge)

	for connectorID, buffers := range cm.connectorBuffers {
		if len(buffers) == 0 {
			continue
		}

		// 检查连接器是否长时间未活动
		lastActivity, exists := cm.lastActivity[connectorID]
		if exists && lastActivity.Before(cutoffTime) {
			// 清理过期缓冲区
			delete(cm.connectorBuffers, connectorID)
			delete(cm.lastActivity, connectorID)
			delete(cm.connectorStatus, connectorID)
			cleanupCount++
			log.Printf("🧹 Cleaned up expired buffers for offline connector %s", connectorID)
		}
	}

	return cleanupCount
}

// StartBufferCleanupRoutine 启动缓冲区清理协程
func (cm *ChannelManager) StartBufferCleanupRoutine() {
	go func() {
		ticker := time.NewTicker(10 * time.Minute) // 每10分钟清理一次
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				cleanupCount := cm.CleanupExpiredConnectorBuffers(1 * time.Hour) // 清理1小时前离线的连接器缓冲
				if cleanupCount > 0 {
					log.Printf("🧹 Cleaned up buffers for %d offline connectors", cleanupCount)
				}
			}
		}
	}()
	log.Println("✓ Started connector buffer cleanup routine")
}

// ------------------------------------------------------------
// 频道订阅申请相关方法（频道外连接器使用）
// ------------------------------------------------------------

// RequestChannelSubscription 申请订阅频道
func (c *Channel) RequestChannelSubscription(subscriberID, role, reason string) (*PermissionChangeRequest, error) {
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()

	if c.Status != ChannelStatusActive {
		return nil, fmt.Errorf("channel is not active")
	}

	// 验证角色
	if role != "sender" && role != "receiver" {
		return nil, fmt.Errorf("invalid role: %s", role)
	}

	// 检查是否已经是参与者
	if c.IsParticipant(subscriberID) {
		return nil, fmt.Errorf("subscriber is already a channel participant")
	}

	// 检查是否已有待处理的申请
	for _, request := range c.permissionRequests {
		if request.RequesterID == subscriberID && request.Status == "pending" {
			return nil, fmt.Errorf("subscription request already exists for this subscriber")
		}
	}

	// 创建订阅申请（复用PermissionChangeRequest结构）
	request := &PermissionChangeRequest{
		RequestID:   uuid.New().String(),
		RequesterID: subscriberID,
		ChangeType:  "add_" + role, // 转换为对应的change_type
		TargetID:    subscriberID,
		Reason:      reason,
		Status:      "pending",
		CreatedAt:   time.Now(),
	}

	c.permissionRequests = append(c.permissionRequests, request)

	log.Printf("✓ Channel subscription requested: %s -> %s (%s)", subscriberID, c.ChannelID, role)
	return request, nil
}

// ApproveChannelSubscription 批准订阅申请
func (c *Channel) ApproveChannelSubscription(approverID, requestID string) (string, error) {
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()

	// 验证批准者是频道参与者
	if !c.IsParticipant(approverID) {
		return "", fmt.Errorf("approver is not a channel participant")
	}

	// 查找请求
	var request *PermissionChangeRequest
	var requestIndex int
	for i, req := range c.permissionRequests {
		if req.RequestID == requestID {
			request = req
			requestIndex = i
			break
		}
	}

	if request == nil {
		return "", fmt.Errorf("subscription request not found")
	}

	if request.Status != "pending" {
		return "", fmt.Errorf("request is not pending")
	}

	// 根据change_type添加参与者
	switch request.ChangeType {
	case "add_sender":
		// 检查是否已经在发送者列表中
		isSender := false
		for _, sender := range c.SenderIDs {
			if sender == request.TargetID {
				isSender = true
				break
			}
		}
		if !isSender {
			c.SenderIDs = append(c.SenderIDs, request.TargetID)
		}
	case "add_receiver":
		// 检查是否已经在接收者列表中
		isReceiver := false
		for _, receiver := range c.ReceiverIDs {
			if receiver == request.TargetID {
				isReceiver = true
				break
			}
		}
		if !isReceiver {
			c.ReceiverIDs = append(c.ReceiverIDs, request.TargetID)
		}
	default:
		return "", fmt.Errorf("invalid change type for subscription: %s", request.ChangeType)
	}

	// 更新请求状态
	now := time.Now()
	request.Status = "approved"
	request.ApprovedBy = approverID
	request.ApprovedAt = &now

	// 移除已处理的请求
	c.permissionRequests = append(c.permissionRequests[:requestIndex], c.permissionRequests[requestIndex+1:]...)

	log.Printf("✓ Channel subscription approved: %s -> %s (%s)", request.TargetID, c.ChannelID, request.ChangeType)
	return request.TargetID, nil
}

// RejectChannelSubscription 拒绝订阅申请
func (c *Channel) RejectChannelSubscription(approverID, requestID, reason string) error {
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()

	// 验证批准者是频道参与者
	if !c.IsParticipant(approverID) {
		return fmt.Errorf("approver is not a channel participant")
	}

	// 查找并更新请求状态
	for _, req := range c.permissionRequests {
		if req.RequestID == requestID {
			if req.Status != "pending" {
				return fmt.Errorf("request is not pending")
			}

			now := time.Now()
			req.Status = "rejected"
			req.RejectedBy = approverID
			req.RejectedAt = &now
			req.RejectReason = reason

			log.Printf("✓ Channel subscription rejected: %s -> %s (reason: %s)", req.TargetID, c.ChannelID, reason)
			return nil
		}
	}

	return fmt.Errorf("subscription request not found")
}

// ------------------------------------------------------------
// 权限变更相关方法（频道内连接器使用）
// ------------------------------------------------------------

// RequestPermissionChange 申请权限变更
func (c *Channel) RequestPermissionChange(requesterID, changeType, targetID, reason string) (*PermissionChangeRequest, error) {
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()

	if c.Status != ChannelStatusActive {
		return nil, fmt.Errorf("channel is not active")
	}

	// 验证请求者是频道参与者
	if !c.IsParticipant(requesterID) {
		return nil, fmt.Errorf("requester is not a channel participant")
	}

	// 验证变更类型
	validChangeTypes := map[string]bool{
		"add_sender":    true,
		"remove_sender": true,
		"add_receiver":  true,
		"remove_receiver": true,
	}
	if !validChangeTypes[changeType] {
		return nil, fmt.Errorf("invalid change type: %s", changeType)
	}

	// 验证目标连接器不是当前参与者
	switch changeType {
	case "add_sender":
		if c.CanSend(targetID) {
			return nil, fmt.Errorf("target is already a sender")
		}
	case "add_receiver":
		if c.CanReceive(targetID) {
			return nil, fmt.Errorf("target is already a receiver")
		}
	case "remove_sender":
		if !c.CanSend(targetID) {
			return nil, fmt.Errorf("target is not a sender")
		}
		// 至少保留一个发送方
		if len(c.SenderIDs) <= 1 {
			return nil, fmt.Errorf("cannot remove the last sender")
		}
	case "remove_receiver":
		if !c.CanReceive(targetID) {
			return nil, fmt.Errorf("target is not a receiver")
		}
		// 至少保留一个接收方
		if len(c.ReceiverIDs) <= 1 {
			return nil, fmt.Errorf("cannot remove the last receiver")
		}
	}

	requestID := uuid.New().String()
	request := &PermissionChangeRequest{
		RequestID:   requestID,
		RequesterID: requesterID,
		ChannelID:   c.ChannelID,
		ChangeType:  changeType,
		TargetID:    targetID,
		Reason:      reason,
		Status:      "pending",
		CreatedAt:   time.Now(),
	}

	c.permissionRequests = append(c.permissionRequests, request)
	c.LastActivity = time.Now()

	// 在统一频道中广播权限变更请求
	go c.broadcastPermissionRequest(request)

	return request, nil
}

// ApprovePermissionChange 批准权限变更
func (c *Channel) ApprovePermissionChange(approverID, requestID string) error {
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()

	if c.Status != ChannelStatusActive {
		return fmt.Errorf("channel is not active")
	}

	// 验证批准者权限
	if approverID != c.ApproverID {
		return fmt.Errorf("only the channel approver can approve permission changes")
	}

	// 查找请求
	var request *PermissionChangeRequest
	for _, req := range c.permissionRequests {
		if req.RequestID == requestID {
			request = req
			break
		}
	}
	if request == nil {
		return fmt.Errorf("permission change request not found")
	}

	if request.Status != "pending" {
		return fmt.Errorf("request is already %s", request.Status)
	}

	// 执行权限变更
	switch request.ChangeType {
	case "add_sender":
		c.SenderIDs = append(c.SenderIDs, request.TargetID)
	case "remove_sender":
		for i, id := range c.SenderIDs {
			if id == request.TargetID {
				c.SenderIDs = append(c.SenderIDs[:i], c.SenderIDs[i+1:]...)
				break
			}
		}
	case "add_receiver":
		c.ReceiverIDs = append(c.ReceiverIDs, request.TargetID)
	case "remove_receiver":
		for i, id := range c.ReceiverIDs {
			if id == request.TargetID {
				c.ReceiverIDs = append(c.ReceiverIDs[:i], c.ReceiverIDs[i+1:]...)
				break
			}
		}
	}

	// 更新请求状态
	now := time.Now()
	request.Status = "approved"
	request.ApprovedAt = &now
	request.ApprovedBy = approverID

	c.LastActivity = time.Now()

	// 在统一频道中广播批准结果
	go c.broadcastPermissionResult(requestID, "approved", approverID, "")

	return nil
}

// RejectPermissionChange 拒绝权限变更
func (c *Channel) RejectPermissionChange(approverID, requestID, reason string) error {
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()

	if c.Status != ChannelStatusActive {
		return fmt.Errorf("channel is not active")
	}

	// 验证批准者权限
	if approverID != c.ApproverID {
		return fmt.Errorf("only the channel approver can reject permission changes")
	}

	// 查找请求
	var request *PermissionChangeRequest
	for _, req := range c.permissionRequests {
		if req.RequestID == requestID {
			request = req
			break
		}
	}
	if request == nil {
		return fmt.Errorf("permission change request not found")
	}

	if request.Status != "pending" {
		return fmt.Errorf("request is already %s", request.Status)
	}

	// 更新请求状态
	request.Status = "rejected"
	request.RejectReason = reason

	c.LastActivity = time.Now()

	// 在统一频道中广播拒绝结果
	go c.broadcastPermissionResult(requestID, "rejected", approverID, reason)

	return nil
}

// GetPermissionRequests 获取权限变更请求列表
func (c *Channel) GetPermissionRequests() []*PermissionChangeRequest {
	c.permissionMu.RLock()
	defer c.permissionMu.RUnlock()

	requests := make([]*PermissionChangeRequest, len(c.permissionRequests))
	copy(requests, c.permissionRequests)
	return requests
}

// StartCleanupRoutine 启动清理协程
func (cm *ChannelManager) StartCleanupRoutine() {
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			cleaned := cm.CleanupInactiveChannels(1 * time.Hour)
			if cleaned > 0 {
				fmt.Printf("Cleaned up %d inactive channels\n", cleaned)
			}
		}
	}()
}



// GetAllChannels 获取所有频道
func (cm *ChannelManager) GetAllChannels() []*Channel {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	channels := make([]*Channel, 0, len(cm.channels))
	for _, channel := range cm.channels {
		channels = append(channels, channel)
	}
	return channels
}

// ControlMessage 控制频道消息结构
type ControlMessage struct {
	MessageType string    `json:"message_type"` // 消息类型：permission_request, permission_approved, permission_rejected, channel_proposal
	Timestamp   time.Time `json:"timestamp"`
	SenderID    string    `json:"sender_id"`

	// 权限变更相关字段
	PermissionRequest *PermissionRequestMessage `json:"permission_request,omitempty"`
	PermissionResult  *PermissionResultMessage  `json:"permission_result,omitempty"`

	// 频道提议相关字段
	ChannelProposal *ChannelProposalMessage `json:"channel_proposal,omitempty"`
}

// PermissionRequestMessage 权限变更请求消息
type PermissionRequestMessage struct {
	RequestID   string `json:"request_id"`
	ChannelID   string `json:"channel_id"`
	ChangeType  string `json:"change_type"`
	TargetID    string `json:"target_id"`
	Reason      string `json:"reason"`
}

// PermissionResultMessage 权限变更结果消息
type PermissionResultMessage struct {
	RequestID    string `json:"request_id"`
	ChannelID    string `json:"channel_id"`
	Action       string `json:"action"` // "approved" or "rejected"
	ApproverID   string `json:"approver_id"`
	RejectReason string `json:"reject_reason,omitempty"`
}

// ChannelProposalMessage 频道提议消息
type ChannelProposalMessage struct {
	ProposalID     string   `json:"proposal_id"`
	ChannelID      string   `json:"channel_id"`
	CreatorID      string   `json:"creator_id"`
	SenderIDs      []string `json:"sender_ids"`
	ReceiverIDs    []string `json:"receiver_ids"`
	DataTopic      string   `json:"data_topic"`
	Reason         string   `json:"reason"`
}

// broadcastPermissionRequest 在统一频道中广播权限变更请求
func (c *Channel) broadcastPermissionRequest(request *PermissionChangeRequest) {
	message := ControlMessage{
		MessageType: "permission_request",
		Timestamp:   time.Now(),
		SenderID:    request.RequesterID,
		PermissionRequest: &PermissionRequestMessage{
			RequestID:  request.RequestID,
			ChannelID:  request.ChannelID,
			ChangeType: request.ChangeType,
			TargetID:   request.TargetID,
			Reason:     request.Reason,
		},
	}

	c.sendControlMessage(c, message) // 在当前频道中发送
}

// broadcastPermissionResult 在统一频道中广播权限变更结果
func (c *Channel) broadcastPermissionResult(requestID, action, approverID, rejectReason string) {
	message := ControlMessage{
		MessageType: "permission_result",
		Timestamp:   time.Now(),
		SenderID:    approverID,
		PermissionResult: &PermissionResultMessage{
			RequestID:    requestID,
			ChannelID:    c.ChannelID,
			Action:       action,
			ApproverID:   approverID,
			RejectReason: rejectReason,
		},
	}

	c.sendControlMessage(c, message) // 在当前频道中发送
}

// broadcastChannelProposal 在统一频道中广播频道提议
func (c *Channel) broadcastChannelProposal(proposal *ChannelProposal) {
	message := ControlMessage{
		MessageType: "channel_proposal",
		Timestamp:   time.Now(),
		SenderID:    proposal.ApproverID,
		ChannelProposal: &ChannelProposalMessage{
			ProposalID:  proposal.ProposalID,
			ChannelID:   c.ChannelID,
			CreatorID:   c.CreatorID,
			SenderIDs:   proposal.SenderIDs,
			ReceiverIDs: proposal.ReceiverIDs,
			DataTopic:   c.DataTopic,
			Reason:      proposal.Reason,
		},
	}

	c.sendControlMessage(c, message) // 在当前频道中发送
}


// sendControlMessage 发送控制消息到统一频道
func (c *Channel) sendControlMessage(channel *Channel, message ControlMessage) {
	messageData, err := json.Marshal(message)
	if err != nil {
		log.Printf("⚠ Failed to marshal control message: %v", err)
		return
	}

	// 创建数据包并推送到统一频道
	sequenceNumber := int64(len(channel.buffer))
	packet := &DataPacket{
		ChannelID:      channel.ChannelID,
		SequenceNumber: sequenceNumber,
		Payload:        messageData,
		SenderID:       message.SenderID,
		TargetIDs:      []string{}, // 广播给所有订阅者
		Timestamp:      message.Timestamp.Unix(),
		MessageType:    MessageTypeControl, // 设置为控制消息类型
	}

	// 推送到频道的缓冲队列
	select {
	case channel.dataQueue <- packet:
		log.Printf("✓ Control message sent to channel %s: %s", channel.ChannelID, message.MessageType)
	default:
		log.Printf("⚠ Channel %s queue full, message dropped", channel.ChannelID)
	}
}

// -----------------------------------------------------------
// 配置文件管理说明：
// 现在频道配置文件由创建者自主指定路径，不再由内核统一管理目录。
// 这提供了更大的灵活性，支持不同的配置管理策略。
//
// 使用方式：
// 1. 创建频道时可选指定配置文件路径
//    channel, err := channelManager.CreateChannel("creator-1", "approver-1",
//        []string{"sender-1"}, []string{"receiver-1"}, "topic-1", false,
//        evidenceConfig, "/path/to/my-channel-config.json")
//
// 2. 从任意配置文件路径创建频道
//    channel, err := channelManager.CreateChannelFromConfig("/any/path/channel-config.json")
//
// 3. 保存频道配置到指定路径
//    err := channelManager.SaveChannelConfig(channelID, "频道名称", "频道描述")
//
// 配置文件JSON格式：
// {
//   "channel_id": "channel-123",
//   "name": "测试频道",
//   "description": "用于测试的频道",
//   "creator_id": "creator-1",
//   "approver_id": "approver-1",
//   "sender_ids": ["sender-1"],
//   "receiver_ids": ["receiver-1"],
//   "data_topic": "test-topic",
//   "encrypted": false,
//   "evidence_config": {
//     "mode": "external",
//     "strategy": "all",
//     "connector_id": "evidence-connector-1",
//     "backup_enabled": false,
//     "retention_days": 30,
//     "compress_data": true
//   },
//   "created_at": "2024-01-01T00:00:00Z",
//   "updated_at": "2024-01-01T00:00:00Z",
//   "version": 1
// }
//
// 优势：
// - 创建者可选择本地文件、共享存储或云存储
// - 支持不同的配置管理策略和工具链
// - 更符合分布式系统的设计理念
// - 内核职责简化，专注核心功能
// -----------------------------------------------------------

// -----------------------------------------------------------
// 外部存证连接器管理方法
// -----------------------------------------------------------

// RegisterEvidenceConnector 注册外部存证连接器
func (cm *ChannelManager) RegisterEvidenceConnector(connectorID, name, description string, capabilities []string, config map[string]string) (*EvidenceConnector, error) {
	cm.evidenceMu.Lock()
	defer cm.evidenceMu.Unlock()

	if connectorID == "" {
		return nil, fmt.Errorf("connector ID cannot be empty")
	}

	if _, exists := cm.evidenceConnectors[connectorID]; exists {
		return nil, fmt.Errorf("evidence connector %s already registered", connectorID)
	}

	connector := &EvidenceConnector{
		ConnectorID:   connectorID,
		Name:          name,
		Description:   description,
		Capabilities:  capabilities,
		Status:        ConnectorStatusOnline,
		RegisteredAt:  time.Now(),
		LastHeartbeat: time.Now(),
		Config:        config,
	}

	cm.evidenceConnectors[connectorID] = connector

	log.Printf("✓ Evidence connector registered: %s (%s)", connectorID, name)
	return connector, nil
}

// UnregisterEvidenceConnector 注销外部存证连接器
func (cm *ChannelManager) UnregisterEvidenceConnector(connectorID string) error {
	cm.evidenceMu.Lock()
	defer cm.evidenceMu.Unlock()

	if _, exists := cm.evidenceConnectors[connectorID]; !exists {
		return fmt.Errorf("evidence connector %s not found", connectorID)
	}

	delete(cm.evidenceConnectors, connectorID)
	log.Printf("✓ Evidence connector unregistered: %s", connectorID)
	return nil
}

// GetEvidenceConnector 获取存证连接器信息
func (cm *ChannelManager) GetEvidenceConnector(connectorID string) (*EvidenceConnector, error) {
	cm.evidenceMu.RLock()
	defer cm.evidenceMu.RUnlock()

	connector, exists := cm.evidenceConnectors[connectorID]
	if !exists {
		return nil, fmt.Errorf("evidence connector %s not found", connectorID)
	}

	return connector, nil
}

// ListEvidenceConnectors 列出所有已注册的存证连接器
func (cm *ChannelManager) ListEvidenceConnectors() []*EvidenceConnector {
	cm.evidenceMu.RLock()
	defer cm.evidenceMu.RUnlock()

	connectors := make([]*EvidenceConnector, 0, len(cm.evidenceConnectors))
	for _, connector := range cm.evidenceConnectors {
		connectors = append(connectors, connector)
	}

	return connectors
}

// UpdateEvidenceConnectorHeartbeat 更新存证连接器心跳
func (cm *ChannelManager) UpdateEvidenceConnectorHeartbeat(connectorID string) error {
	cm.evidenceMu.Lock()
	defer cm.evidenceMu.Unlock()

	connector, exists := cm.evidenceConnectors[connectorID]
	if !exists {
		return fmt.Errorf("evidence connector %s not found", connectorID)
	}

	connector.LastHeartbeat = time.Now()
	connector.Status = ConnectorStatusOnline

	return nil
}

// IsEvidenceConnectorAvailable 检查存证连接器是否可用
func (cm *ChannelManager) IsEvidenceConnectorAvailable(connectorID string) bool {
	cm.evidenceMu.RLock()
	defer cm.evidenceMu.RUnlock()

	connector, exists := cm.evidenceConnectors[connectorID]
	if !exists {
		return false
	}

	// 检查连接器是否在线且心跳在合理时间内
	return connector.Status == ConnectorStatusOnline &&
		   time.Since(connector.LastHeartbeat) < 5*time.Minute
}

// GetAvailableEvidenceConnectors 获取所有可用的存证连接器
func (cm *ChannelManager) GetAvailableEvidenceConnectors() []*EvidenceConnector {
	cm.evidenceMu.RLock()
	defer cm.evidenceMu.RUnlock()

	connectors := make([]*EvidenceConnector, 0)
	for _, connector := range cm.evidenceConnectors {
		if cm.IsEvidenceConnectorAvailable(connector.ConnectorID) {
			connectors = append(connectors, connector)
		}
	}

	return connectors
}

// StartEvidenceConnectorHeartbeatCheck 启动存证连接器心跳检查协程
func (cm *ChannelManager) StartEvidenceConnectorHeartbeatCheck() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute) // 每分钟检查一次
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				cm.checkEvidenceConnectorHeartbeats()
			}
		}
	}()
	log.Println("✓ Started evidence connector heartbeat check routine")
}

// checkEvidenceConnectorHeartbeats 检查存证连接器心跳状态
func (cm *ChannelManager) checkEvidenceConnectorHeartbeats() {
	cm.evidenceMu.Lock()
	defer cm.evidenceMu.Unlock()

	now := time.Now()
	offlineCount := 0

	for _, connector := range cm.evidenceConnectors {
		if now.Sub(connector.LastHeartbeat) > 5*time.Minute {
			if connector.Status == ConnectorStatusOnline {
				connector.Status = ConnectorStatusOffline
				offlineCount++
				log.Printf("📴 Evidence connector %s marked as offline", connector.ConnectorID)
			}
		}
	}

	if offlineCount > 0 {
		log.Printf("📊 Marked %d evidence connectors as offline", offlineCount)
	}
}


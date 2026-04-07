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
	MessageTypeAck      MessageType = "ack"      // ACK 消息
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
	EvidenceModeNone     EvidenceMode = "none"     // 不进行存证
	EvidenceModeInternal EvidenceMode = "internal" // 使用内核内置存证
)

// EvidenceStrategy 存证策略
type EvidenceStrategy string

const (
	EvidenceStrategyAll       EvidenceStrategy = "all"       // 存证所有消息
	EvidenceStrategyData      EvidenceStrategy = "data"      // 只存证数据消息
	EvidenceStrategyControl   EvidenceStrategy = "control"   // 只存证控制消息
	EvidenceStrategyImportant EvidenceStrategy = "important" // 只存证重要消息
)

// EvidenceConfig 存证配置（内核内置）
type EvidenceConfig struct {
	Mode          EvidenceMode     // 存证方式（仅支持 none/internal）
	Strategy      EvidenceStrategy // 存证策略
	RetentionDays int              // 存证数据保留天数
	CompressData bool             // 是否压缩存证数据
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

	// 当前流 FlowId（在 SubscribeData 收包时设置，SendAck 时使用）
	// 解决跨内核场景下 ACK 包丢失原始 FlowId 的问题
	currentFlowID string
	currentFlowMu sync.RWMutex

	// 权限变更管理
	permissionRequests []*PermissionChangeRequest // 权限变更请求列表
	permissionMu       sync.RWMutex               // 权限变更锁

	// 连接器状态管理（重启恢复）
	manager *ChannelManager // 指向ChannelManager的引用，用于访问连接器状态

	// 远端接收者映射：记录每个远端接收者ID（不含kernel前缀）所属的内核ID
	// 例如: connector-A -> kernel-2
	remoteReceivers map[string]string
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
	MessageType    MessageType // 消息类型（数据/控制/存证/ack）
	FlowID         string    // 业务流程ID，用于跟踪完整的数据传输过程
	IsFinal        bool      // 是否是最后一个数据包（流结束标志）
	DataHash       string    // 业务数据哈希（由内核计算并填充）
	// ACK 专用字段
	IsAck bool // 是否是 ACK 数据包
	// ACK_RECEIVED 防重标记：ForwardData 已记录后置 true，避免 SendAck 重复记录
	AckEvidenceRecorded bool
	// 发送完成回调：数据包成功发送给所有订阅者后调用
	// 用于在 ReceiveData goroutine 处理完成后才记录 evidence，避免因果顺序错误
	AfterSent func()
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

// ChannelManager 频道管理器
type ChannelManager struct {
	mu                   sync.RWMutex
	channels             map[string]*Channel
	notifyChannelCreated func(*Channel) // 频道创建通知回调
	permissionChangeCallback func(channelID, connectorID, changeType string) // 权限变更通知回调
	// notifyAfterChannelUpdate is called after a remote channel_update message is processed.
	// addedLocalReceivers contains bare connector IDs of newly added receivers local to this kernel.
	notifyAfterChannelUpdate func(channelID string, addedLocalReceivers []string)

	// 连接器状态跟踪（用于重启恢复）
	connectorStatus  map[string]ConnectorStatus // 连接器状态
	connectorBuffers map[string][]*DataPacket   // 离线连接器的个人缓冲区
	lastActivity     map[string]time.Time       // 连接器最后活动时间
	connectorMu      sync.RWMutex               // 连接器状态的锁

	// 频道配置管理（可选，由创建者指定配置文件路径时使用）
	configManager *ChannelConfigManager // 频道配置管理器
	// forwardToKernel 回调，用于将 DataPacket 转发到远端内核
	// isFinal: 是否是最后一个数据包（流结束标志）
	forwardToKernel func(kernelID string, packet *DataPacket, isFinal bool) error

	// GetNextHopKernel 回调，用于获取多跳路由的下一跳内核
	// 参数: currentKernelID(当前内核ID), targetKernelID(最终目标内核ID)
	// 返回: nextKernelID(下一跳内核ID), address(下一跳地址), port(下一跳端口), hopIndex(当前第几跳), totalHops(总跳数), found(是否找到路由)
	GetNextHopKernel func(currentKernelID, targetKernelID string) (nextKernelID, address string, port int, hopIndex, totalHops int, found bool)

	// GetPreviousHopKernel 回调，用于获取反向路由的上一跳内核（ACK 反向转发用）
	// 参数: sourceKernelID(当前内核ID)
	// 返回: prevKernelID(上一跳内核ID), address(上一跳地址), port(上一跳端口), found(是否找到路由)
	GetPreviousHopKernel func(sourceKernelID string) (prevKernelID, address string, port int, found bool)

	// 当前内核ID（用于跨内核通信时判断是否需要转发）
	kernelID string
}

// NewChannelManager 创建新的频道管理器
func NewChannelManager() *ChannelManager {
	return &ChannelManager{
		channels:            make(map[string]*Channel),
		connectorStatus:     make(map[string]ConnectorStatus),
		connectorBuffers:    make(map[string][]*DataPacket),
		lastActivity:        make(map[string]time.Time),
	}
}

// SetKernelID 设置当前内核ID
func (cm *ChannelManager) SetKernelID(kernelID string) {
	cm.kernelID = kernelID
}

// GetKernelID 获取当前内核ID
func (cm *ChannelManager) GetKernelID() string {
	return cm.kernelID
}

// CreateChannelWithID 创建一个频道并使用指定的 channelID（用于跨内核同步场景）
func (cm *ChannelManager) CreateChannelWithID(channelID, creatorID, approverID string, senderIDs, receiverIDs []string, dataTopic string, encrypted bool, evidenceConfig *EvidenceConfig, configFilePath string) (*Channel, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if channelID == "" {
		return nil, fmt.Errorf("channelID cannot be empty")
	}
	if creatorID == "" {
		return nil, fmt.Errorf("creatorID cannot be empty")
	}
	if len(senderIDs) == 0 || len(receiverIDs) == 0 {
		return nil, fmt.Errorf("senderIDs and receiverIDs must be provided")
	}

	// 如果已存在同 ID 的频道，返回错误
	if _, exists := cm.channels[channelID]; exists {
		return nil, fmt.Errorf("channel %s already exists", channelID)
	}

	channel := &Channel{
		ChannelID:          channelID,
		CreatorID:          creatorID,
		ApproverID:         approverID,
		SenderIDs:          make([]string, len(senderIDs)),
		ReceiverIDs:        make([]string, len(receiverIDs)),
		Encrypted:          encrypted,
		DataTopic:          dataTopic,
		Status:             ChannelStatusProposed,
		CreatedAt:          time.Now(),
		LastActivity:       time.Now(),
		EvidenceConfig:     evidenceConfig,
		ConfigFilePath:     configFilePath,
		dataQueue:          make(chan *DataPacket, 1000),
		subscribers:        make(map[string]chan *DataPacket),
		buffer:             make([]*DataPacket, 0),
		maxBufferSize:      10000,
		permissionRequests: make([]*PermissionChangeRequest, 0),
		manager:            cm,
		ChannelProposal: &ChannelProposal{
			ProposalID:        uuid.New().String(),
			Status:            NegotiationStatusProposed,
			Reason:            "",
			TimeoutSeconds:    300,
			CreatedAt:         time.Now(),
			SenderIDs:         senderIDs,
			ReceiverIDs:       receiverIDs,
			ApproverID:        approverID,
			SenderApprovals:   make(map[string]bool),
			ReceiverApprovals: make(map[string]bool),
		},
		remoteReceivers:    make(map[string]string), // 初始化远端接收者映射
	}

	copy(channel.SenderIDs, senderIDs)
	copy(channel.ReceiverIDs, receiverIDs)

	for _, id := range senderIDs {
		channel.ChannelProposal.SenderApprovals[id] = false
	}
	for _, id := range receiverIDs {
		channel.ChannelProposal.ReceiverApprovals[id] = false
	}

	// 创建者自动批准自己的提议（跨内核场景需要）
	// 需要匹配裸 ID 和带 kernel 前缀的 ID（如 kernel-1:connector-A）
	for _, id := range senderIDs {
		if id == creatorID || strings.HasSuffix(id, ":"+creatorID) {
			channel.ChannelProposal.SenderApprovals[id] = true
		}
	}
	for _, id := range receiverIDs {
		if id == creatorID || strings.HasSuffix(id, ":"+creatorID) {
			channel.ChannelProposal.ReceiverApprovals[id] = true
		}
	}

	// 初始化 remoteReceivers 映射（跨内核数据转发需要）
	currentKernelID := cm.kernelID
	for _, senderID := range senderIDs {
		if strings.Contains(senderID, ":") {
			parts := strings.SplitN(senderID, ":", 2)
			senderKernelID := parts[0]
			connectorID := parts[1]
			// 如果发送者在远程内核上，记录映射
			if senderKernelID != currentKernelID {
				channel.remoteReceivers[connectorID] = senderKernelID
			}
		}
	}
	for _, receiverID := range receiverIDs {
		if strings.Contains(receiverID, ":") {
			parts := strings.SplitN(receiverID, ":", 2)
			receiverKernelID := parts[0]
			connectorID := parts[1]
			// 如果接收者在远程内核上，记录映射
			if receiverKernelID != currentKernelID {
				channel.remoteReceivers[connectorID] = receiverKernelID
			}
		}
	}

	cm.channels[channelID] = channel

	return channel, nil
}

// SetChannelCreatedCallback 设置频道创建通知回调
func (cm *ChannelManager) SetChannelCreatedCallback(callback func(*Channel)) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.notifyChannelCreated = callback
	log.Printf("[OK] Channel creation callback set in ChannelManager")
}

// SetConfigManager 设置频道配置管理器
func (cm *ChannelManager) SetConfigManager(configManager *ChannelConfigManager) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.configManager = configManager
	log.Printf("[OK] Channel config manager set")
}

// SetForwardToKernel 设置将要用于跨内核转发数据的回调函数（由上层多内核管理器设置）
// isFinal: 是否是最后一个数据包（流结束标志）
func (cm *ChannelManager) SetForwardToKernel(fn func(kernelID string, packet *DataPacket, isFinal bool) error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.forwardToKernel = fn
}

// SetGetNextHopKernel 设置获取多跳路由下一跳的回调函数
// 这个回调用于在多跳场景下确定数据的下一跳目标
func (cm *ChannelManager) SetGetNextHopKernel(fn func(currentKernelID, targetKernelID string) (nextKernelID, address string, port int, hopIndex, totalHops int, found bool)) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.GetNextHopKernel = fn
	log.Printf("[OK] Multi-hop route callback set in ChannelManager")
}

// SetGetPreviousHopKernel 设置获取反向路由上一跳的回调函数
// 这个回调用于在多跳场景下确定 ACK 反向转发的下一跳目标
func (cm *ChannelManager) SetGetPreviousHopKernel(fn func(sourceKernelID string) (prevKernelID, address string, port int, found bool)) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.GetPreviousHopKernel = fn
	log.Printf("[OK] Multi-hop previous hop callback set in ChannelManager")
}

// SetPermissionChangeCallback 设置权限变更回调
func (cm *ChannelManager) SetPermissionChangeCallback(callback func(channelID, connectorID, changeType string)) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.permissionChangeCallback = callback
	log.Printf("[OK] Permission change callback set in ChannelManager")
}

// SetChannelUpdateNotifyCallback sets a callback that is invoked after a
// channel_update control message has been processed locally.  The callback
// receives the channelID and a list of bare connector IDs that were newly
// added as local receivers so the server layer can send them an ACCEPTED
// notification without any further locking.
func (cm *ChannelManager) SetChannelUpdateNotifyCallback(callback func(channelID string, addedLocalReceivers []string)) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.notifyAfterChannelUpdate = callback
}

// SetRemoteReceiver 设置远端接收者所属的内核（用于跨内核数据转发）
// connectorID: 连接器ID（不含kernel前缀）
// kernelID: 连接器所属的内核ID
func (c *Channel) SetRemoteReceiver(connectorID, kernelID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.remoteReceivers == nil {
		c.remoteReceivers = make(map[string]string)
	}
	c.remoteReceivers[connectorID] = kernelID
}

// GetRemoteKernelID 获取指定 connector 对应的远端内核 ID
// 返回值 ok 为 true 表示找到了映射
func (c *Channel) GetRemoteKernelID(connectorID string) (kernelID string, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.remoteReceivers == nil {
		return "", false
	}
	kernelID, ok = c.remoteReceivers[connectorID]
	return kernelID, ok
}

// SetCurrentFlowID 设置当前流的 FlowId（SendAck 时用于获取原始 FlowId）
func (c *Channel) SetCurrentFlowID(flowID string) {
	c.currentFlowMu.Lock()
	defer c.currentFlowMu.Unlock()
	c.currentFlowID = flowID
}

// GetCurrentFlowID 获取当前流的 FlowId（SendAck 时使用）
func (c *Channel) GetCurrentFlowID() string {
	c.currentFlowMu.RLock()
	defer c.currentFlowMu.RUnlock()
	return c.currentFlowID
}

// GetOriginKernelID 获取原始发送内核 ID（用于 SendAck 多跳转发 ACK）
// 从 SenderIDs 中查找带 kernel 前缀的发送者，返回第一个找到的内核 ID
func (c *Channel) GetOriginKernelID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, senderID := range c.SenderIDs {
		if strings.Contains(senderID, ":") {
			parts := strings.SplitN(senderID, ":", 2)
			return parts[0] // 返回 kernel ID
		}
	}
	return ""
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
	log.Printf("[OK] Default evidence config updated in ChannelManager")
	return nil
}

// GetDefaultEvidenceConfig 获取默认存证配置
func (cm *ChannelManager) GetDefaultEvidenceConfig() *EvidenceConfig {
	if cm.configManager == nil {
		return &EvidenceConfig{
			Mode:          EvidenceModeNone,
			Strategy:      EvidenceStrategyAll,
			RetentionDays: 30,
			CompressData:  true,
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
	// 需要匹配裸 ID 和带 kernel 前缀的 ID（如 kernel-1:connector-A）
	for _, id := range senderIDs {
		if id == creatorID || strings.HasSuffix(id, ":"+creatorID) {
			senderApprovals[id] = true
		}
	}
	for _, id := range receiverIDs {
		if id == creatorID || strings.HasSuffix(id, ":"+creatorID) {
			receiverApprovals[id] = true
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
		remoteReceivers:     make(map[string]string), // 初始化远端接收者映射
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

	// 检查频道状态 - 如果已经是 active 状态，直接返回成功
	// 注意：虽然返回成功，但如果是从远端转发来的 accept，可能需要重新通知本地参与者
	if channel.Status == ChannelStatusActive {
		log.Printf("[OK] Channel %s is already active, accept request processed", channelID)
		return nil
	}

	// 检查提议是否存在
	if channel.ChannelProposal == nil {
		return fmt.Errorf("channel proposal not found")
	}

	// 检查提议是否已超时
	if time.Since(channel.ChannelProposal.CreatedAt) > time.Duration(channel.ChannelProposal.TimeoutSeconds)*time.Second {
		return fmt.Errorf("channel proposal has expired")
	}

	// 剥离 kernel 前缀，提取纯 connector ID（格式: "kernelID:connectorID" 或直接是 "connectorID"）
	actualID := accepterID
	if idx := strings.LastIndex(accepterID, ":"); idx != -1 {
		actualID = accepterID[idx+1:]
	}

	// 支持带 kernel 前缀和去前缀两种格式，以适配跨内核转发的 accepterId
	candidateIDs := []string{accepterID}
	if actualID != accepterID {
		candidateIDs = append(candidateIDs, actualID)
	}

	// 还需要检查 remoteReceivers 映射，尝试添加 kernel 前缀进行匹配
	// 这样可以处理 "connector-U" 尝试匹配 "kernel-2:connector-U" 的情况
	if channel.remoteReceivers != nil {
		if kernelID, exists := channel.remoteReceivers[actualID]; exists {
			// 添加带 kernel 前缀的 ID 到候选列表
			idWithKernel := fmt.Sprintf("%s:%s", kernelID, actualID)
			candidateIDs = append(candidateIDs, idWithKernel)
		}
	}

	// 若 accepterID 是裸 connector ID（不含 ":"），也尝试用本内核 ID 拼接的格式
	// 这样本内核的连接器可以匹配审批映射中的 "kernelID:connectorID" 格式键
	if cm.kernelID != "" && !strings.Contains(accepterID, ":") {
		idWithLocalKernel := fmt.Sprintf("%s:%s", cm.kernelID, actualID)
		alreadyIn := false
		for _, cid := range candidateIDs {
			if cid == idWithLocalKernel {
				alreadyIn = true
				break
			}
		}
		if !alreadyIn {
			candidateIDs = append(candidateIDs, idWithLocalKernel)
		}
	}

	// 根据接受者身份更新确认状态
	isSender := false
	isReceiver := false
	for _, cid := range candidateIDs {
		if _, ok := channel.ChannelProposal.SenderApprovals[cid]; ok {
			channel.ChannelProposal.SenderApprovals[cid] = true
			isSender = true
		}
		if _, ok := channel.ChannelProposal.ReceiverApprovals[cid]; ok {
			channel.ChannelProposal.ReceiverApprovals[cid] = true
			isReceiver = true
		}
	}

	if !isSender && !isReceiver {
		return fmt.Errorf("only channel participants can accept channel proposal")
	}

	// 检查是否所有参与方都已确认
	// 需要区分本地频道和跨内核频道
	hasRemoteParticipants := false
	for _, id := range channel.SenderIDs {
		if strings.Contains(id, ":") {
			hasRemoteParticipants = true
			break
		}
	}
	if !hasRemoteParticipants {
		for _, id := range channel.ReceiverIDs {
			if strings.Contains(id, ":") {
				hasRemoteParticipants = true
				break
			}
		}
	}

	allApproved := true
	if hasRemoteParticipants {
		// 跨内核频道：需要所有参与者（包括远端）都确认
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
	} else {
		// 本地频道：只需要本地参与者确认
	for id, approved := range channel.ChannelProposal.SenderApprovals {
		// 跳过远端参与者（带 kernel 前缀）
		if strings.Contains(id, ":") {
			continue
		}
		if !approved {
			allApproved = false
				break
		}
	}
	if allApproved {
		for id, approved := range channel.ChannelProposal.ReceiverApprovals {
			// 跳过远端参与者（带 kernel 前缀）
			if strings.Contains(id, ":") {
				continue
			}
			if !approved {
				allApproved = false
					break
				}
			}
		}
	}

	if allApproved {
		// 所有参与方都确认了，激活频道
		channel.Status = ChannelStatusActive
		channel.ChannelProposal.Status = NegotiationStatusAccepted
		channel.LastActivity = time.Now()

		// 启动数据分发协程（确保数据能够被分发到订阅者）
		go channel.startDataDistribution()
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
		remoteReceivers: make(map[string]string), // 初始化远端接收者映射
	}

	// 复制切片
	copy(channel.SenderIDs, config.SenderIDs)
	copy(channel.ReceiverIDs, config.ReceiverIDs)

	// 初始化 remoteReceivers 映射（跨内核数据转发需要）
	currentKernelID := cm.kernelID
	for _, senderID := range config.SenderIDs {
		if strings.Contains(senderID, ":") {
			parts := strings.SplitN(senderID, ":", 2)
			senderKernelID := parts[0]
			connectorID := parts[1]
			// 如果发送者在远程内核上，记录映射
			if senderKernelID != currentKernelID {
				channel.remoteReceivers[connectorID] = senderKernelID
			}
		}
	}
	for _, receiverID := range config.ReceiverIDs {
		if strings.Contains(receiverID, ":") {
			parts := strings.SplitN(receiverID, ":", 2)
			receiverKernelID := parts[0]
			connectorID := parts[1]
			// 如果接收者在远程内核上，记录映射
			if receiverKernelID != currentKernelID {
				channel.remoteReceivers[connectorID] = receiverKernelID
			}
		}
	}

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

	log.Printf("[OK] Channel created from config file: %s", configFilePath)
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

		log.Printf("[OK] Channel config saved: %s", channel.ConfigFilePath)
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
		remoteReceivers:    make(map[string]string), // 初始化远端接收者映射
	}

	// 初始化 remoteReceivers 映射（跨内核数据转发需要）
	currentKernelID := cm.kernelID
	for _, senderID := range senderIDs {
		if strings.Contains(senderID, ":") {
			parts := strings.SplitN(senderID, ":", 2)
			senderKernelID := parts[0]
			connectorID := parts[1]
			// 如果发送者在远程内核上，记录映射
			if senderKernelID != currentKernelID {
				channel.remoteReceivers[connectorID] = senderKernelID
			}
		}
	}
	for _, receiverID := range receiverIDs {
		if strings.Contains(receiverID, ":") {
			parts := strings.SplitN(receiverID, ":", 2)
			receiverKernelID := parts[0]
			connectorID := parts[1]
			// 如果接收者在远程内核上，记录映射
			if receiverKernelID != currentKernelID {
				channel.remoteReceivers[connectorID] = receiverKernelID
			}
		}
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
		// 去掉 kernel 前缀再比较
		actualID := senderID
		if idx := strings.LastIndex(senderID, ":"); idx != -1 {
			actualID = senderID[idx+1:]
		}
		if connectorID == actualID {
			return true
		}
	}
	for _, receiverID := range c.ReceiverIDs {
		// 去掉 kernel 前缀再比较
		actualID := receiverID
		if idx := strings.LastIndex(receiverID, ":"); idx != -1 {
			actualID = receiverID[idx+1:]
		}
		if connectorID == actualID {
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
	// 提取裸 connectorID（去掉 kernel 前缀）
	rawConnectorID := connectorID
	if idx := strings.LastIndex(connectorID, ":"); idx != -1 {
		rawConnectorID = connectorID[idx+1:]
	}

	// 提取 kernelID（如果存在）
	kernelID := ""
	if idx := strings.LastIndex(connectorID, ":"); idx != -1 {
		kernelID = connectorID[:idx]
	}

	for _, senderID := range c.SenderIDs {
		// 精确匹配
		if connectorID == senderID {
			return true
		}
		// 检查 senderID 是否是 "kernel:connector" 格式
		if senderIdx := strings.LastIndex(senderID, ":"); senderIdx != -1 {
			senderKernelID := senderID[:senderIdx]
			senderRaw := senderID[senderIdx+1:]
			// 匹配：完整格式匹配 (kernelID:connector) 或裸 ID 匹配 (connector)
			// 也支持本地 connector 与远端 kernel:connector 格式匹配（当 kernelID 相同时）
			if rawConnectorID == senderRaw {
				return true
			}
			// 支持 kernel:connector 完整格式匹配
			if connectorID == senderID {
				return true
			}
			// 支持跨内核场景：本地 kernel 的 connector 与远端 kernel:connector 匹配
			if kernelID != "" && kernelID == senderKernelID && rawConnectorID == senderRaw {
				return true
			}
		} else {
			// senderID 是裸 ID，直接比较裸 ID
			if rawConnectorID == senderID {
				return true
			}
		}
	}
	return false
}

// CanReceive 检查连接器是否可以在此频道接收数据
func (c *Channel) CanReceive(connectorID string) bool {
	// 提取裸 connectorID（去掉 kernel 前缀）
	rawConnectorID := connectorID
	if idx := strings.LastIndex(connectorID, ":"); idx != -1 {
		rawConnectorID = connectorID[idx+1:]
	}

	for _, receiverID := range c.ReceiverIDs {
		// 精确匹配或裸 ID 匹配
		if connectorID == receiverID {
			return true
		}
		// 检查 receiverID 是否是 "kernel:connector" 格式
		if receiverIdx := strings.LastIndex(receiverID, ":"); receiverIdx != -1 {
			receiverRaw := receiverID[receiverIdx+1:]
			if rawConnectorID == receiverRaw {
				return true
			}
		}
		// 也处理 receiverID 不带内核前缀的情况
		if rawConnectorID == receiverID {
			return true
		}
	}
	return false
}

// IsSender 检查连接器是否是此频道的发送方
func (c *Channel) IsSender(connectorID string) bool {
	// 提取裸 connectorID（去掉 kernel 前缀）
	rawConnectorID := connectorID
	if idx := strings.LastIndex(connectorID, ":"); idx != -1 {
		rawConnectorID = connectorID[idx+1:]
	}

	for _, senderID := range c.SenderIDs {
		// 精确匹配或裸 ID 匹配
		if connectorID == senderID {
			return true
		}
		// 检查 senderID 是否是 "kernel:connector" 格式
		if senderIdx := strings.LastIndex(senderID, ":"); senderIdx != -1 {
			senderRaw := senderID[senderIdx+1:]
			if rawConnectorID == senderRaw {
				return true
			}
		}
		// 也处理 senderID 不带内核前缀的情况
		if rawConnectorID == senderID {
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
	// channel_update and permission_request must be processed even when the channel
	// is not yet active (e.g. PROPOSED state on a remote kernel), so handle them
	// before the status check and return early — they don't need subscriber delivery.
	if packet.MessageType == MessageTypeControl {
		var msg ControlMessage
		if err := json.Unmarshal(packet.Payload, &msg); err == nil {
			switch msg.MessageType {
			case "channel_update":
				if msg.ChannelUpdate != nil {
					c.handleChannelUpdate(msg.ChannelUpdate)
				}
				return nil
			case "permission_request":
				if msg.PermissionRequest != nil {
					c.StorePermissionRequestFromRemote(msg.PermissionRequest, packet.SenderID)
				}
				return nil
			}
		}
	}

	if c.Status != ChannelStatusActive {
		return fmt.Errorf("channel is not active")
	}

	// 权限检查：发送方必须是频道的发送方
	// 注意：ACK 消息由内核转发，SenderID 是 connector-U（接收方），不是发送方 connector-A
	// 因此 ACK 消息需要特殊处理，不能用 CanSend 验证
	// 由于 MessageType 在 gRPC 转发时会丢失，改用 IsAck 字段判断
	if packet.IsAck {
		// ACK 消息由内核转发，跳过发送方权限验证
		// IsAck 字段在 proto DataPacket 中定义，会正确序列化
	} else if packet.MessageType == MessageTypeEvidence {
		// 证据消息由内核发送，不应该被阻止，跳过验证
	} else if packet.MessageType == MessageTypeControl {
		// 控制消息可以由授权参与者发送
		if !c.CanSend(packet.SenderID) {
			return fmt.Errorf("sender %s is not authorized to send data in this channel", packet.SenderID)
		}
	} else {
		// 业务数据包需要验证发送方权限
		if !c.CanSend(packet.SenderID) {
			return fmt.Errorf("sender %s is not authorized to send data in this channel", packet.SenderID)
		}
	}

	c.LastActivity = time.Now()

	// 保存当前流的 FlowId（跨内核场景下 SendAck 时需要使用原始 FlowId）
	// 条件：数据包有 FlowId 且不是 ACK 包（ACK 包的 FlowId 为空，会覆盖已保存的正确值）
	if packet.FlowID != "" {
		c.SetCurrentFlowID(packet.FlowID)
	} else if packet.IsAck && c.GetCurrentFlowID() == "" {
		// ACK 包但没有已保存的 flowID，记录警告（理论上不应该发生）
		log.Printf("[WARN] PushData ACK with empty FlowId and no saved flowID: channelID=%s", packet.ChannelID)
	}

	// 不使用锁，直接检查 subscribers 和 remoteReceivers
	// 因为证据消息是内核内部产生的，不涉及并发修改
	hasSubscribers := len(c.subscribers) > 0

	// 确定需要接收此数据的目标接收者（多对多模式，支持所有接收方）
	targetReceivers := make([]string, 0, len(c.ReceiverIDs))
	targetReceivers = append(targetReceivers, c.ReceiverIDs...)

	// 处理目标列表，支持远端目标格式 kernelID:connectorID
	remoteTargetsByKernel := make(map[string][]string)
	localTargets := make([]string, 0)

	// offline 本地 targets
	offlineTargets := make([]string, 0)

	// ================================================================
	// ACK 反向路由（多跳场景）
	// 数据流正向：kernel-1 → kernel-2 → kernel-3 → connector-X
	// ACK 流反向：connector-X → kernel-3 → kernel-2 → kernel-1
	//
	// 正确的反向路由逻辑：
	// - kernel-3: GetPreviousHop(kernel-3) 返回 kernel-2，转发到 kernel-2
	// - kernel-2: GetPreviousHop(kernel-2) 返回 kernel-1，转发到 kernel-1
	// - kernel-1: GetPreviousHop(kernel-1) 返回空，停止转发
	//
	// 使用 GetPreviousHopKernel(currentKernelID) 获取反向下一跳
	// 签名追加由 kernel 层在 SendAck/ForwardData 中处理。
	// ================================================================
	isCrossKernelAck := false
	if packet.IsAck {
		currentKernelID := ""
		if c.manager != nil {
			currentKernelID = c.manager.kernelID
		}
		for _, targetID := range packet.TargetIDs {
			if strings.Contains(targetID, ":") {
				parts := strings.SplitN(targetID, ":", 2)
				originKernel := parts[0]
				// originKernel != currentKernelID 表示这是反向转发的 ACK，需要继续转发
				if originKernel != "" && originKernel != currentKernelID {
					// 需要反向转发：使用 GetPreviousHopKernel 获取当前内核的上一跳
					// GetPreviousHopKernel(currentKernelID) 返回 currentKernelID 的上一跳
					// 例如：kernel-2 调用 GetPreviousHopKernel(kernel-2) 返回 kernel-1
					if c.manager != nil && c.manager.GetPreviousHopKernel != nil {
						prevKernel, _, _, found := c.manager.GetPreviousHopKernel(currentKernelID)
						if found && prevKernel != "" && prevKernel != currentKernelID {
							isCrossKernelAck = true // 跳过本地队列
							// 转发到反向下一跳（上一跳内核）
							remoteTargetsByKernel[prevKernel] = []string{targetID}
							localTargets = nil // ACK 不发给本地订阅者
							log.Printf("[INFO] ACK reverse routing: %s -> %s (prev hop), origin=%s",
								currentKernelID, prevKernel, targetID)
							break
						}
					}
				}
			}
		}
		// 防止后续对空 TargetIDs 的 early return
		if len(packet.TargetIDs) == 0 {
			return nil
		}
	}

	// 跨内核 ACK 跳过本地队列（避免 connector-A 本地订阅者收到 kernel-3 签名的 ACK）
	if isCrossKernelAck {
		hasSubscribers = false
	}

	// ACK 反向路由场景下，跳过正常的 TargetIDs 处理
	// 因为 ACK 反向路由已经在上面处理过了
	if isCrossKernelAck {
		// 已经设置了 remoteTargetsByKernel 和 isCrossKernelAck
		// 跳过正常的 TargetIDs 处理，避免重复添加
		if len(remoteTargetsByKernel) == 0 {
			return nil
		}
	} else if len(packet.TargetIDs) > 0 {
		for _, targetID := range packet.TargetIDs {
			if strings.Contains(targetID, ":") {
				parts := strings.SplitN(targetID, ":", 2)
				kernelPart := parts[0]
				connectorPart := parts[1]
				remoteTargetsByKernel[kernelPart] = append(remoteTargetsByKernel[kernelPart], connectorPart)
			} else {
				// ACK 消息需要发送给原始发送方（发送方不是接收方，CanReceive 返回 false）
				// 绕过 CanReceive 检查，直接添加到本地目标
				if c.CanReceive(targetID) || packet.IsAck {
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
		// 广播模式：
		// - 将远端接收者 (kernelID:connectorID) 按内核分组用于转发
		// - 本地接收者仍按原逻辑判断是否已订阅/在线
		// - 使用 remoteReceivers 映射来识别实际属于远端的接收者
		// - 对于控制消息，还需要转发给发送方所属的内核

		// 获取当前内核ID（用于跳过转发到本地内核）
		currentKernelID := ""
		if c.manager != nil {
			currentKernelID = c.manager.kernelID
		}

		for _, receiverID := range c.ReceiverIDs {
			// 首先检查是否是跨内核格式
			if strings.Contains(receiverID, ":") {
				parts := strings.SplitN(receiverID, ":", 2)
				kernelPart := parts[0]
				connectorPart := parts[1]
				
				// 如果是当前内核的接收者，当作本地接收者处理
				if kernelPart == currentKernelID {
					// 本地接收者 - 需要正确处理跨内核格式
					// subscribers map 使用裸 connectorID 作为 key
					if _, subscribed := c.subscribers[connectorPart]; subscribed {
						// 订阅者已订阅，数据会通过订阅通道发送
						hasSubscribers = true
						continue // 已订阅，不再检查离线状态
					}
					// 未订阅才检查离线状态
					if c.manager != nil && !c.manager.IsConnectorOnline(connectorPart) {
						offlineTargets = append(offlineTargets, connectorPart)
					}
					continue
				}
				
				// 远端格式 kernelID:connectorID，跳过当前内核（避免循环转发）
				remoteTargetsByKernel[kernelPart] = append(remoteTargetsByKernel[kernelPart], connectorPart)
				continue
			}

			// 检查 remoteReceivers 映射，看是否这个接收者实际上是远端的
		// 需要先提取裸 connectorID 才能正确匹配
		checkReceiverID := receiverID
		if strings.Contains(receiverID, ":") {
			parts := strings.SplitN(receiverID, ":", 2)
			checkReceiverID = parts[1]
		}
		if kernelID, isRemote := c.remoteReceivers[checkReceiverID]; isRemote {
			// 如果映射指向当前内核，应该当作本地接收者处理
				if kernelID == currentKernelID {
				// 这是本地接收者（映射到当前内核）
				localReceiverID := checkReceiverID
				if _, subscribed := c.subscribers[localReceiverID]; subscribed {
					hasSubscribers = true
				} else {
					if c.manager != nil && !c.manager.IsConnectorOnline(localReceiverID) {
						offlineTargets = append(offlineTargets, localReceiverID)
					}
				}
					continue
				}
			// 这是真正的远端接收者，使用映射中的 kernelID
				remoteTargetsByKernel[kernelID] = append(remoteTargetsByKernel[kernelID], receiverID)
				continue
			}

		// 本地接收者 - 需要正确处理跨内核格式 (kernelID:connectorID)
			// subscribers map 使用裸 connectorID 作为 key
			localReceiverID := receiverID
			if strings.Contains(receiverID, ":") {
				parts := strings.SplitN(receiverID, ":", 2)
				localReceiverID = parts[1]
			}
			if _, subscribed := c.subscribers[localReceiverID]; subscribed {
				// 订阅者已订阅，数据会通过订阅通道发送
				hasSubscribers = true
			} else {
				// 未订阅才检查离线状态
				if c.manager != nil && !c.manager.IsConnectorOnline(localReceiverID) {
					offlineTargets = append(offlineTargets, localReceiverID)
				}
			}
		}

		// 对于控制消息（如权限变更请求），还需要转发给发送方所属的内核
		// 这样可以确保所有参与方都能收到权限请求，批准者才能正确处理
		if packet.MessageType == MessageTypeControl {
			for _, senderID := range c.SenderIDs {
				if strings.Contains(senderID, ":") {
					// 远端格式 kernelID:connectorID
					parts := strings.SplitN(senderID, ":", 2)
					kernelPart := parts[0]
					// 跳过当前内核
					if kernelPart == currentKernelID {
						continue
					}
					// 添加到转发目标（空 connector ID 表示转发给整个内核）
					// 避免重复添加
					if _, exists := remoteTargetsByKernel[kernelPart]; !exists {
						remoteTargetsByKernel[kernelPart] = append(remoteTargetsByKernel[kernelPart], "")
					}
				} else {
					// 检查 remoteReceivers 映射
					if kernelID, isRemote := c.remoteReceivers[senderID]; isRemote {
						// 跳过当前内核
						if kernelID == currentKernelID {
							continue
						}
						// 避免重复添加
						if _, exists := remoteTargetsByKernel[kernelID]; !exists {
							remoteTargetsByKernel[kernelID] = append(remoteTargetsByKernel[kernelID], "")
						}
					}
				}
			}
		}
	}

	// 为离线本地连接器缓冲数据
	for _, offlineTarget := range offlineTargets {
		if c.manager != nil {
			c.manager.BufferDataForOfflineConnector(offlineTarget, packet)
		}
	}

	// 决定是否需要频道级别的缓冲
	shouldBuffer := false
	if len(packet.TargetIDs) > 0 {
		// 检查指定的目标接收者是否有未订阅但在线的
		// ACK 消息的目标是发送方（CanReceive 返回 false），也参与缓冲判断
		for _, targetID := range packet.TargetIDs {
			if c.CanReceive(targetID) || packet.IsAck {
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

	// 重要：先发送到订阅通道，再处理频道级别缓冲
	// 这样订阅者可以立即收到数据，未订阅但在线的接收者也可以在订阅后收到缓冲的数据

	// 将数据推送到本地队列（如果有本地订阅者）
	if hasSubscribers {
		select {
		case c.dataQueue <- packet:
			// AfterSent 在数据包成功入队后立即调用（不依赖 startDataDistribution 的异步分发）
			// 保证因果顺序：DATA_SEND 由 forwardToKernel 先记录，然后 AfterSent 才记录 DATA_RECEIVE
			if packet.AfterSent != nil {
				packet.AfterSent()
			}
		case <-time.After(5 * time.Second):
			return fmt.Errorf("timeout pushing data to channel")
		}
	} else if packet.AfterSent != nil {
		// 跨内核频道无本地订阅者时，也需要调用 AfterSent
		packet.AfterSent()
	}

	if shouldBuffer {
		// 有指定的目标接收者未订阅（但在线），暂存数据等待他们订阅
		c.bufferMu.Lock()
		if len(c.buffer) >= c.maxBufferSize {
			c.bufferMu.Unlock()
			return fmt.Errorf("buffer is full, max size: %d", c.maxBufferSize)
		}
		// 复制数据包以避免并发问题
		bufferedPacket := &DataPacket{
			ChannelID:        packet.ChannelID,
			SequenceNumber:  packet.SequenceNumber,
			Payload:          make([]byte, len(packet.Payload)),
			Signature:        packet.Signature,
			Timestamp:        packet.Timestamp,
			SenderID:         packet.SenderID,
			TargetIDs:        make([]string, len(packet.TargetIDs)),
			FlowID:           packet.FlowID,
			IsFinal:          packet.IsFinal,
			DataHash:            packet.DataHash,
			AckEvidenceRecorded: packet.AckEvidenceRecorded,
			AfterSent:        packet.AfterSent,
		}
		copy(bufferedPacket.Payload, packet.Payload)
		copy(bufferedPacket.TargetIDs, packet.TargetIDs)
		c.buffer = append(c.buffer, bufferedPacket)
		c.bufferMu.Unlock()
		return nil
	}

	// 转发到远端内核（如果有远端目标）
	if len(remoteTargetsByKernel) > 0 {
		if c.manager == nil || c.manager.forwardToKernel == nil {
			return fmt.Errorf("forwardToKernel callback not configured")
		}

		currentKernelID := c.manager.kernelID

		for rk, connectorIDs := range remoteTargetsByKernel {
			// 确定实际转发的目标内核（多跳路由或直接转发）
			actualTargetKernel := rk

			// 如果目标是当前内核，只做本地处理，不转发
			if actualTargetKernel == currentKernelID {
				continue
			}

			// 检查是否配置了多跳路由
		if c.manager.GetNextHopKernel != nil {
			nextKernel, _, _, hopIndex, totalHops, found := c.manager.GetNextHopKernel(currentKernelID, rk)
			if found && nextKernel != "" {
				// 使用多跳路由，只转发到下一跳
				actualTargetKernel = nextKernel
				log.Printf("[INFO] Multi-hop: forwarding to next hop %s (hop %d/%d) instead of final target %s",
					nextKernel, hopIndex, totalHops, rk)

					// 如果是最后一跳，设置 IsFinal=true（通知下一跳这是最后一跳数据）
					// 注意：只有当 actualTargetKernel 就是最终目标 rk 时才设置 IsFinal
					// 否则需要在中间跳点处理转发
				}
			}

		outPacket := &DataPacket{
			ChannelID:           packet.ChannelID,
			SequenceNumber:     packet.SequenceNumber,
			Payload:             make([]byte, len(packet.Payload)),
			Signature:           "", // 稍后由 KernelSign 生成
			Timestamp:           packet.Timestamp,
			SenderID:            packet.SenderID,
			TargetIDs:           make([]string, len(connectorIDs)),
			MessageType:         packet.MessageType,
			FlowID:              packet.FlowID,
			IsFinal:             packet.IsFinal,
			DataHash:            packet.DataHash,
			IsAck:               packet.IsAck,
			AckEvidenceRecorded: packet.AckEvidenceRecorded,
			AfterSent:           packet.AfterSent,
		}
		copy(outPacket.Payload, packet.Payload)
		copy(outPacket.TargetIDs, connectorIDs)

		// 传递原始目标内核ID，用于存证记录
		// 使用 TargetIDs 的第一个元素来传递原始目标
		// 若 connectorID 已经包含 kernel 前缀（ACK 反向路由场景），直接使用，不再重复 prepend
		if len(connectorIDs) > 0 {
			rawConnectorID := connectorIDs[0]
			if strings.Contains(rawConnectorID, ":") {
				// 已经是完整格式，直接使用
				outPacket.TargetIDs[0] = rawConnectorID
			} else {
				// bare connector ID，需要 prepend kernel 前缀
				outPacket.TargetIDs[0] = rk + ":" + rawConnectorID
			}
		}

		// 转发签名 = business_data_chain 记录过的签名
		// 直接使用 packet.Signature，保证转发包携带的签名和 business_data_chain 表记录的签名一致
		// 下一跳 kernel 用此签名作为 prev_signature 计算自己的签名并写入自己的 business_data_chain
		outPacket.Signature = packet.Signature
		

		// 转发到下一跳 kernel（kernel-2/3）
		if err := c.manager.forwardToKernel(actualTargetKernel, outPacket, packet.IsFinal); err != nil {
			log.Printf("[WARN] Failed to forward packet to kernel %s: %v", actualTargetKernel, err)
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

	// 发送方（原始数据发送者）需要订阅才能接收 ACK 确认
	// 因此不能直接拒绝所有非接收方——ACK 消息需要发送给原始发送方
	if !c.CanReceive(subscriberID) && !c.IsSender(subscriberID) {
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

	// 在goroutine中发送所有暂存的数据，避免阻塞
	go func() {
		for _, packet := range allBufferedPackets {
			// 检查是否应该发送给此订阅者
			if c.shouldSendToSubscriber(packet, subscriberID) {
				select {
				case subChan <- packet:
					// 成功发送暂存数据
				case <-time.After(1 * time.Second):
					// 超时，跳过
					log.Printf("[WARN] Timeout sending buffered packet to %s", subscriberID)
				}
			}
		}
	}()

	return subChan, nil
}

// shouldSendToSubscriber 判断是否应该将数据包发送给订阅者
func (c *Channel) shouldSendToSubscriber(packet *DataPacket, subscriberID string) bool {
	// 不发送给发送方自己
	if subscriberID == packet.SenderID {
		return false
	}

	// 不发送给其他发送方（业务数据只应流向接收方）
	// 只有 ACK 消息和证据消息才需要发送给发送方（用于确认）
	if !packet.IsAck && packet.MessageType != MessageTypeEvidence {
		for _, senderID := range c.SenderIDs {
			// 处理跨内核格式 (kernelID:connectorID -> connectorID)
			checkID := senderID
			if strings.Contains(senderID, ":") {
				parts := strings.SplitN(senderID, ":", 2)
				checkID = parts[1]
			}
			if checkID == subscriberID {
				return false
			}
		}
	}

	// 如果目标列表为空，广播给所有订阅者
	if len(packet.TargetIDs) == 0 {
		return true
	}
	// 检查订阅者是否在目标列表中
	for _, targetID := range packet.TargetIDs {
		// 直接匹配
		if targetID == subscriberID {
			return true
		}
		// 处理跨内核格式 (kernel-ID:connectorID -> connectorID)
		if strings.Contains(targetID, ":") {
			parts := strings.SplitN(targetID, ":", 2)
			if len(parts) == 2 && parts[1] == subscriberID {
				return true
			}
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
			if c.shouldSendToSubscriber(packet, subscriberID) {
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

// StartDataDistribution exported wrapper to start the internal data distribution goroutine.
// Provided so callers from other packages (e.g. kernel server) can trigger distribution.
func (c *Channel) StartDataDistribution() {
	go c.startDataDistribution()
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

	// 发送方（原始数据发送者）需要订阅才能接收 ACK 确认
	// 因此不能拒绝非参与者——ACK 消息需要发送给原始发送方
	// 这里不再用 IsParticipant 检查，因为 AddParticipant 是空操作
	// 直接允许订阅者加入（由调用方 SubscribeData 负责权限控制）

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

	// 在goroutine中发送所有暂存的数据，避免阻塞
	go func() {
		for _, packet := range allBufferedPackets {
			select {
			case subChan <- packet:
			case <-time.After(5 * time.Second):
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
		log.Printf("[INFO] Connector %s recovered from offline state", connectorID)
	}
}

// MarkConnectorOffline 标记连接器离线
func (cm *ChannelManager) MarkConnectorOffline(connectorID string) {
	cm.connectorMu.Lock()
	defer cm.connectorMu.Unlock()

	cm.connectorStatus[connectorID] = ConnectorStatusOffline
	log.Printf("[INFO] Connector %s marked as offline", connectorID)
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
		ChannelID:        packet.ChannelID,
		SequenceNumber:  packet.SequenceNumber,
		Payload:          make([]byte, len(packet.Payload)),
		Signature:        packet.Signature,
		Timestamp:        packet.Timestamp,
		SenderID:         packet.SenderID,
		TargetIDs:        make([]string, len(packet.TargetIDs)),
		FlowID:           packet.FlowID,
		IsFinal:          packet.IsFinal,
		DataHash:         packet.DataHash,
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
			log.Printf("[INFO] Cleaned up expired buffers for offline connector %s", connectorID)
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
					log.Printf("[INFO] Cleaned up buffers for %d offline connectors", cleanupCount)
				}
			}
		}
	}()
	log.Println("[OK] Started connector buffer cleanup routine")
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

	log.Printf("[OK] Channel subscription requested: %s -> %s (%s)", subscriberID, c.ChannelID, role)
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
	for i, req := range c.permissionRequests {
		if req.RequestID == requestID {
			request = req
			_ = i // 使用 _ 忽略索引，因为我们不再删除请求
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
	// 使用 RequesterID 来获取带内核前缀的发送者ID（如果来自远程内核）
	senderID := request.TargetID
	if request.RequesterID != "" {
		// 如果请求者ID包含内核前缀，使用它；否则使用目标ID
		if strings.Contains(request.RequesterID, ":") {
			senderID = request.RequesterID
		}
	}
	switch request.ChangeType {
	case "add_sender":
		// 检查是否已经在发送者列表中
		isSender := false
		for _, sender := range c.SenderIDs {
			if sender == senderID {
				isSender = true
				break
			}
		}
		if isSender {
			return "", fmt.Errorf("connector %s is already a sender", senderID)
		}
		// 检查是否已经是接收者（禁止双向身份）
		for _, receiver := range c.ReceiverIDs {
			if receiver == senderID {
				return "", fmt.Errorf("connector %s is already a receiver, cannot add as sender", senderID)
			}
		}
		c.SenderIDs = append(c.SenderIDs, senderID)
	case "add_receiver":
		// 使用 RequesterID 来获取带内核前缀的接收者ID（如果来自远程内核）
		receiverID := request.TargetID
		if request.RequesterID != "" && strings.Contains(request.RequesterID, ":") {
			// 如果请求者ID包含内核前缀，接收者也来自同一个远程内核
			parts := strings.SplitN(request.RequesterID, ":", 2)
			kernelPart := parts[0]
			receiverID = kernelPart + ":" + request.TargetID
		}
		// 检查是否已经在接收者列表中
		isReceiver := false
		for _, receiver := range c.ReceiverIDs {
			if receiver == receiverID {
				isReceiver = true
				break
			}
		}
		if isReceiver {
			return "", fmt.Errorf("connector %s is already a receiver", receiverID)
		}
		// 检查是否已经是发送者（禁止双向身份）
		for _, sender := range c.SenderIDs {
			if sender == receiverID {
				return "", fmt.Errorf("connector %s is already a sender, cannot add as receiver", receiverID)
			}
		}
		c.ReceiverIDs = append(c.ReceiverIDs, receiverID)
	default:
		return "", fmt.Errorf("invalid change type for subscription: %s", request.ChangeType)
	}

	// 更新请求状态
	now := time.Now()
	request.Status = "approved"
	request.ApprovedBy = approverID
	request.ApprovedAt = &now

	// 注意：不再删除请求，以便其他批准方法（如 ApprovePermissionChange）也能看到请求已处理
	// c.permissionRequests = append(c.permissionRequests[:requestIndex], c.permissionRequests[requestIndex+1:]...)

	log.Printf("[OK] Channel subscription approved: %s -> %s (%s)", request.TargetID, c.ChannelID, request.ChangeType)

	// 通知远程内核频道已更新（发送方/接收方列表已变更）
	go c.notifyRemoteKernelsOfChannelUpdate()

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

			log.Printf("[OK] Channel subscription rejected: %s -> %s (reason: %s)", req.TargetID, c.ChannelID, reason)
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
		// 检查是否已经是接收者（禁止双向身份）
		if c.CanReceive(targetID) {
			return nil, fmt.Errorf("target is already a receiver, cannot add as sender")
		}
	case "add_receiver":
		if c.CanReceive(targetID) {
			return nil, fmt.Errorf("target is already a receiver")
		}
		// 检查是否已经是发送者（禁止双向身份）
		if c.CanSend(targetID) {
			return nil, fmt.Errorf("target is already a sender, cannot add as receiver")
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
	// 注意：不使用 defer unlock，因为需要在 goroutine 之前手动解锁

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
	// 注意：对于 add_sender，应该使用 TargetID（目标连接器），而不是 RequesterID（请求者）
	switch request.ChangeType {
	case "add_sender":
		// 如果目标ID已经包含内核前缀，直接使用
		// 否则，如果请求者来自远程内核，使用请求者的内核前缀
		// 如果请求者是本地内核的连接器，使用当前内核ID作为前缀
		senderID := request.TargetID
		senderKernelID := ""
		if strings.Contains(senderID, ":") {
			// 目标已经有内核前缀
			parts := strings.SplitN(senderID, ":", 2)
			senderKernelID = parts[0]
		} else if request.RequesterID != "" && strings.Contains(request.RequesterID, ":") {
			// 请求者来自远程内核，提取远程内核ID
			parts := strings.SplitN(request.RequesterID, ":", 2)
			senderKernelID = parts[0]
			senderID = senderKernelID + ":" + request.TargetID
		} else if c.manager != nil {
			// 请求者是本地内核的连接器，使用当前内核ID
			senderKernelID = c.manager.kernelID
			senderID = senderKernelID + ":" + request.TargetID
		}
		// 检查是否已经是接收者（禁止双向身份）
		for _, receiver := range c.ReceiverIDs {
			if receiver == senderID {
				return fmt.Errorf("connector %s is already a receiver, cannot add as sender", senderID)
			}
		}
		c.SenderIDs = append(c.SenderIDs, senderID)

		// 关键修复：无论发送者是否属于本地内核，都需要更新 remoteReceivers
		// 因为可能已经有远程接收者存在，需要能够转发数据到远程内核
		// 直接修改映射，避免调用 SetRemoteReceiver 导致的潜在问题
		if c.manager != nil && c.remoteReceivers != nil {
			// 遍历所有接收者，更新 remoteReceivers
			for _, receiverID := range c.ReceiverIDs {
				if strings.Contains(receiverID, ":") {
					parts := strings.SplitN(receiverID, ":", 2)
					receiverKernelID := parts[0]
					connectorID := parts[1]
					// 如果接收者在远程内核上，记录映射
					if receiverKernelID != c.manager.kernelID {
						c.remoteReceivers[connectorID] = receiverKernelID
					}
				}
			}
		}
	case "remove_sender":
		// 如果目标ID已经包含内核前缀，直接使用
		// 否则，如果请求者来自远程内核，使用请求者的内核前缀
		targetID := request.TargetID
		if !strings.Contains(targetID, ":") && request.RequesterID != "" && strings.Contains(request.RequesterID, ":") {
			parts := strings.SplitN(request.RequesterID, ":", 2)
			kernelPart := parts[0]
			targetID = kernelPart + ":" + request.TargetID
		}
		for i, id := range c.SenderIDs {
			if id == targetID {
				c.SenderIDs = append(c.SenderIDs[:i], c.SenderIDs[i+1:]...)
				break
			}
		}
	case "add_receiver":
		receiverID := request.TargetID
		receiverKernelID := ""
		if strings.Contains(receiverID, ":") {
			// 目标已经有内核前缀
			parts := strings.SplitN(receiverID, ":", 2)
			receiverKernelID = parts[0]
		} else if request.RequesterID != "" && strings.Contains(request.RequesterID, ":") {
			// 请求者来自远程内核，提取远程内核ID
			parts := strings.SplitN(request.RequesterID, ":", 2)
			receiverKernelID = parts[0]
			receiverID = receiverKernelID + ":" + request.TargetID
		} else if c.manager != nil {
			// 请求者是本地内核的连接器，使用当前内核ID
			receiverKernelID = c.manager.kernelID
			receiverID = receiverKernelID + ":" + request.TargetID
		}
		// 检查是否已经是发送者（禁止双向身份）
		for _, sender := range c.SenderIDs {
			if sender == receiverID {
				return fmt.Errorf("connector %s is already a sender, cannot add as receiver", receiverID)
			}
		}
		c.ReceiverIDs = append(c.ReceiverIDs, receiverID)

		// 关键修复：无论接收者是否属于本地内核，都需要更新 remoteReceivers
		// 因为可能已经有远程发送者存在，需要能够转发数据到远程内核
		// 直接修改映射，避免调用 SetRemoteReceiver 导致的潜在问题
		if c.manager != nil && c.remoteReceivers != nil {
			// 遍历所有发送者，更新 remoteReceivers
			for _, senderID := range c.SenderIDs {
				if strings.Contains(senderID, ":") {
					parts := strings.SplitN(senderID, ":", 2)
					senderKernelID := parts[0]
					connectorID := parts[1]
					// 如果发送者在远程内核上，记录映射
					if senderKernelID != c.manager.kernelID {
						c.remoteReceivers[connectorID] = senderKernelID
					}
				}
			}
		}
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
	
	// 通知远程内核频道已更新（发送方/接收方列表已变更）
	
	// 通知被添加的连接器：使用解析后的完整 kernel-qualified ID（而非原始 TargetID），
	// 确保回调可以正确进行跨内核转发。
	if c.manager != nil && c.manager.permissionChangeCallback != nil && (request.ChangeType == "add_sender" || request.ChangeType == "add_receiver") {
		// Resolve the full qualified connector ID from the updated participant lists
		resolvedTargetID := request.TargetID
		switch request.ChangeType {
		case "add_receiver":
			// Find the newly appended receiver (last element)
			if len(c.ReceiverIDs) > 0 {
				for _, rid := range c.ReceiverIDs {
					bare := rid
					if idx := strings.LastIndex(rid, ":"); idx != -1 {
						bare = rid[idx+1:]
					}
					targetBare := request.TargetID
					if idx := strings.LastIndex(request.TargetID, ":"); idx != -1 {
						targetBare = request.TargetID[idx+1:]
					}
					if bare == targetBare {
						resolvedTargetID = rid
					}
				}
			}
		case "add_sender":
			if len(c.SenderIDs) > 0 {
				for _, sid := range c.SenderIDs {
					bare := sid
					if idx := strings.LastIndex(sid, ":"); idx != -1 {
						bare = sid[idx+1:]
					}
					targetBare := request.TargetID
					if idx := strings.LastIndex(request.TargetID, ":"); idx != -1 {
						targetBare = request.TargetID[idx+1:]
					}
					if bare == targetBare {
						resolvedTargetID = sid
					}
				}
			}
		}
		go c.manager.permissionChangeCallback(c.ChannelID, resolvedTargetID, request.ChangeType)
	}
	
	// 先解锁，再调用 notifyRemoteKernelsOfChannelUpdate
	c.permissionMu.Unlock()
	go c.notifyRemoteKernelsOfChannelUpdate()
	
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

// GetPermissionRequestByID 根据请求ID获取权限变更请求
func (c *Channel) GetPermissionRequestByID(requestID string) *PermissionChangeRequest {
	c.permissionMu.RLock()
	defer c.permissionMu.RUnlock()

	for _, req := range c.permissionRequests {
		if req.RequestID == requestID {
			return req
		}
	}
	return nil
}

// StorePermissionRequestFromRemote 从远端内核接收并存储权限变更请求
// 当跨内核频道中一个内核收到权限请求时，需要在本地存储以便批准者可以批准
func (c *Channel) StorePermissionRequestFromRemote(permReq *PermissionRequestMessage, requesterID string) {
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()

	// 检查请求是否已经存在（避免重复存储）
	for _, req := range c.permissionRequests {
		if req.RequestID == permReq.RequestID {
			log.Printf("[INFO] Permission request %s already exists locally, skipping", permReq.RequestID)
			return
		}
	}

	// 验证权限变更请求
	// 检查是否已经有双向身份（禁止将接收方添加为发送方或将发送方添加为接收方）
	targetID := permReq.TargetID
	switch permReq.ChangeType {
	case "add_sender":
		// 检查是否已经是接收者（禁止双向身份）
		if c.CanReceive(targetID) {
			log.Printf("[FAILED] Rejecting permission request: target %s is already a receiver, cannot add as sender", targetID)
			return
		}
	case "add_receiver":
		// 检查是否已经是发送者（禁止双向身份）
		if c.CanSend(targetID) {
			log.Printf("[FAILED] Rejecting permission request: target %s is already a sender, cannot add as receiver", targetID)
			return
		}
	}

	// 创建新的权限变更请求
	request := &PermissionChangeRequest{
		RequestID:   permReq.RequestID,
		RequesterID: requesterID,
		ChannelID:   permReq.ChannelID,
		ChangeType:  permReq.ChangeType,
		TargetID:    permReq.TargetID,
		Reason:      permReq.Reason,
		Status:      "pending",
		CreatedAt:   time.Now(),
	}

	c.permissionRequests = append(c.permissionRequests, request)
	log.Printf("[OK] Stored permission request %s from remote kernel (requester: %s)", request.RequestID, requesterID)
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
	MessageType string    `json:"message_type"` // 消息类型：permission_request, permission_approved, permission_rejected, channel_proposal, channel_update
	Timestamp   time.Time `json:"timestamp"`
	SenderID    string    `json:"sender_id"`

	// 权限变更相关字段
	PermissionRequest *PermissionRequestMessage `json:"permission_request,omitempty"`
	PermissionResult  *PermissionResultMessage  `json:"permission_result,omitempty"`

	// 频道提议相关字段
	ChannelProposal *ChannelProposalMessage `json:"channel_proposal,omitempty"`

	// 频道更新相关字段
	ChannelUpdate *ChannelUpdateMessage `json:"channel_update,omitempty"`
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

// ChannelUpdateMessage 频道更新消息（用于跨内核同步发送方/接收方列表）
type ChannelUpdateMessage struct {
	ChannelID   string   `json:"channel_id"`
	CreatorID   string   `json:"creator_id"`
	SenderIDs   []string `json:"sender_ids"`
	ReceiverIDs []string `json:"receiver_ids"`
	Status      string   `json:"status"`
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

// notifyRemoteKernelsOfChannelUpdate 通知远程内核频道已更新（发送方/接收方列表变更）
func (c *Channel) notifyRemoteKernelsOfChannelUpdate() {
	if c.manager == nil || c.manager.forwardToKernel == nil {
		return
	}

	currentKernelID := c.manager.kernelID

	// 收集所有远程内核
	remoteKernels := make(map[string]bool)

	// 检查发送方
	for _, senderID := range c.SenderIDs {
		if strings.Contains(senderID, ":") {
			parts := strings.SplitN(senderID, ":", 2)
			kernelPart := parts[0]
			if kernelPart != currentKernelID {
				remoteKernels[kernelPart] = true
			}
		}
	}

	// 检查接收方
	for _, receiverID := range c.ReceiverIDs {
		if strings.Contains(receiverID, ":") {
			parts := strings.SplitN(receiverID, ":", 2)
			kernelPart := parts[0]
			if kernelPart != currentKernelID {
				remoteKernels[kernelPart] = true
			}
		}
	}

	// 也检查 remoteReceivers 映射
	for _, kernelID := range c.remoteReceivers {
		if kernelID != currentKernelID {
			remoteKernels[kernelID] = true
		}
	}
	
	// 发送频道更新通知到所有远程内核
	for kernelID := range remoteKernels {
		// 创建一个频道更新控制消息
		msg := ControlMessage{
			MessageType: "channel_update",
			Timestamp:   time.Now(),
			SenderID:    currentKernelID,
			ChannelUpdate: &ChannelUpdateMessage{
				ChannelID:   c.ChannelID,
				CreatorID:   c.CreatorID,
				SenderIDs:   c.SenderIDs,
				ReceiverIDs: c.ReceiverIDs,
				Status:      string(c.Status),
			},
		}

		msgBytes, err := json.Marshal(msg)
		if err != nil {
			log.Printf("[WARN] Failed to marshal channel update message: %v", err)
			continue
		}

		packet := &DataPacket{
			ChannelID:   c.ChannelID,
			Payload:     msgBytes,
			MessageType: MessageTypeControl,
			SenderID:    currentKernelID,
		}

		if err := c.manager.forwardToKernel(kernelID, packet, false); err != nil {
			log.Printf("[WARN] Failed to notify kernel %s of channel update: %v", kernelID, err)
		} else {
			log.Printf("[OK] Notified kernel %s of channel update for channel %s", kernelID, c.ChannelID)
		}
	}
}

// handleChannelUpdate 处理从远程内核接收的频道更新消息
func (c *Channel) handleChannelUpdate(update *ChannelUpdateMessage) {
	c.mu.Lock()

	// Snapshot old receiver set to detect newly added local receivers
	oldReceiversSet := make(map[string]bool, len(c.ReceiverIDs))
	for _, id := range c.ReceiverIDs {
		oldReceiversSet[id] = true
	}

	// 更新发送方列表
	c.SenderIDs = update.SenderIDs
	// 更新接收方列表
	c.ReceiverIDs = update.ReceiverIDs
	// 更新状态
	if update.Status != "" {
		c.Status = ChannelStatus(update.Status)
	}

	// 更新 remoteReceivers 映射（确保数据能正确转发）
	// 从 SenderIDs 中提取远端发送者
	for _, senderID := range c.SenderIDs {
		if strings.Contains(senderID, ":") {
			parts := strings.SplitN(senderID, ":", 2)
			kernelID := parts[0]
			connectorID := parts[1]
			// 更新映射（无论是否本地）
			if c.manager != nil {
				c.remoteReceivers[connectorID] = kernelID
			}
		}
	}
	// 从 ReceiverIDs 中提取远端接收者
	for _, receiverID := range c.ReceiverIDs {
		if strings.Contains(receiverID, ":") {
			parts := strings.SplitN(receiverID, ":", 2)
			kernelID := parts[0]
			connectorID := parts[1]
			// 更新映射（无论是否本地）
			if c.manager != nil {
				c.remoteReceivers[connectorID] = kernelID
			}
		}
	}

	log.Printf("[OK] Channel %s updated: senders=%v, receivers=%v, status=%s",
		c.ChannelID, c.SenderIDs, c.ReceiverIDs, c.Status)

	// Collect newly added local receivers so the server layer can notify them.
	currentKernelID := ""
	if c.manager != nil {
		currentKernelID = c.manager.kernelID
	}
	var addedLocalReceivers []string
	for _, receiverID := range c.ReceiverIDs {
		if oldReceiversSet[receiverID] {
			continue // already existed
		}
		// Determine whether this receiver belongs to the local kernel
		connID := receiverID
		isLocal := true
		if strings.Contains(receiverID, ":") {
			parts := strings.SplitN(receiverID, ":", 2)
			if parts[0] != currentKernelID {
				isLocal = false
			} else {
				connID = parts[1]
			}
		}
		if isLocal {
			addedLocalReceivers = append(addedLocalReceivers, connID)
		}
	}

	channelID := c.ChannelID
	manager := c.manager

	c.mu.Unlock() // unlock before goroutine to avoid holding lock during callbacks

	if manager != nil && manager.notifyAfterChannelUpdate != nil && len(addedLocalReceivers) > 0 {
		go manager.notifyAfterChannelUpdate(channelID, addedLocalReceivers)
	}
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
		log.Printf("[WARN] Failed to marshal control message: %v", err)
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
		log.Printf("[OK] Control message sent to channel %s: %s", channel.ChannelID, message.MessageType)
	default:
		log.Printf("[WARN] Channel %s queue full, message dropped", channel.ChannelID)
	}

	// 对于权限请求和权限批准结果，需要转发到远程内核
	// 使用异步调用避免阻塞 gRPC 响应
	if channel.manager != nil && (message.MessageType == "permission_request" || message.MessageType == "permission_result") {
		go channel.forwardControlMessageToRemoteKernels(packet)
	}
}

// forwardControlMessageToRemoteKernels 转发控制消息到远程内核
func (c *Channel) forwardControlMessageToRemoteKernels(packet *DataPacket) {
	if c.manager == nil || c.manager.forwardToKernel == nil {
		log.Printf("[WARN] forwardControlMessageToRemoteKernels: forwardToKernel is nil, cannot forward")
		return
	}

	// 获取当前内核ID
	currentKernelID := c.manager.kernelID
	log.Printf("[INFO] forwardControlMessageToRemoteKernels: currentKernelID=%s, SenderIDs=%v, ReceiverIDs=%v", 
		currentKernelID, c.SenderIDs, c.ReceiverIDs)

	// 对于权限请求，如果发送方没有内核前缀，添加当前内核前缀
	// 这样远程内核就知道发送方属于哪个内核
	if packet.SenderID != "" && !strings.Contains(packet.SenderID, ":") {
		// 检查发送方是否属于远程（通过 remoteReceivers）
		if remoteKernel, ok := c.remoteReceivers[packet.SenderID]; ok {
			// 发送方属于远程内核
			packet.SenderID = remoteKernel + ":" + packet.SenderID
		} else {
			// 发送方属于当前内核，添加当前内核前缀
			packet.SenderID = currentKernelID + ":" + packet.SenderID
		}
	}

	// 收集需要转发到的远程内核
	remoteKernels := make(map[string][]string)

	// 遍历发送方 - 转发到发送方所在的内核
	for _, senderID := range c.SenderIDs {
		if strings.Contains(senderID, ":") {
			parts := strings.SplitN(senderID, ":", 2)
			kernelPart := parts[0]
			// 跳过当前内核
			if kernelPart != currentKernelID {
				remoteKernels[kernelPart] = append(remoteKernels[kernelPart], "")
			}
		}
	}

	// 遍历接收方 - 转发到接收方所在的内核
	for _, receiverID := range c.ReceiverIDs {
		if strings.Contains(receiverID, ":") {
			parts := strings.SplitN(receiverID, ":", 2)
			kernelPart := parts[0]
			// 跳过当前内核
			if kernelPart != currentKernelID {
				if _, exists := remoteKernels[kernelPart]; !exists {
					remoteKernels[kernelPart] = append(remoteKernels[kernelPart], "")
				}
			}
		} else {
			// 检查 remoteReceivers 映射
			if kernelID, isRemote := c.remoteReceivers[receiverID]; isRemote {
				if kernelID != currentKernelID {
					if _, exists := remoteKernels[kernelID]; !exists {
						remoteKernels[kernelID] = append(remoteKernels[kernelID], "")
					}
				}
			}
		}
	}

	// 转发到所有远程内核
	log.Printf("[INFO] forwardControlMessageToRemoteKernels: remoteKernels=%v", remoteKernels)
	for kernelID := range remoteKernels {
		if err := c.manager.forwardToKernel(kernelID, packet, false); err != nil {
			log.Printf("[WARN] Failed to forward control message to kernel %s: %v", kernelID, err)
		} else {
			log.Printf("[OK] Forwarded control message to kernel %s", kernelID)
		}
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

package circulation

import (
	"fmt"
	"log"
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

// ChannelType 频道类型
type ChannelType int

const (
	ChannelTypeData    ChannelType = 1 // 真实数据频道
	ChannelTypeControl ChannelType = 2 // 控制数据频道
	ChannelTypeLog     ChannelType = 3 // 日志数据频道
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

// Channel 数据传输频道（单对单模式，明确指定发送方和接收方）
type Channel struct {
	ChannelID     string
	CreatorID     string        // 创建者ID（不一定是发送方）
	ApproverID    string        // 权限变更批准者ID（默认是创建者）
	SenderIDs     []string      // 发送方ID列表
	ReceiverIDs   []string      // 接收方ID列表
	ChannelType   ChannelType   // 频道类型（数据/控制/日志）
	Encrypted     bool          // 是否加密传输
	RelatedChannelIDs []string  // 关联频道ID列表（数据频道关联控制和日志频道）
	DataTopic     string
	Status        ChannelStatus
	CreatedAt     time.Time
	ClosedAt      *time.Time
	LastActivity  time.Time

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
}

// DataPacket 数据包
type DataPacket struct {
	ChannelID      string
	SequenceNumber int64
	Payload        []byte
	Signature      string
	Timestamp      int64
	SenderID       string   // 发送方ID
	TargetIDs      []string // 目标接收者ID列表（为空则广播给所有订阅者）
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

// ChannelManager 频道管理器
type ChannelManager struct {
	mu                   sync.RWMutex
	channels             map[string]*Channel
	notifyChannelCreated func(*Channel) // 频道创建通知回调
}

// NewChannelManager 创建新的频道管理器
func NewChannelManager() *ChannelManager {
	return &ChannelManager{
		channels: make(map[string]*Channel),
	}
}

// SetChannelCreatedCallback 设置频道创建通知回调
func (cm *ChannelManager) SetChannelCreatedCallback(callback func(*Channel)) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.notifyChannelCreated = callback
	log.Printf("✓ Channel creation callback set in ChannelManager")
}

// ProposeChannel 提议创建频道（协商第一阶段）
func (cm *ChannelManager) ProposeChannel(creatorID, approverID string, senderIDs, receiverIDs []string, dataTopic string, channelType ChannelType, encrypted bool, reason string, timeoutSeconds int32) (*Channel, error) {
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
		if allIDs[id] && channelType != ChannelTypeLog {
			return nil, fmt.Errorf("duplicate sender ID: %s", id)
		}
		allIDs[id] = true
	}
	for _, id := range receiverIDs {
		if id == "" {
			return nil, fmt.Errorf("receiver ID cannot be empty")
		}
		if allIDs[id] && channelType != ChannelTypeLog {
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

	channel := &Channel{
		ChannelID:         channelID,
		CreatorID:         creatorID,
		ApproverID:        approverID,
		SenderIDs:         senderIDs,
		ReceiverIDs:       receiverIDs,
		ChannelType:       channelType,
		Encrypted:         encrypted,
		RelatedChannelIDs: []string{}, // 协商阶段先为空
		DataTopic:         dataTopic,
		Status:            ChannelStatusProposed,
		CreatedAt:         time.Now(),
		LastActivity:      time.Now(),
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

		// 如果是数据频道，异步创建配套的存证频道（避免死锁）
		if channel.ChannelType == ChannelTypeData {
			log.Printf("🔄 Starting asynchronous evidence channel creation for data channel %s", channelID)
			go func() {
				log.Printf("🔄 Evidence channel goroutine started for %s", channelID)
				cm.mu.Lock()
				// 重新获取频道引用（以防在异步操作期间被修改）
				dataChannel, exists := cm.channels[channelID]
				cm.mu.Unlock()

				if !exists {
					log.Printf("⚠ Data channel %s no longer exists, skipping evidence channel creation", channelID)
					return
				}

				log.Printf("🔄 Creating evidence channel for data channel %s (type: %v)", channelID, dataChannel.ChannelType)
				evidenceChannel, err := cm.createEvidenceChannel(dataChannel)
				if err != nil {
					log.Printf("⚠ Failed to create evidence channel for %s: %v", channelID, err)
					return
				}

				// 更新关联频道ID
				cm.mu.Lock()
				if ch, exists := cm.channels[channelID]; exists {
					ch.RelatedChannelIDs = append(ch.RelatedChannelIDs, evidenceChannel.ChannelID)
					log.Printf("✓ Updated data channel %s with evidence channel %s", channelID, evidenceChannel.ChannelID)
				}
				evidenceChannel.RelatedChannelIDs = []string{channel.ChannelID}
				cm.mu.Unlock()

				log.Printf("✓ Created evidence channel %s for data channel %s", evidenceChannel.ChannelID, channelID)

				// 发送evidence频道创建通知（关键修复！）
				if cm.notifyChannelCreated != nil {
					log.Printf("📢 Sending notification for evidence channel %s", evidenceChannel.ChannelID)
					cm.notifyChannelCreated(evidenceChannel)
				} else {
					log.Printf("⚠ No notification callback set for evidence channel %s", evidenceChannel.ChannelID)
				}
			}()
		}
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

// CreateChannel 创建新的数据传输频道（单对单模式，明确指定发送方和接收方）
func (cm *ChannelManager) CreateChannel(creatorID, approverID string, senderIDs, receiverIDs []string, dataTopic string, channelType ChannelType, encrypted bool, relatedChannelIDs []string) (*Channel, error) {
	// 创建主频道
	channel, err := cm.createChannelInternal(creatorID, approverID, senderIDs, receiverIDs, dataTopic, channelType, encrypted, relatedChannelIDs)
	if err != nil {
		return nil, err
	}

	// 如果是数据频道，自动创建配套的存证频道
	if channelType == ChannelTypeData {
		evidenceChannel, err := cm.createEvidenceChannel(channel)
		if err != nil {
			// 如果存证频道创建失败，关闭主频道
			cm.CloseChannel(channel.ChannelID)
			return nil, fmt.Errorf("failed to create evidence channel: %w", err)
		}

		// 更新关联频道ID
		cm.mu.Lock()
		channel.RelatedChannelIDs = append(channel.RelatedChannelIDs, evidenceChannel.ChannelID)
		evidenceChannel.RelatedChannelIDs = []string{channel.ChannelID}
		cm.mu.Unlock()
	}

	return channel, nil
}

// createChannelInternal 创建频道的核心逻辑（不包含存证频道创建）
func (cm *ChannelManager) createChannelInternal(creatorID, approverID string, senderIDs, receiverIDs []string, dataTopic string, channelType ChannelType, encrypted bool, relatedChannelIDs []string) (*Channel, error) {
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
		if allIDs[id] && channelType != ChannelTypeLog {
			return nil, fmt.Errorf("duplicate sender ID: %s", id)
		}
		allIDs[id] = true
	}
	for _, id := range receiverIDs {
		if id == "" {
			return nil, fmt.Errorf("receiver ID cannot be empty")
		}
		if allIDs[id] && channelType != ChannelTypeLog {
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
		ChannelType:        channelType,
		Encrypted:          encrypted,
		RelatedChannelIDs:  relatedChannelIDs,
		DataTopic:          dataTopic,
		Status:             ChannelStatusActive,
		CreatedAt:          time.Now(),
		LastActivity:       time.Now(),
		dataQueue:          make(chan *DataPacket, 1000), // 缓冲队列
		subscribers:        make(map[string]chan *DataPacket),
		buffer:             make([]*DataPacket, 0),
		maxBufferSize:      10000, // 最多暂存10000个数据包
		permissionRequests: make([]*PermissionChangeRequest, 0),
	}

	cm.channels[channelID] = channel

	// 启动数据分发协程
	go channel.startDataDistribution()

	return channel, nil
}

// CreateChannelGroup 创建频道组（数据频道+控制频道+日志频道）
func (cm *ChannelManager) CreateChannelGroup(creatorID, approverID string, senderIDs, receiverIDs []string, dataTopic string) (dataChannel, controlChannel, logChannel *Channel, err error) {
	// 创建数据频道（加密）
	dataChannel, err = cm.CreateChannel(creatorID, approverID, senderIDs, receiverIDs, dataTopic, ChannelTypeData, true, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create data channel: %w", err)
	}

	// 创建控制频道（明文）
	controlChannel, err = cm.CreateChannel(creatorID, approverID, senderIDs, receiverIDs, dataTopic+"-control", ChannelTypeControl, false, nil)
	if err != nil {
		// 如果控制频道创建失败，关闭数据频道
		cm.CloseChannel(dataChannel.ChannelID)
		return nil, nil, nil, fmt.Errorf("failed to create control channel: %w", err)
	}

	// 创建日志频道（明文）
	logChannel, err = cm.CreateChannel(creatorID, approverID, senderIDs, receiverIDs, dataTopic+"-log", ChannelTypeLog, false, nil)
	if err != nil {
		// 如果日志频道创建失败，关闭数据频道和控制频道
		cm.CloseChannel(dataChannel.ChannelID)
		cm.CloseChannel(controlChannel.ChannelID)
		return nil, nil, nil, fmt.Errorf("failed to create log channel: %w", err)
	}

	// 更新关联频道ID
	cm.mu.Lock()
	dataChannel.RelatedChannelIDs = []string{controlChannel.ChannelID, logChannel.ChannelID}
	controlChannel.RelatedChannelIDs = []string{dataChannel.ChannelID, logChannel.ChannelID}
	logChannel.RelatedChannelIDs = []string{dataChannel.ChannelID, controlChannel.ChannelID}
	cm.mu.Unlock()

	return dataChannel, controlChannel, logChannel, nil
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

	// 如果指定了目标列表，验证是否包含有效的接收方
	if len(packet.TargetIDs) > 0 {
		// 验证目标列表中的接收者是否都是频道接收方
		validTargets := make([]string, 0)
		for _, targetID := range packet.TargetIDs {
			if c.CanReceive(targetID) {
				validTargets = append(validTargets, targetID)
			}
		}
		if len(validTargets) == 0 {
			// 目标列表中没有有效的接收方，数据不会发送
			return nil
		}
		targetReceivers = validTargets
	}
	
	// 检查所有目标接收者是否都已订阅
	allTargetsSubscribed := true
	if len(targetReceivers) > 0 {
		for _, targetID := range targetReceivers {
			if _, subscribed := c.subscribers[targetID]; !subscribed {
				allTargetsSubscribed = false
				break
			}
		}
	} else {
		// 没有目标接收者（只有发送者自己），不需要缓冲
		allTargetsSubscribed = true
	}
	c.mu.RUnlock()

	if !allTargetsSubscribed {
		// 有目标接收者未订阅，暂存数据
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

	// 所有目标接收者都已订阅，直接发送到队列
	if hasSubscribers {
		select {
		case c.dataQueue <- packet:
			return nil
		case <-time.After(5 * time.Second):
			return fmt.Errorf("timeout pushing data to channel")
		}
	}
	
	// 没有订阅者且没有目标接收者，数据丢失（这种情况不应该发生）
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

	// 先发送暂存的数据
	c.bufferMu.Lock()
	bufferedPackets := make([]*DataPacket, len(c.buffer))
	copy(bufferedPackets, c.buffer)
	c.buffer = c.buffer[:0] // 清空缓冲区
	c.bufferMu.Unlock()

	// 在goroutine中发送暂存的数据，避免阻塞
	go func() {
		for _, packet := range bufferedPackets {
			// 检查是否应该发送给此订阅者
			if shouldSendToSubscriber(packet, subscriberID) {
				select {
				case subChan <- packet:
					// 成功发送暂存数据
				case <-time.After(1 * time.Second):
					// 超时，跳过
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

// createEvidenceChannel 为数据频道创建配套的存证频道
func (cm *ChannelManager) createEvidenceChannel(dataChannel *Channel) (*Channel, error) {
	// 存证频道包含所有数据传输的参与者（发送方+接收方）
	allParticipants := append(dataChannel.SenderIDs, dataChannel.ReceiverIDs...)

	// 存证频道是广播模式：所有参与者都可以发送和接收存证数据
	// 但是createChannelInternal不允许同一个ID既是发送方又是接收方
	// 所以我们需要为evidence频道创建特殊的角色分配

	// 对于evidence频道，我们将所有参与者都设为发送方，所有参与者都设为接收方
	// 但要避免ID冲突，所以我们使用不同的ID列表来绕过验证
	evidenceSenders := make([]string, len(allParticipants))
	evidenceReceivers := make([]string, len(allParticipants))
	copy(evidenceSenders, allParticipants)
	copy(evidenceReceivers, allParticipants)

	evidenceTopic := dataChannel.DataTopic + "-evidence"

	evidenceChannel, err := cm.createChannelInternal(
		dataChannel.CreatorID,           // 存证频道创建者与数据频道相同
		dataChannel.ApproverID,          // 存证频道批准者与数据频道相同
		evidenceSenders,                 // 发送方：所有参与者都可以发送存证数据
		evidenceReceivers,               // 接收方：所有参与者都可以接收存证数据
		evidenceTopic,                   // 主题：数据主题 + "-evidence"
		ChannelTypeLog,                  // 类型：日志频道（明文传输）
		false,                           // 不加密（存证数据本身需要可验证）
		nil,                             // 关联频道ID（稍后设置）
	)

	if err != nil {
		return nil, err
	}

	return evidenceChannel, nil
}




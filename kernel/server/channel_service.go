package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	pb "github.com/trusted-space/kernel/proto/kernel/v1"
	"github.com/trusted-space/kernel/kernel/circulation"
	"github.com/trusted-space/kernel/kernel/control"
	"github.com/trusted-space/kernel/kernel/database"
	"github.com/trusted-space/kernel/kernel/evidence"
	"github.com/trusted-space/kernel/kernel/security"
)

// NotificationManager 通知管理器，管理接收方的通知通道
type NotificationManager struct {
	mu             sync.RWMutex
	notifications  map[string]chan *pb.ChannelNotification // key: receiverID
	channelManager *circulation.ChannelManager
	registry       *control.Registry
}

// NewNotificationManager 创建通知管理器
func NewNotificationManager(channelManager *circulation.ChannelManager, registry *control.Registry) *NotificationManager {
	return &NotificationManager{
		notifications:  make(map[string]chan *pb.ChannelNotification),
		channelManager: channelManager,
		registry:       registry,
	}
}

// Register 注册接收方的通知通道
func (nm *NotificationManager) Register(receiverID string) chan *pb.ChannelNotification {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	// 如果已存在，先关闭旧的
	if oldChan, exists := nm.notifications[receiverID]; exists {
		close(oldChan)
	}

	// 创建新的通知通道
	notifyChan := make(chan *pb.ChannelNotification, 10)
	nm.notifications[receiverID] = notifyChan
	return notifyChan
}

// Unregister 注销接收方的通知通道
func (nm *NotificationManager) Unregister(receiverID string) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if notifyChan, exists := nm.notifications[receiverID]; exists {
		close(notifyChan)
		delete(nm.notifications, receiverID)
	}
}

// Notify 通知接收方有新频道创建，并根据状态决定是否自动订阅
func (nm *NotificationManager) Notify(receiverID string, notification *pb.ChannelNotification) error {
	maxRetries := 3
	retryDelay := 1 * time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		nm.mu.RLock()
		notifyChan, exists := nm.notifications[receiverID]
		nm.mu.RUnlock()

		if !exists {
			if attempt < maxRetries {
				log.Printf("[WARN] Notification attempt %d/%d for %s: no listener, waiting %v...",
					attempt+1, maxRetries+1, receiverID, retryDelay)
				time.Sleep(retryDelay)
				continue
			}
			log.Printf("[WARN] Notification failed for %s: no listener registered after %d retries", receiverID, maxRetries+1)
			// 不返回错误，因为通知失败不应该阻断主要流程
			return nil
		}

		// 检查连接器状态，决定是否自动订阅
		isActive := nm.registry.IsActive(receiverID)

		// 发送通知（所有连接器都会收到通知）
		select {
		case notifyChan <- notification:
			// 通知发送成功
		case <-time.After(5 * time.Second):
			if attempt < maxRetries {
				log.Printf("[WARN] Notification timeout for %s, attempt %d/%d, retrying...",
					receiverID, attempt+1, maxRetries+1)
				time.Sleep(retryDelay)
				continue
			}
			log.Printf("[WARN] Notification failed for %s: timeout after %d retries", receiverID, maxRetries+1)
			// 不返回错误，继续执行
			return nil
		}

		// 如果连接器处于活跃状态，自动订阅频道
		if isActive {
			go nm.autoSubscribe(receiverID, notification.ChannelId)
		}

		return nil
	}

	return nil
}

// autoSubscribe 自动订阅频道（仅对活跃状态的连接器）
// 注意：实际的订阅需要连接器端通过SubscribeData RPC完成
// 这里我们只是标记连接器应该自动订阅，连接器收到通知后会检查自己的状态
// 如果处于active状态，连接器会自动调用SubscribeData进行订阅
func (nm *NotificationManager) autoSubscribe(connectorID, channelID string) {
	// 获取频道
	channel, err := nm.channelManager.GetChannel(channelID)
	if err != nil {
		// 频道不存在或已关闭，不进行订阅
		return
	}

	// 检查连接器是否是频道参与者
	if !channel.IsParticipant(connectorID) {
		// 如果不是参与者，先添加为参与者
		if err := channel.AddParticipant(connectorID); err != nil {
			return
		}
	}

	// 注意：实际的订阅操作需要连接器端完成
	// 内核端无法主动为连接器建立SubscribeData流连接
	// 连接器收到通知后，如果处于active状态，会自动调用SubscribeData
}

// ChannelServiceServer 实现频道服务
type ChannelServiceServer struct {
	pb.UnimplementedChannelServiceServer
	channelManager       *circulation.ChannelManager
	policyEngine         *control.PolicyEngine
	registry             *control.Registry
	auditLog             *evidence.AuditLog
	NotificationManager  *NotificationManager
	multiKernelManager   *MultiKernelManager
	businessChainService *BusinessChainServiceServer
	businessChainManager *BusinessChainManager
}

// NewChannelServiceServer 创建频道服务
func NewChannelServiceServer(
	channelManager *circulation.ChannelManager,
	policyEngine *control.PolicyEngine,
	registry *control.Registry,
	auditLog *evidence.AuditLog,
	multiKernelManager *MultiKernelManager,
	dbManager *database.DBManager,
) *ChannelServiceServer {
	businessChainManager := NewBusinessChainManager(dbManager)

	server := &ChannelServiceServer{
		channelManager:       channelManager,
		policyEngine:        policyEngine,
		registry:            registry,
		auditLog:            auditLog,
		NotificationManager: NewNotificationManager(channelManager, registry),
		multiKernelManager:  multiKernelManager,
		businessChainManager: businessChainManager,
	}

	// 设置evidence频道创建通知回调
	channelManager.SetChannelCreatedCallback(server.notifyChannelCreated)

	return server
}

// SetBusinessChainService 设置业务哈希链服务
func (s *ChannelServiceServer) SetBusinessChainService(bcs *BusinessChainServiceServer) {
	s.businessChainService = bcs
	// 同时设置 businessChainManager，使 SendAck 等方法可以直接使用
	if bcs != nil {
		s.businessChainManager = bcs.Manager()
	}
}

// notifyChannelCreated 处理异步创建的频道通知（特别是evidence频道）
func (s *ChannelServiceServer) notifyChannelCreated(channel *circulation.Channel) {
	// 构建通知消息
	notification := &pb.ChannelNotification{
		ChannelId:         channel.ChannelID,
		CreatorId:         channel.CreatorID,
		SenderIds:         channel.SenderIDs,
		ReceiverIds:       channel.ReceiverIDs,
		Encrypted:         channel.Encrypted,
		DataTopic:         channel.DataTopic,
		CreatedAt:         channel.CreatedAt.Unix(),
		NegotiationStatus: pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED, // 异步创建的频道直接激活
		ProposalId:        "",                                                      // 异步创建的频道没有提议ID
	}

	// 添加存证配置（如果有）
	if channel.EvidenceConfig != nil {
		notification.EvidenceConfig = &pb.EvidenceConfig{
			Mode:          string(channel.EvidenceConfig.Mode),
			Strategy:      string(channel.EvidenceConfig.Strategy),
			RetentionDays: int32(channel.EvidenceConfig.RetentionDays),
			CompressData:  channel.EvidenceConfig.CompressData,
		}
	}

	// 异步发送通知
	go func() {
		// 通知所有发送方
		for _, senderID := range channel.SenderIDs {
			if err := s.notifyParticipant(senderID, notification); err != nil {
				log.Printf("[WARN] Failed to notify sender %s: %v", senderID, err)
			}
		}

		// 通知所有接收方
		for _, receiverID := range channel.ReceiverIDs {
			if err := s.notifyParticipant(receiverID, notification); err != nil {
				log.Printf("[WARN] Failed to notify receiver %s: %v", receiverID, err)
			}
		}

		// 通知创建者（如果创建者不是参与方）
		isCreatorParticipant := false
		for _, id := range channel.SenderIDs {
			if id == channel.CreatorID {
				isCreatorParticipant = true
				break
			}
		}
		if !isCreatorParticipant {
			for _, id := range channel.ReceiverIDs {
				if id == channel.CreatorID {
					isCreatorParticipant = true
					break
				}
			}
		}
		if !isCreatorParticipant {
			if err := s.notifyParticipant(channel.CreatorID, notification); err != nil {
				log.Printf("[WARN] Failed to notify creator %s: %v", channel.CreatorID, err)
			}
		}
	}()
}

// NotifyParticipant is the exported wrapper for notifyParticipant.
// It sends a channel notification to a local or remote connector, handling
// cross-kernel forwarding automatically (e.g. from permission-change callbacks).
func (s *ChannelServiceServer) NotifyParticipant(participantID string, notification *pb.ChannelNotification) error {
	return s.notifyParticipant(participantID, notification)
}

// notifyParticipant 将通知发送给本地或远端参与者（支持 kernel:connector 格式的远端转发）
// 同时也会检查 channel 的 remoteReceivers 映射来确定是否为远程参与者
func (s *ChannelServiceServer) notifyParticipant(participantID string, notification *pb.ChannelNotification) error {
	// 减少日志输出：只在首次通知或重要状态变化时记录

	// 首先检查 remoteReceivers 映射来确定是否为远程参与者
	// 这样可以处理裸 connectorID 但实际在远程内核上的情况
	remoteKernelID := ""
	if channel, err := s.channelManager.GetChannel(notification.ChannelId); err == nil && channel != nil {
		if kernelID, ok := channel.GetRemoteKernelID(participantID); ok {
			remoteKernelID = kernelID
		}
	}

	// 远端格式: kernelID:connectorID
	isRemote := remoteKernelID != "" || strings.Contains(participantID, ":")

	if isRemote {
		// 确定 kernelID 和 connectorID
		var kernelID, connectorID string
		if remoteKernelID != "" {
			// 使用 remoteReceivers 映射获取的 kernelID
			kernelID = remoteKernelID
			connectorID = participantID
		} else {
			// 从 participantID 中提取
			parts := strings.SplitN(participantID, ":", 2)
			kernelID = parts[0]
			connectorID = parts[1]
		}

	// 如果目标内核是本内核，直接使用本地通知管理器
	if s.multiKernelManager != nil && s.multiKernelManager.config != nil && kernelID == s.multiKernelManager.config.KernelID {
		return s.NotificationManager.Notify(connectorID, notification)
	}

		// 查找目标内核连接信息
		s.multiKernelManager.kernelsMu.RLock()
		kinfo, exists := s.multiKernelManager.kernels[kernelID]
		s.multiKernelManager.kernelsMu.RUnlock()

		if !exists || kinfo == nil || kinfo.conn == nil {
			log.Printf("[WARN]️ Cannot forward to kernel %s: not connected", kernelID)
			return fmt.Errorf("not connected to kernel %s", kernelID)
		}

		// 使用远端内核的 IdentityService（主服务器端口）来调用 ChannelService.NotifyChannelCreated，
		// 因为 ChannelService 注册在主端口的 gRPC 服务上，而 kinfo.conn 是内核间的 kernel-to-kernel 连接。
		conn, err := s.multiKernelManager.connectToKernelIdentityService(kinfo)
		if err != nil {
			log.Printf("[WARN] Failed to connect to identity service of kernel %s: %v", kernelID, err)
			return fmt.Errorf("failed to connect to identity service of kernel %s: %w", kernelID, err)
		}
		defer conn.Close()

		chClient := pb.NewChannelServiceClient(conn)
		// 构建可解析的 SenderId，包含 origin kernel、proposal id（可选）以及协商状态（可选）
		senderWithMeta := s.multiKernelManager.config.KernelID
		// 附加 proposal id（如果有）用于远端创建占位时复用相同的 proposal id
		if notification.ProposalId != "" {
			senderWithMeta = fmt.Sprintf("%s|%s", senderWithMeta, notification.ProposalId)
		}
		// 附加协商状态标记（如果为已接受或已拒绝），便于远端直接将占位标记为相应状态
		if notification.NegotiationStatus == pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED {
			senderWithMeta = fmt.Sprintf("%s|%s", senderWithMeta, "ACCEPTED")
		} else if notification.NegotiationStatus == pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_REJECTED {
			senderWithMeta = fmt.Sprintf("%s|%s", senderWithMeta, "REJECTED")
		}

	_, err = chClient.NotifyChannelCreated(context.Background(), &pb.NotifyChannelRequest{
			ReceiverId: connectorID,
			ChannelId:  notification.ChannelId,
			SenderId:   senderWithMeta,
			DataTopic:  notification.DataTopic,
		})
		if err != nil {
			log.Printf("[WARN] Failed to forward notification to kernel %s: %v", kernelID, err)
		}
		return err
	}

	// 本地参与者
	return s.NotificationManager.Notify(participantID, notification)
}

// CreateChannel method removed - channels must be created through proposal process

// ProposeChannel 提议创建频道（协商第一阶段）
func (s *ChannelServiceServer) ProposeChannel(ctx context.Context, req *pb.ProposeChannelRequest) (*pb.ProposeChannelResponse, error) {
	// 验证创建者身份
	creatorID := req.CreatorId
	if creatorID == "" {
		// 如果没有指定创建者，默认使用当前连接器
		var err error
		creatorID, err = security.ExtractConnectorIDFromContext(ctx)
		if err != nil {
			return &pb.ProposeChannelResponse{
				Success: false,
				Message: fmt.Sprintf("authentication failed: %v", err),
			}, nil
		}
	} else {
		if err := security.VerifyConnectorID(ctx, creatorID); err != nil {
			return &pb.ProposeChannelResponse{
				Success: false,
				Message: fmt.Sprintf("creator authentication failed: %v", err),
			}, nil
		}
	}

	// 验证发送方和接收方列表
	if len(req.SenderIds) == 0 {
		return &pb.ProposeChannelResponse{
			Success: false,
			Message: "at least one sender_id is required",
		}, nil
	}
	if len(req.ReceiverIds) == 0 {
		return &pb.ProposeChannelResponse{
			Success: false,
			Message: "at least one receiver_id is required",
		}, nil
	}

	// 检查是否有重复的ID
	allIDs := make(map[string]bool)
	for _, id := range req.SenderIds {
		if id == "" {
			return &pb.ProposeChannelResponse{
				Success: false,
				Message: "sender_id cannot be empty",
			}, nil
		}
		if allIDs[id] {
			return &pb.ProposeChannelResponse{
				Success: false,
				Message: fmt.Sprintf("duplicate sender_id: %s", id),
			}, nil
		}
		allIDs[id] = true
	}
	for _, id := range req.ReceiverIds {
		if id == "" {
			return &pb.ProposeChannelResponse{
				Success: false,
				Message: "receiver_id cannot be empty",
			}, nil
		}
		if allIDs[id] {
			return &pb.ProposeChannelResponse{
				Success: false,
				Message: fmt.Sprintf("receiver_id %s conflicts with sender", id),
			}, nil
		}
		allIDs[id] = true
	}

	// 检查创建者和所有参与者是否在线
	if !s.registry.IsOnline(creatorID) {
		return &pb.ProposeChannelResponse{
			Success: false,
			Message: "creator is not online",
		}, nil
	}

	// 检查所有发送方是否在线
	for _, senderID := range req.SenderIds {
		if !s.registry.IsOnline(senderID) {
			return &pb.ProposeChannelResponse{
				Success: false,
				Message: fmt.Sprintf("sender %s is not online", senderID),
			}, nil
		}
	}

	// 检查所有接收方是否在线
	for _, receiverID := range req.ReceiverIds {
		if !s.registry.IsOnline(receiverID) {
			return &pb.ProposeChannelResponse{
				Success: false,
				Message: fmt.Sprintf("receiver %s is not online", receiverID),
			}, nil
		}
	}

	// 统一频道模式，所有频道都使用相同处理逻辑
	encrypted := req.Encrypted
	if !req.Encrypted {
		encrypted = true // 统一频道默认加密
	}

	// 权限检查：检查所有发送方到所有接收方的权限（ACL）
	for _, senderID := range req.SenderIds {
		for _, receiverID := range req.ReceiverIds {
			allowed, reason := s.policyEngine.CheckPermission(senderID, receiverID)
			if !allowed {
			s.auditLog.SubmitBasicEvidence(
				creatorID,
				evidence.EventTypePermissionChange,
				"",
				"",
				evidence.DirectionInternal,
				senderID,
			)
				return &pb.ProposeChannelResponse{
					Success: false,
					Message: fmt.Sprintf("permission denied: %s cannot send data to %s: %s", senderID, receiverID, reason),
				}, nil
			}
		}
	}

	// 确定批准者ID
	approverID := req.ApproverId
	if approverID == "" {
		approverID = creatorID // 默认批准者是创建者
	}

	// 创建频道提议
	// 转换存证配置（仅支持内核内置存证）
	var evidenceConfig *circulation.EvidenceConfig
	if req.EvidenceConfig != nil {
		evidenceConfig = &circulation.EvidenceConfig{
			Mode:          circulation.EvidenceMode(req.EvidenceConfig.Mode),
			Strategy:      circulation.EvidenceStrategy(req.EvidenceConfig.Strategy),
			RetentionDays: int(req.EvidenceConfig.RetentionDays),
			CompressData:  req.EvidenceConfig.CompressData,
		}
	}

	channel, err := s.channelManager.ProposeChannel(
		creatorID,
		approverID,
		req.SenderIds,
		req.ReceiverIds,
		req.DataTopic,
		encrypted,
		evidenceConfig,     // evidenceConfig
		req.ConfigFilePath, // configFilePath
		req.Reason,
		req.TimeoutSeconds,
	)
	if err != nil {
		return &pb.ProposeChannelResponse{
			Success: false,
			Message: fmt.Sprintf("failed to propose channel: %v", err),
		}, nil
	}

	// 记录审计日志（频道创建是状态声明事件，不使用 source/target）
	s.auditLog.SubmitBasicEvidence(
		"",
		evidence.EventTypeChannelCreated,
		channel.ChannelID,
		"", // dataHash 为空：频道创建不涉及业务数据哈希
		evidence.DirectionInternal,
		"", // targetID 为空：频道创建不涉及数据流向
	)

	// 发送提议通知给相关方
	go func() {
		notification := &pb.ChannelNotification{
			ChannelId:   channel.ChannelID,
			CreatorId:   creatorID,
			SenderIds:   req.SenderIds,
			ReceiverIds: req.ReceiverIds,
			// ChannelType:       req.ChannelType, // 已废弃 - 统一频道架构
			Encrypted:         encrypted,
			DataTopic:         req.DataTopic,
			CreatedAt:         channel.CreatedAt.Unix(),
			NegotiationStatus: pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_PROPOSED,
			ProposalId:        channel.ChannelProposal.ProposalID,
		}

		// 添加存证配置（如果有）
		if req.EvidenceConfig != nil {
			notification.EvidenceConfig = req.EvidenceConfig
		}

		// 通知所有接收方需要接受提议（创建者除外，因为已自动接受）
		for _, receiverID := range req.ReceiverIds {
			if receiverID != creatorID { // 创建者不需要收到通知，因为已经自动接受
				if err := s.notifyParticipant(receiverID, notification); err != nil {
					log.Printf("[WARN] Failed to notify receiver %s: %v", receiverID, err)
				}
			}
		}

		// 通知所有发送方需要接受提议（创建者已自动接受，不需要通知）
		for _, senderID := range req.SenderIds {
			if senderID != creatorID { // 创建者不需要收到通知，因为已经自动接受
				if err := s.notifyParticipant(senderID, notification); err != nil {
					log.Printf("[WARN] Failed to notify sender %s: %v", senderID, err)
				}
			}
		}
	}()

	return &pb.ProposeChannelResponse{
		Success:    true,
		ChannelId:  channel.ChannelID,
		ProposalId: channel.ChannelProposal.ProposalID,
		Message:    "channel proposal created successfully",
	}, nil
}

// AcceptChannelProposal 接受频道提议（协商第二阶段）
func (s *ChannelServiceServer) AcceptChannelProposal(ctx context.Context, req *pb.AcceptChannelProposalRequest) (*pb.AcceptChannelProposalResponse, error) {
	// 验证接受者身份：优先以 connector 证书验证；若失败，允许已知内核以 "kernelID:connectorID" 格式代为转发
	isForwarded := false
	if err := security.VerifyConnectorID(ctx, req.AccepterId); err != nil {
		// 从上下文提取证书 CN
		certID, err2 := security.ExtractConnectorIDFromContext(ctx)
		if err2 != nil {
			return &pb.AcceptChannelProposalResponse{
				Success: false,
				Message: fmt.Sprintf("authentication failed: %v", err),
			}, nil
		}

		// 检查是否是内核转发请求
		// 情况1：证书 CN 是特定内核 ID（如 kernel-1, kernel-2）
		// 情况2：证书 CN 是通用的 trusted-data-space-kernel，但 AccepterId 包含 kernel- 前缀
		isValidForwardedRequest := false

		// 先检查证书 CN 是否在 kernels 列表中
		s.multiKernelManager.kernelsMu.RLock()
		_, isKnownKernel := s.multiKernelManager.kernels[certID]
		s.multiKernelManager.kernelsMu.RUnlock()

		if isKnownKernel {
			// 证书 CN 是已知内核
			if strings.HasPrefix(req.AccepterId, certID+":") {
				isValidForwardedRequest = true
			}
		} else if certID == "trusted-data-space-kernel" {
			// 证书 CN 是通用内核证书，检查 AccepterId 是否包含 kernel- 前缀
			// 格式应为 kernel-X:connector-Y
			if strings.Contains(req.AccepterId, ":") {
				parts := strings.SplitN(req.AccepterId, ":", 2)
				if strings.HasPrefix(parts[0], "kernel-") {
					isValidForwardedRequest = true
				}
			}
		}

		if !isValidForwardedRequest {
			return &pb.AcceptChannelProposalResponse{
				Success: false,
				Message: fmt.Sprintf("authentication failed: not a valid forwarded request (cert=%s, accepter=%s)", certID, req.AccepterId),
			}, nil
		}

		// 这是一个被转发的接受请求
		isForwarded = true
	}

	// 获取频道信息，检查提议状态
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	if err != nil {
		return &pb.AcceptChannelProposalResponse{
			Success: false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	// 检查提议是否已被拒绝
	if channel.ChannelProposal != nil && channel.ChannelProposal.Status == circulation.NegotiationStatusRejected {
		return &pb.AcceptChannelProposalResponse{
			Success: false,
			Message: "channel proposal has been rejected by another participant",
		}, nil
	}

	// 接受频道提议
	if err := s.channelManager.AcceptChannelProposal(req.ChannelId, req.AccepterId); err != nil {
		return &pb.AcceptChannelProposalResponse{
			Success: false,
			Message: fmt.Sprintf("failed to accept channel proposal: %v", err),
		}, nil
	}

	// 如果这是本地连接器的 accept（非转发请求），需要转发到其他相关内核
	if !isForwarded {
		go s.forwardAcceptToRemoteKernels(req.ChannelId, req.AccepterId, req.ProposalId)
	}

	// 重新获取频道状态（AcceptChannelProposal 可能已更新状态）
	channel, err = s.channelManager.GetChannel(req.ChannelId)
	if err != nil {
		return &pb.AcceptChannelProposalResponse{
			Success: false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	// 检查是否所有参与方都已确认
	// 跨内核频道：需要所有远端参与者都确认后才能激活
	allApproved := true
	hasRemoteParticipants := false

	// 先检查是否有远端参与者
	for id := range channel.ChannelProposal.SenderApprovals {
		if strings.Contains(id, ":") {
			hasRemoteParticipants = true
			break
		}
	}
	if !hasRemoteParticipants {
		for id := range channel.ChannelProposal.ReceiverApprovals {
			if strings.Contains(id, ":") {
				hasRemoteParticipants = true
				break
			}
		}
	}

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
		// 所有参与方都已确认，频道正式创建，发送创建通知
		go func() {
			notification := &pb.ChannelNotification{
				ChannelId:         channel.ChannelID,
				CreatorId:         channel.CreatorID,
				SenderIds:         channel.SenderIDs,
				ReceiverIds:       channel.ReceiverIDs,
				Encrypted:         channel.Encrypted,
				DataTopic:         channel.DataTopic,
				CreatedAt:         channel.CreatedAt.Unix(),
				NegotiationStatus: pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED,
				ProposalId:        channel.ChannelProposal.ProposalID,
			}

			// 添加存证配置（如果有）
			if channel.EvidenceConfig != nil {
				notification.EvidenceConfig = &pb.EvidenceConfig{
					Mode:          string(channel.EvidenceConfig.Mode),
					Strategy:      string(channel.EvidenceConfig.Strategy),
					RetentionDays: int32(channel.EvidenceConfig.RetentionDays),
					CompressData:  channel.EvidenceConfig.CompressData,
				}
			}

			// 通知所有发送方
			for _, senderID := range channel.SenderIDs {
				if err := s.notifyParticipant(senderID, notification); err != nil {
					log.Printf("[WARN] Failed to notify sender %s: %v", senderID, err)
				} else {
				}
			}

			// 通知所有接收方
			for _, receiverID := range channel.ReceiverIDs {
				if err := s.notifyParticipant(receiverID, notification); err != nil {
					log.Printf("[WARN] Failed to notify receiver %s: %v", receiverID, err)
				} else {
				}
			}

			// 通知创建者（如果创建者不是参与方）
			isParticipant := false
			for _, senderID := range channel.SenderIDs {
				if channel.CreatorID == senderID {
					isParticipant = true
					break
				}
			}
			if !isParticipant {
				for _, receiverID := range channel.ReceiverIDs {
					if channel.CreatorID == receiverID {
						isParticipant = true
						break
					}
				}
			}
			if !isParticipant {
				if err := s.notifyParticipant(channel.CreatorID, notification); err != nil {
					log.Printf("[WARN] Failed to notify creator %s: %v", channel.CreatorID, err)
				} else {
				}
			}


			// 主动通知其他内核频道已激活（用于跨内核场景）
			// 这样 origin kernel 能更新其本地频道状态
			s.notifyOtherKernelsChannelActivated(channel, notification)
		}()
	}

	// 注意：当allApproved为false时，不再发送协商状态更新通知
	// 因为这会导致与拒绝通知混淆，而且协商仍在进行中
	// 注意：非转发 accept 的跨内核转发已由 forwardAcceptToRemoteKernels（line ~669）处理，此处无需重复

	return &pb.AcceptChannelProposalResponse{
		Success: true,
		Message: "channel proposal accepted successfully",
	}, nil
}

// forwardAcceptToRemoteKernels 将 accept 转发到其他相关内核
func (s *ChannelServiceServer) forwardAcceptToRemoteKernels(channelID, accepterID, proposalID string) {
	localKernelID := s.multiKernelManager.config.KernelID

	s.multiKernelManager.kernelsMu.RLock()
	kernels := make([]*KernelInfo, 0, len(s.multiKernelManager.kernels))
	for _, k := range s.multiKernelManager.kernels {
		kernels = append(kernels, k)
	}
	s.multiKernelManager.kernelsMu.RUnlock()

	for _, k := range kernels {
		if k.KernelID == localKernelID {
			continue
		}
		// 建立到对方主服务器的临时连接（用于调用 ChannelService）
		conn, err := s.multiKernelManager.connectToKernelIdentityService(k)
		if err != nil {
			log.Printf("[WARN] Failed to connect to kernel %s for forwarding accept: %v", k.KernelID, err)
			continue
		}
		chClient := pb.NewChannelServiceClient(conn)
		// 格式：localKernelID:accepterID（如 kernel-2:connector-U）
		forwardAccepter := fmt.Sprintf("%s:%s", localKernelID, accepterID)
		_, err = chClient.AcceptChannelProposal(context.Background(), &pb.AcceptChannelProposalRequest{
			AccepterId: forwardAccepter,
			ChannelId:  channelID,
			ProposalId: proposalID,
		})
		conn.Close()
		if err != nil {
			log.Printf("[WARN] Failed to forward accept to kernel %s: %v", k.KernelID, err)
		} else {
		}
	}
}

// notifyOtherKernelsChannelActivated 通知其他内核频道已激活（用于跨内核场景）
// 这样其他内核能更新其本地频道状态为 active，并启动数据分发
func (s *ChannelServiceServer) notifyOtherKernelsChannelActivated(channel *circulation.Channel, notification *pb.ChannelNotification) {
	if s.multiKernelManager == nil {
		return
	}

	localKernelID := s.multiKernelManager.config.KernelID

	// 设置本地远端接收者映射（用于从本内核向其他内核转发数据）
	// 遍历 receiverIDs，识别远端接收者并设置映射
	for _, receiverID := range channel.ReceiverIDs {
		if strings.Contains(receiverID, ":") {
			// 格式: kernelID:connectorID
			parts := strings.SplitN(receiverID, ":", 2)
			kernelID := parts[0]
			connectorID := parts[1]
			// 设置映射：connectorID -> kernelID（不含kernel前缀）
			channel.SetRemoteReceiver(connectorID, kernelID)
		}
	}
	// 同样处理发送方（如果有远端发送方）
	for _, senderID := range channel.SenderIDs {
		if strings.Contains(senderID, ":") {
			parts := strings.SplitN(senderID, ":", 2)
			kernelID := parts[0]
			connectorID := parts[1]
			channel.SetRemoteReceiver(connectorID, kernelID)
		}
	}

	s.multiKernelManager.kernelsMu.RLock()
	kernels := make([]*KernelInfo, 0, len(s.multiKernelManager.kernels))
	for _, k := range s.multiKernelManager.kernels {
		kernels = append(kernels, k)
	}
	s.multiKernelManager.kernelsMu.RUnlock()

	for _, k := range kernels {
		if k.KernelID == localKernelID {
			continue // 跳过本内核
		}

		// 建立到对方主服务器的临时连接
		conn, err := s.multiKernelManager.connectToKernelIdentityService(k)
		if err != nil {
			log.Printf("[WARN] Failed to connect to kernel %s for activation notification: %v", k.KernelID, err)
			continue
		}
		defer conn.Close()

		chClient := pb.NewChannelServiceClient(conn)

		// 构建带有 ACCEPTED 状态的 SenderId
		// 格式: "kernelID|proposalId|STATUS" - STATUS 必须是第3部分
		senderWithMeta := fmt.Sprintf("%s||ACCEPTED", localKernelID)

		// 获取 creator 的纯 connector ID（如果有 kernel 前缀）
		creatorID := notification.CreatorId
		if idx := strings.LastIndex(notification.CreatorId, ":"); idx != -1 {
			creatorID = notification.CreatorId[idx+1:]
		}

		_, err = chClient.NotifyChannelCreated(context.Background(), &pb.NotifyChannelRequest{
			ReceiverId: creatorID, // 通知创建者所在内核
			ChannelId:  channel.ChannelID,
			SenderId:   senderWithMeta,
			DataTopic:  notification.DataTopic,
		})
		if err != nil {
			log.Printf("[WARN] Failed to notify kernel %s of channel activation: %v", k.KernelID, err)
		} else {
		}
	}
}

// RejectChannelProposal 拒绝频道提议（协商结束）
func (s *ChannelServiceServer) RejectChannelProposal(ctx context.Context, req *pb.RejectChannelProposalRequest) (*pb.RejectChannelProposalResponse, error) {
	// 验证拒绝者身份
	if err := security.VerifyConnectorID(ctx, req.RejecterId); err != nil {
		return &pb.RejectChannelProposalResponse{
			Success: false,
			Message: fmt.Sprintf("authentication failed: %v", err),
		}, nil
	}

	// 拒绝频道提议
	if err := s.channelManager.RejectChannelProposal(req.ChannelId, req.RejecterId, req.Reason); err != nil {
		return &pb.RejectChannelProposalResponse{
			Success: false,
			Message: fmt.Sprintf("failed to reject channel proposal: %v", err),
		}, nil
	}

	// 记录审计日志
	s.auditLog.SubmitBasicEvidence(
		req.RejecterId,
		evidence.EventTypeChannelClosed,
		req.ChannelId,
		"", // dataHash 为空：频道关闭不涉及业务数据哈希
		evidence.DirectionInternal,
		req.RejecterId,
	)

	// 异步通知频道创建者频道被拒绝
	go func() {
		channel, err := s.channelManager.GetChannel(req.ChannelId)
		if err != nil {
			log.Printf("[WARN] Failed to get channel info: %v", err)
			return
		}

		notification := &pb.ChannelNotification{
			ChannelId:         channel.ChannelID,
			CreatorId:         channel.CreatorID,
			SenderIds:         channel.SenderIDs,
			ReceiverIds:       channel.ReceiverIDs,
			Encrypted:         channel.Encrypted,
			DataTopic:         channel.DataTopic,
			CreatedAt:         channel.CreatedAt.Unix(),
			NegotiationStatus: pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_REJECTED,
			ProposalId:        channel.ChannelProposal.ProposalID,
		}

		// 通知创建者
		if err := s.notifyParticipant(channel.CreatorID, notification); err != nil {
			log.Printf("[WARN] Failed to notify creator %s: %v", channel.CreatorID, err)
		}

		// 通知所有发送方
		for _, senderID := range channel.SenderIDs {
			if err := s.notifyParticipant(senderID, notification); err != nil {
				log.Printf("[WARN] Failed to notify sender %s: %v", senderID, err)
			}
		}
	}()

	return &pb.RejectChannelProposalResponse{
		Success: true,
		Message: "channel proposal rejected successfully",
	}, nil
}

// StreamData 处理数据流推送
// 核心逻辑：接收 connector 数据包，计算 data_hash，签名，写入 evidence + chain，转发
func (s *ChannelServiceServer) StreamData(stream pb.ChannelService_StreamDataServer) error {
	ctx := stream.Context()
	var senderID string
	var channelID string
	var flowID string
	var isCrossKernel bool
	var targetKernelID string
	var currentKernelID string
	var dataHashAccumulator []byte

	// 业务哈希链：记录是否已初始化
	var chainInitMu sync.Mutex

	for {
		packet, err := stream.Recv()
		if err == io.EOF {
			// 流结束
			if channelID != "" && senderID != "" && flowID != "" {
				if s.multiKernelManager != nil && s.multiKernelManager.config != nil {
					currentKernelID = s.multiKernelManager.config.KernelID
				}

					// 流签名
				if flowID != "" {
					if _, err := s.auditLog.SignFlow(flowID); err != nil {
						log.Printf("[WARN] Failed to sign flow %s: %v", flowID, err)
					}
				}

				// 发送流结束标志给接收方内核
				if isCrossKernel && targetKernelID != "" {
					if ch, err := s.channelManager.GetChannel(channelID); err == nil {
						endPacket := &circulation.DataPacket{
							ChannelID:      channelID,
							SequenceNumber: -1,
							SenderID:       senderID,
							Payload:        []byte{},
							FlowID:         flowID,
							IsFinal:       true,
						}
						if err := ch.PushData(endPacket); err != nil {
							log.Printf("[WARN] Failed to send end packet to kernel %s: %v", targetKernelID, err)
						}
					}
				}
			}
			return nil
		}
		if err != nil {
			return err
		}

		// 首次接收，验证频道
		if channelID == "" {
			channelID = packet.ChannelId

			channel, err := s.channelManager.GetChannel(channelID)
			if err != nil {
				return fmt.Errorf("invalid channel: %v", err)
			}

			senderID = packet.SenderId
			if senderID == "" {
				return fmt.Errorf("sender_id is required in packet")
			}

			if err := security.VerifyConnectorID(ctx, senderID); err != nil {
				return fmt.Errorf("sender verification failed: %v", err)
			}

			if !channel.IsParticipant(senderID) {
				return fmt.Errorf("sender %s is not a participant of this channel", senderID)
			}

			if packet.FlowId != "" {
				flowID = packet.FlowId
			} else {
				flowID = uuid.New().String()
			}

			if s.multiKernelManager != nil && s.multiKernelManager.config != nil {
				currentKernelID = s.multiKernelManager.config.KernelID
			}

			isCrossKernel = false
			targetKernelID = ""

			for _, receiverID := range channel.ReceiverIDs {
				if strings.Contains(receiverID, ":") {
					isCrossKernel = true
					parts := strings.Split(receiverID, ":")
					if len(parts) >= 2 {
						targetKernelID = parts[0]
					}
					break
				}
			}
			if !isCrossKernel {
				for _, sid := range channel.SenderIDs {
					if strings.Contains(sid, ":") {
						isCrossKernel = true
						break
					}
				}
			}
		}

		// 获取频道
		channel, err := s.channelManager.GetChannel(packet.ChannelId)
		if err != nil {
			return fmt.Errorf("channel not found: %v", err)
		}

		if channel.Status != circulation.ChannelStatusActive {
			return fmt.Errorf("channel is not active: status=%s", channel.Status)
		}

		// ================================================================
		// 业务哈希链处理
		// ================================================================
		chainInitMu.Lock()

		// 检查 senderID 是否属于当前 kernel 的本地 connector
		// 只有本地 connector 发送的数据才需要在 kernel 的 business_data_chain 中记录
		// 来自其他 kernel 转发的数据由 kernel_service.go 的 ForwardData 处理（ForwardData 中已有条件判断 currentKernelID != req.SourceKernelId）
		isLocalSender := true
		if strings.Contains(senderID, ":") {
			parts := strings.SplitN(senderID, ":", 2)
			if len(parts) >= 2 && parts[0] != currentKernelID {
				// senderID 带有其他 kernel 的前缀，说明是转发数据，不需要在 StreamData 中记录
				isLocalSender = false
			}
		}

		// data_hash 直接使用 connector 发送的 packet.DataHash
		// packet.DataHash 由 connector 计算: SHA256(payload + prevHash)
		// prevHash 从数据库获取，用于构建 kernel 本地的哈希链
		var dataHash string
		if packet.DataHash != "" {
			dataHash = packet.DataHash
		} else {
			// 兼容：无 dataHash 时使用简单 SHA256(payload)
			h := sha256.Sum256(packet.Payload)
			dataHash = hex.EncodeToString(h[:])
		}

		// 获取上一一跳的签名（来自 packet.Signature，即 connector 的签名）
		prevSignature := packet.Signature
		
		// 计算内核签名: RSA_Sign(dataHash + prevSignature)
		kernelSignature, signErr := security.KernelSign(dataHash, prevSignature)

		// 判断数据类型（用于 evidence metadata）
		dataCategory := determineDataCategory(packet.Payload)

		// 写入 evidence_records（kernel ← sender 接收数据）
		// source=senderID（A 发出数据），event=DATA_SEND（A 相关）
		if signErr == nil {
			s.auditLog.SubmitBasicEvidenceWithMetadata(
				senderID,
				evidence.EventTypeDataSend,
				channelID,
				dataHash,
				evidence.DirectionInternal,
				currentKernelID,
				flowID,
				map[string]string{"data_category": dataCategory},
			)
		}

		// 写入 evidence_records（kernel → receiver 发送数据）
		// source=kernel（向 B 发送），event=DATA_RECEIVE（B 相关）
		// 注意：跨内核场景下不记录 DATA_RECEIVE，
		// 跨内核的接收证据由 kernel_service.go ForwardData 中统一记录
		if signErr == nil && len(channel.ReceiverIDs) > 0 && !isCrossKernel {
			s.auditLog.SubmitBasicEvidenceWithMetadata(
				currentKernelID,
				evidence.EventTypeDataReceive,
				channelID,
				dataHash,
				evidence.DirectionInternal,
				channel.ReceiverIDs[0],
				flowID,
				map[string]string{"data_category": dataCategory},
			)
		}

		// 写入 business_data_chain
		// 只有本地 connector 发送的数据才记录（避免转发数据被错误记录）
		if signErr == nil && s.businessChainManager != nil && isLocalSender {
			err := s.businessChainManager.RecordDataHash(
				senderID,
				channelID,
				computedDataHash,
				prevHash,
				prevSignature, // prev_signature = connector 的签名
				kernelSignature,
			)
			if err != nil {
				log.Printf("[WARN] Failed to record business chain: %v", err)
			} else {
				log.Printf("[OK] Business chain recorded: channel=%s, data_hash=%s", channelID, computedDataHash)
			}
		}

		chainInitMu.Unlock()

		// ================================================================
		// 转发数据包（更新 data_hash 和 signature）
		// ================================================================
		dataPacket := &circulation.DataPacket{
			ChannelID:      packet.ChannelId,
			SequenceNumber: packet.SequenceNumber,
			Payload:        packet.Payload,
			Timestamp:      packet.Timestamp,
			SenderID:       packet.SenderId,
			TargetIDs:      packet.TargetIds,
			FlowID:         flowID,
		}

		// 填充业务哈希链信息
		if signErr == nil {
			dataPacket.DataHash = computedDataHash
			dataPacket.Signature = kernelSignature
		} else {
			// 兼容：没有签名时保留原 signature
			dataPacket.Signature = packet.Signature
			dataPacket.DataHash = computedDataHash
		}

		if err := channel.PushData(dataPacket); err != nil {
			return fmt.Errorf("failed to push data: %v", err)
		}

		// 累积数据哈希（用于流结束时的 final hash）
		dataHashAccumulator = append(dataHashAccumulator, packet.Payload...)

		// 发送确认
		if err := stream.Send(&pb.TransferStatus{
			ChannelId:            packet.ChannelId,
			LastSequenceReceived: packet.SequenceNumber,
			Success:              true,
			Message:              "packet received",
		}); err != nil {
			return err
		}
	}
}
	// SubscribeData 订阅频道数据
func (s *ChannelServiceServer) SubscribeData(req *pb.SubscribeRequest, stream pb.ChannelService_SubscribeDataServer) error {
	ctx := stream.Context()

	// 验证订阅者身份
	if err := security.VerifyConnectorID(ctx, req.ConnectorId); err != nil {
		return fmt.Errorf("subscriber verification failed: %v", err)
	}

	// 检测是否是重启恢复
	isRecovery := s.channelManager.IsConnectorRestarting(req.ConnectorId)
	s.channelManager.MarkConnectorOnline(req.ConnectorId)

	// 在连接断开时标记为离线
	defer func() {
		s.channelManager.MarkConnectorOffline(req.ConnectorId)
	}()

	// 获取频道
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	if err != nil {
		return fmt.Errorf("channel not found: %v", err)
	}

	// 验证订阅者是否是频道参与者（如果不是，自动加入）
	if !channel.IsParticipant(req.ConnectorId) {
		// 自动将订阅者添加为参与者
		channel.AddParticipant(req.ConnectorId)
	}

	// 订阅频道
	dataChan, err := channel.SubscribeWithRecovery(req.ConnectorId, isRecovery)
	if err != nil {
		return fmt.Errorf("subscription failed: %v", err)
	}
	defer channel.Unsubscribe(req.ConnectorId)

	// 如果是从离线状态恢复，发送频道激活通知
	if isRecovery {
		go func() {
			// 构造频道通知
			negotiationStatus := pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED

			notification := &pb.ChannelNotification{
				ChannelId:         channel.ChannelID,
				CreatorId:         channel.CreatorID,
				SenderIds:         channel.SenderIDs,
				ReceiverIds:       channel.ReceiverIDs,
				Encrypted:         channel.Encrypted,
				DataTopic:         channel.DataTopic,
				CreatedAt:         channel.CreatedAt.Unix(),
				NegotiationStatus: negotiationStatus,
			}

			// 添加存证配置（如果有）
			if channel.EvidenceConfig != nil {
				notification.EvidenceConfig = &pb.EvidenceConfig{
					Mode:          string(channel.EvidenceConfig.Mode),
					Strategy:      string(channel.EvidenceConfig.Strategy),
					RetentionDays: int32(channel.EvidenceConfig.RetentionDays),
					CompressData:  channel.EvidenceConfig.CompressData,
				}
			}

			// 发送通知给重新连接的连接器
			if err := s.notifyParticipant(req.ConnectorId, notification); err != nil {
				log.Printf("[WARN]️ Failed to send recovery notification to %s: %v", req.ConnectorId, err)
			}
		}()
	}

	// 持续发送数据
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case packet, ok := <-dataChan:
			if !ok {
				// 频道已关闭
				return nil
			}

			// 发送数据包
			pbPacket := &pb.DataPacket{
				ChannelId:         packet.ChannelID,
				SequenceNumber:    packet.SequenceNumber,
				Payload:          packet.Payload,
				Signature:        packet.Signature,
				Timestamp:        packet.Timestamp,
			DataHash:  packet.DataHash,
			IsAck:     packet.IsAck,
		}

			if err := stream.Send(pbPacket); err != nil {
				return err
			}
		}
	}
}

// CloseChannel 关闭频道
func (s *ChannelServiceServer) CloseChannel(ctx context.Context, req *pb.CloseChannelRequest) (*pb.CloseChannelResponse, error) {
	// 验证请求者身份
	if err := security.VerifyConnectorID(ctx, req.RequesterId); err != nil {
		return &pb.CloseChannelResponse{
			Success: false,
			Message: fmt.Sprintf("authentication failed: %v", err),
		}, nil
	}

	// 获取频道
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	if err != nil {
		return &pb.CloseChannelResponse{
			Success: false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	// 验证请求者是否是频道参与者
	if !channel.IsParticipant(req.RequesterId) {
		return &pb.CloseChannelResponse{
			Success: false,
			Message: "only channel participants can close the channel",
		}, nil
	}

	// 关闭频道
	if err := s.channelManager.CloseChannel(req.ChannelId); err != nil {
		return &pb.CloseChannelResponse{
			Success: false,
			Message: fmt.Sprintf("failed to close channel: %v", err),
		}, nil
	}

	// 记录频道关闭
	s.auditLog.SubmitBasicEvidence(
		req.RequesterId,
		evidence.EventTypeChannelClosed,
		req.ChannelId,
		"",
		evidence.DirectionInternal,
		req.ChannelId,
	)

	return &pb.CloseChannelResponse{
		Success: true,
		Message: "channel closed successfully",
	}, nil
}

// SendAck 处理接收方（connector-B）发送的 ACK，转发给原始发送方（connector-A）
// ACK 流程：connector-B → kernel → connector-A
// 多跳场景：kernel-3 SendAck → multiKernelManager.ForwardData → kernel-2 ForwardData → kernel-1 ForwardData → connector-A
func (s *ChannelServiceServer) SendAck(ctx context.Context, req *pb.AckPacket) (*pb.SendAckResponse, error) {
	channelID := req.ChannelId
	dataHash := req.DataHash
	connectorSignature := req.Signature // connector-B（connector-X）的签名

	// 1. 验证频道存在
	channel, err := s.channelManager.GetChannel(channelID)
	if err != nil {
		return &pb.SendAckResponse{
			Success: false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	if channel.Status != circulation.ChannelStatusActive {
		return &pb.SendAckResponse{
			Success: false,
			Message: fmt.Sprintf("channel is not active: %s", channel.Status),
		}, nil
	}

	// 2. 验证发送者（connector-B）是该频道的参与者
	if !channel.IsParticipant(req.SenderId) {
		return &pb.SendAckResponse{
			Success: false,
			Message: fmt.Sprintf("sender %s is not a participant of channel %s", req.SenderId, channelID),
		}, nil
	}

	// 2b. 验证发送者（connector-B）是该频道的接收方（只有接收方才能发送 ACK）
	if !channel.CanReceive(req.SenderId) {
		return &pb.SendAckResponse{
			Success: false,
			Message: fmt.Sprintf("sender %s is not a receiver of channel %s, only receivers can send ACK", req.SenderId, channelID),
		}, nil
	}

	// 3. 获取原始发送方 ID（connector-A）
	var senderID string
	for _, sid := range channel.SenderIDs {
		if sid != req.SenderId {
			senderID = sid
			break
		}
	}
	if senderID == "" {
		return &pb.SendAckResponse{
			Success: false,
			Message: "no original sender found for this channel",
		}, nil
	}

	// 4. 获取 FlowId
	ackFlowID := channel.GetCurrentFlowID()
	if ackFlowID == "" {
		ackFlowID = req.FlowId
	}

	// 5. 确定原始发送内核（kernel-1）
	originKernelID := channel.GetOriginKernelID()
	currentKernelID := s.multiKernelManager.config.KernelID

	// 6. 生成内核对 ACK 的签名：RSA_Sign(prevSignature)
	// ACK 包的签名输入只有上一跳的签名，不含 dataHash
	kernelAckSignature, signErr := security.KernelSign("", connectorSignature)
	if signErr != nil {
		log.Printf("[WARN] Failed to sign ACK at kernel: %v", signErr)
		return &pb.SendAckResponse{
			Success: false,
			Message: fmt.Sprintf("failed to sign ACK: %v", signErr),
		}, nil
	}

	// 8. 构造 AckPacket（放入 DataPacket.Payload 用于 ForwardData）
	// Signature 已合并为当前 kernel 对 ACK 的签名，下一跳直接读取 GetSignature() 作为 prev_signature
	ackPB := &pb.AckPacket{
		ChannelId:  channelID,
		DataHash:   dataHash,
		SenderId:   req.SenderId,
		Signature:  connectorSignature, // connector-X 签名
		FlowId:    ackFlowID,
	}
	ackPayload, err := proto.Marshal(ackPB)
	if err != nil {
		log.Printf("[WARN] Failed to marshal ACK packet: %v", err)
		return &pb.SendAckResponse{
			Success: false,
			Message: fmt.Sprintf("failed to marshal ACK: %v", err),
		}, nil
	}

	// 9. 记录 ACK_RECEIVED（当前 kernel - 即 kernel-3）
	if s.auditLog != nil {
		ackMetadata := map[string]string{
			"connector_signature":     connectorSignature,
			"kernel_signature":       kernelAckSignature,
			"ack_sender":            req.SenderId,
			"original_sender_kernel": originKernelID,
		}
		if _, err := s.auditLog.SubmitBasicEvidenceWithMetadata(
			currentKernelID,
			evidence.EventTypeAckReceived,
			channelID,
			dataHash,
			evidence.DirectionInternal,
			"",
			ackFlowID,
			ackMetadata,
		); err != nil {
			log.Printf("[WARN] SendAck: failed to submit ACK_RECEIVED evidence: %v", err)
		} else {
			log.Printf("[OK] SendAck: ACK_RECEIVED recorded at kernel=%s, data_hash=%.20s", currentKernelID, dataHash)
		}
	}

	// 10. 记录 ACK 到 business_data_chain（ACK 签名链，当前 kernel 即 kernel-3）
	// prev_signature = connectorSignature（connector-X 对 ACK 的签名）
	// signature = kernelAckSignature（kernel-3 对 ACK 的签名）
	if s.businessChainManager != nil {
		if err := s.businessChainManager.RecordAck(
			"", channelID,
			connectorSignature, kernelAckSignature,
		); err != nil {
			log.Printf("[WARN] SendAck: failed to record ACK business chain: %v", err)
		} else {
			log.Printf("[OK] SendAck: ACK business chain recorded at kernel=%s, data_hash=%.20s", currentKernelID, dataHash)
		}
	}

	// 11. 构造 DataPacket（用于本地分发和多跳转发）
	// TargetIDs 设为 originKernelID:connector-A，用于 ACK 反向路由
	// FlowID = ackFlowID（从 channel.GetCurrentFlowID 获取）
	// Signature = kernelAckSignature（当前 kernel 对 ACK 的签名，下一跳作为 prev_signature 使用）
	ackPacket := &circulation.DataPacket{
		ChannelID:            channelID,
		SequenceNumber:       0,
		Payload:              ackPayload,
		SenderID:             req.SenderId,
		TargetIDs:            []string{originKernelID + ":" + senderID}, // 用于 ACK 反向路由
		MessageType:          circulation.MessageTypeAck,
		FlowID:               ackFlowID,
		IsAck:                true,
		DataHash:             dataHash,
		Signature:            kernelAckSignature, // 当前 kernel 对 ACK 的签名，下一跳作为 prev_signature
		AckEvidenceRecorded:  true,             // ACK_RECEIVED 已记录，不再重复
	}

	// 12. PushData：ACK 反向路由会统一处理多跳转发
	// PushData 中的 ACK 反向路由逻辑会根据 TargetIDs 中的 originKernelID
	// 自动查找上一跳并转发 ACK，不再需要 SendAck 直接调用 ForwardData
	if err := channel.PushData(ackPacket); err != nil {
		log.Printf("[WARN] SendAck: failed to push ACK to channel: %v", err)
		return &pb.SendAckResponse{
			Success: false,
			Message: fmt.Sprintf("failed to push ACK: %v", err),
		}, nil
	}

	log.Printf("[OK] ACK processed: data_hash=%.20s, from=%s, to=%s, origin_kernel=%s",
		dataHash, req.SenderId, senderID, originKernelID)
	return &pb.SendAckResponse{
		Success: true,
		Message: "ACK processed successfully",
	}, nil
}

// WaitForChannelNotification
func (s *ChannelServiceServer) WaitForChannelNotification(req *pb.WaitNotificationRequest, stream pb.ChannelService_WaitForChannelNotificationServer) error {
	ctx := stream.Context()

	// 验证接收方身份
	if err := security.VerifyConnectorID(ctx, req.ReceiverId); err != nil {
		return fmt.Errorf("receiver authentication failed: %v", err)
	}

	// 注册通知通道
	notifyChan := s.NotificationManager.Register(req.ReceiverId)
	defer s.NotificationManager.Unregister(req.ReceiverId)

	// 持续监听通知
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case notification, ok := <-notifyChan:
			if !ok {
				// 通道已关闭
				return nil
			}

			// 发送通知给接收方
			if err := stream.Send(notification); err != nil {
				return fmt.Errorf("failed to send notification: %v", err)
			}
		}
	}
}

// NotifyChannelCreated 处理频道创建通知（内部使用，用于测试）
func (s *ChannelServiceServer) NotifyChannelCreated(ctx context.Context, req *pb.NotifyChannelRequest) (*pb.NotifyChannelResponse, error) {
	// 解析 SenderId 中的 originStatus（如果有）
	originStatus := ""
	originProposalId := ""
	originKernel := req.SenderId
	if strings.Contains(originKernel, "|") {
		parts := strings.SplitN(originKernel, "|", 3)
		originKernel = parts[0]
		if len(parts) >= 2 {
			originProposalId = parts[1]
		}
		if len(parts) >= 3 {
			originStatus = parts[2]
		}
	}

	channel, err := s.channelManager.GetChannel(req.ChannelId)

	// 标记是否已经为 ACCEPTED 状态通知过本地参与者（避免重复通知）
	// 需要在 if err != nil 块之外声明，以便在后续代码中使用
	notifiedForAccept := false

	// 频道已存在 - 减少日志输出

	if err != nil {
		// 如果本地没有该频道，尝试从请求中的 SenderId（origin kernel）获取频道详细信息并在本地创建占位频道
		originKernel = req.SenderId
		if strings.Contains(originKernel, "|") {
			parts := strings.SplitN(originKernel, "|", 3)
			originKernel = parts[0]
			if len(parts) >= 2 {
				originProposalId = parts[1]
			}
			if len(parts) >= 3 {
				originStatus = parts[2]
			}
		}
		if originKernel == "" {
			return &pb.NotifyChannelResponse{
				Success: false,
				Message: fmt.Sprintf("channel not found: %v", err),
			}, nil
		}

		// 查找 origin kernel client
		s.multiKernelManager.kernelsMu.RLock()
		originInfo, exists := s.multiKernelManager.kernels[originKernel]
		s.multiKernelManager.kernelsMu.RUnlock()

		if !exists || originInfo == nil {
			return &pb.NotifyChannelResponse{
				Success: false,
				Message: fmt.Sprintf("channel not found and origin kernel %s not connected", originKernel),
			}, nil
		}

		// 确保已建立到 origin kernel 的连接
		if originInfo.Client == nil || originInfo.conn == nil {
			log.Printf("[WARN]️ Origin kernel %s has no active connection, attempting to reconnect...", originKernel)
			err := s.multiKernelManager.EnsureKernelConnected(originKernel)
			if err != nil {
				return &pb.NotifyChannelResponse{
					Success: false,
					Message: fmt.Sprintf("failed to connect to origin kernel %s: %v", originKernel, err),
				}, nil
			}
			// 重新获取 originInfo
			s.multiKernelManager.kernelsMu.RLock()
			originInfo, exists = s.multiKernelManager.kernels[originKernel]
			s.multiKernelManager.kernelsMu.RUnlock()
			if originInfo == nil || originInfo.Client == nil {
				return &pb.NotifyChannelResponse{
					Success: false,
					Message: fmt.Sprintf("failed to establish connection to origin kernel %s", originKernel),
				}, nil
			}
		}

		// 使用带超时的上下文防止永久阻塞
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		infoResp, err := originInfo.Client.GetCrossKernelChannelInfo(ctx, &pb.GetCrossKernelChannelInfoRequest{
			RequesterKernelId: s.multiKernelManager.config.KernelID,
			ChannelId:         req.ChannelId,
		})
		if err != nil || !infoResp.Found {
			log.Printf("[WARN]️ GetCrossKernelChannelInfo failed: err=%v, infoResp=%v", err, infoResp)
			return &pb.NotifyChannelResponse{
				Success: false,
				Message: fmt.Sprintf("failed to fetch channel info from origin %s: %v", originKernel, err),
			}, nil
		}

		// 构造参与者列表
		senderIDs := make([]string, 0, len(infoResp.SenderIds))
		receiverIDs := make([]string, 0, len(infoResp.ReceiverIds))
		localKID := s.multiKernelManager.config.KernelID
		for _, p := range infoResp.SenderIds {
			cid := p.ConnectorId
			// 始终保留 kernel 前缀（如果有），这样其他内核才能正确转发通知
			// 这样可以避免出现 "no listener registered" 的问题
			if p.KernelId != "" {
				// 如果 KernelId 不为空，始终使用 kernel:connector 格式
				cid = fmt.Sprintf("%s:%s", p.KernelId, p.ConnectorId)
			} else if strings.Contains(cid, ":") {
				// 如果 ConnectorId 本身包含 ":"，尝试提取 kernel 前缀
				parts := strings.SplitN(cid, ":", 2)
				if parts[0] != localKID {
					cid = fmt.Sprintf("%s:%s", parts[0], parts[1])
				}
				// 否则保持 bare connector ID
			}
			senderIDs = append(senderIDs, cid)
		}
		for _, p := range infoResp.ReceiverIds {
			cid := p.ConnectorId
			// 始终保留 kernel 前缀（如果有），这样其他内核才能正确转发通知
			if p.KernelId != "" {
				// 如果 KernelId 不为空，始终使用 kernel:connector 格式
				cid = fmt.Sprintf("%s:%s", p.KernelId, p.ConnectorId)
			} else if strings.Contains(cid, ":") {
				// 如果 ConnectorId 本身包含 ":"，尝试提取 kernel 前缀
				parts := strings.SplitN(cid, ":", 2)
				if parts[0] != localKID {
					cid = fmt.Sprintf("%s:%s", parts[0], parts[1])
				}
				// 否则保持 bare connector ID
			}
			receiverIDs = append(receiverIDs, cid)
		}

		// 在本地创建占位频道
		channel, err = s.channelManager.CreateChannelWithID(req.ChannelId, infoResp.CreatorConnectorId, infoResp.CreatorConnectorId, senderIDs, receiverIDs, infoResp.DataTopic, infoResp.Encrypted, nil, "")
		if err != nil {
			return &pb.NotifyChannelResponse{
				Success: false,
				Message: fmt.Sprintf("failed to create local placeholder channel: %v", err),
			}, nil
		}

		// 设置远端接收者映射（用于跨内核数据转发通知）
		for _, receiverID := range receiverIDs {
			if strings.Contains(receiverID, ":") {
				parts := strings.SplitN(receiverID, ":", 2)
				kernelID := parts[0]
				connectorID := parts[1]
				channel.SetRemoteReceiver(connectorID, kernelID)
			}
		}
		// 同样处理发送方
		for _, senderID := range senderIDs {
			if strings.Contains(senderID, ":") {
				parts := strings.SplitN(senderID, ":", 2)
				kernelID := parts[0]
				connectorID := parts[1]
				channel.SetRemoteReceiver(connectorID, kernelID)
			}
		}

		if originProposalId != "" && channel.ChannelProposal != nil {
			channel.ChannelProposal.ProposalID = originProposalId
		}

		// 如果 SenderId 带来了 ACCEPTED 状态，说明远端参与者已接受
		// 此时需要检查本地是否也已接受，如果都已接受则激活频道
		if originStatus == "ACCEPTED" {
			// Auto-approve only participants that belong to the origin kernel — they have
			// already accepted on their side.  Local participants on this kernel must still
			// go through an explicit AcceptChannelProposal so the channel activates only
			// after all parties have agreed.
			for senderID := range channel.ChannelProposal.SenderApprovals {
				if strings.HasPrefix(senderID, originKernel+":") {
					channel.ChannelProposal.SenderApprovals[senderID] = true
				}
			}
			for receiverID := range channel.ChannelProposal.ReceiverApprovals {
				if strings.HasPrefix(receiverID, originKernel+":") {
					channel.ChannelProposal.ReceiverApprovals[receiverID] = true
				}
			}

			// 检查是否所有参与者都已接受
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

		if allApproved {
			// 所有参与者都已接受，激活频道
			channel.Status = circulation.ChannelStatusActive
			channel.ChannelProposal.Status = circulation.NegotiationStatusAccepted
		channel.LastActivity = time.Now()

			// 记录 CHANNEL_CREATED evidence（kernel-2/3 上没有 ProposeChannel，所以在此记录）
			if s.auditLog != nil {
				s.auditLog.SubmitBasicEvidence(
					s.multiKernelManager.config.KernelID,
					evidence.EventTypeChannelCreated,
					channel.ChannelID,
					"",
					evidence.DirectionInternal,
					"",
				)
			}

			// 启动数据分发协程
			go channel.StartDataDistribution()

			// 通知所有参与者频道已激活
			notification := &pb.ChannelNotification{
					ChannelId:         channel.ChannelID,
					CreatorId:         channel.CreatorID,
					SenderIds:         channel.SenderIDs,
					ReceiverIds:       channel.ReceiverIDs,
					Encrypted:         channel.Encrypted,
					DataTopic:         channel.DataTopic,
					CreatedAt:         channel.CreatedAt.Unix(),
					NegotiationStatus: pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED,
				}
				if channel.ChannelProposal != nil {
					notification.ProposalId = channel.ChannelProposal.ProposalID
				}

				// 通知所有参与者
				for _, senderID := range channel.SenderIDs {
					if err := s.notifyParticipant(senderID, notification); err != nil {
						log.Printf("[WARN] Failed to notify sender %s: %v", senderID, err)
					}
				}
				for _, receiverID := range channel.ReceiverIDs {
					if err := s.notifyParticipant(receiverID, notification); err != nil {
						log.Printf("[WARN] Failed to notify receiver %s: %v", receiverID, err)
					}
				}

				// 返回成功响应
				return &pb.NotifyChannelResponse{
					Success: true,
					Message: "channel activated",
				}, nil
			}

			// 如果不是所有参与者都已接受，设置标志但保持状态为 proposed
			notifiedForAccept = true

			// 远端参与方的批准状态由 AcceptChannelProposal 转发链路统一更新，
			// 但在 NotifyChannelCreated 收到 ACCEPTED 通知时，我们需要使用正确的方式来更新状态
			// 从 originKernel 发来的 ACCEPTED 说明该 kernel 上的参与者已接受

			// 遍历 ReceiverIDs 和 SenderIDs，找到属于 originKernel 的参与者并更新批准状态
			for _, receiverID := range channel.ReceiverIDs {
				// 检查是否以 originKernel: 开头
				if strings.HasPrefix(receiverID, originKernel+":") {
					channel.ChannelProposal.ReceiverApprovals[receiverID] = true
				}
			}
			for _, senderID := range channel.SenderIDs {
				if strings.HasPrefix(senderID, originKernel+":") {
					channel.ChannelProposal.SenderApprovals[senderID] = true
				}
			}

			// 检查是否所有参与者都已接受，如果是则激活频道
			allApproved = true
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

		if allApproved {
			// 所有参与者都已接受，激活频道
			channel.Status = circulation.ChannelStatusActive
			channel.ChannelProposal.Status = circulation.NegotiationStatusAccepted
			channel.LastActivity = time.Now()

			// 启动数据分发协程
			go channel.StartDataDistribution()

			// 通知所有参与者
			notification := &pb.ChannelNotification{
				ChannelId:         channel.ChannelID,
				CreatorId:         channel.CreatorID,
				SenderIds:         channel.SenderIDs,
				ReceiverIds:       channel.ReceiverIDs,
				Encrypted:         channel.Encrypted,
				DataTopic:         channel.DataTopic,
				CreatedAt:         channel.CreatedAt.Unix(),
				NegotiationStatus: pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED,
			}
			if channel.ChannelProposal != nil {
				notification.ProposalId = channel.ChannelProposal.ProposalID
			}

			// 记录 CHANNEL_CREATED evidence（kernel-2/3 上没有 ProposeChannel，所以在此记录）
			if s.auditLog != nil {
				s.auditLog.SubmitBasicEvidence(
					s.multiKernelManager.config.KernelID,
					evidence.EventTypeChannelCreated,
					channel.ChannelID,
					"",
					evidence.DirectionInternal,
					"",
				)
			}

			localKernelID := s.multiKernelManager.config.KernelID
			for _, senderID := range channel.SenderIDs {
					isLocal := true
					if strings.Contains(senderID, ":") {
						parts := strings.SplitN(senderID, ":", 2)
						if parts[0] != localKernelID {
							isLocal = false
						}
					}
					if isLocal {
						if err := s.notifyParticipant(senderID, notification); err != nil {
							log.Printf("[WARN] Failed to notify sender %s: %v", senderID, err)
						}
					}
				}
				for _, receiverID := range channel.ReceiverIDs {
					isLocal := true
					if strings.Contains(receiverID, ":") {
						parts := strings.SplitN(receiverID, ":", 2)
						if parts[0] != localKernelID {
							isLocal = false
						}
					}
					if isLocal {
						if err := s.notifyParticipant(receiverID, notification); err != nil {
							log.Printf("[WARN] Failed to notify receiver %s: %v", receiverID, err)
						}
					}
				}

				// 通知远端内核频道已激活
				go s.notifyOtherKernelsChannelActivated(channel, notification)

				return &pb.NotifyChannelResponse{
					Success: true,
					Message: "channel activated",
				}, nil
			}

			// 通知所有本地参与者有新的频道提议（需要 accept）
			notification := &pb.ChannelNotification{
				ChannelId:         channel.ChannelID,
				CreatorId:         channel.CreatorID,
				SenderIds:         channel.SenderIDs,
				ReceiverIds:       channel.ReceiverIDs,
				Encrypted:         channel.Encrypted,
				DataTopic:         channel.DataTopic,
				CreatedAt:         channel.CreatedAt.Unix(),
				NegotiationStatus: pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_PROPOSED,
			}
			if channel.ChannelProposal != nil {
				notification.ProposalId = channel.ChannelProposal.ProposalID
			}

			// 通知所有本地参与者（去重，避免重复通知同一个连接器）
			localKernelID := s.multiKernelManager.config.KernelID
			notifiedParticipants := make(map[string]bool) // 用于去重
			for _, senderID := range channel.SenderIDs {
				// 提取裸 connector ID 用于去重
				rawConnectorID := senderID
				if idx := strings.LastIndex(senderID, ":"); idx != -1 {
					rawConnectorID = senderID[idx+1:]
				}

				// 检查是否已经通知过（去重）
				if notifiedParticipants[rawConnectorID] {
					continue
				}

				// 检查是否是本地参与者（不含 ":" 或 kernelID 与本地相同）
				isLocal := true
				if strings.Contains(senderID, ":") {
					parts := strings.SplitN(senderID, ":", 2)
					if parts[0] != localKernelID {
						isLocal = false
					}
				}
				if isLocal {
					notifiedParticipants[rawConnectorID] = true
					if err := s.notifyParticipant(senderID, notification); err != nil {
						log.Printf("[WARN] Failed to notify sender %s: %v", senderID, err)
					} else {
					}
				}
			}
			for _, receiverID := range channel.ReceiverIDs {
				// 提取裸 connector ID 用于去重
				rawConnectorID := receiverID
				if idx := strings.LastIndex(receiverID, ":"); idx != -1 {
					rawConnectorID = receiverID[idx+1:]
				}

				// 检查是否已经通知过（去重）
				if notifiedParticipants[rawConnectorID] {
					continue
				}

				// 检查是否是本地参与者（不含 ":" 或 kernelID 与本地相同）
				isLocal := true
				if strings.Contains(receiverID, ":") {
					parts := strings.SplitN(receiverID, ":", 2)
					if parts[0] != localKernelID {
						isLocal = false
					}
				}
				if isLocal {
					notifiedParticipants[rawConnectorID] = true
					if err := s.notifyParticipant(receiverID, notification); err != nil {
						log.Printf("[WARN] Failed to notify receiver %s: %v", receiverID, err)
					} else {
					}
				}
			}
		} else if originStatus == "REJECTED" {
			channel.ChannelProposal.Status = circulation.NegotiationStatusRejected
			channel.Status = circulation.ChannelStatusClosed
		}
	}

	// 如果频道已存在但收到远端的 ACCEPTED 通知
	// 说明创建者已接受，但本地仍需 accept 才能激活
	// 只有在第一次没有通知过的情况下才通知（避免重复通知）
	if channel.Status == circulation.ChannelStatusProposed && originStatus == "ACCEPTED" && !notifiedForAccept {
		log.Printf("Channel %s: creator has accepted, waiting for local accept",
			channel.ChannelID, originKernel)

		// 更新协商状态为 proposed（如果之前不是）
		if channel.ChannelProposal != nil && channel.ChannelProposal.Status != circulation.NegotiationStatusProposed {
			channel.ChannelProposal.Status = circulation.NegotiationStatusProposed
		}

		// 通知所有本地参与者
		notification := &pb.ChannelNotification{
			ChannelId:         channel.ChannelID,
			CreatorId:         channel.CreatorID,
			SenderIds:         channel.SenderIDs,
			ReceiverIds:       channel.ReceiverIDs,
			Encrypted:         channel.Encrypted,
			DataTopic:         channel.DataTopic,
			CreatedAt:         channel.CreatedAt.Unix(),
			NegotiationStatus: pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_PROPOSED,
		}
		if channel.ChannelProposal != nil {
			notification.ProposalId = channel.ChannelProposal.ProposalID
		}

		// 通知所有本地参与者（去重，避免重复通知同一个连接器）
		notifiedParticipants := make(map[string]bool) // 用于去重
		for _, senderID := range channel.SenderIDs {
			// 提取裸 connector ID 用于去重
			rawConnectorID := senderID
			if idx := strings.LastIndex(senderID, ":"); idx != -1 {
				rawConnectorID = senderID[idx+1:]
			}

			// 检查是否已经通知过（去重）
			if notifiedParticipants[rawConnectorID] {
				continue
			}

			// 检查是否是本地参与者
			isLocal := true
			if strings.Contains(senderID, ":") {
				parts := strings.SplitN(senderID, ":", 2)
				if parts[0] != s.multiKernelManager.config.KernelID {
					isLocal = false
				}
			}
			if isLocal {
				notifiedParticipants[rawConnectorID] = true
				if err := s.notifyParticipant(senderID, notification); err != nil {
					log.Printf("[WARN] Failed to notify sender %s: %v", senderID, err)
				}
			}
		}
		for _, receiverID := range channel.ReceiverIDs {
			// 提取裸 connector ID 用于去重
			rawConnectorID := receiverID
			if idx := strings.LastIndex(receiverID, ":"); idx != -1 {
				rawConnectorID = receiverID[idx+1:]
			}

			// 检查是否已经通知过（去重）
			if notifiedParticipants[rawConnectorID] {
				continue
			}

			// 检查是否是本地参与者
			isLocal := true
			if strings.Contains(receiverID, ":") {
				parts := strings.SplitN(receiverID, ":", 2)
				if parts[0] != s.multiKernelManager.config.KernelID {
					isLocal = false
				}
			}
			if isLocal {
				notifiedParticipants[rawConnectorID] = true
				if err := s.notifyParticipant(receiverID, notification); err != nil {
					log.Printf("[WARN] Failed to notify receiver %s: %v", receiverID, err)
				}
			}
		}

		// 在通知本地参与者后，更新远端参与者的批准状态并检查是否所有参与者都已接受
		// 从 originKernel 发来的 ACCEPTED 说明该 kernel 上的参与者已接受
		for _, receiverID := range channel.ReceiverIDs {
			// 检查是否以 originKernel: 开头
			if strings.HasPrefix(receiverID, originKernel+":") {
				channel.ChannelProposal.ReceiverApprovals[receiverID] = true
			}
		}
		for _, senderID := range channel.SenderIDs {
			if strings.HasPrefix(senderID, originKernel+":") {
				channel.ChannelProposal.SenderApprovals[senderID] = true
			}
		}

		// 检查是否所有参与者都已接受，如果是则激活频道
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

		if allApproved {
			// 所有参与者都已接受，激活频道
			channel.Status = circulation.ChannelStatusActive
			channel.ChannelProposal.Status = circulation.NegotiationStatusAccepted
			channel.LastActivity = time.Now()

			// 启动数据分发协程
			go channel.StartDataDistribution()

			// 通知所有参与者频道已激活
			activatedNotification := &pb.ChannelNotification{
				ChannelId:         channel.ChannelID,
				CreatorId:         channel.CreatorID,
				SenderIds:         channel.SenderIDs,
				ReceiverIds:       channel.ReceiverIDs,
				Encrypted:         channel.Encrypted,
				DataTopic:         channel.DataTopic,
				CreatedAt:         channel.CreatedAt.Unix(),
				NegotiationStatus: pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED,
			}
			if channel.ChannelProposal != nil {
				activatedNotification.ProposalId = channel.ChannelProposal.ProposalID
			}
			// 通知所有参与者
			for _, senderID := range channel.SenderIDs {
				s.notifyParticipant(senderID, activatedNotification)
			}
			for _, receiverID := range channel.ReceiverIDs {
				s.notifyParticipant(receiverID, activatedNotification)
			}
		}
	}

	// 设置远端接收者映射（用于跨内核数据转发）
	for _, senderID := range channel.SenderIDs {
		if strings.Contains(senderID, ":") {
			parts := strings.SplitN(senderID, ":", 2)
			kernelID := parts[0]
			connectorID := parts[1]
			channel.SetRemoteReceiver(connectorID, kernelID)
		}
	}
	for _, receiverID := range channel.ReceiverIDs {
		if strings.Contains(receiverID, ":") {
			parts := strings.SplitN(receiverID, ":", 2)
			kernelID := parts[0]
			connectorID := parts[1]
			channel.SetRemoteReceiver(connectorID, kernelID)
		}
	}

	// 构造通知并发送
	notification := &pb.ChannelNotification{
		ChannelId:   channel.ChannelID,
		CreatorId:   channel.CreatorID,
		SenderIds:   channel.SenderIDs,
		ReceiverIds: channel.ReceiverIDs,
		Encrypted:   channel.Encrypted,
		DataTopic:   channel.DataTopic,
		CreatedAt:   channel.CreatedAt.Unix(),
		NegotiationStatus: func() pb.ChannelNegotiationStatus {
			if channel.Status == circulation.ChannelStatusActive {
				return pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED
			}
			return pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_PROPOSED
		}(),
	}
	if channel.ChannelProposal != nil {
		notification.ProposalId = channel.ChannelProposal.ProposalID
	}

	if err := s.notifyParticipant(req.ReceiverId, notification); err != nil {
		log.Printf("[WARN]️ Failed to notify receiver %s of channel %s: %v", req.ReceiverId, channel.ChannelID, err)
		return &pb.NotifyChannelResponse{
			Success: false,
			Message: fmt.Sprintf("failed to notify: %v", err),
		}, nil
	}
	// 减少日志输出 - 成功通知已经在调用方记录

	return &pb.NotifyChannelResponse{
		Success: true,
		Message: "notification sent successfully",
	}, nil
}

// GetChannelInfo 获取频道信息
func (s *ChannelServiceServer) GetChannelInfo(ctx context.Context, req *pb.GetChannelInfoRequest) (*pb.GetChannelInfoResponse, error) {
	// 验证请求者身份
	if err := security.VerifyConnectorID(ctx, req.RequesterId); err != nil {
		return &pb.GetChannelInfoResponse{
			Found:   false,
			Message: fmt.Sprintf("authentication failed: %v", err),
		}, nil
	}

	// 获取频道
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	if err != nil {
		return &pb.GetChannelInfoResponse{
			Found:   false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	// 统一频道都作为数据频道处理

	// 获取协商状态
	var negotiationStatus pb.ChannelNegotiationStatus
	var proposalId string

	if channel.ChannelProposal != nil {
		switch channel.ChannelProposal.Status {
		case circulation.NegotiationStatusProposed:
			negotiationStatus = pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_PROPOSED
		case circulation.NegotiationStatusAccepted:
			negotiationStatus = pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED
		case circulation.NegotiationStatusRejected:
			negotiationStatus = pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_REJECTED
		default:
			negotiationStatus = pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_UNKNOWN
		}
		proposalId = channel.ChannelProposal.ProposalID
	} else {
		// 如果没有提议信息，说明频道已激活
		if channel.Status == circulation.ChannelStatusActive {
			negotiationStatus = pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED
		} else {
			negotiationStatus = pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_UNKNOWN
		}
	}

	return &pb.GetChannelInfoResponse{
		Found:             true,
		ChannelId:         channel.ChannelID,
		CreatorId:         channel.CreatorID,
		ApproverId:        channel.ApproverID,
		SenderIds:         channel.SenderIDs,
		ReceiverIds:       channel.ReceiverIDs,
		Encrypted:         channel.Encrypted,
		DataTopic:         channel.DataTopic,
		Status:            string(channel.Status),
		CreatedAt:         channel.CreatedAt.Unix(),
		LastActivity:      channel.LastActivity.Unix(),
		NegotiationStatus: negotiationStatus,
		ProposalId:        proposalId,
		Message:           "channel found",
	}, nil
}

// RequestPermissionChange 申请权限变更
func (s *ChannelServiceServer) RequestPermissionChange(ctx context.Context, req *pb.RequestPermissionChangeRequest) (*pb.RequestPermissionChangeResponse, error) {
	// 验证请求者身份
	if err := security.VerifyConnectorID(ctx, req.RequesterId); err != nil {
		return &pb.RequestPermissionChangeResponse{
			Success: false,
			Message: fmt.Sprintf("authentication failed: %v", err),
		}, nil
	}

	// 获取频道
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	if err != nil {
		return &pb.RequestPermissionChangeResponse{
			Success: false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	// 申请权限变更
	request, err := channel.RequestPermissionChange(req.RequesterId, req.ChangeType, req.TargetId, req.Reason)
	if err != nil {
		return &pb.RequestPermissionChangeResponse{
			Success: false,
			Message: fmt.Sprintf("failed to request permission change: %v", err),
		}, nil
	}

	// 记录审计日志
	eventType := evidence.EventTypePermissionRequest
	if strings.Contains(req.ChangeType, "remove") {
		eventType = evidence.EventTypePermissionRevoked
	}

	s.auditLog.SubmitBasicEvidence(
		req.RequesterId,
		eventType,
		req.ChannelId,
		"", // dataHash 为空：权限变更不涉及业务数据哈希
		evidence.DirectionInternal,
		req.TargetId,
	)

	// 注意：不需要手动同步到远程内核，因为 broadcastPermissionRequest 会自动处理跨内核转发
	// 这样可以避免重复存储

	return &pb.RequestPermissionChangeResponse{
		Success:   true,
		RequestId: request.RequestID,
		Message:   "permission change request submitted successfully",
	}, nil
}

// syncPermissionRequestToRemoteKernels 同步权限请求到其他相关内核
func (s *ChannelServiceServer) syncPermissionRequestToRemoteKernels(req *pb.RequestPermissionChangeRequest, request *circulation.PermissionChangeRequest) {
	if s.multiKernelManager == nil {
		return
	}

	localKernelID := s.multiKernelManager.GetKernelID()

	// 获取频道的远程参与者
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	if err != nil {
		log.Printf("Failed to get channel for permission sync: %v", err)
		return
	}

	// 收集远程内核
	remoteKernels := make(map[string]bool)
	for _, senderID := range channel.SenderIDs {
		if strings.Contains(senderID, ":") {
			parts := strings.SplitN(senderID, ":", 2)
			kernelID := parts[0]
			if kernelID != localKernelID {
				remoteKernels[kernelID] = true
			}
		}
	}
	for _, receiverID := range channel.ReceiverIDs {
		if strings.Contains(receiverID, ":") {
			parts := strings.SplitN(receiverID, ":", 2)
			kernelID := parts[0]
			if kernelID != localKernelID {
				remoteKernels[kernelID] = true
			}
		}
	}

	// 同步到每个远程内核
	for kernelID := range remoteKernels {
		s.syncPermissionRequestToKernel(kernelID, req.ChannelId, request)
	}
}

// syncPermissionRequestToKernel 同步权限请求到指定内核
func (s *ChannelServiceServer) syncPermissionRequestToKernel(kernelID, channelID string, request *circulation.PermissionChangeRequest) {
	s.multiKernelManager.kernelsMu.RLock()
	kinfo, exists := s.multiKernelManager.kernels[kernelID]
	s.multiKernelManager.kernelsMu.RUnlock()

	if !exists {
		log.Printf("Kernel %s not found for permission request sync", kernelID)
		return
	}

	// 连接到目标内核的 KernelService
	conn, err := s.multiKernelManager.connectToKernelIdentityService(kinfo)
	if err != nil {
		log.Printf("Failed to connect to kernel %s for permission sync: %v", kernelID, err)
		return
	}
	defer conn.Close()

	client := pb.NewKernelServiceClient(conn)
	localKernelID := s.multiKernelManager.GetKernelID()

	_, err = client.SyncPermissionRequest(context.Background(), &pb.SyncPermissionRequestRequest{
		SourceKernelId: localKernelID,
		ChannelId:     channelID,
		Request: &pb.PermissionChangeRequest{
			RequestId:   request.RequestID,
			RequesterId: request.RequesterID,
			ChannelId:   request.ChannelID,
			ChangeType:  request.ChangeType,
			TargetId:   request.TargetID,
			Reason:     request.Reason,
			Status:     request.Status,
			CreatedAt:  request.CreatedAt.Unix(),
		},
	})

	if err != nil {
		log.Printf("Failed to sync permission request to kernel %s: %v", kernelID, err)
		return
	}

	log.Printf("Successfully synced permission request %s to kernel %s", request.RequestID, kernelID)
}

// ApprovePermissionChange 批准权限变更
func (s *ChannelServiceServer) ApprovePermissionChange(ctx context.Context, req *pb.ApprovePermissionChangeRequest) (*pb.ApprovePermissionChangeResponse, error) {
	// 验证批准者身份
	if err := security.VerifyConnectorID(ctx, req.ApproverId); err != nil {
		return &pb.ApprovePermissionChangeResponse{
			Success: false,
			Message: fmt.Sprintf("authentication failed: %v", err),
		}, nil
	}

	// 获取频道
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	if err != nil {
		return &pb.ApprovePermissionChangeResponse{
			Success: false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	// 批准权限变更
	if err := channel.ApprovePermissionChange(req.ApproverId, req.RequestId); err != nil {
		return &pb.ApprovePermissionChangeResponse{
			Success: false,
			Message: fmt.Sprintf("failed to approve permission change: %v", err),
		}, nil
	}

	// 获取权限请求信息用于存证
	permRequest := channel.GetPermissionRequestByID(req.RequestId)
	targetID := ""
	if permRequest != nil {
		targetID = permRequest.TargetID
	}

	// 异步记录审计日志，避免阻塞 gRPC 响应
	go s.auditLog.SubmitBasicEvidence(
		req.ApproverId,
		evidence.EventTypePermissionGranted,
		req.ChannelId,
		"", // dataHash 为空：权限授予不涉及业务数据哈希
		evidence.DirectionInternal,
		targetID,
	)

	return &pb.ApprovePermissionChangeResponse{
		Success: true,
		Message: "permission change approved successfully",
	}, nil
}

// ------------------------------------------------------------
// 频道订阅申请相关方法实现
// ------------------------------------------------------------

// RequestChannelSubscription 申请订阅频道（频道外连接器使用）
func (s *ChannelServiceServer) RequestChannelSubscription(ctx context.Context, req *pb.RequestChannelSubscriptionRequest) (*pb.RequestChannelSubscriptionResponse, error) {
	// 验证申请者身份
	if err := security.VerifyConnectorID(ctx, req.SubscriberId); err != nil {
		return &pb.RequestChannelSubscriptionResponse{
			Success: false,
			Message: fmt.Sprintf("authentication failed: %v", err),
		}, nil
	}

	// 获取频道
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	if err != nil {
		return &pb.RequestChannelSubscriptionResponse{
			Success: false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	// 验证申请的角色
	if req.Role != "sender" && req.Role != "receiver" {
		return &pb.RequestChannelSubscriptionResponse{
			Success: false,
			Message: "invalid role: must be 'sender' or 'receiver'",
		}, nil
	}

	// 申请订阅（频道外连接器可以申请）
	request, err := channel.RequestChannelSubscription(req.SubscriberId, req.Role, req.Reason)
	if err != nil {
		return &pb.RequestChannelSubscriptionResponse{
			Success: false,
			Message: fmt.Sprintf("failed to request channel subscription: %v", err),
		}, nil
	}

	// 记录审计日志
	s.auditLog.SubmitBasicEvidence(
		req.SubscriberId,
		evidence.EventTypePermissionRequest,
		req.ChannelId,
		"", // dataHash 为空：权限请求不涉及业务数据哈希
		evidence.DirectionInternal,
		req.ChannelId,
	)

	return &pb.RequestChannelSubscriptionResponse{
		Success:   true,
		RequestId: request.RequestID,
		Message:   "channel subscription request submitted successfully",
	}, nil
}

// ApproveChannelSubscription 批准订阅申请
func (s *ChannelServiceServer) ApproveChannelSubscription(ctx context.Context, req *pb.ApproveChannelSubscriptionRequest) (*pb.ApproveChannelSubscriptionResponse, error) {
	// 验证批准者身份
	if err := security.VerifyConnectorID(ctx, req.ApproverId); err != nil {
		return &pb.ApproveChannelSubscriptionResponse{
			Success: false,
			Message: fmt.Sprintf("authentication failed: %v", err),
		}, nil
	}

	// 获取频道
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	if err != nil {
		return &pb.ApproveChannelSubscriptionResponse{
			Success: false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	// 批准订阅申请
	subscriberID, err := channel.ApproveChannelSubscription(req.ApproverId, req.RequestId)
	if err != nil {
		return &pb.ApproveChannelSubscriptionResponse{
			Success: false,
			Message: fmt.Sprintf("failed to approve channel subscription: %v", err),
		}, nil
	}

	// 记录审计日志
	s.auditLog.SubmitBasicEvidence(
		req.ApproverId,
		evidence.EventTypePermissionGranted,
		req.ChannelId,
		"", // dataHash 为空：权限授予不涉及业务数据哈希
		evidence.DirectionInternal,
		subscriberID,
	)

	// 发送频道更新通知给新订阅者
	go func() {
		if err := s.sendChannelUpdateNotification(channel, subscriberID); err != nil {
			log.Printf("[WARN]️ Failed to send channel update notification to %s: %v", subscriberID, err)
		}
	}()

	return &pb.ApproveChannelSubscriptionResponse{
		Success: true,
		Message: "channel subscription approved successfully",
	}, nil
}

// RejectChannelSubscription 拒绝订阅申请
func (s *ChannelServiceServer) RejectChannelSubscription(ctx context.Context, req *pb.RejectChannelSubscriptionRequest) (*pb.RejectChannelSubscriptionResponse, error) {
	// 验证批准者身份
	if err := security.VerifyConnectorID(ctx, req.ApproverId); err != nil {
		return &pb.RejectChannelSubscriptionResponse{
			Success: false,
			Message: fmt.Sprintf("authentication failed: %v", err),
		}, nil
	}

	// 获取频道
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	if err != nil {
		return &pb.RejectChannelSubscriptionResponse{
			Success: false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	// 拒绝订阅申请
	if err := channel.RejectChannelSubscription(req.ApproverId, req.RequestId, req.Reason); err != nil {
		return &pb.RejectChannelSubscriptionResponse{
			Success: false,
			Message: fmt.Sprintf("failed to reject channel subscription: %v", err),
		}, nil
	}

	// 记录审计日志
	// 获取被拒绝的订阅者ID
	rejectedSubscriber := ""
	requestID := req.RequestId // pb 请求中的 RequestId
	for _, permReq := range channel.GetPermissionRequests() {
		if permReq.RequestID == requestID {
			rejectedSubscriber = permReq.RequesterID
			break
		}
	}

	s.auditLog.SubmitBasicEvidence(
		req.ApproverId,
		evidence.EventTypePermissionDenied,
		req.ChannelId,
		"", // dataHash 为空：权限拒绝不涉及业务数据哈希
		evidence.DirectionInternal,
		rejectedSubscriber,
	)

	return &pb.RejectChannelSubscriptionResponse{
		Success: true,
		Message: "channel subscription rejected successfully",
	}, nil
}

// sendChannelUpdateNotification 发送频道更新通知给指定连接器
func (s *ChannelServiceServer) sendChannelUpdateNotification(channel *circulation.Channel, subscriberID string) error {
	// 构造频道通知（统一频道）

	negotiationStatus := pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED

	notification := &pb.ChannelNotification{
		ChannelId:   channel.ChannelID,
		CreatorId:   channel.CreatorID,
		SenderIds:   channel.SenderIDs,
		ReceiverIds: channel.ReceiverIDs,
		// 统一频道
		Encrypted: channel.Encrypted,
		// 统一频道无关联频道
		DataTopic:         channel.DataTopic,
		CreatedAt:         channel.CreatedAt.Unix(),
		NegotiationStatus: negotiationStatus,
	}

	// 发送通知给指定的订阅者（支持远端 kernel:connector 格式）
	return s.notifyParticipant(subscriberID, notification)
}

// ------------------------------------------------------------
// 权限变更相关方法实现（频道内连接器使用）
// ------------------------------------------------------------

// RejectPermissionChange 拒绝权限变更
func (s *ChannelServiceServer) RejectPermissionChange(ctx context.Context, req *pb.RejectPermissionChangeRequest) (*pb.RejectPermissionChangeResponse, error) {
	// 验证批准者身份
	if err := security.VerifyConnectorID(ctx, req.ApproverId); err != nil {
		return &pb.RejectPermissionChangeResponse{
			Success: false,
			Message: fmt.Sprintf("authentication failed: %v", err),
		}, nil
	}

	// 获取频道
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	if err != nil {
		return &pb.RejectPermissionChangeResponse{
			Success: false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	// 拒绝权限变更
	if err := channel.RejectPermissionChange(req.ApproverId, req.RequestId, req.Reason); err != nil {
		return &pb.RejectPermissionChangeResponse{
			Success: false,
			Message: fmt.Sprintf("failed to reject permission change: %v", err),
		}, nil
	}

	// 获取权限请求信息用于存证
	permRequest := channel.GetPermissionRequestByID(req.RequestId)
	targetID := ""
	if permRequest != nil {
		targetID = permRequest.TargetID
	}

	// 记录审计日志
	s.auditLog.SubmitBasicEvidence(
		req.ApproverId,
		evidence.EventTypePermissionDenied,
		req.ChannelId,
		"", // dataHash 为空：权限拒绝不涉及业务数据哈希
		evidence.DirectionInternal,
		targetID,
	)

	return &pb.RejectPermissionChangeResponse{
		Success: true,
		Message: "permission change rejected successfully",
	}, nil
}

// GetPermissionRequests 获取权限变更请求列表
func (s *ChannelServiceServer) GetPermissionRequests(ctx context.Context, req *pb.GetPermissionRequestsRequest) (*pb.GetPermissionRequestsResponse, error) {
	// 验证请求者身份
	if err := security.VerifyConnectorID(ctx, req.RequesterId); err != nil {
		return &pb.GetPermissionRequestsResponse{
			Success: false,
			Message: fmt.Sprintf("authentication failed: %v", err),
		}, nil
	}

	// 获取频道
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	if err != nil {
		return &pb.GetPermissionRequestsResponse{
			Success: false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	// 验证请求者是否是频道参与者或批准者
	if !channel.IsParticipant(req.RequesterId) && req.RequesterId != channel.ApproverID {
		return &pb.GetPermissionRequestsResponse{
			Success: false,
			Message: "only channel participants or approver can view permission requests",
		}, nil
	}

	// 获取权限变更请求列表
	requests := channel.GetPermissionRequests()

	// 转换为proto格式
	pbRequests := make([]*pb.PermissionChangeRequest, 0, len(requests))
	for _, request := range requests {
		req := &pb.PermissionChangeRequest{
			RequestId:   request.RequestID,
			RequesterId: request.RequesterID,
			ChannelId:   request.ChannelID,
			ChangeType:  request.ChangeType,
			TargetId:    request.TargetID,
			Reason:      request.Reason,
			Status:      request.Status,
			CreatedAt:   request.CreatedAt.Unix(),
			ApprovedBy:  request.ApprovedBy,
		}
		if request.ApprovedAt != nil {
			req.ApprovedAt = request.ApprovedAt.Unix()
		}
		pbRequests = append(pbRequests, req)
	}

	// 添加从其他内核同步过来的权限请求
	if s.multiKernelManager != nil {
		remoteRequests := s.multiKernelManager.GetRemotePermissionRequests(req.ChannelId)
		for _, r := range remoteRequests {
			req := &pb.PermissionChangeRequest{
				RequestId:   r.RequestID,
				RequesterId: r.RequesterID,
				ChannelId:   r.ChannelID,
				ChangeType:  r.ChangeType,
				TargetId:    r.TargetID,
				Reason:      r.Reason,
				Status:      r.Status,
				CreatedAt:   r.CreatedAt.Unix(),
			}
			pbRequests = append(pbRequests, req)
		}
	}

	return &pb.GetPermissionRequestsResponse{
		Success:  true,
		Requests: pbRequests,
		Message:  "permission requests retrieved successfully",
	}, nil
}

// determineDataCategory 根据 payload 判断数据类型
// 返回 "business" 表示业务数据，返回控制消息类型如 "channel_proposal" 等
func determineDataCategory(payload []byte) string {
	// 尝试解析为 JSON
	var msg struct {
		MessageType string `json:"message_type"`
	}
	if err := json.Unmarshal(payload, &msg); err == nil {
		// 如果成功解析，检查 MessageType
		if msg.MessageType != "" {
			return msg.MessageType
		}
	}
	// 无法解析或没有 MessageType，默认为业务数据
	return "business"
}

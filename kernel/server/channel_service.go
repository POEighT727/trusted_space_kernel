package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	pb "github.com/trusted-space/kernel/proto/kernel/v1"
	"github.com/trusted-space/kernel/kernel/circulation"
	"github.com/trusted-space/kernel/kernel/control"
	"github.com/trusted-space/kernel/kernel/evidence"
	"github.com/trusted-space/kernel/kernel/security"
)

// NotificationManager 通知管理器，管理接收方的通知通道
type NotificationManager struct {
	mu          sync.RWMutex
	notifications map[string]chan *pb.ChannelNotification // key: receiverID
	channelManager *circulation.ChannelManager
	registry      *control.Registry
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
	log.Printf("✓ Notification channel registered for connector %s", receiverID)
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
	nm.mu.RLock()
	notifyChan, exists := nm.notifications[receiverID]
	nm.mu.RUnlock()

	if !exists {
		log.Printf("⚠ Notification attempted for %s but no listener registered", receiverID)
		return fmt.Errorf("receiver %s is not waiting for notifications", receiverID)
	}

	// 检查连接器状态，决定是否自动订阅
	isActive := nm.registry.IsActive(receiverID)
	
	// 发送通知（所有连接器都会收到通知）
	select {
	case notifyChan <- notification:
		// 通知发送成功
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timeout sending notification to %s", receiverID)
	}

	// 如果连接器处于活跃状态，自动订阅频道
	if isActive {
		go nm.autoSubscribe(receiverID, notification.ChannelId)
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
	channelManager      *circulation.ChannelManager
	policyEngine        *control.PolicyEngine
	registry            *control.Registry
	auditLog            *evidence.AuditLog
	NotificationManager *NotificationManager
	multiKernelManager  *MultiKernelManager
}

// NewChannelServiceServer 创建频道服务
func NewChannelServiceServer(
	channelManager *circulation.ChannelManager,
	policyEngine *control.PolicyEngine,
	registry *control.Registry,
	auditLog *evidence.AuditLog,
	multiKernelManager *MultiKernelManager,
) *ChannelServiceServer {
	server := &ChannelServiceServer{
		channelManager:      channelManager,
		policyEngine:        policyEngine,
		registry:            registry,
		auditLog:            auditLog,
		NotificationManager: NewNotificationManager(channelManager, registry),
		multiKernelManager:  multiKernelManager,
}

	// 设置evidence频道创建通知回调
	channelManager.SetChannelCreatedCallback(server.notifyChannelCreated)
	log.Printf("✓ Evidence channel notification callback set")

	return server
}

// notifyChannelCreated 处理异步创建的频道通知（特别是evidence频道）
func (s *ChannelServiceServer) notifyChannelCreated(channel *circulation.Channel) {
	log.Printf("📢 发送异步创建频道通知: %s (发送方: %v, 接收方: %v)",
		channel.ChannelID, channel.SenderIDs, channel.ReceiverIDs)

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
		ProposalId:        "", // 异步创建的频道没有提议ID
	}

	// 添加存证配置（如果有）
	if channel.EvidenceConfig != nil {
		notification.EvidenceConfig = &pb.EvidenceConfig{
			Mode:           string(channel.EvidenceConfig.Mode),
			Strategy:       string(channel.EvidenceConfig.Strategy),
			ConnectorId:    channel.EvidenceConfig.ConnectorID,
			BackupEnabled:  channel.EvidenceConfig.BackupEnabled,
			RetentionDays:  int32(channel.EvidenceConfig.RetentionDays),
			CompressData:   channel.EvidenceConfig.CompressData,
			CustomSettings: channel.EvidenceConfig.CustomSettings,
		}
	}

	// 异步发送通知
	go func() {
		// 通知所有发送方
		for _, senderID := range channel.SenderIDs {
			if err := s.notifyParticipant(senderID, notification); err != nil {
				log.Printf("⚠ Failed to notify sender %s: %v", senderID, err)
			}
		}

		// 通知所有接收方
		for _, receiverID := range channel.ReceiverIDs {
			log.Printf("🔍 DEBUG: about to notify receiver: %s", receiverID)
			if err := s.notifyParticipant(receiverID, notification); err != nil {
				log.Printf("⚠ Failed to notify receiver %s: %v", receiverID, err)
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
				log.Printf("⚠ Failed to notify creator %s: %v", channel.CreatorID, err)
			}
		}
	}()
}

// notifyParticipant 将通知发送给本地或远端参与者（支持 kernel:connector 格式的远端转发）
func (s *ChannelServiceServer) notifyParticipant(participantID string, notification *pb.ChannelNotification) error {
	log.Printf("📨 notifyParticipant: participantID=%s, channel=%s, NegotiationStatus=%v", 
		participantID, notification.ChannelId, notification.NegotiationStatus)
	
	// 远端格式: kernelID:connectorID
	if strings.Contains(participantID, ":") {
		parts := strings.SplitN(participantID, ":", 2)
		kernelID := parts[0]
		connectorID := parts[1]

		// 如果目标内核是本内核，直接使用本地通知管理器
		if s.multiKernelManager != nil && s.multiKernelManager.config != nil && kernelID == s.multiKernelManager.config.KernelID {
			log.Printf("📨 Local notification to %s", connectorID)
			return s.NotificationManager.Notify(connectorID, notification)
		}

		// 查找目标内核连接信息
		s.multiKernelManager.kernelsMu.RLock()
		kinfo, exists := s.multiKernelManager.kernels[kernelID]
		s.multiKernelManager.kernelsMu.RUnlock()
		
		log.Printf("📨 Checking kernel %s: exists=%v, kinfo=%v, conn=%v", 
			kernelID, exists, kinfo != nil, kinfo != nil && kinfo.conn != nil)
		
		if !exists || kinfo == nil || kinfo.conn == nil {
			log.Printf("⚠️ Cannot forward to kernel %s: not connected", kernelID)
			return fmt.Errorf("not connected to kernel %s", kernelID)
		}

		// 使用远端内核的 IdentityService（主服务器端口）来调用 ChannelService.NotifyChannelCreated，
		// 因为 ChannelService 注册在主端口的 gRPC 服务上，而 kinfo.conn 是内核间的 kernel-to-kernel 连接。
		log.Printf("→ Forwarding channel notification to kernel %s for connector %s (channel %s)", kernelID, connectorID, notification.ChannelId)
		conn, err := s.multiKernelManager.connectToKernelIdentityService(kinfo)
		if err != nil {
			log.Printf("⚠ Failed to connect to identity service of kernel %s: %v", kernelID, err)
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
			log.Printf("📨 Adding ACCEPTED status to senderWithMeta: %s", senderWithMeta)
		} else if notification.NegotiationStatus == pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_REJECTED {
			senderWithMeta = fmt.Sprintf("%s|%s", senderWithMeta, "REJECTED")
		}

		log.Printf("📨 Calling NotifyChannelCreated on kernel %s: ReceiverId=%s, SenderId=%s", 
			kernelID, connectorID, senderWithMeta)
		
		resp, err := chClient.NotifyChannelCreated(context.Background(), &pb.NotifyChannelRequest{
			ReceiverId: connectorID,
			ChannelId:  notification.ChannelId,
			SenderId:   senderWithMeta,
			DataTopic:  notification.DataTopic,
		})
		if err != nil {
			log.Printf("⚠ Failed to forward notification to kernel %s: %v", kernelID, err)
		} else {
			log.Printf("✓ Notification forwarded to kernel %s for connector %s: success=%v, msg=%s", 
				kernelID, connectorID, resp.Success, resp.Message)
		}
		return err
	}

	// 本地参与者
	log.Printf("📨 Local notification to %s", participantID)
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
				s.auditLog.SubmitEvidence(
					creatorID,
					evidence.EventTypePolicyViolation,
					"",
					"",
					map[string]string{
						"sender":   senderID,
						"receiver": receiverID,
						"reason":   reason,
						"context":  "channel_proposal",
					},
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
	// 转换存证配置
	var evidenceConfig *circulation.EvidenceConfig
	if req.EvidenceConfig != nil {
		evidenceConfig = &circulation.EvidenceConfig{
			Mode:           circulation.EvidenceMode(req.EvidenceConfig.Mode),
			Strategy:       circulation.EvidenceStrategy(req.EvidenceConfig.Strategy),
			ConnectorID:    req.EvidenceConfig.ConnectorId,
			BackupEnabled:  req.EvidenceConfig.BackupEnabled,
			RetentionDays:  int(req.EvidenceConfig.RetentionDays),
			CompressData:   req.EvidenceConfig.CompressData,
			CustomSettings: req.EvidenceConfig.CustomSettings,
		}
	}

	channel, err := s.channelManager.ProposeChannel(
		creatorID,
		approverID,
		req.SenderIds,
		req.ReceiverIds,
		req.DataTopic,
		encrypted,
		evidenceConfig, // evidenceConfig
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

	// 记录审计日志
				s.auditLog.SubmitEvidence(
		creatorID,
		evidence.EventTypeChannelCreated, // 使用相同的审计类型，但添加上下文
		channel.ChannelID,
		channel.ChannelProposal.ProposalID,
					map[string]string{
			"senders":     fmt.Sprintf("%v", req.SenderIds),
			"receivers":   fmt.Sprintf("%v", req.ReceiverIds),
			"data_topic":  req.DataTopic,
			"channel_type": "unified", // 统一频道架构
			"encrypted":   fmt.Sprintf("%v", encrypted),
			"reason":      req.Reason,
			"context":     "channel_proposal",
					},
				)

	// 发送提议通知给相关方
	go func() {
		notification := &pb.ChannelNotification{
			ChannelId:         channel.ChannelID,
			CreatorId:         creatorID,
			SenderIds:         req.SenderIds,
			ReceiverIds:       req.ReceiverIds,
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
					log.Printf("⚠ Failed to notify receiver %s: %v", receiverID, err)
				}
			}
		}

		// 通知所有发送方需要接受提议（创建者已自动接受，不需要通知）
		for _, senderID := range req.SenderIds {
			if senderID != creatorID { // 创建者不需要收到通知，因为已经自动接受
				if err := s.notifyParticipant(senderID, notification); err != nil {
					log.Printf("⚠ Failed to notify sender %s: %v", senderID, err)
				}
			}
		}

		log.Printf("✓ 频道提议已创建，等待参与方确认")
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
		// 不是来自连接器证书，尝试判断是否来自已知内核（转发）
		certID, err2 := security.ExtractConnectorIDFromContext(ctx)
		if err2 != nil {
			return &pb.AcceptChannelProposalResponse{
				Success: false,
				Message: fmt.Sprintf("authentication failed: %v", err),
			}, nil
		}
		// 如果证书 CN 属于已知内核，并且请求的 AccepterId 以 "certID:" 前缀，则视为内核转发
		s.multiKernelManager.kernelsMu.RLock()
		_, isKernel := s.multiKernelManager.kernels[certID]
		s.multiKernelManager.kernelsMu.RUnlock()
		if !isKernel {
			return &pb.AcceptChannelProposalResponse{
				Success: false,
				Message: fmt.Sprintf("authentication failed: %v", err),
			}, nil
		}
		if !strings.HasPrefix(req.AccepterId, certID+":") {
			return &pb.AcceptChannelProposalResponse{
				Success: false,
				Message: fmt.Sprintf("authentication failed: forwarded accepter id mismatch (cert %s)", certID),
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

	// 如果频道已经是 active 状态，且这是来自远端内核的 accept，
	// 需要通知远端内核频道已激活（因为频道可能是在创建者自动接受时激活的）
	if channel.Status == circulation.ChannelStatusActive && isForwarded {
		log.Printf("✓ DEBUG: Channel %s already active, isForwarded=%v, notifying...", channel.ChannelID, isForwarded)
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
		go s.notifyOtherKernelsChannelActivated(channel, notification)

		// 同时通知本地创建者（如果创建者是本地参与者且尚未收到通知）
		log.Printf("✓ DEBUG: CreatorID=%s, contains colon=%v", channel.CreatorID, strings.Contains(channel.CreatorID, ":"))
		if channel.CreatorID != "" && !strings.Contains(channel.CreatorID, ":") {
			log.Printf("✓ DEBUG: About to notify creator %s", channel.CreatorID)
			if err := s.notifyParticipant(channel.CreatorID, notification); err != nil {
				log.Printf("⚠ Failed to notify creator %s of channel activation: %v", channel.CreatorID, err)
			} else {
				log.Printf("✓ Sent ACCEPTED notification to creator %s", channel.CreatorID)
			}
		}
	}

	// 记录审计日志
	s.auditLog.SubmitEvidence(
		req.AccepterId,
		evidence.EventTypeChannelCreated, // 频道正式创建
		req.ChannelId,
		req.ProposalId,
		map[string]string{
			"accepter": req.AccepterId,
			"context":  "channel_accepted",
		},
	)

	// 检查是否所有参与方都已确认
	allApproved := true
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

	// 设置远端接收者映射（用于跨内核数据转发）
	// 在频道激活时立即设置，而不是等到所有参与方都确认
	for _, receiverID := range channel.ReceiverIDs {
		if strings.Contains(receiverID, ":") {
			parts := strings.SplitN(receiverID, ":", 2)
			kernelID := parts[0]
			connectorID := parts[1]
			channel.SetRemoteReceiver(connectorID, kernelID)
		}
	}
	for _, senderID := range channel.SenderIDs {
		if strings.Contains(senderID, ":") {
			parts := strings.SplitN(senderID, ":", 2)
			kernelID := parts[0]
			connectorID := parts[1]
			channel.SetRemoteReceiver(connectorID, kernelID)
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
					Mode:           string(channel.EvidenceConfig.Mode),
					Strategy:       string(channel.EvidenceConfig.Strategy),
					ConnectorId:    channel.EvidenceConfig.ConnectorID,
					BackupEnabled:  channel.EvidenceConfig.BackupEnabled,
					RetentionDays:  int32(channel.EvidenceConfig.RetentionDays),
					CompressData:   channel.EvidenceConfig.CompressData,
					CustomSettings: channel.EvidenceConfig.CustomSettings,
				}
			}

			// 通知所有发送方
			for _, senderID := range channel.SenderIDs {
				if err := s.notifyParticipant(senderID, notification); err != nil {
					log.Printf("⚠ Failed to notify sender %s: %v", senderID, err)
				} else {
					log.Printf("✓ Sent ACCEPTED notification to sender %s", senderID)
				}
			}

			// 通知所有接收方
			for _, receiverID := range channel.ReceiverIDs {
				if err := s.notifyParticipant(receiverID, notification); err != nil {
					log.Printf("⚠ Failed to notify receiver %s: %v", receiverID, err)
				} else {
					log.Printf("✓ Sent ACCEPTED notification to receiver %s", receiverID)
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
					log.Printf("⚠ Failed to notify creator %s: %v", channel.CreatorID, err)
				} else {
					log.Printf("✓ Sent ACCEPTED notification to creator %s", channel.CreatorID)
				}
			}

			log.Printf("✓ 频道 %s 已正式创建，所有参与方已确认", channel.ChannelID)

			// 主动通知其他内核频道已激活（用于跨内核场景）
			// 这样 origin kernel 能更新其本地频道状态
			s.notifyOtherKernelsChannelActivated(channel, notification)
		}()
	}

	// 如果这次接受是由本地连接器发起（非内核转发），则将该接受通知转发给所有其他已连接的内核，
	// 以便 origin 内核或其他远端能够更新它们的提议状态。
	if !isForwarded {
		localKernelID := s.multiKernelManager.config.KernelID
		go func() {
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
					continue
				}
				chClient := pb.NewChannelServiceClient(conn)
				forwardAccepter := fmt.Sprintf("%s:%s", localKernelID, req.AccepterId)
				_, err = chClient.AcceptChannelProposal(context.Background(), &pb.AcceptChannelProposalRequest{
					AccepterId: forwardAccepter,
					ChannelId:  req.ChannelId,
					ProposalId: req.ProposalId,
				})
				conn.Close()
				_ = err // 静默转发：避免刷屏，失败由对端超时/状态查询兜底
			}
		}()
	}
	// 注意：当allApproved为false时，不再发送协商状态更新通知
	// 因为这会导致与拒绝通知混淆，而且协商仍在进行中

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
			log.Printf("⚠ Failed to connect to kernel %s for forwarding accept: %v", k.KernelID, err)
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
			log.Printf("⚠ Failed to forward accept to kernel %s: %v", k.KernelID, err)
		} else {
			log.Printf("✓ Forwarded accept from %s to kernel %s", accepterID, k.KernelID)
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
			log.Printf("⚠ Failed to connect to kernel %s for activation notification: %v", k.KernelID, err)
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
			log.Printf("⚠ Failed to notify kernel %s of channel activation: %v", k.KernelID, err)
		} else {
			log.Printf("✓ Notified kernel %s that channel %s is activated", k.KernelID, channel.ChannelID)
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
	s.auditLog.SubmitEvidence(
		req.RejecterId,
		evidence.EventTypeChannelClosed, // 频道被拒绝，相当于关闭
		req.ChannelId,
		req.ProposalId,
		map[string]string{
			"rejecter":  req.RejecterId,
			"reason":    req.Reason,
			"context":   "channel_rejected",
		},
	)
	
	// 异步通知频道创建者频道被拒绝
	go func() {
		channel, err := s.channelManager.GetChannel(req.ChannelId)
		if err != nil {
			log.Printf("⚠ Failed to get channel info: %v", err)
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
			log.Printf("⚠ Failed to notify creator %s: %v", channel.CreatorID, err)
		}

		// 通知所有发送方
		for _, senderID := range channel.SenderIDs {
			if err := s.notifyParticipant(senderID, notification); err != nil {
				log.Printf("⚠ Failed to notify sender %s: %v", senderID, err)
			}
		}
	}()

	return &pb.RejectChannelProposalResponse{
		Success: true,
		Message: "channel proposal rejected successfully",
	}, nil
}

// StreamData 处理数据流推送
func (s *ChannelServiceServer) StreamData(stream pb.ChannelService_StreamDataServer) error {
	ctx := stream.Context()
	var senderID string
	var senderIDWithKernel string // 带有 kernel 前缀的 senderID，用于跨内核存证
	var channelID string
	var dataHashAccumulator []byte
	var flowID string // 业务流程ID，用于跟踪完整的数据传输过程
	var isCrossKernel bool // 标记是否是跨内核频道

	for {
		packet, err := stream.Recv()
		if err == io.EOF {
			// 流结束，记录传输完成
			if channelID != "" && senderID != "" && flowID != "" {
				log.Printf("🔄 Recording TRANSFER_END for channel %s, sender %s, flow: %s", channelID, senderIDWithKernel, flowID)
				finalHash := sha256.Sum256(dataHashAccumulator)
				if _, err := s.auditLog.SubmitEvidenceWithFlowID(
					flowID,
					senderIDWithKernel,  // 使用带有 kernel 前缀的 senderID
					evidence.EventTypeTransferEnd,
					channelID,
					hex.EncodeToString(finalHash[:]),
					map[string]string{
						"packet_count": fmt.Sprintf("%d", len(dataHashAccumulator)/32),
					},
				); err != nil {
					log.Printf("⚠ Failed to submit TRANSFER_END evidence: %v", err)
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

			// 验证发送方身份
			if err := security.VerifyConnectorID(ctx, senderID); err != nil {
				return fmt.Errorf("sender verification failed: %v", err)
			}

			// 验证发送方是否是频道参与者
			if !channel.IsParticipant(senderID) {
				return fmt.Errorf("sender %s is not a participant of this channel", senderID)
			}

			// 检查是否是跨内核频道
			isCrossKernel = false
			senderIDWithKernel = senderID
			for _, receiverID := range channel.ReceiverIDs {
				if strings.Contains(receiverID, ":") {
					// 存在远端接收者，这是跨内核频道
					isCrossKernel = true
					break
				}
			}
			for _, senderIDInChannel := range channel.SenderIDs {
				if strings.Contains(senderIDInChannel, ":") {
					// 存在远端发送者，这是跨内核频道
					isCrossKernel = true
					break
				}
			}

			// 如果是跨内核频道，构建带有 kernel 前缀的 senderID
			if isCrossKernel && s.multiKernelManager != nil && s.multiKernelManager.config != nil {
				kernelID := s.multiKernelManager.config.KernelID
				senderIDWithKernel = fmt.Sprintf("%s:%s", kernelID, senderID)
				log.Printf("🔄 Cross-kernel channel detected, using senderIDWithKernel=%s", senderIDWithKernel)
			}

			// 生成业务流程ID（用于跟踪完整的数据传输过程）
			flowID = uuid.New().String()

			// 记录传输开始
			targetsStr := ""
			if len(packet.TargetIds) > 0 {
				targetsStr = fmt.Sprintf("%v", packet.TargetIds)
			} else {
				targetsStr = "broadcast"
			}

			// 生成业务流程ID
			flowID = uuid.New().String()

			log.Printf("🔄 Recording TRANSFER_START for channel %s, sender %s, flow: %s", channelID, senderIDWithKernel, flowID)
			if _, err := s.auditLog.SubmitEvidenceWithFlowID(
				flowID,
				senderIDWithKernel,  // 使用带有 kernel 前缀的 senderID
				evidence.EventTypeTransferStart,
				channelID,
				"",
				map[string]string{
					"targets": targetsStr,
				},
			); err != nil {
				log.Printf("⚠ Failed to submit TRANSFER_START evidence: %v", err)
			}
		}

		// 获取频道
		channel, err := s.channelManager.GetChannel(packet.ChannelId)
		if err != nil {
			return fmt.Errorf("channel not found: %v", err)
		}

		// 检查频道是否处于活跃状态（协商完成后才能传输数据）
		if channel.Status != circulation.ChannelStatusActive {
			return fmt.Errorf("channel is not active: status=%s", channel.Status)
		}


		// 推送数据到频道
		dataPacket := &circulation.DataPacket{
			ChannelID:      packet.ChannelId,
			SequenceNumber: packet.SequenceNumber,
			Payload:        packet.Payload,
			Signature:      packet.Signature,
			Timestamp:      packet.Timestamp,
			SenderID:       packet.SenderId,
			TargetIDs:      packet.TargetIds,
		}

		if err := channel.PushData(dataPacket); err != nil {
			return fmt.Errorf("failed to push data: %v", err)
		}

		// 累积数据哈希
		hash := sha256.Sum256(packet.Payload)
		dataHashAccumulator = append(dataHashAccumulator, hash[:]...)

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
	log.Printf("🔍 DEBUG SubscribeData: connector=%s, channel=%s", req.ConnectorId, req.ChannelId)
	ctx := stream.Context()

	// 验证订阅者身份
	if err := security.VerifyConnectorID(ctx, req.ConnectorId); err != nil {
		return fmt.Errorf("subscriber verification failed: %v", err)
	}

	// 检测是否是重启恢复
	isRecovery := s.channelManager.IsConnectorRestarting(req.ConnectorId)
	s.channelManager.MarkConnectorOnline(req.ConnectorId)
	log.Printf("🔍 DEBUG SubscribeData: %s marked as online", req.ConnectorId)

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
		log.Printf("🔄 Connector %s recovered from offline state, sending channel notification", req.ConnectorId)
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
					Mode:           string(channel.EvidenceConfig.Mode),
					Strategy:       string(channel.EvidenceConfig.Strategy),
					ConnectorId:    channel.EvidenceConfig.ConnectorID,
					BackupEnabled:  channel.EvidenceConfig.BackupEnabled,
					RetentionDays:  int32(channel.EvidenceConfig.RetentionDays),
					CompressData:   channel.EvidenceConfig.CompressData,
					CustomSettings: channel.EvidenceConfig.CustomSettings,
				}
			}

			// 发送通知给重新连接的连接器
			if err := s.notifyParticipant(req.ConnectorId, notification); err != nil {
				log.Printf("⚠️ Failed to send recovery notification to %s: %v", req.ConnectorId, err)
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
				ChannelId:      packet.ChannelID,
				SequenceNumber: packet.SequenceNumber,
				Payload:        packet.Payload,
				Signature:      packet.Signature,
				Timestamp:      packet.Timestamp,
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
	s.auditLog.SubmitEvidence(
		req.RequesterId,
		evidence.EventTypeChannelClosed,
		req.ChannelId,
		"",
		map[string]string{
			"closed_by": req.RequesterId,
		},
	)

	return &pb.CloseChannelResponse{
		Success: true,
		Message: "channel closed successfully",
	}, nil
}

// WaitForChannelNotification 等待频道创建通知（接收方使用）
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
	channel, err := s.channelManager.GetChannel(req.ChannelId)

	// 解析 SenderId 中的 originStatus（如果有）
	// 格式: "kernelID" 或 "kernelID|proposalId" 或 "kernelID|proposalId|STATUS"
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

	if err != nil {
		// 如果本地没有该频道，尝试从请求中的 SenderId（origin kernel）获取频道详细信息并在本地创建占位频道
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
		if !exists || originInfo == nil || originInfo.Client == nil {
			return &pb.NotifyChannelResponse{
				Success: false,
				Message: fmt.Sprintf("channel not found and origin kernel %s not connected", originKernel),
			}, nil
		}

		infoResp, err := originInfo.Client.GetCrossKernelChannelInfo(context.Background(), &pb.GetCrossKernelChannelInfoRequest{
			RequesterKernelId: s.multiKernelManager.config.KernelID,
			ChannelId:         req.ChannelId,
		})
		if err != nil || !infoResp.Found {
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
			if strings.Contains(cid, ":") {
				parts := strings.SplitN(cid, ":", 2)
				if parts[0] == localKID {
					cid = parts[1]
				} else {
					cid = fmt.Sprintf("%s:%s", parts[0], parts[1])
				}
			} else if p.KernelId != "" && p.KernelId != localKID {
				cid = fmt.Sprintf("%s:%s", p.KernelId, p.ConnectorId)
			}
			senderIDs = append(senderIDs, cid)
		}
		for _, p := range infoResp.ReceiverIds {
			cid := p.ConnectorId
			if strings.Contains(cid, ":") {
				parts := strings.SplitN(cid, ":", 2)
				if parts[0] == localKID {
					cid = parts[1]
				} else {
					cid = fmt.Sprintf("%s:%s", parts[0], parts[1])
				}
			} else if p.KernelId != "" && p.KernelId != localKID {
				cid = fmt.Sprintf("%s:%s", p.KernelId, p.ConnectorId)
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

		if originProposalId != "" && channel.ChannelProposal != nil {
			channel.ChannelProposal.ProposalID = originProposalId
		}

		// 如果 SenderId 带来了 ACCEPTED 状态，说明创建者已接受
		// 但本地参与者仍需 accept 才能激活频道（必须先 accept 规则）
		if originStatus == "ACCEPTED" {
			// 设置为 PROPOSED 状态，等待本地参与者 accept
			channel.ChannelProposal.Status = circulation.NegotiationStatusProposed
			channel.Status = circulation.ChannelStatusProposed
			channel.LastActivity = time.Now()

			log.Printf("✓ Channel %s received from kernel %s (creator accepted), waiting for local accept",
				channel.ChannelID, originKernel)

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

			// 通知所有本地参与者
			for _, senderID := range channel.SenderIDs {
				if !strings.Contains(senderID, ":") {
					if err := s.notifyParticipant(senderID, notification); err != nil {
						log.Printf("⚠ Failed to notify sender %s: %v", senderID, err)
					} else {
						log.Printf("✓ Sent PROPOSED notification to sender %s", senderID)
					}
				}
			}
			for _, receiverID := range channel.ReceiverIDs {
				if !strings.Contains(receiverID, ":") {
					log.Printf("🔍 DEBUG: about to notify receiver: %s", receiverID)
					if err := s.notifyParticipant(receiverID, notification); err != nil {
						log.Printf("⚠ Failed to notify receiver %s: %v", receiverID, err)
					} else {
						log.Printf("✓ Sent PROPOSED notification to receiver %s", receiverID)
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
	if channel.Status == circulation.ChannelStatusProposed && originStatus == "ACCEPTED" {
		log.Printf("✓ Channel %s: creator (kernel %s) has accepted, waiting for local accept",
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

		// 通知所有本地参与者
		for _, senderID := range channel.SenderIDs {
			if !strings.Contains(senderID, ":") {
				log.Printf("🔍 DEBUG: about to notify sender (existing channel): %s", senderID)
				if err := s.notifyParticipant(senderID, notification); err != nil {
					log.Printf("⚠ Failed to notify sender %s: %v", senderID, err)
				}
			}
		}
		for _, receiverID := range channel.ReceiverIDs {
			if !strings.Contains(receiverID, ":") {
				log.Printf("🔍 DEBUG: about to notify receiver (existing channel): %s", receiverID)
				if err := s.notifyParticipant(receiverID, notification); err != nil {
					log.Printf("⚠ Failed to notify receiver %s: %v", receiverID, err)
				}
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
		ChannelId:         channel.ChannelID,
		CreatorId:         channel.CreatorID,
		SenderIds:         channel.SenderIDs,
		ReceiverIds:       channel.ReceiverIDs,
		Encrypted:         channel.Encrypted,
		DataTopic:         channel.DataTopic,
		CreatedAt:         channel.CreatedAt.Unix(),
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
		return &pb.NotifyChannelResponse{
			Success: false,
			Message: fmt.Sprintf("failed to notify: %v", err),
		}, nil
	}

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

	s.auditLog.SubmitEvidence(
		req.RequesterId,
		eventType,
		req.ChannelId,
		request.RequestID,
		map[string]string{
			"change_type": req.ChangeType,
			"target_id":   req.TargetId,
			"reason":      req.Reason,
			"context":     "permission_change_request",
		},
	)

	return &pb.RequestPermissionChangeResponse{
		Success:   true,
		RequestId: request.RequestID,
		Message:   "permission change request submitted successfully",
	}, nil
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

	// 记录审计日志
	s.auditLog.SubmitEvidence(
		req.ApproverId,
		evidence.EventTypePermissionGranted,
		req.ChannelId,
		req.RequestId,
		map[string]string{
			"context": "permission_change_approved",
		},
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
	s.auditLog.SubmitEvidence(
		req.SubscriberId,
		evidence.EventTypePermissionRequest, // 复用权限请求事件类型
		req.ChannelId,
		request.RequestID,
		map[string]string{
			"action":       "subscription_request",
			"requested_role": req.Role,
			"reason":       req.Reason,
		},
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
	s.auditLog.SubmitEvidence(
		req.ApproverId,
		evidence.EventTypePermissionGranted, // 复用权限批准事件类型
		req.ChannelId,
		req.RequestId,
		map[string]string{
			"action": "subscription_approved",
			"subscriber": subscriberID,
		},
	)

	// 发送频道更新通知给新订阅者
	go func() {
		if err := s.sendChannelUpdateNotification(channel, subscriberID); err != nil {
			log.Printf("⚠️ Failed to send channel update notification to %s: %v", subscriberID, err)
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
	s.auditLog.SubmitEvidence(
		req.ApproverId,
		evidence.EventTypePermissionDenied, // 复用权限拒绝事件类型
		req.ChannelId,
		req.RequestId,
		map[string]string{
			"action":  "subscription_rejected",
			"reason":  req.Reason,
		},
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
		ChannelId:         channel.ChannelID,
		CreatorId:         channel.CreatorID,
		SenderIds:         channel.SenderIDs,
		ReceiverIds:       channel.ReceiverIDs,
 // 统一频道
		Encrypted:         channel.Encrypted,
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

	// 记录审计日志
	s.auditLog.SubmitEvidence(
		req.ApproverId,
		evidence.EventTypePermissionDenied,
		req.ChannelId,
		req.RequestId,
		map[string]string{
			"reason":  req.Reason,
			"context": "permission_change_rejected",
		},
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
	pbRequests := make([]*pb.PermissionChangeRequest, len(requests))
	for i, request := range requests {
		pbRequests[i] = &pb.PermissionChangeRequest{
			RequestId:     request.RequestID,
			RequesterId:   request.RequesterID,
			ChannelId:     request.ChannelID,
			ChangeType:    request.ChangeType,
			TargetId:      request.TargetID,
			Reason:        request.Reason,
			Status:        request.Status,
			CreatedAt:     request.CreatedAt.Unix(),
			ApprovedBy:    request.ApprovedBy,
		}
		if request.ApprovedAt != nil {
			pbRequests[i].ApprovedAt = request.ApprovedAt.Unix()
		}
	}

	return &pb.GetPermissionRequestsResponse{
		Success:  true,
		Requests: pbRequests,
		Message:  "permission requests retrieved successfully",
	}, nil
}


package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	pb "github.com/trusted-space/kernel/proto/kernel/v1"
	"github.com/trusted-space/kernel/kernel/circulation"
	"github.com/trusted-space/kernel/kernel/control"
	"github.com/trusted-space/kernel/kernel/evidence"
	"github.com/trusted-space/kernel/kernel/security"
)

type KernelServiceServer struct {
	pb.UnimplementedKernelServiceServer

	multiKernelManager *MultiKernelManager
	channelManager     *circulation.ChannelManager
	registry           *control.Registry
	notificationManager *NotificationManager
	auditLog             *evidence.AuditLog
	businessChainManager *BusinessChainManager
}

// NewKernelServiceServer 创建内核服务服务器
func NewKernelServiceServer(multiKernelManager *MultiKernelManager,
	channelManager *circulation.ChannelManager, registry *control.Registry, notificationManager *NotificationManager, auditLog *evidence.AuditLog, businessChainManager *BusinessChainManager) *KernelServiceServer {

	return &KernelServiceServer{
		multiKernelManager:  multiKernelManager,
		channelManager:      channelManager,
		registry:            registry,
		notificationManager: notificationManager,
		auditLog:            auditLog,
		businessChainManager: businessChainManager,
	}
}

// RegisterKernel 注册内核
func (s *KernelServiceServer) RegisterKernel(ctx context.Context, req *pb.RegisterKernelRequest) (*pb.RegisterKernelResponse, error) {
	// 避免在 interconnect 协商路径产生重复日志：当请求带有 is_interconnect_request 或 is_interconnect_approve 时，
	// 后续分支会产生更有意义的日志，所以这里跳过初始注册日志以减少噪音。
	if !req.IsInterconnectRequest && !req.IsInterconnectApprove {
	}

	// 验证内核ID不冲突
	if req.KernelId == s.multiKernelManager.config.KernelID {
		// 即使ID冲突，也返回本端CA证书以便对方保存
		ownCACertData, err := os.ReadFile(s.multiKernelManager.config.CACertPath)
		if err != nil {
			ownCACertData = nil
		}
		return &pb.RegisterKernelResponse{
			Success:           false,
			Message:           "kernel ID conflict",
			PeerCaCertificate: ownCACertData,
		}, nil
	}

	// 检查是否已经注册
	s.multiKernelManager.kernelsMu.RLock()
	_, exists := s.multiKernelManager.kernels[req.KernelId]
	s.multiKernelManager.kernelsMu.RUnlock()

	if exists {
		// 即使内核已注册，也需要返回本端CA证书以便对方保存
		ownCACertData, err := os.ReadFile(s.multiKernelManager.config.CACertPath)
		if err != nil {
			ownCACertData = nil
		}
		return &pb.RegisterKernelResponse{
			Success:           false,
			Message:           "kernel already registered",
			PeerCaCertificate: ownCACertData,
		}, nil
	}

	// 如果这是一个互联批准（interconnect_approve），直接完成注册（由目标发起approve时调用）
	if req.IsInterconnectApprove {
		// 保存对方的CA证书（如果提供）
		if len(req.CaCertificate) > 0 {
			peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", req.KernelId)
			if err := os.WriteFile(peerCACertPath, req.CaCertificate, 0644); err != nil {
				log.Printf("Warning: failed to save peer CA certificate for %s: %v", req.KernelId, err)
			} else {
				// suppressed detailed CA saved log
			}
		}

		// 创建内核信息并保存（直接认为已注册）
		kernelInfo := &KernelInfo{
			KernelID:      req.KernelId,
			Address:       req.Address,
			Port:          int(req.Port),
			MainPort:      int(req.Port),
			Status:        "active",
			LastHeartbeat: time.Now().Unix(),
			PublicKey:     req.PublicKey,
			Description:   "",
		}
		s.multiKernelManager.kernelsMu.Lock()
		s.multiKernelManager.kernels[req.KernelId] = kernelInfo
		s.multiKernelManager.kernelsMu.Unlock()

		// 重要：同步创建到新注册内核的客户端连接（用于后续 ForwardData 和通知转发）
		targetPort := int(req.Port) + 2 // kernel-to-kernel 端口 = 主端口 + 2
		if err := s.multiKernelManager.createKernelClient(req.KernelId, req.Address, targetPort); err != nil {
			log.Printf("[WARN] Failed to create client for approved kernel %s: %v", req.KernelId, err)
		} else {
			log.Printf("Client connection established for approved kernel %s", req.KernelId)
		}

		// 广播本内核已知的其他内核给新注册的内核
		go s.multiKernelManager.BroadcastKnownKernels(req.KernelId)

		// 注意：kernel-1 作为被批准方（接收方），kernel-2 已在自己的 RegisterKernel 响应中记录了 INTERCONNECT_APPROVED 事件，
		// 故 kernel-1 此处不再重复记录。

		ownCACertData, err := os.ReadFile(s.multiKernelManager.config.CACertPath)
		if err != nil {
			ownCACertData = nil
		}

		return &pb.RegisterKernelResponse{
			Success:           true,
			Message:           "kernel registered via approve",
			SessionToken:      fmt.Sprintf("session_%s_%d", req.KernelId, time.Now().Unix()),
			PeerCaCertificate: ownCACertData,
			KnownKernels:      []*pb.KernelInfo{},
		}, nil
	}

	// 如果这是一个互联请求（由发起方主动发起），则创建一个待审批请求并返回 request id
	if req.IsInterconnectRequest {
		// 使用 UUID 生成 request id
		requestID := uuid.New().String()

		// 记录请求方的主端口和内核通信端口（假定 kernel_port = main_port + 2）
		mainPort := int(req.Port)
		kernelPort := mainPort + 2
		pending := &PendingInterconnectRequest{
			RequestID:         requestID,
			RequesterKernelID: req.KernelId,
			Address:           req.Address,
			MainPort:          mainPort,
			KernelPort:        kernelPort,
			CaCertificate:     req.CaCertificate,
			Timestamp:         req.Timestamp,
			Status:            "pending",
		}
		// add to pending list
		s.multiKernelManager.AddPendingRequest(pending)

		// 记录互联请求存证
		if s.auditLog != nil {
			s.auditLog.SubmitBasicEvidence(
				req.KernelId,
				evidence.EventTypeInterconnectRequested,
				"", // 无频道ID
				"", // 无业务数据哈希
				evidence.DirectionIncoming,
				s.multiKernelManager.config.KernelID,
			)
		}

		// 尝试读取自己的 CA 证书返回给对端（方便对端保存）
		ownCACertData, err := os.ReadFile(s.multiKernelManager.config.CACertPath)
		if err != nil {
			ownCACertData = nil
		}


		return &pb.RegisterKernelResponse{
			Success:           true,
			Message:           fmt.Sprintf("interconnect_request_id:%s;status:pending", requestID),
			SessionToken:      "", // not a session yet
			PeerCaCertificate: ownCACertData,
			KnownKernels:      []*pb.KernelInfo{}, // not registering yet
		}, nil
	}

	// 保存对方的CA证书（如果提供）
	if len(req.CaCertificate) > 0 {
		peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", req.KernelId)
		if err := os.WriteFile(peerCACertPath, req.CaCertificate, 0644); err != nil {
			log.Printf("Warning: failed to save peer CA certificate for %s: %v", req.KernelId, err)
		} else {
		}
	}

	// 创建内核信息
	kernelInfo := &KernelInfo{
		KernelID:      req.KernelId,
		Address:       req.Address,
		Port:          int(req.Port), // 这个在注册上下文中是主服务器端口
		MainPort:      int(req.Port), // 主服务器端口
		Status:        "active",
		LastHeartbeat: time.Now().Unix(),
		PublicKey:     req.PublicKey,
		Description:   "",
	}

	// 保存到已知内核列表
	s.multiKernelManager.kernelsMu.Lock()
	s.multiKernelManager.kernels[req.KernelId] = kernelInfo
	s.multiKernelManager.kernelsMu.Unlock()

	// 创建到新注册内核的客户端连接（用于后续 ForwardData 等调用）
	// kernel_port 为主端口+2
	targetPort := int(req.Port) + 2

	// 同步创建客户端连接（重要：确保连接在发送任何通知前已建立）
	if err := s.multiKernelManager.createKernelClient(req.KernelId, req.Address, targetPort); err != nil {
		log.Printf("[WARN] Failed to create client for registered kernel %s: %v", req.KernelId, err)
	} else {
		log.Printf("Client connection established for kernel %s", req.KernelId)
	}

	// 广播本内核已知的其他内核给新注册的内核
	go s.multiKernelManager.BroadcastKnownKernels(req.KernelId)

	// 读取自己的CA证书
	ownCACertData, err := os.ReadFile(s.multiKernelManager.config.CACertPath)
	if err != nil {
		log.Printf("Warning: failed to read own CA certificate: %v", err)
		ownCACertData = nil
	}

	// 返回已知内核列表
	knownKernels := make([]*pb.KernelInfo, 0)
	s.multiKernelManager.kernelsMu.RLock()
	for _, k := range s.multiKernelManager.kernels {
		if k.KernelID != req.KernelId { // 不包含刚注册的内核
			knownKernels = append(knownKernels, &pb.KernelInfo{
				KernelId:      k.KernelID,
				Address:       k.Address,
				Port:          int32(k.Port),
				Status:        k.Status,
				LastHeartbeat: k.LastHeartbeat,
				PublicKey:     k.PublicKey,
			})
		}
	}
	s.multiKernelManager.kernelsMu.RUnlock()

	log.Printf("Kernel %s registered successfully", req.KernelId)

	return &pb.RegisterKernelResponse{
		Success:           true,
		Message:           "kernel registered successfully",
		SessionToken:      fmt.Sprintf("session_%s_%d", req.KernelId, time.Now().Unix()),
		PeerCaCertificate: ownCACertData, // 返回自己的CA证书
		KnownKernels:      knownKernels,
	}, nil
}

// KernelHeartbeat 内核心跳
func (s *KernelServiceServer) KernelHeartbeat(ctx context.Context, req *pb.KernelHeartbeatRequest) (*pb.KernelHeartbeatResponse, error) {
	// 更新内核状态
	s.multiKernelManager.kernelsMu.Lock()
	if kernelInfo, exists := s.multiKernelManager.kernels[req.KernelId]; exists {
		kernelInfo.LastHeartbeat = time.Now().Unix()
		kernelInfo.Status = "active"
	}
	s.multiKernelManager.kernelsMu.Unlock()

	// heartbeat logging suppressed (kept internal state updates only)

	return &pb.KernelHeartbeatResponse{
		Acknowledged: true,
		ServerTimestamp: time.Now().Unix(),
		Updates:       []*pb.KernelStatusUpdate{}, // TODO: 实现状态更新通知
	}, nil
}

// DiscoverKernels 发现内核
func (s *KernelServiceServer) DiscoverKernels(ctx context.Context, req *pb.DiscoverKernelsRequest) (*pb.DiscoverKernelsResponse, error) {
	kernels := make([]*pb.KernelInfo, 0)

	s.multiKernelManager.kernelsMu.RLock()
	for _, kernel := range s.multiKernelManager.kernels {
		if req.FilterStatus == "" || kernel.Status == req.FilterStatus {
			kernels = append(kernels, &pb.KernelInfo{
				KernelId:      kernel.KernelID,
				Address:       kernel.Address,
				Port:          int32(kernel.Port),
				Status:        kernel.Status,
				LastHeartbeat: kernel.LastHeartbeat,
				PublicKey:     kernel.PublicKey,
			})
		}
	}
	s.multiKernelManager.kernelsMu.RUnlock()

	return &pb.DiscoverKernelsResponse{
		Kernels:    kernels,
		TotalCount: int32(len(kernels)),
	}, nil
}

// SyncKnownKernels 同步已知内核列表
// 当收到其他内核发来的已知内核列表时，将新内核加入本地列表并返回本列表中对方不知道的内核
func (s *KernelServiceServer) SyncKnownKernels(ctx context.Context, req *pb.SyncKnownKernelsRequest) (*pb.SyncKnownKernelsResponse, error) {
	log.Printf("Received SyncKnownKernels from kernel %s with %d known kernels", req.SourceKernelId, len(req.KnownKernels))

	newlyKnownKernels := make([]*pb.KernelInfo, 0)

	// 处理收到的已知内核列表
	for _, kernelInfo := range req.KnownKernels {
		// 跳过自己
		if kernelInfo.KernelId == s.multiKernelManager.config.KernelID {
			continue
		}

		// 检查是否已经在本地列表中
		s.multiKernelManager.kernelsMu.Lock()
		existing, exists := s.multiKernelManager.kernels[kernelInfo.KernelId]
		if !exists {
			// 新内核，添加到本地列表
			// 注意：port 在 KernelInfo 中是主端口，内核间通信端口需要 +2
			mainPort := int(kernelInfo.Port)
			kernelPort := mainPort + 2

			s.multiKernelManager.kernels[kernelInfo.KernelId] = &KernelInfo{
				KernelID:      kernelInfo.KernelId,
				Address:       kernelInfo.Address,
				Port:          kernelPort,     // 内核间通信端口
				MainPort:      mainPort,       // 主服务器端口
				Status:        kernelInfo.Status,
				LastHeartbeat: kernelInfo.LastHeartbeat,
				PublicKey:     kernelInfo.PublicKey,
			}

			// 主动向新内核发起互联请求
			go func(kid string, addr string, port int) {
				err := s.multiKernelManager.connectToKernelInternal(kid, addr, port, false)
				if err != nil && !strings.Contains(err.Error(), "already connected") {
					log.Printf("[WARN] Failed to connect to kernel %s: %v", kid, err)
				}
				// 连接后，立即向对方发送本内核已知的内核列表
				s.multiKernelManager.SyncKnownKernelsToKernel(kid, addr, port)
			}(kernelInfo.KernelId, kernelInfo.Address, kernelPort)

			// 这是一个对方知道但我们之前不知道的内核
			newlyKnownKernels = append(newlyKnownKernels, kernelInfo)
		} else {
			// 已存在，更新心跳（如果对方的心跳更新）
			if kernelInfo.LastHeartbeat > existing.LastHeartbeat {
				existing.LastHeartbeat = kernelInfo.LastHeartbeat
				existing.Status = kernelInfo.Status
			}
		}
		s.multiKernelManager.kernelsMu.Unlock()
	}

	// 获取本地的已知内核中对方可能不知道的（排除发送方自己）
	s.multiKernelManager.kernelsMu.RLock()
	for _, localKernel := range s.multiKernelManager.kernels {
		if localKernel.KernelID == req.SourceKernelId {
			continue
		}

		// 检查对方是否知道这个内核
		knownByPeer := false
		for _, remoteKernel := range req.KnownKernels {
			if remoteKernel.KernelId == localKernel.KernelID {
				knownByPeer = true
				break
			}
		}

		if !knownByPeer {
			newlyKnownKernels = append(newlyKnownKernels, &pb.KernelInfo{
				KernelId:      localKernel.KernelID,
				Address:       localKernel.Address,
				Port:          int32(localKernel.MainPort), // 使用主端口
				Status:        localKernel.Status,
				LastHeartbeat: localKernel.LastHeartbeat,
				PublicKey:     localKernel.PublicKey,
			})
		}
	}
	s.multiKernelManager.kernelsMu.RUnlock()

	log.Printf("SyncKnownKernels completed: %d newly known kernels for peer, %d new for us",
		len(newlyKnownKernels), len(newlyKnownKernels))

	return &pb.SyncKnownKernelsResponse{
		Success:           true,
		Message:           "kernels synced successfully",
		NewlyKnownKernels: newlyKnownKernels,
	}, nil
}

// CreateCrossKernelChannel 创建跨内核频道
func (s *KernelServiceServer) CreateCrossKernelChannel(ctx context.Context, req *pb.CreateCrossKernelChannelRequest) (*pb.CreateCrossKernelChannelResponse, error) {
	// 实现跨内核频道创建（协商）：
	// - 发起端在本内核创建频道提议（ProposeChannel）
	// - 对于涉及到的远端内核，向它们发送同样的请求作为"提议通知"（远端会返回 accept/reject）
	// - 如果所有远端均接受，则在本内核对远端参与者标记批准（AcceptChannelProposal），激活频道

	localKernelID := s.multiKernelManager.config.KernelID

	// 如果请求中没有提供 CreatorKernelId，则视为来自本地（Connector 直接发起），注入本地 KernelID
	if req.CreatorKernelId == "" {
		req.CreatorKernelId = localKernelID
	}

	// 如果这是远端内核转发过来的提议（CreatorKernelId != 本地），在本地创建一个提议记录并通知本地参与者
	if req.CreatorKernelId != localKernelID {
		log.Printf("Received cross-kernel proposal notification from kernel %s (creator connector %s)", req.CreatorKernelId, req.CreatorConnectorId)

		// 验证参与者中的连接器是否存在于本地注册表
		// 注意：这里只检查"裸"connectorID（没有指定KernelId或KernelId与本内核相同的）
		// 因为远端指定本内核的连接器时，只会传connectorId而不会带KernelId
		for _, p := range req.SenderIds {
			// 如果没有指定KernelId或指定的是本内核，则检查本地是否存在
			if p.KernelId == "" || p.KernelId == localKernelID {
				if _, err := s.registry.GetConnector(p.ConnectorId); err != nil {
					log.Printf("[WARN] Sender connector %s not found locally (kernel %s)", p.ConnectorId, p.KernelId)
					return &pb.CreateCrossKernelChannelResponse{
						Success: false,
						Message: fmt.Sprintf("sender connector %s not found locally: %v. Note: If this connector is not exposed to other kernels, please negotiate offline first.", p.ConnectorId, err),
					}, nil
				}
			}
		}
		for _, p := range req.ReceiverIds {
			// 如果没有指定KernelId或指定的是本内核，则检查本地是否存在
			if p.KernelId == "" || p.KernelId == localKernelID {
				if _, err := s.registry.GetConnector(p.ConnectorId); err != nil {
					log.Printf("[WARN] Receiver connector %s not found locally (kernel %s)", p.ConnectorId, p.KernelId)
					return &pb.CreateCrossKernelChannelResponse{
						Success: false,
						Message: fmt.Sprintf("receiver connector %s not found locally: %v. Note: If this connector is not exposed to other kernels, please negotiate offline first.", p.ConnectorId, err),
					}, nil
				}
			}
		}

		// 构建参与者列表：本地使用裸 connectorID，远端使用 kernel:connector 格式
		senderIDs := make([]string, 0, len(req.SenderIds))
		receiverIDs := make([]string, 0, len(req.ReceiverIds))
		for _, p := range req.SenderIds {
			if p.KernelId != "" && p.KernelId != localKernelID {
				senderIDs = append(senderIDs, fmt.Sprintf("%s:%s", p.KernelId, p.ConnectorId))
			} else {
				senderIDs = append(senderIDs, p.ConnectorId)
			}
		}
		for _, p := range req.ReceiverIds {
			if p.KernelId != "" && p.KernelId != localKernelID {
				receiverIDs = append(receiverIDs, fmt.Sprintf("%s:%s", p.KernelId, p.ConnectorId))
			} else {
				receiverIDs = append(receiverIDs, p.ConnectorId)
			}
		}

		// 转换存证配置（如果有）用于本地提议（仅支持内核内置存证）
		var evConfig *circulation.EvidenceConfig
		if req.EvidenceConfig != nil {
			evConfig = &circulation.EvidenceConfig{
				Mode:          circulation.EvidenceMode(req.EvidenceConfig.Mode),
				Strategy:      circulation.EvidenceStrategy(req.EvidenceConfig.Strategy),
				RetentionDays: int(req.EvidenceConfig.RetentionDays),
				CompressData:  req.EvidenceConfig.CompressData,
			}
		}

		// 在本地创建频道提议（以远端创建者作为提议者）
		channel, err := s.channelManager.ProposeChannel(req.CreatorConnectorId, req.CreatorConnectorId, senderIDs, receiverIDs, req.DataTopic, req.Encrypted, evConfig, "", req.Reason, 300)
		if err != nil {
			log.Printf("[WARN] Failed to create local proposal for cross-kernel notification: %v", err)
			return &pb.CreateCrossKernelChannelResponse{
				Success: false,
				Message: fmt.Sprintf("failed to create local proposal: %v", err),
			}, nil
		}

		// 构建通知并发送给本地参与者（只通知本地 connector）
		notification := &pb.ChannelNotification{
			ChannelId:         channel.ChannelID,
			CreatorId:         req.CreatorConnectorId,
			SenderIds:         make([]string, 0),
			ReceiverIds:       make([]string, 0),
			Encrypted:         req.Encrypted,
			DataTopic:         req.DataTopic,
			CreatedAt:         channel.CreatedAt.Unix(),
			NegotiationStatus: pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_PROPOSED,
			ProposalId:        channel.ChannelProposal.ProposalID,
		}
		// 填充发送方/接收方为字符串（对本地参与者使用裸ID）
		for _, id := range senderIDs {
			notification.SenderIds = append(notification.SenderIds, id)
		}
		for _, id := range receiverIDs {
			notification.ReceiverIds = append(notification.ReceiverIds, id)
		}

		// 只通知本地参与者（如果 participant 字符串中不包含 ':'，说明是本地）
		for _, id := range notification.ReceiverIds {
			if !strings.Contains(id, ":") {
				if err := s.notificationManager.Notify(id, notification); err != nil {
					log.Printf("[WARN] Failed to notify local receiver %s: %v", id, err)
				} else {
					log.Printf("Notified local receiver %s of cross-kernel proposal %s", id, channel.ChannelID)
				}
			}
		}
		for _, id := range notification.SenderIds {
			if !strings.Contains(id, ":") {
				if err := s.notificationManager.Notify(id, notification); err != nil {
					log.Printf("[WARN] Failed to notify local sender %s: %v", id, err)
				} else {
					log.Printf("Notified local sender %s of cross-kernel proposal %s", id, channel.ChannelID)
				}
			}
		}

		// 设置远端接收者映射（用于跨内核数据转发和接受确认）
		// 注意：在远端发来通知的分支中，也需要设置这个映射，否则 AcceptChannelProposal 无法正确匹配 ID
		for _, receiverID := range receiverIDs {
			if strings.Contains(receiverID, ":") {
				parts := strings.SplitN(receiverID, ":", 2)
				kernelID := parts[0]
				connectorID := parts[1]
				channel.SetRemoteReceiver(connectorID, kernelID)
			} else {
				// 本地接收者也需要设置映射，指向本地内核
				channel.SetRemoteReceiver(receiverID, localKernelID)
			}
		}
		// 同样处理发送方（如果有远端发送方）
		for _, senderID := range senderIDs {
			if strings.Contains(senderID, ":") {
				parts := strings.SplitN(senderID, ":", 2)
				kernelID := parts[0]
				connectorID := parts[1]
				channel.SetRemoteReceiver(connectorID, kernelID)
			}
		}

		return &pb.CreateCrossKernelChannelResponse{
			Success: true,
			Message: "proposal received and local notifications dispatched",
		}, nil
	}

	// 构建参与者 ID 列表
	// 重要：始终使用 kernelID:connectorID 格式存储，以便 GetCrossKernelChannelInfo 能正确返回 KernelId
	senderIDs := make([]string, 0, len(req.SenderIds))
	receiverIDs := make([]string, 0, len(req.ReceiverIds))
	remoteKernels := make(map[string]bool)
	for _, p := range req.SenderIds {
		if p.KernelId != "" {
			// 远端或本地，都使用 kernel:connector 格式
			senderIDs = append(senderIDs, fmt.Sprintf("%s:%s", p.KernelId, p.ConnectorId))
			if p.KernelId != localKernelID {
				remoteKernels[p.KernelId] = true
			}
		} else {
			// 如果没有 KernelId，使用本地 kernel
			senderIDs = append(senderIDs, fmt.Sprintf("%s:%s", localKernelID, p.ConnectorId))
		}
	}
	for _, p := range req.ReceiverIds {
		if p.KernelId != "" {
			// 远端或本地，都使用 kernel:connector 格式
			receiverIDs = append(receiverIDs, fmt.Sprintf("%s:%s", p.KernelId, p.ConnectorId))
			if p.KernelId != localKernelID {
				remoteKernels[p.KernelId] = true
			}
		} else {
			// 如果没有 KernelId，使用本地 kernel
			receiverIDs = append(receiverIDs, fmt.Sprintf("%s:%s", localKernelID, p.ConnectorId))
		}
	}

	// 验证参与者中的本地连接器是否存在于本地注册表
	// 注意：这里只检查没有指定KernelId或指定的是本内核的连接器
	for _, p := range req.SenderIds {
		if p.KernelId == "" || p.KernelId == localKernelID {
			if _, err := s.registry.GetConnector(p.ConnectorId); err != nil {
				log.Printf("[WARN] Sender connector %s not found locally (kernel %s)", p.ConnectorId, p.KernelId)
				return &pb.CreateCrossKernelChannelResponse{
					Success: false,
					Message: fmt.Sprintf("sender connector %s not found locally: %v. Note: If this connector is not exposed to other kernels, please negotiate offline first.", p.ConnectorId, err),
				}, nil
			}
		}
	}
	for _, p := range req.ReceiverIds {
		if p.KernelId == "" || p.KernelId == localKernelID {
			if _, err := s.registry.GetConnector(p.ConnectorId); err != nil {
				log.Printf("[WARN] Receiver connector %s not found locally (kernel %s)", p.ConnectorId, p.KernelId)
				return &pb.CreateCrossKernelChannelResponse{
					Success: false,
					Message: fmt.Sprintf("receiver connector %s not found locally: %v. Note: If this connector is not exposed to other kernels, please negotiate offline first.", p.ConnectorId, err),
				}, nil
			}
		}
	}

	// 将 proto EvidenceConfig 转换为内部 circulation.EvidenceConfig（仅支持内核内置存证）
	var evConfig *circulation.EvidenceConfig
	if req.EvidenceConfig != nil {
		evConfig = &circulation.EvidenceConfig{
			Mode:          circulation.EvidenceMode(req.EvidenceConfig.Mode),
			Strategy:      circulation.EvidenceStrategy(req.EvidenceConfig.Strategy),
			RetentionDays: int(req.EvidenceConfig.RetentionDays),
			CompressData:  req.EvidenceConfig.CompressData,
		}
	}

	// 在本内核创建频道提议（本地管理）
	channel, err := s.channelManager.ProposeChannel(req.CreatorConnectorId, req.CreatorConnectorId, senderIDs, receiverIDs, req.DataTopic, req.Encrypted, evConfig, "", req.Reason, 300)
	if err != nil {
		return &pb.CreateCrossKernelChannelResponse{
			Success: false,
			Message: fmt.Sprintf("failed to propose channel: %v", err),
		}, nil
	}

	// 设置远端接收者映射（用于跨内核数据转发）
	// 注意：即使接收者是本地的，也需要设置映射以便通知时能正确识别
	for _, receiverID := range receiverIDs {
		if strings.Contains(receiverID, ":") {
			parts := strings.SplitN(receiverID, ":", 2)
			kernelID := parts[0]
			connectorID := parts[1]
			channel.SetRemoteReceiver(connectorID, kernelID)
		} else {
			// 本地接收者也需要设置映射，指向本地内核
			channel.SetRemoteReceiver(receiverID, localKernelID)
		}
	}
	// 同样处理发送方（如果有远端发送方）
	for _, senderID := range senderIDs {
		if strings.Contains(senderID, ":") {
			parts := strings.SplitN(senderID, ":", 2)
			kernelID := parts[0]
			connectorID := parts[1]
			channel.SetRemoteReceiver(connectorID, kernelID)
		}
	}

	// 向所有远端内核发送提议通知
	for rk := range remoteKernels {
		// 查找已连接的内核信息
		s.multiKernelManager.kernelsMu.RLock()
		kinfo, exists := s.multiKernelManager.kernels[rk]
		s.multiKernelManager.kernelsMu.RUnlock()
		if !exists {
			// 目标内核未连接，拒绝提议
			// 回退：拒绝本地提议
			_ = s.channelManager.RejectChannelProposal(channel.ChannelID, req.CreatorConnectorId, fmt.Sprintf("remote kernel %s not connected", rk))
			return &pb.CreateCrossKernelChannelResponse{
				Success: false,
				Message: fmt.Sprintf("remote kernel %s not connected", rk),
			}, nil
		}

		// 通知远端内核，让远端在本地创建占位频道并通知其本地连接器。
		// 这里使用目标内核的 IdentityService（主端口）建立连接并调用其 ChannelService.NotifyChannelCreated，
		// 并在请求中填入本地 kernel id 作为 SenderId，以便远端能够回溯 origin 详细信息。
		// 为每个远端内核上的具体连接器逐个发送 NotifyChannelCreated（ReceiverId 必须是远端 connector id）
		// 收集属于该远端内核的 connector IDs（来自原始 req 的 SenderIds/ReceiverIds）
		targetConnectorIDs := make([]string, 0)
		for _, p := range req.SenderIds {
			if p.KernelId == rk && p.ConnectorId != "" {
				targetConnectorIDs = append(targetConnectorIDs, p.ConnectorId)
			}
		}
		for _, p := range req.ReceiverIds {
			if p.KernelId == rk && p.ConnectorId != "" {
				targetConnectorIDs = append(targetConnectorIDs, p.ConnectorId)
			}
		}

		if len(targetConnectorIDs) == 0 {
			// 没有具体目标连接器，跳过（理论上不会发生）
			log.Printf("[WARN] No target connectors for kernel %s, skipping notify", rk)
			continue
		}

		identityConn, err := s.multiKernelManager.connectToKernelIdentityService(kinfo)
		if err != nil {
			log.Printf("[WARN] Failed to connect to identity service of kernel %s: %v", rk, err)
			_ = s.channelManager.RejectChannelProposal(channel.ChannelID, req.CreatorConnectorId, fmt.Sprintf("failed to notify remote kernel %s: %v", rk, err))
			return &pb.CreateCrossKernelChannelResponse{
				Success: false,
				Message: fmt.Sprintf("failed to notify remote kernel %s: %v", rk, err),
			}, nil
		}

		chClient := pb.NewChannelServiceClient(identityConn)
		// 对该内核的每个目标连接器分别通知
			for _, targetCID := range targetConnectorIDs {
				// 将 origin 的 proposal id 和状态一并带上，格式为 "kernelID|proposalID|STATUS"，
				// 方便远端创建占位频道时使用相同的 proposal id 并正确设置远端接收者映射。
				senderWithMeta := localKernelID
				if channel.ChannelProposal != nil && channel.ChannelProposal.ProposalID != "" {
					senderWithMeta = fmt.Sprintf("%s|%s|%s", localKernelID, channel.ChannelProposal.ProposalID, "ACCEPTED")
				} else {
					senderWithMeta = fmt.Sprintf("%s|%s", localKernelID, "ACCEPTED")
				}
				_, err = chClient.NotifyChannelCreated(context.Background(), &pb.NotifyChannelRequest{
					ReceiverId: targetCID,
					ChannelId:  channel.ChannelID,
					SenderId:   senderWithMeta,
					DataTopic:  channel.DataTopic,
				})
			if err != nil {
				log.Printf("[WARN] NotifyChannelCreated RPC to kernel %s for connector %s failed: %v", rk, targetCID, err)
				_ = s.channelManager.RejectChannelProposal(channel.ChannelID, req.CreatorConnectorId, fmt.Sprintf("remote kernel %s rejected: %v", rk, err))
				identityConn.Close()
				return &pb.CreateCrossKernelChannelResponse{
					Success: false,
					Message: fmt.Sprintf("remote kernel %s rejected: %v", rk, err),
				}, nil
			}
		}
		identityConn.Close()
	}

	return &pb.CreateCrossKernelChannelResponse{
		Success:   true,
		ChannelId: channel.ChannelID,
		Message:   "cross-kernel channel proposed and approved by peers",
	}, nil
}

// ForwardData 转发数据
func (s *KernelServiceServer) ForwardData(ctx context.Context, req *pb.ForwardDataRequest) (*pb.ForwardDataResponse, error) {
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	if err != nil {
		log.Printf("[WARN] ForwardData: channel not found: %v", err)
		return &pb.ForwardDataResponse{
			Success: false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	// 转发数据到频道
	// 提取 ACK 包中的 data_hash（用于存证）
	// ACK 包的 data_hash 在 Payload 序列化的 AckPacket 中，需要先提取
	ackDataHash := ""
	if req.DataPacket.GetIsAck() && len(req.DataPacket.Payload) > 0 {
		var ackPB pb.AckPacket
		if err := proto.Unmarshal(req.DataPacket.Payload, &ackPB); err == nil {
			ackDataHash = ackPB.DataHash
		}
	}
	// 业务数据包用 DataHash 字段，ACK 包用 Payload 中提取的 ackDataHash
	packetDataHashFromProto := req.DataPacket.GetDataHash()
	if ackDataHash != "" {
		packetDataHashFromProto = ackDataHash
	}
	dataPacket := &circulation.DataPacket{
		ChannelID:           req.DataPacket.ChannelId,
		SequenceNumber:     req.DataPacket.SequenceNumber,
		Payload:             req.DataPacket.Payload,
		Signature:           req.DataPacket.Signature,
		Timestamp:           req.DataPacket.Timestamp,
		SenderID:            req.DataPacket.SenderId,
		TargetIDs:           req.DataPacket.TargetIds,
		FlowID:              req.DataPacket.GetFlowId(), // 原始 FlowID（connector 可能未设置）
		IsFinal:             req.GetIsFinal(),
		DataHash:            packetDataHashFromProto,
		IsAck:               req.DataPacket.GetIsAck(),
		// Signature 字段在下面单独赋值（用于传递上一跳 kernel 的签名）
	}
	// 确保 FlowID 不为空（跨内核数据流必须有 FlowID 用于存证关联）
	if dataPacket.FlowID == "" {
		// ACK 包：使用 channel 中保存的原始 flowID（kernel-2 的 SendAck 设置了正确值）
		// 业务数据包：生成新 UUID（不应该发生，因为业务数据包 flowID 在 StreamData 就设置了）
		dataPacket.FlowID = uuid.New().String()
		if dataPacket.IsAck {
			if ch, err := s.channelManager.GetChannel(dataPacket.ChannelID); err == nil {
				if savedFlowID := ch.GetCurrentFlowID(); savedFlowID != "" {
					dataPacket.FlowID = savedFlowID
				}
			}
		}
	}
	// 统一使用 dataPacket.FlowID（后续所有 evidence 和 chain 记录都用这个）
	flowID := dataPacket.FlowID

	// 根据 payload 内容判断消息类型
	var testMsg circulation.ControlMessage
	dataCategory := "business"
	if err := json.Unmarshal(req.DataPacket.Payload, &testMsg); err == nil {
		if testMsg.MessageType == "channel_update" || testMsg.MessageType == "permission_request" {
			dataPacket.MessageType = circulation.MessageTypeControl
			dataCategory = "control"
		}
	}
	// ACK 消息设置 MessageType
	if req.DataPacket.GetIsAck() {
		dataPacket.MessageType = circulation.MessageTypeAck
		dataCategory = "control"
	}

	// END 空包不记录 evidence（跳过 PushData 后的存证）
	isEndPacket := len(req.DataPacket.Payload) == 0 && !req.DataPacket.GetIsAck()
	isAckPacket := req.DataPacket.GetIsAck()
	packetDataHash := packetDataHashFromProto

	// 多跳转发需要解析原始目标内核
	currentKernelID := s.multiKernelManager.config.KernelID
	originalTargetKernelID := ""
	for _, targetID := range req.DataPacket.TargetIds {
		if strings.Contains(targetID, ":") {
			parts := strings.SplitN(targetID, ":", 2)
			if len(parts) >= 2 {
				originalTargetKernelID = parts[0]
				break
			}
		}
	}
	isFinal := dataPacket.IsFinal

	// 存证记录在 AfterSent callback 中执行（PushData → startDataDistribution 完成数据分发后才触发）
	// 这样保证 DATA_RECEIVE 在 forwardToKernel DATA_SEND 之后记录（因果顺序正确）
	// 注意：ACK_RECEIVED 不在 AfterSent 中记录，只由 SendAck（单跳）或 ForwardData（多跳第一跳）记录一次
	recordEvidence := func() {
		// ACK 包直接返回（不记录 DATA_RECEIVED）
		// 只由 SendAck 记录一次 ACK_RECEIVED
		if isAckPacket {
			return
		}

		// 空 data_hash（如 END 心跳包）跳过 DATA_RECEIVE 记录
		if packetDataHash == "" {
			return
		}

		// 业务数据包记录 DATA_RECEIVE
		// AfterSent 时 source=req.SourceKernelId（上一跳内核，当前内核刚刚从它收到数据）
		// direct call 时 source=req.SourceKernelId（原始发送方，用于存证关联）
		// 注意：不再使用 currentKernelID 作为 source，因为 DATA_RECEIVE 表示"收到数据"，
		// source 应为数据来源方，而非接收方自己
		dataReceiveSource := req.SourceKernelId
		if _, err := s.auditLog.SubmitBasicEvidenceWithMetadata(
			dataReceiveSource,
			evidence.EventTypeDataReceive,
			req.ChannelId,
			packetDataHash,
			evidence.DirectionInternal,
			currentKernelID,
			flowID,
			map[string]string{"data_category": dataCategory},
		); err != nil {
			log.Printf("[WARN] Failed to submit DATA_RECEIVE evidence: %v", err)
		}

		// 记录 kernel → connector 的 DATA_RECEIVE（只有最终目标内核才记录）
		connectorTarget := ""
		if originalTargetKernelID == currentKernelID {
			for _, receiverID := range channel.ReceiverIDs {
				if strings.Contains(receiverID, ":") {
					parts := strings.SplitN(receiverID, ":", 2)
					if parts[0] == currentKernelID {
						connectorTarget = parts[1]
						break
					}
				} else {
					connectorTarget = receiverID
					break
				}
			}
			if connectorTarget == "" {
				for _, t := range channel.ReceiverIDs {
					if strings.Contains(t, ":") {
						connectorTarget = strings.SplitN(t, ":", 2)[1]
					} else {
						connectorTarget = t
					}
					break
				}
			}
		}
		if connectorTarget != "" {
			if _, err := s.auditLog.SubmitBasicEvidenceWithMetadata(
				currentKernelID,
				evidence.EventTypeDataReceive,
				req.ChannelId,
				packetDataHash,
				evidence.DirectionInternal,
				connectorTarget,
				flowID,
				map[string]string{"data_category": dataCategory},
			); err != nil {
				log.Printf("[WARN] Failed to submit kernel->connector DATA_RECEIVE evidence: %v", err)
			}
		}
	}

	// ================================================================
	// ACK_RECEIVED 记录
	// 对于多跳转发：中间内核（kernel-2, kernel-3 等）和原始发送方（kernel-1）都要记录
	// 只有上一跳不记录（避免重复）
	// ================================================================
	if isAckPacket && currentKernelID != req.SourceKernelId && s.auditLog != nil && !dataPacket.AckEvidenceRecorded {
		// 提取 flow_id（AckPacket 中的 FlowId 优先）
		ackFlowID := flowID
		var ackPB pb.AckPacket
		if proto.Unmarshal(req.DataPacket.Payload, &ackPB) == nil {
			if ackPB.FlowId != "" {
				ackFlowID = ackPB.FlowId
			}
		}
		if _, err := s.auditLog.SubmitBasicEvidenceWithMetadata(
			currentKernelID,
			evidence.EventTypeAckReceived,
			req.ChannelId,
			packetDataHashFromProto,
			evidence.DirectionInternal,
			"",
			ackFlowID,
			map[string]string{"data_category": "business"},
		); err != nil {
			log.Printf("[WARN] Failed to submit ACK_RECEIVED evidence: %v", err)
		} else {
			// 记录成功后设置标记，防止 forwardToKernel 触发重复 ForwardData
			dataPacket.AckEvidenceRecorded = true
			log.Printf("[OK] ACK_RECEIVED recorded: kernel=%s, data_hash=%.20s, dedup=%s", currentKernelID, packetDataHashFromProto, req.ChannelId+":"+ackFlowID+":"+packetDataHashFromProto)
		}
	}

	// 提前计算当前 kernel 对 ACK 的签名，用于 business_chain 和传递给下一跳
	// ACK 包签名输入: prevSignature（上一跳 kernel 的 ACK 签名，即 req.DataPacket.Signature），不含 dataHash
	// Signature 已合并：上一跳 kernel 的签名直接保存在 Signature 字段中
	// 用于 ACK 链的 prev_signature（下一跳读取 GetSignature() 即可）
	var currentKernelAckSig string
	var sigErrForAck error
	if isAckPacket && s.businessChainManager != nil {
		// 上一跳 kernel 的 ACK 签名（上一跳在 ForwardData 中设置的 dataPacket.Signature）
		prevAckSig := req.DataPacket.GetSignature()
		currentKernelAckSig, sigErrForAck = security.KernelSign("", prevAckSig)
		if sigErrForAck != nil {
			currentKernelAckSig = prevAckSig
		}
		// 更新 Signature 为当前 kernel 的 ACK 签名，下一跳直接读取 GetSignature() 作为 prev_signature
		dataPacket.Signature = currentKernelAckSig
	}

	// ================================================================
	// 数据包签名预计算（移到 PushData 之前，与 ACK 包保持一致）
	// 签名输入: computedDataHash + packet.Signature (上一跳的签名)
	// ================================================================
	var currentKernelDataSig string
	var computedDataHash string
	if !isAckPacket && currentKernelID != req.SourceKernelId && s.businessChainManager != nil {
		// 获取本地 DB 的 prevHash（上一跳 kernel 记录的 data_hash）
		prevHash := ""
		lastHash, _, err := s.businessChainManager.GetLastDataHash(req.ChannelId)
		if err == nil && lastHash != "" {
			prevHash = lastHash
		}

		// 计算 data_hash = SHA256(payload + prevHash)
		// 与 connector 保持一致：dataHash = SHA256(data + prevHash)
		var hashInput []byte
		if prevHash != "" {
			hashInput = append(req.DataPacket.Payload, []byte(prevHash)...)
		} else {
			hashInput = req.DataPacket.Payload
		}
		h := sha256.Sum256(hashInput)
		computedDataHash = hex.EncodeToString(h[:])

		// 验证：当前计算的 data_hash 应与上一跳传来的 data_hash 一致
		if packetDataHashFromProto != "" && computedDataHash != packetDataHashFromProto {
			log.Printf("[WARN] data_hash mismatch in kernel %s: computed=%s, prev=%s, channel=%s",
				currentKernelID, computedDataHash, packetDataHashFromProto, req.ChannelId)
		}

		// prevSignature = 上一跳 kernel 的签名（已合并到 Signature 字段中）
		prevSignature := req.DataPacket.GetSignature()
		// 当前 kernel 的签名: RSA_Sign(computedDataHash + prevSignature)
		var sigErr error
		currentKernelDataSig, sigErr = security.KernelSign(computedDataHash, prevSignature)
		if sigErr != nil {
			currentKernelDataSig = prevSignature
			log.Printf("[WARN] Failed to pre-compute data signature in kernel %s: %v", currentKernelID, sigErr)
		}
		// 更新 dataPacket 用于 PushData（PushData 会将数据分发给本地订阅者，同时转发到下一跳）
		dataPacket.Signature = currentKernelDataSig
		// DataHash 保持不变（来自上一跳）
	}

	dataPacket.AfterSent = func() {
		if !isEndPacket {
			recordEvidence()
		}
	}
	err = channel.PushData(dataPacket)
	if err != nil {
		// PushData 失败时仍然继续（对于中间跳如 kernel-2，没有本地订阅者，PushData 会失败，这是正常的）
		// 必须继续执行 business_chain 记录和转发到下一跳
		log.Printf("[WARN] ForwardData: PushData failed (will continue for business chain and forwarding): %v", err)
	} else {
		log.Printf("Data forwarded from kernel %s to channel %s", req.SourceKernelId, req.ChannelId)
	}

	// END 空包不进行任何记录（跳过所有 evidence 和 business_data_chain 记录）
	if isEndPacket {
		return &pb.ForwardDataResponse{
			Success:          true,
			Message:          "END packet forwarded (no evidence recorded)",
			ForwardedSequence: req.DataPacket.SequenceNumber,
		}, nil
	}

	// ================================================================
	// 业务哈希链记录
	//   ACK 链（所有 kernel）：每一跳都记录 RecordAck
	//     prev_signature = 上一跳传来的签名值（dataPacket.Signature）
	//     signature     = 当前 kernel 对传入签名的 RSA 签名
	//   数据包链（仅 kernel-2/3）：RecordDataHash
	// ================================================================
	if flowID != "" && !isEndPacket && s.businessChainManager != nil {
		if isAckPacket {
			// ---- 所有 kernel（kernel-3/2/1）：记录 ACK 签名链 ----
			// prev_signature = req.DataPacket.Signature（上一跳 kernel 对 ACK 的签名，即 kernel-3 的签名）
			// 当前 kernel 的签名 = currentKernelAckSig（当前 kernel 对上一跳签名的签名）
			prevAckSig := req.DataPacket.GetSignature()
			if err := s.businessChainManager.RecordAck(
					"", req.ChannelId,
					prevAckSig, currentKernelAckSig,
				); err != nil {
					log.Printf("[WARN] Failed to record ACK business chain: %v", err)
				} else {
					log.Printf("[OK] ACK business chain recorded: kernel=%s, prev_sig=%.20s..., sig=%.20s...",
						currentKernelID, prevAckSig, currentKernelAckSig)
				}
			// dataPacket.Signature 已在 PushData 之前更新为 currentKernelAckSig
		} else if currentKernelID != req.SourceKernelId {
			// ---- kernel-2/3：业务数据包写入业务哈希链 ----
			// data_hash 直接使用 packetDataHashFromProto（来自 connector，不重新计算）
			// 数据库链的 prev_hash（上一跳 kernel 在 DB 中记录的 data_hash）
			prevHash := ""
			lastHash, _, err := s.businessChainManager.GetLastDataHash(req.ChannelId)
			if err == nil && lastHash != "" {
				prevHash = lastHash
			}
			// prevSignature = 上一跳 kernel 的签名（原始请求中的 Signature）
			prevSignature := req.DataPacket.GetSignature()
			// 使用预先计算好的签名 currentKernelDataSig
			if err := s.businessChainManager.RecordDataHash(
				"", req.ChannelId, computedDataHash, prevHash,
				prevSignature, currentKernelDataSig,
			); err != nil {
				log.Printf("[WARN] Failed to record business chain in kernel: %v", err)
			} else {
				log.Printf("[OK] Business chain recorded: channel=%s, data_hash=%s, prev_signature=%.20s...",
					req.ChannelId, computedDataHash, prevSignature)
			}
			// dataPacket.Signature 和 dataPacket.DataHash 已在 PushData 之前更新
		}
		// kernel-1 侧的业务数据包不写入 business_data_chain（由 connector-A 写入）
	}

	// ----------------------------------------
	// 4. 流结束：生成 flow signature
	// ----------------------------------------
	if isFinal && flowID != "" {
		// 多跳转发存证（kernel-2 -> kernel-3）：仅在存在下一跳时记录
		// ACK 包不经过多跳（只转发给相邻内核）
		if !isAckPacket && originalTargetKernelID != "" && originalTargetKernelID != currentKernelID {
			if s.multiKernelManager != nil && s.multiKernelManager.multiHopConfigManager != nil {
				nextKernel, _, _, hopIndex, totalHops, found := s.multiKernelManager.multiHopConfigManager.GetNextHop(currentKernelID, originalTargetKernelID)
				if found && nextKernel != "" {
					if _, err := s.auditLog.SubmitBasicEvidenceWithMetadata(
						currentKernelID,
						evidence.EventTypeDataSend,
						req.ChannelId,
						packetDataHash,
						evidence.DirectionInternal,
						nextKernel,
						flowID,
						map[string]string{"data_category": dataCategory},
					); err != nil {
						log.Printf("[WARN] Failed to submit kernel->kernel DATA_SEND evidence (multi-hop): %v", err)
					} else {
						log.Printf("Recorded kernel->kernel DATA_SEND (multi-hop): %s -> %s (hop %d/%d), flow: %s",
							currentKernelID, nextKernel, hopIndex, totalHops, flowID)
					}
				}
			}
		}

		// 流签名：ACK 包不生成流签名
		if !isAckPacket {
			if _, err := s.auditLog.SignFlow(flowID); err != nil {
				log.Printf("[WARN] Failed to sign flow %s: %v", flowID, err)
			} else {
				log.Printf("[OK] Flow signature generated for flow_id: %s", flowID)
			}
		}
	}

	return &pb.ForwardDataResponse{
		Success:          true,
		Message:          "data forwarded successfully",
		ForwardedSequence: req.DataPacket.SequenceNumber,
	}, nil
}

// GetCrossKernelChannelInfo 获取跨内核频道信息
func (s *KernelServiceServer) GetCrossKernelChannelInfo(ctx context.Context, req *pb.GetCrossKernelChannelInfoRequest) (*pb.GetCrossKernelChannelInfoResponse, error) {
	
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	
	if err != nil {
		log.Printf("[WARN]️ GetCrossKernelChannelInfo: channel not found: %v", err)
		return &pb.GetCrossKernelChannelInfoResponse{
			Found:  false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	// 构建返回的参与者列表
	// 对于远端连接器（有 kernel:connector 格式），KernelId 设置为对应内核 ID，ConnectorId 为裸 ID
	senders := make([]*pb.CrossKernelParticipant, 0, len(channel.SenderIDs))
	receivers := make([]*pb.CrossKernelParticipant, 0, len(channel.ReceiverIDs))
	localKernelID := s.multiKernelManager.config.KernelID
	for _, sID := range channel.SenderIDs {
		var kernelId, connectorId string
		if strings.Contains(sID, ":") {
			parts := strings.SplitN(sID, ":", 2)
			kernelId = parts[0]
			connectorId = parts[1]
		} else {
			// Local connector: always include this kernel's ID so remote kernels
			// store the participant with the correct kernel prefix.
			kernelId = localKernelID
			connectorId = sID
		}
		senders = append(senders, &pb.CrossKernelParticipant{
			KernelId:    kernelId,
			ConnectorId: connectorId,
		})
	}
	for _, rID := range channel.ReceiverIDs {
		var kernelId, connectorId string
		if strings.Contains(rID, ":") {
			parts := strings.SplitN(rID, ":", 2)
			kernelId = parts[0]
			connectorId = parts[1]
		} else {
			// Local connector: always include this kernel's ID so remote kernels
			// store the participant with the correct kernel prefix.
			kernelId = localKernelID
			connectorId = rID
		}
		receivers = append(receivers, &pb.CrossKernelParticipant{
			KernelId:    kernelId,
			ConnectorId: connectorId,
		})
	}

	return &pb.GetCrossKernelChannelInfoResponse{
		Found:              true,
		ChannelId:          channel.ChannelID,
		CreatorKernelId:    s.multiKernelManager.config.KernelID,
		CreatorConnectorId: channel.CreatorID,
		SenderIds:          senders,
		ReceiverIds:        receivers,
		DataTopic:          channel.DataTopic,
		Encrypted:          channel.Encrypted,
		Status:             string(channel.Status),
		CreatedAt:          channel.CreatedAt.Unix(),
		LastActivity:       channel.LastActivity.Unix(),
		NegotiationStatus:  s.channelStatusToProto(channel.Status),
		Message:            "channel info retrieved successfully",
	}, nil
}

// channelStatusToProto 将内部 ChannelStatus 转换为 proto ChannelNegotiationStatus
func (s *KernelServiceServer) channelStatusToProto(status circulation.ChannelStatus) pb.ChannelNegotiationStatus {
	switch status {
	case circulation.ChannelStatusProposed:
		return pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_PROPOSED
	case circulation.ChannelStatusActive:
		return pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED
	case circulation.ChannelStatusClosed:
		return pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_REJECTED
	default:
		return pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_UNKNOWN
	}
}

// SyncConnectorInfo 同步连接器信息
func (s *KernelServiceServer) SyncConnectorInfo(ctx context.Context, req *pb.SyncConnectorInfoRequest) (*pb.SyncConnectorInfoResponse, error) {
	log.Printf("Syncing %d connectors from kernel %s", len(req.Connectors), req.SourceKernelId)

	syncedCount := 0
	for _, connectorInfo := range req.Connectors {
		// 更新本地注册表：将远端连接器视为已注册（KernelId 字段用于标注来源）
		if connectorInfo.ConnectorId == "" {
			log.Printf("[WARN] Skipping connector with empty id from kernel %s", req.SourceKernelId)
			continue
		}

		// 尝试注册（如果已存在则会更新信息）
		if err := s.registry.Register(connectorInfo.ConnectorId, connectorInfo.EntityType, connectorInfo.PublicKey, ""); err != nil {
			log.Printf("[WARN] Failed to register connector %s from kernel %s: %v", connectorInfo.ConnectorId, req.SourceKernelId, err)
			continue
		}

		// 同步状态（如果有提供），将字符串转换为 control.ConnectorStatus
		if connectorInfo.Status != "" {
			// 尝试设置状态，忽略错误（例如未知ID等）
			_ = s.registry.SetStatus(connectorInfo.ConnectorId, control.ConnectorStatus(connectorInfo.Status))
		}

		syncedCount++
	}

	return &pb.SyncConnectorInfoResponse{
		Success:     true,
		Message:     "connector info synced successfully",
		SyncedCount: int32(syncedCount),
	}, nil
}

// SyncPermissionRequest 同步权限请求到其他内核
func (s *KernelServiceServer) SyncPermissionRequest(ctx context.Context, req *pb.SyncPermissionRequestRequest) (*pb.SyncPermissionRequestResponse, error) {
	if req == nil || req.ChannelId == "" || req.Request == nil {
		return &pb.SyncPermissionRequestResponse{
			Success: false,
			Message: "invalid request: channel_id and request are required",
		}, nil
	}

	// 转换 proto 请求到内部结构
	remoteReq := &RemotePermissionRequest{
		RequestID:     req.Request.RequestId,
		RequesterID:   req.Request.RequesterId,
		ChannelID:     req.Request.ChannelId,
		ChangeType:    req.Request.ChangeType,
		TargetID:      req.Request.TargetId,
		Reason:        req.Request.Reason,
		Status:        req.Request.Status,
		SourceKernelID: req.SourceKernelId,
		CreatedAt:     time.Unix(req.Request.CreatedAt, 0),
	}

	// 添加到远程权限请求列表
	s.multiKernelManager.AddRemotePermissionRequest(remoteReq)

	// 同时存储到频道本地的权限请求列表，这样 connector 才能通过 list-permissions 看到
	// 并使用 approve-permission 批准
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	if err == nil && channel != nil {
		// 构造 PermissionRequestMessage 用于存储
		permReqMsg := &circulation.PermissionRequestMessage{
			RequestID:  remoteReq.RequestID,
			ChannelID:  remoteReq.ChannelID,
			ChangeType: remoteReq.ChangeType,
			TargetID:   remoteReq.TargetID,
			Reason:     remoteReq.Reason,
		}
		// 从 SourceKernelID 提取 requesterID（格式：kernel-X:connector-Y）
		requesterID := remoteReq.SourceKernelID
		channel.StorePermissionRequestFromRemote(permReqMsg, requesterID)
		log.Printf("Stored permission request %s to channel %s local list", remoteReq.RequestID, remoteReq.ChannelID)
	} else {
		log.Printf("[WARN]️ Cannot get channel %s to store permission request: %v", remoteReq.ChannelID, err)
	}


	return &pb.SyncPermissionRequestResponse{
		Success: true,
		Message: "permission request synced successfully",
	}, nil
}

// GetRemotePermissionRequests 获取其他内核同步过来的权限请求
func (s *KernelServiceServer) GetRemotePermissionRequests(ctx context.Context, req *pb.GetRemotePermissionRequestsRequest) (*pb.GetRemotePermissionRequestsResponse, error) {
	if req == nil || req.ChannelId == "" {
		return &pb.GetRemotePermissionRequestsResponse{
			Success:  false,
			Message:  "invalid request: channel_id is required",
			Requests: nil,
		}, nil
	}

	// 获取远程权限请求
	remoteReqs := s.multiKernelManager.GetRemotePermissionRequests(req.ChannelId)

	// 转换为 proto 格式
	pbReqs := make([]*pb.PermissionChangeRequest, 0, len(remoteReqs))
	for _, r := range remoteReqs {
		pbReqs = append(pbReqs, &pb.PermissionChangeRequest{
			RequestId:   r.RequestID,
			RequesterId: r.RequesterID,
			ChannelId:   r.ChannelID,
			ChangeType:  r.ChangeType,
			TargetId:    r.TargetID,
			Reason:      r.Reason,
			Status:      r.Status,
			CreatedAt:   r.CreatedAt.Unix(),
		})
	}

	return &pb.GetRemotePermissionRequestsResponse{
		Success:  true,
		Requests: pbReqs,
		Message:  "remote permission requests retrieved successfully",
	}, nil
}

// NotifyMultiHopApproved 多跳链路审批通知
// 当目标内核审批了互联请求后，通过此 RPC 主动通知发起方，
// 发起方收到后自动重试建立到该内核的连接，无需手动重新执行 connect-kernel --route。
func (s *KernelServiceServer) NotifyMultiHopApproved(ctx context.Context, req *pb.NotifyMultiHopApprovedRequest) (*pb.NotifyMultiHopApprovedResponse, error) {
	log.Printf("Received multi-hop approval notification: approved_kernel=%s, approver=%s, request_id=%s, hop=%d/%d",
		req.ApprovedKernelId, req.ApproverKernelId, req.RequestId, req.HopIndex, req.HopTotal)

	// 记录存证
	if s.auditLog != nil {
		s.auditLog.SubmitBasicEvidence(
			req.ApproverKernelId,
			evidence.EventTypeInterconnectApproved,
			"",
			"", // 无业务数据哈希
			evidence.DirectionIncoming,
			req.ApprovedKernelId,
		)
	}

	// 调用 MultiKernelManager 的多跳审批回调，触发自动重连
	if s.multiKernelManager != nil && s.multiKernelManager.OnMultiHopApproved != nil {
		s.multiKernelManager.OnMultiHopApproved(req)
	}

	return &pb.NotifyMultiHopApprovedResponse{
		Success: true,
		Message: "approval notification received",
	}, nil
}

// HopEstablished 多跳链路建立完成通知
// 当中间节点审批后并自动建立了下一跳连接时，通过此 RPC 通知发起方"该 hop 已由我代为完成"，
// 发起方收到后自动建立对应连接。
func (s *KernelServiceServer) HopEstablished(ctx context.Context, req *pb.HopEstablishedRequest) (*pb.HopEstablishedResponse, error) {
	log.Printf("HopEstablished notification: source=%s, hop=%s, target=%s, target_address=%s:%d",
		req.SourceKernelId, req.HopId, req.TargetKernelId, req.TargetAddress, req.TargetPort)

	// 记录存证：中间节点已完成 hop 审批
	if s.auditLog != nil {
		s.auditLog.SubmitBasicEvidence(
			req.SourceKernelId,
			evidence.EventTypeInterconnectApproved,
			"",
			"",
			evidence.DirectionInternal,
			req.TargetKernelId,
		)
	}

	// 通知 MultiKernelManager 触发自动建立到目标内核的连接
	if s.multiKernelManager != nil && s.multiKernelManager.OnHopEstablished != nil {
		s.multiKernelManager.OnHopEstablished(req)
	}

	return &pb.HopEstablishedResponse{
		Success: true,
		Message: fmt.Sprintf("hop %s established notification received", req.HopId),
	}, nil
}

// ForwardAckNotification ACK 强制转发通知
// 当 kernel-3 SendAck 时，通过此 RPC 通知 kernel-2 和 kernel-1：
// 1. 让它们记录 ACK_RECEIVED 和 business_data_chain
// 2. 让 kernel-2 继续转发给 kernel-1
func (s *KernelServiceServer) ForwardAckNotification(ctx context.Context, req *pb.ForwardAckNotificationRequest) (*pb.ForwardAckNotificationResponse, error) {
	log.Printf("ForwardAckNotification: source=%s, channel=%s, data_hash=%.20s..., flow=%s, is_final=%v",
		req.SourceKernelId, req.ChannelId, req.DataHash, req.FlowId, req.IsFinal)

	currentKernelID := s.multiKernelManager.config.KernelID

	// ================================================================
	// 1. 记录 ACK_RECEIVED 存证
	// ================================================================
	if _, err := s.auditLog.SubmitBasicEvidenceWithMetadata(
		currentKernelID,
		evidence.EventTypeAckReceived,
		req.ChannelId,
		req.DataHash,
		evidence.DirectionInternal,
		"",
		req.FlowId,
		map[string]string{"data_category": "business"},
	); err != nil {
		log.Printf("[WARN] Failed to submit ACK_RECEIVED evidence: %v", err)
	} else {
		log.Printf("[OK] ACK_RECEIVED recorded via ForwardAckNotification: kernel=%s, data_hash=%.20s...",
			currentKernelID, req.DataHash)
	}

	// ================================================================
	// 2. 记录 business_data_chain（ACK 签名链续接）
	// ================================================================
	// 当前 kernel 对 ACK 的签名：RSA_Sign(prevSignature)
	// ACK 包签名输入只有 prevSignature，不含 dataHash
	prevSigFromPrevHop := req.KernelSignature
	currentKernelAckSig, sigErr := security.KernelSign("", prevSigFromPrevHop)
	if sigErr != nil {
		currentKernelAckSig = prevSigFromPrevHop
	}

	if s.businessChainManager != nil {
		// RecordAck(prevSignature, signature):
		//   prevSignature = req.KernelSignature（kernel-3 的 ACK 签名，ACK 链的正确前驱）
		//   signature = 当前 kernel 对 ACK 的签名
		if err := s.businessChainManager.RecordAck(
			"", req.ChannelId,
			prevSigFromPrevHop, currentKernelAckSig,
		); err != nil {
			log.Printf("[WARN] Failed to record ACK business chain: %v", err)
		} else {
			log.Printf("[OK] ACK business chain recorded via ForwardAckNotification: prev_sig=%.20s...",
				prevSigFromPrevHop)
		}
	}

	// ================================================================
	// 3. 如果不是最后一跳，需要继续转发给上一个内核
	// ================================================================
	if !req.IsFinal && s.multiKernelManager.multiHopConfigManager != nil {
		// 上一跳 = sourceKernelId 的上一跳（当前内核的上一跳）
		prevKernelID, _, _, found := s.multiKernelManager.multiHopConfigManager.GetPreviousHop(req.SourceKernelId)
		if found && prevKernelID != "" {
			log.Printf("Forwarding ACK to previous kernel: %s", prevKernelID)

			// 构造转发请求，将 is_final 设为 true（因为是当前内核的上一跳）
			// 注意：kernel-2 收到通知后，会再次调用 ForwardAckNotification(kernel-1, is_final=true)
			notifyReq := &pb.ForwardAckNotificationRequest{
				SourceKernelId:     currentKernelID,
				ChannelId:         req.ChannelId,
				DataHash:          req.DataHash,
				ConnectorSignature: req.ConnectorSignature,
				KernelSignature:   currentKernelAckSig,
				PrevSignature:     req.PrevSignature, // 传上一跳 kernel-3 的 ACK 签名，不是自己的
				FlowId:            req.FlowId,
				OriginConnectorId: req.OriginConnectorId,
				IsFinal:           true, // kernel-1 是最终目标，不再转发
			}

			// 调用上一跳内核的 ForwardAckNotification
			if err := s.multiKernelManager.ForwardAckToKernel(prevKernelID, notifyReq); err != nil {
				log.Printf("[WARN] Failed to forward ACK to kernel %s: %v", prevKernelID, err)
			} else {
				log.Printf("[OK] ACK forwarded to kernel %s", prevKernelID)
			}
		} else {
			log.Printf("[INFO] No previous kernel found for %s, this might be hop 1", req.SourceKernelId)
		}
	}

	return &pb.ForwardAckNotificationResponse{
		Success: true,
		Message: "ACK notification processed",
	}, nil
}

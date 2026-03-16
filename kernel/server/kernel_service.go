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
	"sync"
	"time"

	"github.com/google/uuid"
	pb "github.com/trusted-space/kernel/proto/kernel/v1"
	"github.com/trusted-space/kernel/kernel/circulation"
	"github.com/trusted-space/kernel/kernel/control"
	"github.com/trusted-space/kernel/kernel/evidence"
)

// KernelServiceServer 内核服务服务器
type KernelServiceServer struct {
	pb.UnimplementedKernelServiceServer

	multiKernelManager *MultiKernelManager
	channelManager     *circulation.ChannelManager
	registry           *control.Registry
	notificationManager *NotificationManager
	auditLog           *evidence.AuditLog

	// 跨内核数据转发的哈希累加器（用于流结束时计算完整数据的哈希）
	// key: channelID_flowID, value: 累积的字节数据
	dataHashAccumulator map[string][]byte
	dataHashMu          sync.Mutex
}

// NewKernelServiceServer 创建内核服务服务器
func NewKernelServiceServer(multiKernelManager *MultiKernelManager,
	channelManager *circulation.ChannelManager, registry *control.Registry, notificationManager *NotificationManager, auditLog *evidence.AuditLog) *KernelServiceServer {

	return &KernelServiceServer{
		multiKernelManager:  multiKernelManager,
		channelManager:      channelManager,
		registry:            registry,
		notificationManager: notificationManager,
		auditLog:            auditLog,
		dataHashAccumulator: make(map[string][]byte),
	}
}

// RegisterKernel 注册内核
func (s *KernelServiceServer) RegisterKernel(ctx context.Context, req *pb.RegisterKernelRequest) (*pb.RegisterKernelResponse, error) {
	// 避免在 interconnect 协商路径产生重复日志：当请求带有 is_interconnect_request 或 is_interconnect_approve 时，
	// 后续分支会产生更有意义的日志，所以这里跳过初始注册日志以减少噪音。
	if !req.IsInterconnectRequest && !req.IsInterconnectApprove {
		log.Printf("Kernel %s registering from %s:%d", req.KernelId, req.Address, req.Port)
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
		log.Printf("✓ Saved kernel %s to kernels map (via approve)", req.KernelId)
		s.multiKernelManager.kernelsMu.Unlock()

		// 重要：同步创建到新注册内核的客户端连接（用于后续 ForwardData 和通知转发）
		targetPort := int(req.Port) + 2 // kernel-to-kernel 端口 = 主端口 + 2
		log.Printf("🔧 Creating client for approved kernel %s at %s:%d", req.KernelId, req.Address, targetPort)
		if err := s.multiKernelManager.createKernelClient(req.KernelId, req.Address, targetPort); err != nil {
			log.Printf("⚠ Failed to create client for approved kernel %s: %v", req.KernelId, err)
		} else {
			log.Printf("✓ Client connection established for approved kernel %s", req.KernelId)
		}

		// 广播本内核已知的其他内核给新注册的内核
		go s.multiKernelManager.BroadcastKnownKernels(req.KernelId)

		// 记录互联批准存证
		if s.auditLog != nil {
			s.auditLog.SubmitBasicEvidence(
				s.multiKernelManager.config.KernelID,
				evidence.EventTypeInterconnectApproved,
				"",
				"",
				evidence.DirectionIncoming,
				req.KernelId,
			)
		}

		ownCACertData, err := os.ReadFile(s.multiKernelManager.config.CACertPath)
		if err != nil {
			ownCACertData = nil
		}

		log.Printf("Kernel %s registered via approve by peer", req.KernelId)
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
				requestID,
				evidence.DirectionIncoming,
				s.multiKernelManager.config.KernelID,
			)
		}

		// 尝试读取自己的 CA 证书返回给对端（方便对端保存）
		ownCACertData, err := os.ReadFile(s.multiKernelManager.config.CACertPath)
		if err != nil {
			ownCACertData = nil
		}

		log.Printf("Saved interconnect request %s from kernel %s", requestID, req.KernelId)

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
			log.Printf("Saved peer CA certificate for kernel %s", req.KernelId)
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
	log.Printf("✓ Saved kernel %s to kernels map, conn=%v", req.KernelId, kernelInfo.conn)
	s.multiKernelManager.kernelsMu.Unlock()

	// 创建到新注册内核的客户端连接（用于后续 ForwardData 等调用）
	// kernel_port 为主端口+2
	targetPort := int(req.Port) + 2
	log.Printf("🔧 About to create client for kernel %s at %s:%d", req.KernelId, req.Address, targetPort)
	
	// 同步创建客户端连接（重要：确保连接在发送任何通知前已建立）
	if err := s.multiKernelManager.createKernelClient(req.KernelId, req.Address, targetPort); err != nil {
		log.Printf("⚠ Failed to create client for registered kernel %s: %v", req.KernelId, err)
	} else {
		log.Printf("✓ Client connection established for kernel %s", req.KernelId)
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
			log.Printf("✓ Added new kernel %s from sync (via %s)", kernelInfo.KernelId, req.SourceKernelId)

			// 主动向新内核发起互联请求
			go func(kid string, addr string, port int) {
				err := s.multiKernelManager.connectToKernelInternal(kid, addr, port, false)
				if err != nil && !strings.Contains(err.Error(), "already connected") {
					log.Printf("⚠ Failed to connect to kernel %s: %v", kid, err)
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
					log.Printf("⚠ Sender connector %s not found locally (kernel %s)", p.ConnectorId, p.KernelId)
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
					log.Printf("⚠ Receiver connector %s not found locally (kernel %s)", p.ConnectorId, p.KernelId)
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
			log.Printf("⚠ Failed to create local proposal for cross-kernel notification: %v", err)
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
					log.Printf("⚠ Failed to notify local receiver %s: %v", id, err)
				} else {
					log.Printf("✓ Notified local receiver %s of cross-kernel proposal %s", id, channel.ChannelID)
				}
			}
		}
		for _, id := range notification.SenderIds {
			if !strings.Contains(id, ":") {
				if err := s.notificationManager.Notify(id, notification); err != nil {
					log.Printf("⚠ Failed to notify local sender %s: %v", id, err)
				} else {
					log.Printf("✓ Notified local sender %s of cross-kernel proposal %s", id, channel.ChannelID)
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
				log.Printf("⚠ Sender connector %s not found locally (kernel %s)", p.ConnectorId, p.KernelId)
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
				log.Printf("⚠ Receiver connector %s not found locally (kernel %s)", p.ConnectorId, p.KernelId)
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
		log.Printf("→ Notifying kernel %s to create local placeholder for channel %s", rk, channel.ChannelID)
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
			log.Printf("⚠ No target connectors for kernel %s, skipping notify", rk)
			continue
		}

		identityConn, err := s.multiKernelManager.connectToKernelIdentityService(kinfo)
		if err != nil {
			log.Printf("⚠ Failed to connect to identity service of kernel %s: %v", rk, err)
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
				log.Printf("⚠ NotifyChannelCreated RPC to kernel %s for connector %s failed: %v", rk, targetCID, err)
				_ = s.channelManager.RejectChannelProposal(channel.ChannelID, req.CreatorConnectorId, fmt.Sprintf("remote kernel %s rejected: %v", rk, err))
				identityConn.Close()
				return &pb.CreateCrossKernelChannelResponse{
					Success: false,
					Message: fmt.Sprintf("remote kernel %s rejected: %v", rk, err),
				}, nil
			}
			log.Printf("✓ Notified kernel %s to inform connector %s of channel %s", rk, targetCID, channel.ChannelID)
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
	// 检查频道是否存在
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	if err != nil {
		log.Printf("⚠ ForwardData: channel not found: %v", err)
		return &pb.ForwardDataResponse{
			Success: false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	// 转发数据到频道
	// 注意：需要根据 payload 内容判断消息类型（proto 没有 MessageType 字段）
	// 获取 flow_id（用于跨内核关联）
	flowID := req.DataPacket.GetFlowId()

	dataPacket := &circulation.DataPacket{
		ChannelID:      req.DataPacket.ChannelId,
		SequenceNumber: req.DataPacket.SequenceNumber,
		Payload:        req.DataPacket.Payload,
		Signature:      req.DataPacket.Signature,
		Timestamp:      req.DataPacket.Timestamp,
		SenderID:       req.DataPacket.SenderId,
		TargetIDs:      req.DataPacket.TargetIds,
		FlowID:         flowID,
		IsFinal:        req.GetIsFinal(),
	}

	log.Printf("🔍 DEBUG ForwardData: TargetIDs=%v, SenderID=%s, IsFinal=%v", req.DataPacket.TargetIds, req.DataPacket.SenderId, req.GetIsFinal())

	// 根据 payload 内容判断消息类型
	var testMsg circulation.ControlMessage
	dataCategory := "business"
	if err := json.Unmarshal(req.DataPacket.Payload, &testMsg); err == nil {
		if testMsg.MessageType == "channel_update" || testMsg.MessageType == "permission_request" {
			dataPacket.MessageType = circulation.MessageTypeControl
			dataCategory = "control"
		}
	}
	err = channel.PushData(dataPacket)
	if err != nil {
		log.Printf("⚠ ForwardData: PushData failed: %v", err)
		return &pb.ForwardDataResponse{
			Success: false,
			Message: fmt.Sprintf("failed to forward data: %v", err),
		}, nil
	}

	log.Printf("Data forwarded from kernel %s to channel %s", req.SourceKernelId, req.ChannelId)

	// 记录存证：kernel-1 -> kernel-2 (DATA_RECEIVE)
	// 用于记录数据从远程内核到达当前内核
	if s.auditLog != nil {
		// 获取当前内核ID
		currentKernelID := ""
		if s.multiKernelManager != nil && s.multiKernelManager.config != nil {
			currentKernelID = s.multiKernelManager.config.KernelID
		}

		// 检查是否是最后一个数据包（流结束）
		isFinal := dataPacket.IsFinal

		// 解析原始目标内核ID（如果有）
		// 格式: "kernelID:connectorID" 或 "connectorID"
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

		log.Printf("🔍 DEBUG ForwardData: isFinal=%v, flowID=%s, payloadLen=%d, originalTarget=%s",
			isFinal, flowID, len(req.DataPacket.Payload), originalTargetKernelID)

		// 累积数据哈希（用于流结束时计算完整数据的哈希）
		// 只有非结束数据包才累积哈希
		// 注意：与 channel_service.go 保持一致 - 累积原始数据
		accumulatorKey := req.ChannelId + "_" + flowID
		if !isFinal && len(req.DataPacket.Payload) > 0 {
			s.dataHashMu.Lock()
			s.dataHashAccumulator[accumulatorKey] = append(s.dataHashAccumulator[accumulatorKey], req.DataPacket.Payload...)
			s.dataHashMu.Unlock()
		}

		// 如果是最后一个数据包，记录完整的 DATA_RECEIVE 存证
		if isFinal && flowID != "" {
			s.dataHashMu.Lock()
			accumulatedData := s.dataHashAccumulator[accumulatorKey]
			// 计算完整数据的哈希
			finalHash := sha256.Sum256(accumulatedData)
			// 清除累加器
			delete(s.dataHashAccumulator, accumulatorKey)
			s.dataHashMu.Unlock()

			log.Printf("🔄 Recording DATA_RECEIVE complete for channel %s, flow: %s", req.ChannelId, flowID)

			// 构建 metadata
			metadata := map[string]string{
				"data_category": dataCategory,
			}

			// 记录存证：kernel-1 -> kernel-2 (DATA_RECEIVE)，带 flow_id 和 data_hash
			if _, err := s.auditLog.SubmitBasicEvidenceWithMetadata(
				req.SourceKernelId, // source: kernel-1 (发送方)
				evidence.EventTypeDataReceive,
				req.ChannelId,
				hex.EncodeToString(finalHash[:]),
				evidence.DirectionInternal,
				currentKernelID, // target: kernel-2 (接收方内核)
				flowID,
				metadata,
			); err != nil {
				log.Printf("⚠ Failed to submit kernel->kernel DATA_RECEIVE evidence: %v", err)
			} else {
				log.Printf("✓ Recorded kernel->kernel DATA_RECEIVE: %s -> %s, flow: %s", req.SourceKernelId, currentKernelID, flowID)
			}

			// 记录存证：kernel-2 -> kernel-3 (DATA_SEND) - 多跳转发存证
			// 只有当存在原始目标内核ID 且 不是最终目标时，才记录转发存证
			if originalTargetKernelID != "" && originalTargetKernelID != currentKernelID {
				// 检查当前内核到原始目标是否有路由配置
				if s.multiKernelManager != nil && s.multiKernelManager.multiHopConfigManager != nil {
					nextKernel, _, _, hopIndex, totalHops, found := s.multiKernelManager.multiHopConfigManager.GetNextHop(currentKernelID, originalTargetKernelID)
					if found && nextKernel != "" {
						// 记录转发存证：当前内核 -> 下一跳内核
						if _, err := s.auditLog.SubmitBasicEvidenceWithMetadata(
							currentKernelID, // source: 当前内核
							evidence.EventTypeDataSend,
							req.ChannelId,
							hex.EncodeToString(finalHash[:]),
							evidence.DirectionInternal,
							nextKernel, // target: 下一跳内核
							flowID,
							metadata,
						); err != nil {
							log.Printf("⚠ Failed to submit kernel->kernel DATA_SEND evidence (multi-hop): %v", err)
						} else {
							log.Printf("✓ Recorded kernel->kernel DATA_SEND (multi-hop): %s -> %s (hop %d/%d), flow: %s",
								currentKernelID, nextKernel, hopIndex, totalHops, flowID)
						}
					}
				}
			}

			// 记录存证：kernel-2 -> connector-U (DATA_RECEIVE)，带 flow_id 和 data_hash
			log.Printf("🔍 DEBUG: Recording connector DATA_RECEIVE, ReceiverIDs=%v, currentKernelID=%s", channel.ReceiverIDs, currentKernelID)
			for _, receiverID := range channel.ReceiverIDs {
				// 跳过远程接收者
				if strings.Contains(receiverID, ":") {
					parts := strings.SplitN(receiverID, ":", 2)
					if len(parts) >= 2 && parts[0] != currentKernelID {
						continue
					}
				}
				targetConnectorID := receiverID
				if strings.Contains(receiverID, ":") {
					parts := strings.SplitN(receiverID, ":", 2)
					targetConnectorID = parts[1]
				}
				if _, err := s.auditLog.SubmitBasicEvidenceWithMetadata(
					currentKernelID,
					evidence.EventTypeDataReceive,
					req.ChannelId,
					hex.EncodeToString(finalHash[:]),
					evidence.DirectionInternal,
					targetConnectorID,
					flowID,
					metadata,
				); err != nil {
					log.Printf("⚠ Failed to submit kernel->connector DATA_RECEIVE evidence: %v", err)
				}
			}
		}
		// 移除了实时 DATA_RECEIVE 记录，只保留流结束时的完整记录
	}

	return &pb.ForwardDataResponse{
		Success:          true,
		Message:          "data forwarded successfully",
		ForwardedSequence: req.DataPacket.SequenceNumber,
	}, nil
}

// GetCrossKernelChannelInfo 获取跨内核频道信息
func (s *KernelServiceServer) GetCrossKernelChannelInfo(ctx context.Context, req *pb.GetCrossKernelChannelInfoRequest) (*pb.GetCrossKernelChannelInfoResponse, error) {
	log.Printf("📌 GetCrossKernelChannelInfo called: channel=%s, requester=%s", req.ChannelId, req.RequesterKernelId)
	
	channel, err := s.channelManager.GetChannel(req.ChannelId)
	log.Printf("📌 GetCrossKernelChannelInfo: channel found=%v, err=%v", channel != nil, err)
	
	if err != nil {
		log.Printf("⚠️ GetCrossKernelChannelInfo: channel not found: %v", err)
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

	log.Printf("📌 GetCrossKernelChannelInfo: returning channel info: id=%s, creator=%s, senders=%v, receivers=%v",
		channel.ChannelID, channel.CreatorID, len(senders), len(receivers))

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
		Message:            "channel info retrieved successfully",
	}, nil
}

// SyncConnectorInfo 同步连接器信息
func (s *KernelServiceServer) SyncConnectorInfo(ctx context.Context, req *pb.SyncConnectorInfoRequest) (*pb.SyncConnectorInfoResponse, error) {
	log.Printf("Syncing %d connectors from kernel %s", len(req.Connectors), req.SourceKernelId)

	syncedCount := 0
	for _, connectorInfo := range req.Connectors {
		// 更新本地注册表：将远端连接器视为已注册（KernelId 字段用于标注来源）
		if connectorInfo.ConnectorId == "" {
			log.Printf("⚠ Skipping connector with empty id from kernel %s", req.SourceKernelId)
			continue
		}

		// 尝试注册（如果已存在则会更新信息）
		if err := s.registry.Register(connectorInfo.ConnectorId, connectorInfo.EntityType, connectorInfo.PublicKey, ""); err != nil {
			log.Printf("⚠ Failed to register connector %s from kernel %s: %v", connectorInfo.ConnectorId, req.SourceKernelId, err)
			continue
		}

		// 同步状态（如果有提供），将字符串转换为 control.ConnectorStatus
		if connectorInfo.Status != "" {
			// 尝试设置状态，忽略错误（例如未知ID等）
			_ = s.registry.SetStatus(connectorInfo.ConnectorId, control.ConnectorStatus(connectorInfo.Status))
		}

		log.Printf("Synced connector %s from kernel %s", connectorInfo.ConnectorId, req.SourceKernelId)
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
		log.Printf("✓ Stored permission request %s to channel %s local list", remoteReq.RequestID, remoteReq.ChannelID)
	} else {
		log.Printf("⚠️ Cannot get channel %s to store permission request: %v", remoteReq.ChannelID, err)
	}

	log.Printf("Synced permission request %s from kernel %s for channel %s", 
		remoteReq.RequestID, req.SourceKernelId, remoteReq.ChannelID)

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

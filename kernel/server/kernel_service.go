package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"time"

	pb "github.com/trusted-space/kernel/proto/kernel/v1"
	"github.com/trusted-space/kernel/kernel/circulation"
	"github.com/trusted-space/kernel/kernel/control"
)

// KernelServiceServer 内核服务服务器
type KernelServiceServer struct {
	pb.UnimplementedKernelServiceServer

	multiKernelManager *MultiKernelManager
	channelManager     *circulation.ChannelManager
	registry           *control.Registry
}

// NewKernelServiceServer 创建内核服务服务器
func NewKernelServiceServer(multiKernelManager *MultiKernelManager,
	channelManager *circulation.ChannelManager, registry *control.Registry) *KernelServiceServer {

	return &KernelServiceServer{
		multiKernelManager: multiKernelManager,
		channelManager:     channelManager,
		registry:           registry,
	}
}

// RegisterKernel 注册内核
func (s *KernelServiceServer) RegisterKernel(ctx context.Context, req *pb.RegisterKernelRequest) (*pb.RegisterKernelResponse, error) {
	// 避免在 interconnect 协商路径产生重复日志：当请求带有 interconnect_request 或 interconnect_approve 时，
	// 后续分支会产生更有意义的日志，所以这里跳过初始注册日志以减少噪音。
	if md := req.GetMetadata(); md == nil || (md["interconnect_request"] != "true" && md["interconnect_approve"] != "true") {
		log.Printf("Kernel %s registering from %s:%d", req.KernelId, req.Address, req.Port)
	}

	// 验证内核ID不冲突
	if req.KernelId == s.multiKernelManager.config.KernelID {
		return &pb.RegisterKernelResponse{
			Success: false,
			Message: "kernel ID conflict",
		}, nil
	}

	// 检查是否已经注册
	s.multiKernelManager.kernelsMu.RLock()
	_, exists := s.multiKernelManager.kernels[req.KernelId]
	s.multiKernelManager.kernelsMu.RUnlock()

	if exists {
		return &pb.RegisterKernelResponse{
			Success: false,
			Message: "kernel already registered",
		}, nil
	}

	// 如果这是一个互联批准（interconnect_approve），直接完成注册（由目标发起approve时调用）
	if md := req.GetMetadata(); md != nil {
		if v, ok := md["interconnect_approve"]; ok && (v == "true" || v == "1") {
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
	}

	// 如果这是一个互联请求（由发起方主动发起），则创建一个待审批请求并返回 request id
	if md := req.GetMetadata(); md != nil {
		if v, ok := md["interconnect_request"]; ok && (v == "true" || v == "1") {
			// 使用 SHA256(hash of kernelID + timestamp) 生成 request id，格式为 hex 字符串
			raw := fmt.Sprintf("%s-%d", req.KernelId, time.Now().UnixNano())
			sum := sha256.Sum256([]byte(raw))
			requestID := hex.EncodeToString(sum[:])

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
	s.multiKernelManager.kernelsMu.Unlock()

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

// CreateCrossKernelChannel 创建跨内核频道
func (s *KernelServiceServer) CreateCrossKernelChannel(ctx context.Context, req *pb.CreateCrossKernelChannelRequest) (*pb.CreateCrossKernelChannelResponse, error) {
	// 实现跨内核频道创建（协商）：
	// - 发起端在本内核创建频道提议（ProposeChannel）
	// - 对于涉及到的远端内核，向它们发送同样的请求作为“提议通知”（远端会返回 accept/reject）
	// - 如果所有远端均接受，则在本内核对远端参与者标记批准（AcceptChannelProposal），激活频道

	localKernelID := s.multiKernelManager.config.KernelID

	// 如果这是远端内核转发过来的提议（CreatorKernelId != 本地），直接返回接受（远端提议通知）
	if req.CreatorKernelId != localKernelID {
		// 简单自动接受远端的提议通知（不在远端创建完整频道实例，远端只需告知是否接受）
		return &pb.CreateCrossKernelChannelResponse{
			Success: true,
			Message: "proposal received and accepted",
		}, nil
	}

	// 构建参与者 ID 列表（本地 connector 使用原 ID，远端 connector 使用 kernel:connector 格式）
	senderIDs := make([]string, 0, len(req.SenderIds))
	receiverIDs := make([]string, 0, len(req.ReceiverIds))
	remoteKernels := make(map[string]bool)
	for _, p := range req.SenderIds {
		if p.KernelId != "" && p.KernelId != localKernelID {
			senderIDs = append(senderIDs, fmt.Sprintf("%s:%s", p.KernelId, p.ConnectorId))
			remoteKernels[p.KernelId] = true
		} else {
			senderIDs = append(senderIDs, p.ConnectorId)
		}
	}
	for _, p := range req.ReceiverIds {
		if p.KernelId != "" && p.KernelId != localKernelID {
			receiverIDs = append(receiverIDs, fmt.Sprintf("%s:%s", p.KernelId, p.ConnectorId))
			remoteKernels[p.KernelId] = true
		} else {
			receiverIDs = append(receiverIDs, p.ConnectorId)
		}
	}

	// 将 proto EvidenceConfig 转换为内部 circulation.EvidenceConfig（如果有）
	var evConfig *circulation.EvidenceConfig
	if req.EvidenceConfig != nil {
		evConfig = &circulation.EvidenceConfig{
			Mode:           circulation.EvidenceMode(req.EvidenceConfig.Mode),
			Strategy:       circulation.EvidenceStrategy(req.EvidenceConfig.Strategy),
			ConnectorID:    req.EvidenceConfig.ConnectorId,
			BackupEnabled:  req.EvidenceConfig.BackupEnabled,
			RetentionDays:  int(req.EvidenceConfig.RetentionDays),
			CompressData:   req.EvidenceConfig.CompressData,
			CustomSettings: req.EvidenceConfig.CustomSettings,
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

		// 发送提议通知到远端内核（对方会返回 success=true 表示接受）
		// 使用同样的请求结构；远端会把 CreatorKernelId != local 做为提议通知并接受
		_, err := kinfo.Client.CreateCrossKernelChannel(context.Background(), req)
		if err != nil {
			// 远端返回错误或拒绝：回退本地提议
			_ = s.channelManager.RejectChannelProposal(channel.ChannelID, req.CreatorConnectorId, fmt.Sprintf("remote kernel %s rejected: %v", rk, err))
			return &pb.CreateCrossKernelChannelResponse{
				Success: false,
				Message: fmt.Sprintf("remote kernel %s rejected: %v", rk, err),
			}, nil
		}
	}

	// 所有远端内核均接受，标记远端参与者为已批准（本地接受）
	// 逐个批准参与者（包含远端标识）
	for _, id := range senderIDs {
		_ = s.channelManager.AcceptChannelProposal(channel.ChannelID, id)
	}
	for _, id := range receiverIDs {
		_ = s.channelManager.AcceptChannelProposal(channel.ChannelID, id)
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
		return &pb.ForwardDataResponse{
			Success: false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	// 转发数据到频道
	dataPacket := &circulation.DataPacket{
		ChannelID:      req.DataPacket.ChannelId,
		SequenceNumber: req.DataPacket.SequenceNumber,
		Payload:        req.DataPacket.Payload,
		Signature:      req.DataPacket.Signature,
		Timestamp:      req.DataPacket.Timestamp,
		SenderID:       req.DataPacket.SenderId,
		TargetIDs:      req.DataPacket.TargetIds,
	}
	err = channel.PushData(dataPacket)
	if err != nil {
		return &pb.ForwardDataResponse{
			Success: false,
			Message: fmt.Sprintf("failed to forward data: %v", err),
		}, nil
	}

	log.Printf("Data forwarded from kernel %s to channel %s", req.SourceKernelId, req.ChannelId)

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
		return &pb.GetCrossKernelChannelInfoResponse{
			Found:  false,
			Message: fmt.Sprintf("channel not found: %v", err),
		}, nil
	}

	return &pb.GetCrossKernelChannelInfoResponse{
		Found:             true,
		ChannelId:         channel.ChannelID,
		CreatorKernelId:   "", // TODO: 从频道信息中获取创建者内核ID
		CreatorConnectorId: channel.CreatorID,
		DataTopic:         channel.DataTopic,
		Encrypted:         channel.Encrypted,
		Status:            string(channel.Status),
		CreatedAt:         channel.CreatedAt.Unix(),
		LastActivity:      channel.LastActivity.Unix(),
		Message:           "channel info retrieved successfully",
	}, nil
}

// SyncConnectorInfo 同步连接器信息
func (s *KernelServiceServer) SyncConnectorInfo(ctx context.Context, req *pb.SyncConnectorInfoRequest) (*pb.SyncConnectorInfoResponse, error) {
	log.Printf("Syncing %d connectors from kernel %s", len(req.Connectors), req.SourceKernelId)

	syncedCount := 0
	for _, connectorInfo := range req.Connectors {
		// TODO: 实现连接器信息同步逻辑
		// 这里应该更新本地连接器注册表，处理跨内核连接器信息

		log.Printf("Synced connector %s from kernel %s", connectorInfo.ConnectorId, req.SourceKernelId)
		syncedCount++
	}

	return &pb.SyncConnectorInfoResponse{
		Success:     true,
		Message:     "connector info synced successfully",
		SyncedCount: int32(syncedCount),
	}, nil
}

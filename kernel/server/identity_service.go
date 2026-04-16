package server

import (
	"context"
	"fmt"
	"log"
	"strings"
	"gopkg.in/yaml.v3"
	"os"
	"time"

	"github.com/google/uuid"
	pb "github.com/trusted-space/kernel/proto/kernel/v1"
	"github.com/trusted-space/kernel/kernel/circulation"
	"github.com/trusted-space/kernel/kernel/control"
	"github.com/trusted-space/kernel/kernel/evidence"
	"github.com/trusted-space/kernel/kernel/security"
)

const KernelVersion = "v1.0.0"

// IdentityServiceServer 实现身份与准入服务
type IdentityServiceServer struct {
	pb.UnimplementedIdentityServiceServer
	registry            *control.Registry
	auditLog            *evidence.AuditLog
	ca                  *security.CA
	channelManager      *circulation.ChannelManager
	notificationManager *NotificationManager
	multiKernelManager  *MultiKernelManager
	tokenManager        *control.TokenManager // Bootstrap Token 管理器
}

// NewIdentityServiceServer 创建身份服务
func NewIdentityServiceServer(registry *control.Registry, auditLog *evidence.AuditLog, ca *security.CA, channelManager *circulation.ChannelManager, notificationManager *NotificationManager, multiKernelManager *MultiKernelManager, tokenManager *control.TokenManager) *IdentityServiceServer {
	return &IdentityServiceServer{
		registry:            registry,
		auditLog:            auditLog,
		ca:                  ca,
		channelManager:      channelManager,
		notificationManager: notificationManager,
		multiKernelManager:  multiKernelManager,
		tokenManager:        tokenManager,
	}
}

// Handshake 处理连接器握手
func (s *IdentityServiceServer) Handshake(ctx context.Context, req *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	// 从证书中提取 connector_id
	_, err := security.ExtractConnectorIDFromContext(ctx)
	if err != nil {
		// 记录认证失败
		s.auditLog.SubmitEvidence(
			req.ConnectorId,
			evidence.EventTypeAuthFail,
			"",
			"",
		)

		return &pb.HandshakeResponse{
			Success: false,
			Message: fmt.Sprintf("authentication failed: %v", err),
		}, nil
	}

	// 验证请求中的 connector_id 是否与证书匹配
	if err := security.VerifyConnectorID(ctx, req.ConnectorId); err != nil {
		s.auditLog.SubmitEvidence(
			req.ConnectorId,
			evidence.EventTypeAuthFail,
			"",
			"",
		)

		return &pb.HandshakeResponse{
			Success: false,
			Message: fmt.Sprintf("connector ID mismatch: %v", err),
		}, nil
	}

	// 生成会话令牌
	sessionToken := uuid.New().String()

	// 确定连接器是否公开信息（默认为true）
	// Exposed 是指针类型，nil 表示未设置，默认为 true
	exposed := true
	if req.Exposed != nil {
		exposed = *req.Exposed
	}

	// 转换结构化数据目录（proto -> internal）
	var dataCatalogItems []*control.DataCatalogItem
	if len(req.DataCatalogItems) > 0 {
		dataCatalogItems = make([]*control.DataCatalogItem, 0, len(req.DataCatalogItems))
		for _, item := range req.DataCatalogItems {
			dataCatalogItems = append(dataCatalogItems, &control.DataCatalogItem{
				Id:      item.Id,
				Name:    item.Name,
				Type:    item.Type,
				Exposed: item.Exposed,
			})
		}
	}

	// 注册连接器（传递公开状态和数据目录）
	if err := s.registry.Register(req.ConnectorId, req.EntityType, req.PublicKey, sessionToken, exposed, req.DataCatalog, dataCatalogItems); err != nil {
		return &pb.HandshakeResponse{
			Success: false,
			Message: fmt.Sprintf("registration failed: %v", err),
		}, nil
	}

	// 记录认证成功
	s.auditLog.SubmitEvidence(
		req.ConnectorId,
		evidence.EventTypeAuthSuccess,
		"",
		"",
	)

	// 检查是否是重启恢复，如果是则发送频道恢复通知
	if s.channelManager.IsConnectorRestarting(req.ConnectorId) {
		s.sendChannelRecoveryNotifications(req.ConnectorId)
	}

	return &pb.HandshakeResponse{
		Success:       true,
		SessionToken:  sessionToken,
		KernelVersion: KernelVersion,
		Message:       "handshake successful",
	}, nil
}

// sendChannelRecoveryNotifications 发送频道恢复通知给重启的连接器
func (s *IdentityServiceServer) sendChannelRecoveryNotifications(connectorID string) {
	// 获取所有活跃频道
	channels := s.channelManager.GetAllChannels()

	// 异步发送恢复通知，允许连接器有时间启动通知监听器
	go func() {
		// 等待连接器启动通知监听器（最多等待10秒）
		maxRetries := 20
		for i := 0; i < maxRetries; i++ {
			time.Sleep(500 * time.Millisecond) // 每500ms检查一次

			successCount := 0
			totalCount := 0

			for _, channel := range channels {
				// 只发送连接器参与的频道通知
				if channel.Status == circulation.ChannelStatusActive && channel.IsParticipant(connectorID) {
					totalCount++

					// 构造频道通知（统一频道）

					negotiationStatus := pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED

					notification := &pb.ChannelNotification{
						ChannelId:         channel.ChannelID,
						CreatorId:         channel.CreatorID,
						SenderIds:         channel.SenderIDs,
						ReceiverIds:       channel.ReceiverIDs,
						// ChannelType:       pb.ChannelType_CHANNEL_TYPE_DATA, // 已废弃
						Encrypted:         channel.Encrypted,
						// RelatedChannelIds: []string{}, // 已废弃
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

					// 发送通知
					if err := s.notificationManager.Notify(connectorID, notification); err != nil {
						log.Printf("[WARN] Recovery notification attempt %d for channel %s to %s failed: %v",
							i+1, channel.ChannelID, connectorID, err)
					} else {
						successCount++
					}
				}
			}

			// 如果所有通知都发送成功，退出重试循环
			if successCount == totalCount && totalCount > 0 {
				return
			}
		}

		log.Printf("[WARN] Failed to send recovery notifications to %s after %d attempts", connectorID, maxRetries)
	}()
}

// Heartbeat 处理心跳请求
func (s *IdentityServiceServer) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	// 验证连接器 ID
	if err := security.VerifyConnectorID(ctx, req.ConnectorId); err != nil {
		return &pb.HeartbeatResponse{
			Acknowledged: false,
		}, fmt.Errorf("connector ID verification failed: %w", err)
	}

	// 更新心跳
	if err := s.registry.UpdateHeartbeat(req.ConnectorId); err != nil {
		return &pb.HeartbeatResponse{
			Acknowledged: false,
		}, fmt.Errorf("heartbeat update failed: %w", err)
	}

	return &pb.HeartbeatResponse{
		Acknowledged:    true,
		ServerTimestamp: time.Now().Unix(),
	}, nil
}

// DiscoverConnectors 发现空间中的其他连接器
func (s *IdentityServiceServer) DiscoverConnectors(ctx context.Context, req *pb.DiscoverRequest) (*pb.DiscoverResponse, error) {
	// 验证请求者身份
	// 对于内核间请求，RequesterId是内核ID，需要特殊处理
	isKernelRequest := strings.HasPrefix(req.RequesterId, "kernel-")
	if !isKernelRequest {
		// 普通连接器请求：验证连接器ID
		if err := security.VerifyConnectorID(ctx, req.RequesterId); err != nil {
			return &pb.DiscoverResponse{
				TotalCount: 0,
			}, fmt.Errorf("requester authentication failed: %w", err)
		}
		// 如果存在已连接的内核，汇总本地 + 远端连接器供连接器查看
		// 注意：本地连接器可以看到本内核的所有连接器（无论是否公开）
		// 只有跨内核的连接器才需要过滤（只返回公开的）
		if s.multiKernelManager != nil && s.multiKernelManager.GetConnectedKernelCount() > 0 {
			// 先添加本地所有连接器（无论是否公开）
			localConnectors := s.registry.ListConnectors()
			allConnectors := make([]*pb.ConnectorInfo, 0)

			// 添加本地连接器（全部添加，无论是否公开）
			for _, conn := range localConnectors {
				// 过滤掉请求者自身
				if conn.ConnectorID == req.RequesterId {
					continue
				}
				// 类型过滤
				if req.FilterType == "" || conn.EntityType == req.FilterType {
					allConnectors = append(allConnectors, &pb.ConnectorInfo{
						ConnectorId:   conn.ConnectorID,
						EntityType:    conn.EntityType,
						PublicKey:     conn.PublicKey,
						Status:        string(conn.Status),
						LastHeartbeat: conn.LastHeartbeat.Unix(),
						RegisteredAt:  conn.RegisteredAt.Unix(),
						KernelId:      s.multiKernelManager.config.KernelID,
						DataCatalog:   conn.DataCatalog,
						Exposed:      conn.Exposed,
						DataCatalogItems: convertToPBDataCatalogItems(conn.DataCatalogItems),
					})
				}
			}

			// 再添加远端连接器（只添加公开的）
			remoteConnectors, err := s.multiKernelManager.CollectAllConnectors()
			if err != nil {
				log.Printf("Failed to collect remote connectors for Discover: %v", err)
			} else {
				for _, c := range remoteConnectors {
					// 过滤掉请求者自身
					if c.ConnectorId == req.RequesterId && c.KernelId == s.multiKernelManager.config.KernelID {
						continue
					}
					// 类型过滤
					if req.FilterType == "" || c.EntityType == req.FilterType {
						allConnectors = append(allConnectors, c)
					}
				}
			}

			return &pb.DiscoverResponse{
				Connectors: allConnectors,
				TotalCount: int32(len(allConnectors)),
			}, nil
		}
	} else {
		// 内核间请求：验证内核身份（TODO: 实现内核身份验证）
		// 目前暂时允许所有内核请求，但应该添加更严格的验证
		log.Printf("Kernel-to-kernel request from %s", req.RequesterId)
	}

	// 对于内核间请求，仅返回本地注册表中的连接器（避免递归调用到其他内核）
	// 只返回公开的连接器 (Exposed=true)
	if isKernelRequest {
		allConnectors := s.registry.ListExposedConnectors() // 只返回公开的连接器
		pbConnectors := make([]*pb.ConnectorInfo, 0, len(allConnectors))
		for _, info := range allConnectors {
			// 过滤数据目录，只返回 exposed=true 的数据项
			exposedItems := make([]*pb.DataCatalogItem, 0)
			for _, item := range info.DataCatalogItems {
				if item.Exposed {
					exposedItems = append(exposedItems, &pb.DataCatalogItem{
						Id:      item.Id,
						Name:    item.Name,
						Type:    item.Type,
						Exposed: item.Exposed,
					})
				}
			}
			pbConnectors = append(pbConnectors, &pb.ConnectorInfo{
				ConnectorId:   info.ConnectorID,
				EntityType:    info.EntityType,
				PublicKey:     info.PublicKey,
				Status:        string(info.Status),
				LastHeartbeat: info.LastHeartbeat.Unix(),
				RegisteredAt:  info.RegisteredAt.Unix(),
				KernelId:      s.multiKernelManager.config.KernelID,
				DataCatalog:   info.DataCatalog,
				Exposed:      info.Exposed,
				DataCatalogItems: exposedItems,
			})
		}
		// 可选类型过滤
		if req.FilterType != "" {
			filtered := make([]*pb.ConnectorInfo, 0)
			for _, c := range pbConnectors {
				if c.EntityType == req.FilterType {
					filtered = append(filtered, c)
				}
			}
			pbConnectors = filtered
		}
		return &pb.DiscoverResponse{
			Connectors: pbConnectors,
			TotalCount: int32(len(pbConnectors)),
		}, nil
	}

	// 普通连接器请求：返回本地注册表中的连接器（只返回公开的）
	allConnectors := s.registry.ListExposedConnectors() // 只返回公开的连接器
	// 过滤掉请求者自己
	filtered := make([]*control.ConnectorInfo, 0)
	for _, info := range allConnectors {
		if info.ConnectorID != req.RequesterId {
			// 如果指定了类型过滤，则进行过滤
			if req.FilterType == "" || info.EntityType == req.FilterType {
				filtered = append(filtered, info)
			}
		}
	}

	// 转换为proto格式
	pbConnectors := make([]*pb.ConnectorInfo, 0, len(filtered))
	for _, info := range filtered {
		pbConnectors = append(pbConnectors, &pb.ConnectorInfo{
			ConnectorId:     info.ConnectorID,
			EntityType:    info.EntityType,
			PublicKey:     info.PublicKey,
			Status:        string(info.Status), // 将ConnectorStatus转换为string
			LastHeartbeat: info.LastHeartbeat.Unix(),
			RegisteredAt:  info.RegisteredAt.Unix(),
			DataCatalog:   info.DataCatalog,
			Exposed:      info.Exposed,
			DataCatalogItems: convertToPBDataCatalogItems(info.DataCatalogItems),
		})
	}

	return &pb.DiscoverResponse{
		Connectors: pbConnectors,
		TotalCount: int32(len(pbConnectors)),
	}, nil
}

// convertToPBDataCatalogItems 将内部 DataCatalogItem 列表转换为 protobuf 格式
func convertToPBDataCatalogItems(items []*control.DataCatalogItem) []*pb.DataCatalogItem {
	if len(items) == 0 {
		return nil
	}
	result := make([]*pb.DataCatalogItem, 0, len(items))
	for _, item := range items {
		result = append(result, &pb.DataCatalogItem{
			Id:      item.Id,
			Name:    item.Name,
			Type:    item.Type,
			Exposed: item.Exposed,
		})
	}
	return result
}

// GetConnectorInfo 获取连接器详细信息（支持跨内核查询）
func (s *IdentityServiceServer) GetConnectorInfo(ctx context.Context, req *pb.GetConnectorInfoRequest) (*pb.GetConnectorInfoResponse, error) {
	// 验证请求者身份
	// 注意：内核间请求（RequesterId 以 "kernel-" 开头）的身份由 mTLS 在传输层保证，此处跳过连接器身份验证
	isKernelRequest := strings.HasPrefix(req.RequesterId, "kernel-")
	if !isKernelRequest {
		if err := security.VerifyConnectorID(ctx, req.RequesterId); err != nil {
			return &pb.GetConnectorInfoResponse{
				Found:   false,
				Message: fmt.Sprintf("requester authentication failed: %v", err),
			}, nil
		}
	}

	connectorID := req.ConnectorId
	targetKernelID := req.TargetKernelId

	// 解析 connector_id 是否包含 kernel: 前缀（格式: kernel-1:connector-A）
	if strings.Contains(connectorID, ":") {
		parts := strings.SplitN(connectorID, ":", 2)
		if len(parts) == 2 {
			targetKernelID = parts[0]
			connectorID = parts[1]
		}
	}

	// 如果指定了目标内核ID，向远端内核查询
	if targetKernelID != "" && targetKernelID != s.multiKernelManager.config.KernelID {
		// 向远端内核查询
		resp, err := s.multiKernelManager.GetRemoteConnectorInfo(targetKernelID, connectorID)
		if err != nil {
			return &pb.GetConnectorInfoResponse{
				Found:   false,
				Message: fmt.Sprintf("failed to query remote kernel %s: %v", targetKernelID, err),
			}, nil
		}
		return resp, nil
	}

	// 获取本地连接器信息
	info, err := s.registry.GetConnector(connectorID)
	if err != nil {
		return &pb.GetConnectorInfoResponse{
			Found:   false,
			Message: fmt.Sprintf("connector not found: %v", err),
		}, nil
	}

	// 检查连接器是否公开（expose_to_others）
	// 不公开的连接器只能被自己查询，或者在同一内核内被其他连接器查询
	if !info.Exposed {
		// 连接器查询自己的信息：允许
		if !isKernelRequest && req.RequesterId == connectorID {
			// 连接器查询自己的信息
		} else if isKernelRequest && targetKernelID == s.multiKernelManager.config.KernelID {
			// 内核查询自己内核下的连接器信息：允许
		} else {
			// 跨内核查询不公开的连接器：拒绝
			return &pb.GetConnectorInfoResponse{
				Found:   false,
				Message: fmt.Sprintf("connector %s is not exposed to other kernels", connectorID),
			}, nil
		}
	}

	// 构建响应数据
	// 如果是跨内核请求，过滤数据目录只返回 exposed=true 的项
	// 如果是本地请求，返回所有数据项
	var dataCatalogItems []*pb.DataCatalogItem
	var dataCatalog []string
	if isKernelRequest {
		// 跨内核请求：只返回暴露的数据项
		dataCatalogItems = make([]*pb.DataCatalogItem, 0)
		dataCatalog = make([]string, 0)
		for _, item := range info.DataCatalogItems {
			if item.Exposed {
				dataCatalogItems = append(dataCatalogItems, &pb.DataCatalogItem{
					Id:      item.Id,
					Name:    item.Name,
					Type:    item.Type,
					Exposed: item.Exposed,
				})
				dataCatalog = append(dataCatalog, item.Name)
			}
		}
	} else {
		// 本地请求：返回所有数据项
		dataCatalogItems = convertToPBDataCatalogItems(info.DataCatalogItems)
		dataCatalog = info.DataCatalog
	}

	return &pb.GetConnectorInfoResponse{
		Found: true,
		Connector: &pb.ConnectorInfo{
			ConnectorId:   info.ConnectorID,
			EntityType:    info.EntityType,
			PublicKey:     info.PublicKey,
			Status:        string(info.Status),
			LastHeartbeat: info.LastHeartbeat.Unix(),
			RegisteredAt:  info.RegisteredAt.Unix(),
			KernelId:      s.multiKernelManager.config.KernelID,
			DataCatalog:   dataCatalog, // 跨内核请求时只包含 exposed=true 的项
			Exposed:       info.Exposed,
			DataCatalogItems: dataCatalogItems,
		},
		Message: "connector found",
	}, nil
}

// RegisterConnector 处理连接器首次注册和证书申请
// 注意：此方法不需要mTLS验证，因为连接器还没有证书
func (s *IdentityServiceServer) RegisterConnector(ctx context.Context, req *pb.RegisterConnectorRequest) (*pb.RegisterConnectorResponse, error) {
	// 验证基本字段
	if req.ConnectorId == "" {
		return &pb.RegisterConnectorResponse{
			Success: false,
			Message: "connector_id is required",
		}, nil
	}

	if req.EntityType == "" {
		return &pb.RegisterConnectorResponse{
			Success: false,
			Message: "entity_type is required",
		}, nil
	}

	// 检查连接器是否已注册
	if _, err := s.registry.GetConnector(req.ConnectorId); err == nil {
		return &pb.RegisterConnectorResponse{
			Success: false,
			Message: fmt.Sprintf("connector %s already registered", req.ConnectorId),
		}, nil
	}

	// 验证配置YAML格式（如果提供）
	if req.ConfigYaml != "" {
		var config map[string]interface{}
		if err := yaml.Unmarshal([]byte(req.ConfigYaml), &config); err != nil {
			return &pb.RegisterConnectorResponse{
				Success: false,
				Message: fmt.Sprintf("invalid config YAML: %v", err),
			}, nil
		}
		// 这里可以添加更详细的配置验证逻辑
	}

	// Bootstrap Token 验证（如果启用）
	if s.tokenManager.IsEnabled() {
		registrationCode := req.RegistrationCode
		if registrationCode == "" {
			return &pb.RegisterConnectorResponse{
				Success: false,
				Message: "bootstrap token required (registration_code is empty)",
			}, nil
		}

		// 验证 Token
		token, err := s.tokenManager.ValidateToken(registrationCode, req.ConnectorId)
		if err != nil {
			return &pb.RegisterConnectorResponse{
				Success: false,
				Message: fmt.Sprintf("invalid bootstrap token: %v", err),
			}, nil
		}

		// 记录 Token 验证成功审��日志
		s.auditLog.SubmitEvidence(
			req.ConnectorId,
			evidence.EventTypeAuthSuccess, // 使用认证成功事件类型
			"",
			fmt.Sprintf("bootstrap token %s validated for connector %s", token.Code, req.ConnectorId),
		)
	}

	// 使用CA签发证书
	certPEM, keyPEM, err := s.ca.IssueConnectorCert(req.ConnectorId, nil, nil)
	if err != nil {
		return &pb.RegisterConnectorResponse{
			Success: false,
			Message: fmt.Sprintf("failed to issue certificate: %v", err),
		}, nil
	}

	// 读取CA证书
	caCertPEM, err := os.ReadFile(s.ca.CertPath)
	if err != nil {
		return &pb.RegisterConnectorResponse{
			Success: false,
			Message: fmt.Sprintf("failed to read CA certificate: %v", err),
		}, nil
	}

	// 标记 Token 为已使用（如果提供了 token）
	if s.tokenManager.IsEnabled() && req.RegistrationCode != "" {
		if err := s.tokenManager.MarkTokenUsed(req.RegistrationCode, req.ConnectorId); err != nil {
			// Token 标记失败不应该阻止注册，但记录警告
			log.Printf("[WARN] Failed to mark token as used: %v", err)
		} else {
			// 记录 Token 使用审计日志
			s.auditLog.SubmitEvidence(
				req.ConnectorId,
				evidence.EventTypeAuthSuccess,
				"",
				fmt.Sprintf("bootstrap token consumed: %s", req.RegistrationCode),
			)
		}
	}

	// 记录注册事件
	s.auditLog.SubmitEvidence(
		req.ConnectorId,
		evidence.EventTypeConnectorRegistered,
		"",
		"",
	)

	return &pb.RegisterConnectorResponse{
		Success:       true,
		Message:        "connector registered successfully",
		Certificate:    certPEM,
		PrivateKey:     keyPEM,
		CaCertificate:  caCertPEM,
		ConnectorId:    req.ConnectorId,
	}, nil
}

// SetConnectorStatus 设置连接器状态
func (s *IdentityServiceServer) SetConnectorStatus(ctx context.Context, req *pb.SetConnectorStatusRequest) (*pb.SetConnectorStatusResponse, error) {
	// 验证请求者身份
	if err := security.VerifyConnectorID(ctx, req.RequesterId); err != nil {
		return &pb.SetConnectorStatusResponse{
			Success: false,
			Message: fmt.Sprintf("requester authentication failed: %v", err),
		}, nil
	}

	// 验证请求者是否有权限设置该连接器的状态（只能设置自己的状态）
	if req.RequesterId != req.ConnectorId {
		return &pb.SetConnectorStatusResponse{
			Success: false,
			Message: "connector can only set its own status",
		}, nil
	}

	// 验证状态值
	validStatuses := map[string]bool{
		"active":   true,
		"inactive": true,
		"closed":   true,
	}
	if !validStatuses[req.Status] {
		return &pb.SetConnectorStatusResponse{
			Success: false,
			Message: fmt.Sprintf("invalid status: %s, must be one of: active, inactive, closed", req.Status),
		}, nil
	}

	// 获取当前状态
	info, err := s.registry.GetConnector(req.ConnectorId)
	if err != nil {
		return &pb.SetConnectorStatusResponse{
			Success: false,
			Message: fmt.Sprintf("connector not found: %v", err),
		}, nil
	}

	previousStatus := string(info.Status)

	// 设置新状态
	newStatus := control.ConnectorStatus(req.Status)
	if err := s.registry.SetStatus(req.ConnectorId, newStatus); err != nil {
		return &pb.SetConnectorStatusResponse{
			Success: false,
			Message: fmt.Sprintf("failed to set status: %v", err),
		}, nil
	}

	// 记录状态变更
	s.auditLog.SubmitEvidence(
		req.ConnectorId,
		evidence.EventTypeConnectorStatusChanged,
		"",
		"",
	)

	return &pb.SetConnectorStatusResponse{
		Success:        true,
		Message:        "status updated successfully",
		PreviousStatus: previousStatus,
		CurrentStatus:   req.Status,
	}, nil
}


package server

import (
	"context"
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"time"

	"github.com/google/uuid"
	pb "github.com/trusted-space/kernel/proto/kernel/v1"
	"github.com/trusted-space/kernel/kernel/control"
	"github.com/trusted-space/kernel/kernel/evidence"
	"github.com/trusted-space/kernel/kernel/security"
)

const KernelVersion = "v1.0.0"

// IdentityServiceServer 实现身份与准入服务
type IdentityServiceServer struct {
	pb.UnimplementedIdentityServiceServer
	registry  *control.Registry
	auditLog  *evidence.AuditLog
	ca        *security.CA
}

// NewIdentityServiceServer 创建身份服务
func NewIdentityServiceServer(registry *control.Registry, auditLog *evidence.AuditLog, ca *security.CA) *IdentityServiceServer {
	return &IdentityServiceServer{
		registry: registry,
		auditLog: auditLog,
		ca:       ca,
	}
}

// Handshake 处理连接器握手
func (s *IdentityServiceServer) Handshake(ctx context.Context, req *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	// 从证书中提取 connector_id
	certID, err := security.ExtractConnectorIDFromContext(ctx)
	if err != nil {
		// 记录认证失败
		s.auditLog.SubmitEvidence(
			req.ConnectorId,
			evidence.EventTypeAuthFail,
			"",
			"",
			map[string]string{"error": err.Error()},
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
			map[string]string{"error": "connector ID mismatch"},
		)
		
		return &pb.HandshakeResponse{
			Success: false,
			Message: fmt.Sprintf("connector ID mismatch: %v", err),
		}, nil
	}

	// 生成会话令牌
	sessionToken := uuid.New().String()

	// 注册连接器
	if err := s.registry.Register(req.ConnectorId, req.EntityType, req.PublicKey, sessionToken); err != nil {
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
		map[string]string{
			"entity_type": req.EntityType,
			"cert_cn":     certID,
		},
	)

	return &pb.HandshakeResponse{
		Success:       true,
		SessionToken:  sessionToken,
		KernelVersion: KernelVersion,
		Message:       "handshake successful",
	}, nil
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
	if err := security.VerifyConnectorID(ctx, req.RequesterId); err != nil {
		return &pb.DiscoverResponse{
			TotalCount: 0,
		}, fmt.Errorf("requester authentication failed: %w", err)
	}

	// 获取所有连接器列表
	allConnectors := s.registry.ListConnectors()
	
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
		})
	}

	return &pb.DiscoverResponse{
		Connectors: pbConnectors,
		TotalCount: int32(len(pbConnectors)),
	}, nil
}

// GetConnectorInfo 获取连接器详细信息
func (s *IdentityServiceServer) GetConnectorInfo(ctx context.Context, req *pb.GetConnectorInfoRequest) (*pb.GetConnectorInfoResponse, error) {
	// 验证请求者身份
	if err := security.VerifyConnectorID(ctx, req.RequesterId); err != nil {
		return &pb.GetConnectorInfoResponse{
			Found:   false,
			Message: fmt.Sprintf("requester authentication failed: %v", err),
		}, nil
	}

	// 获取连接器信息
	info, err := s.registry.GetConnector(req.ConnectorId)
	if err != nil {
		return &pb.GetConnectorInfoResponse{
			Found:   false,
			Message: fmt.Sprintf("connector not found: %v", err),
		}, nil
	}

	return &pb.GetConnectorInfoResponse{
		Found: true,
		Connector: &pb.ConnectorInfo{
			ConnectorId:   info.ConnectorID,
			EntityType:    info.EntityType,
			PublicKey:     info.PublicKey,
			Status:        string(info.Status), // 将ConnectorStatus转换为string
			LastHeartbeat: info.LastHeartbeat.Unix(),
			RegisteredAt:  info.RegisteredAt.Unix(),
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

	// 记录注册事件
	s.auditLog.SubmitEvidence(
		req.ConnectorId,
		evidence.EventTypeConnectorRegistered,
		"",
		"",
		map[string]string{
			"entity_type": req.EntityType,
			"has_config":  fmt.Sprintf("%v", req.ConfigYaml != ""),
		},
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
		map[string]string{
			"previous_status": previousStatus,
			"new_status":       req.Status,
		},
	)

	return &pb.SetConnectorStatusResponse{
		Success:        true,
		Message:        "status updated successfully",
		PreviousStatus: previousStatus,
		CurrentStatus:   req.Status,
	}, nil
}


package tempchat

import (
	"context"
	"log"
	"time"

	pb "github.com/trusted-space/kernel/proto/kernel/v1"
)

// TempChatServiceServer TempChatService 服务实现
type TempChatServiceServer struct {
	pb.UnimplementedTempChatServiceServer
	manager *TempChatManager
}

// NewTempChatServiceServer 创建 TempChatService 服务
func NewTempChatServiceServer(manager *TempChatManager) *TempChatServiceServer {
	return &TempChatServiceServer{
		manager: manager,
	}
}

// RegisterTempSession 注册临时会话
func (s *TempChatServiceServer) RegisterTempSession(ctx context.Context, req *pb.RegisterTempSessionRequest) (*pb.RegisterTempSessionResponse, error) {
	// 注册会话
	session, err := s.manager.RegisterSession(req)
	if err != nil {
		log.Printf("[ERROR] TempChat: 注册会话失败: %v", err)
		return &pb.RegisterTempSessionResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	return &pb.RegisterTempSessionResponse{
		Success:   true,
		SessionId: session.SessionID,
		Message:   "注册成功",
	}, nil
}

// TempSessionHeartbeat 心跳保活
func (s *TempChatServiceServer) TempSessionHeartbeat(ctx context.Context, req *pb.TempSessionHeartbeatRequest) (*pb.TempSessionHeartbeatResponse, error) {
	if err := s.manager.Heartbeat(req.SessionId, req.ConnectorId); err != nil {
		log.Printf("[WARN] TempChat: 心跳失败: %v", err)
		return &pb.TempSessionHeartbeatResponse{
			Success:    false,
			Message:    err.Error(),
			ServerTime: time.Now().Unix(),
		}, nil
	}

	return &pb.TempSessionHeartbeatResponse{
		Success:    true,
		Message:    "心跳成功",
		ServerTime: time.Now().Unix(),
	}, nil
}

// UnregisterTempSession 注销临时会话
func (s *TempChatServiceServer) UnregisterTempSession(ctx context.Context, req *pb.UnregisterTempSessionRequest) (*pb.UnregisterTempSessionResponse, error) {
	if err := s.manager.UnregisterSession(req.SessionId, req.ConnectorId); err != nil {
		log.Printf("[WARN] TempChat: 注销会话失败: %v", err)
		return &pb.UnregisterTempSessionResponse{
			Success: false,
			Message:  err.Error(),
		}, nil
	}

	return &pb.UnregisterTempSessionResponse{
		Success: true,
		Message: "注销成功",
	}, nil
}

// ListOnlineConnectors 查询在线连接器
func (s *TempChatServiceServer) ListOnlineConnectors(ctx context.Context, req *pb.ListOnlineConnectorsRequest) (*pb.ListOnlineConnectorsResponse, error) {
	// 获取本地在线连接器
	connectors := s.manager.GetLocalConnectors()

	// 如果需要包含远程连接器
	if req.IncludeRemote {
		// 获取远程运维方的在线连接器
		remoteConnectors := s.manager.GetRemoteConnectors(req.KernelId)
		connectors = append(connectors, remoteConnectors...)
	}

	// 按内核ID过滤（如果指定了）
	if req.KernelId != "" {
		var filtered []*pb.ConnectorOnlineInfo
		for _, c := range connectors {
			if c.KernelId == req.KernelId {
				filtered = append(filtered, c)
			}
		}
		connectors = filtered
	}

	return &pb.ListOnlineConnectorsResponse{
		Success:    true,
		Connectors: connectors,
		Message:    "查询成功",
	}, nil
}

// SendTempMessage 发送临时消息
func (s *TempChatServiceServer) SendTempMessage(ctx context.Context, req *pb.SendTempMessageRequest) (*pb.SendTempMessageResponse, error) {
	// 确定发送者内核ID
	senderKernelID := s.manager.GetKernelID()

	// 路由消息
	messageID, err := s.manager.RouteMessage(
		req.SenderId,
		senderKernelID,
		req.ReceiverId,
		req.Payload,
		req.FlowId,
	)
	if err != nil {
		log.Printf("[WARN] TempChat: 发送消息失败: %v", err)
		return &pb.SendTempMessageResponse{
			Success:  false,
			Message:  err.Error(),
		}, nil
	}

	return &pb.SendTempMessageResponse{
		Success:  true,
		MessageId: messageID,
		Message:  "消息已发送",
	}, nil
}

// ReceiveTempMessage 接收临时消息（流式）
func (s *TempChatServiceServer) ReceiveTempMessage(req *pb.ReceiveTempMessageRequest, stream pb.TempChatService_ReceiveTempMessageServer) error {
	// 获取消息通道
	msgChan, err := s.manager.ReceiveMessages(req.SessionId, req.ConnectorId)
	if err != nil {
		log.Printf("[WARN] TempChat: 获取消息通道失败: %v", err)
		return err
	}

	log.Printf("[INFO] TempChat: 连接器 %s 开始接收临时消息", req.ConnectorId)

	// 持续发送消息到流
	for {
		select {
		case <-stream.Context().Done():
			log.Printf("[INFO] TempChat: 连接器 %s 停止接收临时消息", req.ConnectorId)
			return nil
		case msg, ok := <-msgChan:
			if !ok {
				log.Printf("[INFO] TempChat: 消息通道已关闭 connector=%s", req.ConnectorId)
				return nil
			}
			if err := stream.Send(msg); err != nil {
				log.Printf("[WARN] TempChat: 发送消息到流失败: %v", err)
				return err
			}
		}
	}
}

// SyncOnlineConnectors 同步在线连接器列表（运维方之间）
func (s *TempChatServiceServer) SyncOnlineConnectors(ctx context.Context, req *pb.SyncOnlineConnectorsRequest) (*pb.SyncOnlineConnectorsResponse, error) {
	// 更新远程运维方连接状态
	s.manager.SetRemoteOpsConnection(req.SourceKernelId, "", 0)

	log.Printf("[INFO] TempChat: 同步在线连接器 from kernel=%s, count=%d",
		req.SourceKernelId, len(req.Connectors))

	// TODO: 存储远程连接器列表，供 ListOnlineConnectors 使用

	return &pb.SyncOnlineConnectorsResponse{
		Success: true,
		Message: "同步成功",
	}, nil
}

// ForwardTempMessage 转发临时消息（运维方之间）
func (s *TempChatServiceServer) ForwardTempMessage(ctx context.Context, req *pb.ForwardTempMessageRequest) (*pb.ForwardTempMessageResponse, error) {
	log.Printf("[INFO] TempChat: 转发消息 from=%s, to=%s, messageID=%s",
		req.SenderId, req.ReceiverId, req.MessageId)

	// 路由消息到本地连接器
	_, err := s.manager.RouteMessage(
		req.SenderId,
		req.SenderKernelId,
		req.ReceiverId,
		req.Payload,
		req.FlowId,
	)
	if err != nil {
		log.Printf("[WARN] TempChat: 转发消息失败: %v", err)
		return &pb.ForwardTempMessageResponse{
			Success: false,
			Message:  err.Error(),
		}, nil
	}

	return &pb.ForwardTempMessageResponse{
		Success: true,
		Message: "转发成功",
	}, nil
}

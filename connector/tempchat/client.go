package tempchat

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "github.com/trusted-space/kernel/proto/kernel/v1"
	connclient "github.com/trusted-space/kernel/connector/client"
)

// Config 连接器临时通信配置
type Config struct {
	ConnectorID     string
	KernelID       string
	EntityType     string
	Address         string // 连接器监听地址
	Port            int    // 连接器监听端口
	ServerAddr      string // 运维方（内核）地址
	CACertPath      string
	ClientCertPath  string
	ClientKeyPath   string
	ServerName      string
	ExposeToOthers  bool   // 是否向其他内核暴露
}

// TempChatClient 连接器临时通信客户端
type TempChatClient struct {
	config       *Config
	conn         *grpc.ClientConn
	tempChatSvc  pb.TempChatServiceClient

	sessionID    string
	connectorID  string
	kernelID     string

	ctx    context.Context
	cancel context.CancelFunc

	// 消息处理
	messageHandler func(senderID string, payload []byte) error

	// 连接状态
	connected bool
}

// NewTempChatClient 创建临时通信客户端
func NewTempChatClient(config *Config) (*TempChatClient, error) {
	return &TempChatClient{
		config:      config,
		connectorID: config.ConnectorID,
		kernelID:    config.KernelID,
		ctx:         context.Background(),
		connected:   false,
	}, nil
}

// Connect 连接到运维方并注册会话
func (c *TempChatClient) Connect() error {
	// 创建 gRPC 连接
	var creds credentials.TransportCredentials
	if c.config.ClientCertPath != "" && c.config.ClientKeyPath != "" {
		// 使用 mTLS
		tlsConfig, err := connclient.LoadTLSConfig(c.config.CACertPath, c.config.ClientCertPath, c.config.ClientKeyPath, c.config.ServerName)
		if err != nil {
			return fmt.Errorf("加载TLS配置失败: %w", err)
		}
		creds = credentials.NewTLS(tlsConfig)
	} else {
		// 使用普通 TLS
		tlsConfig := &tls.Config{
			InsecureSkipVerify: true, // 跳过验证，用于首次连接
			NextProtos:        []string{"h2"},
		}
		creds = credentials.NewTLS(tlsConfig)
	}

	conn, err := grpc.Dial(
		c.config.ServerAddr,
		grpc.WithTransportCredentials(creds),
		grpc.WithBlock(),
		grpc.WithTimeout(30*time.Second),
	)
	if err != nil {
		return fmt.Errorf("连接到运维方失败: %w", err)
	}

	c.conn = conn
	c.tempChatSvc = pb.NewTempChatServiceClient(conn)

	// 注册临时会话
	session, err := c.register()
	if err != nil {
		c.conn.Close()
		return fmt.Errorf("注册会话失败: %w", err)
	}

	c.sessionID = session.SessionId
	c.connected = true

	log.Printf("[OK] TempChat: 连接到运维方成功 (session: %s)", c.sessionID)

	// 启动心跳协程
	go c.startHeartbeat()

	// 启动消息接收协程
	go c.startMessageReceiver()

	return nil
}

// register 注册临时会话
func (c *TempChatClient) register() (*pb.RegisterTempSessionResponse, error) {
	resp, err := c.tempChatSvc.RegisterTempSession(c.ctx, &pb.RegisterTempSessionRequest{
		ConnectorId:    c.connectorID,
		KernelId:      c.kernelID,
		EntityType:    c.config.EntityType,
		ExposeToOthers: c.config.ExposeToOthers,
	})
	if err != nil {
		return nil, fmt.Errorf("注册请求失败: %w", err)
	}

	if !resp.Success {
		return nil, fmt.Errorf("注册失败: %s", resp.Message)
	}

	return resp, nil
}

// startHeartbeat 启动心跳协程
func (c *TempChatClient) startHeartbeat() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if !c.connected {
				continue
			}

			_, err := c.tempChatSvc.TempSessionHeartbeat(c.ctx, &pb.TempSessionHeartbeatRequest{
				SessionId:   c.sessionID,
				ConnectorId: c.connectorID,
				Timestamp:  time.Now().Unix(),
			})
			if err != nil {
				log.Printf("[WARN] TempChat: 心跳失败: %v", err)
			}
		}
	}
}

// startMessageReceiver 启动消息接收协程
func (c *TempChatClient) startMessageReceiver() {
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			if err := c.receiveMessages(); err != nil {
				log.Printf("[WARN] TempChat: 接收消息失败: %v", err)
				time.Sleep(5 * time.Second) // 失败后等待重试
			}
		}
	}
}

// receiveMessages 接收消息（流式）
func (c *TempChatClient) receiveMessages() error {
	stream, err := c.tempChatSvc.ReceiveTempMessage(c.ctx, &pb.ReceiveTempMessageRequest{
		ConnectorId: c.connectorID,
		SessionId:   c.sessionID,
	})
	if err != nil {
		return fmt.Errorf("创建消息流失败: %w", err)
	}

	log.Printf("[INFO] TempChat: 开始接收消息")

	for {
		msg, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("接收消息失败: %w", err)
		}

		log.Printf("[INFO] TempChat: 收到消息 from=%s, messageID=%s, content=%s, flowID=%s",
			msg.SenderId, msg.MessageId, string(msg.Payload), msg.FlowId)

		if c.messageHandler != nil {
			if err := c.messageHandler(msg.SenderId, msg.Payload); err != nil {
				log.Printf("[WARN] TempChat: 处理消息失败: %v", err)
			}
		}
	}
}

// SendMessage 发送消息给目标连接器
func (c *TempChatClient) SendMessage(receiverID string, payload []byte, flowID string) (string, error) {
	if !c.connected {
		return "", fmt.Errorf("未连接到运维方")
	}

	resp, err := c.tempChatSvc.SendTempMessage(c.ctx, &pb.SendTempMessageRequest{
		SenderId:  c.connectorID,
		ReceiverId: receiverID,
		Payload:   payload,
		Timestamp: time.Now().Unix(),
		FlowId:   flowID,
	})
	if err != nil {
		return "", fmt.Errorf("发送消息失败: %w", err)
	}

	if !resp.Success {
		return "", fmt.Errorf("发送失败: %s", resp.Message)
	}

	log.Printf("[OK] TempChat: 消息已发送 to=%s, messageID=%s", receiverID, resp.MessageId)
	return resp.MessageId, nil
}

// ListOnlineConnectors 列出在线连接器
func (c *TempChatClient) ListOnlineConnectors(kernelID string, includeRemote bool) ([]*pb.ConnectorOnlineInfo, error) {
	if !c.connected {
		return nil, fmt.Errorf("未连接到运维方")
	}

	resp, err := c.tempChatSvc.ListOnlineConnectors(c.ctx, &pb.ListOnlineConnectorsRequest{
		RequesterId:  c.connectorID,
		KernelId:     kernelID,
		IncludeRemote: includeRemote,
	})
	if err != nil {
		return nil, fmt.Errorf("查询失败: %w", err)
	}

	if !resp.Success {
		return nil, fmt.Errorf("查询失败: %s", resp.Message)
	}

	return resp.Connectors, nil
}

// SetMessageHandler 设置消息处理回调
func (c *TempChatClient) SetMessageHandler(handler func(senderID string, payload []byte) error) {
	c.messageHandler = handler
}

// Close 关闭连接
func (c *TempChatClient) Close() error {
	c.connected = false

	if c.cancel != nil {
		c.cancel()
	}

	// 注销会话
	if c.tempChatSvc != nil && c.sessionID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		c.tempChatSvc.UnregisterTempSession(ctx, &pb.UnregisterTempSessionRequest{
			SessionId:   c.sessionID,
			ConnectorId: c.connectorID,
		})
	}

	if c.conn != nil {
		c.conn.Close()
	}

	log.Printf("[INFO] TempChat: 连接已关闭")
	return nil
}

// IsConnected 检查是否已连接
func (c *TempChatClient) IsConnected() bool {
	return c.connected
}

// GetSessionID 获取会话ID
func (c *TempChatClient) GetSessionID() string {
	return c.sessionID
}

// GetConnectorID 获取连接器ID
func (c *TempChatClient) GetConnectorID() string {
	return c.connectorID
}

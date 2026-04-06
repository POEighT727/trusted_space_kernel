package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "github.com/trusted-space/kernel/proto/kernel/v1"
	"github.com/trusted-space/kernel/kernel/circulation"
	"github.com/trusted-space/kernel/kernel/control"
	"github.com/trusted-space/kernel/kernel/evidence"
)
type RemotePermissionRequest struct {
	RequestID     string
	RequesterID   string
	ChannelID     string
	ChangeType    string
	TargetID      string
	Reason        string
	Status        string
	SourceKernelID string
	CreatedAt     time.Time
}

// KernelConfig 内核配置
type KernelConfig struct {
	KernelID          string
	KernelType        string
	Description       string
	Address           string
	Port              int
	KernelPort        int
	CACertPath        string
	KernelCertPath    string
	KernelKeyPath     string
	HeartbeatInterval int
	ConnectTimeout    int
	MaxRetries        int
}

// KernelInfo 内核信息
type KernelInfo struct {
	KernelID      string
	Address       string
	Port          int    // 内核间通信端口
	MainPort      int    // 主服务器端口（用于IdentityService等）
	Status        string
	LastHeartbeat int64
	PublicKey     string
	Description   string
	Client        pb.KernelServiceClient
	conn          *grpc.ClientConn
}

// MultiKernelManager 多内核管理器
type MultiKernelManager struct {
	config         *KernelConfig
	registry       *control.Registry
	channelManager *circulation.ChannelManager
	auditLog       *evidence.AuditLog
	// businessChainManager 业务哈希链管理器（由外部注入，供 KernelServiceServer 使用）
	businessChainManager *BusinessChainManager

	// multiHopConfigManager 多跳路由配置管理器（由外部注入）
	multiHopConfigManager *MultiHopConfigManager

	kernels   map[string]*KernelInfo
	kernelsMu sync.RWMutex
	pendingRequests   map[string]*PendingInterconnectRequest
	pendingRequestsMu sync.RWMutex

	running bool
	// NotificationManager 用于内核间服务通知本地连接器（由外部注入）
	notificationManager *NotificationManager

	// 同步的权限请求（key: channelID, value: 权限请求列表）
	remotePermissionRequests     map[string][]*RemotePermissionRequest
	remotePermissionRequestsMu sync.RWMutex

	// ========== 多跳链路自动建立机制 ==========
	// 待审批的 hop 追踪器（key: requestID, value: hopInfo）
	pendingHops     map[string]*PendingHopInfo
	pendingHopsMu   sync.RWMutex
	// 当前活跃的多跳路由建立会话（key: routeName, value: 会话）
	multiHopSessions     map[string]*MultiHopSession
	multiHopSessionsMu   sync.RWMutex
	// 多跳审批回调：收到审批通知后自动触发重连
	OnMultiHopApproved func(notification *pb.NotifyMultiHopApprovedRequest)
	// Hop 建立完成回调：收到 HopEstablished RPC 后自动触发重连
	OnHopEstablished func(req *pb.HopEstablishedRequest)
}

// NewMultiKernelManager 创建多内核管理器
func NewMultiKernelManager(config *KernelConfig, registry *control.Registry,
	channelManager *circulation.ChannelManager) (*MultiKernelManager, error) {

	manager := &MultiKernelManager{
		config:         config,
		registry:       registry,
		channelManager: channelManager,
		kernels:        make(map[string]*KernelInfo),
		pendingRequests: make(map[string]*PendingInterconnectRequest),
		remotePermissionRequests: make(map[string][]*RemotePermissionRequest),
		running:        true,
		pendingHops:    make(map[string]*PendingHopInfo),
		multiHopSessions: make(map[string]*MultiHopSession),
	}

	return manager, nil
}

// SetNotificationManager 注入 NotificationManager（由 main 初始化后设置）
func (m *MultiKernelManager) SetNotificationManager(nm *NotificationManager) {
	m.notificationManager = nm
}

// SetAuditLog 注入 AuditLog（由 main 初始化后设置）
func (m *MultiKernelManager) SetAuditLog(al *evidence.AuditLog) {
	m.auditLog = al
}

// SetBusinessChainManager 注入 BusinessChainManager（由 main 初始化后设置）
func (m *MultiKernelManager) SetBusinessChainManager(bcm *BusinessChainManager) {
	m.businessChainManager = bcm
}

// SetMultiHopConfigManager 设置多跳路由配置管理器
func (m *MultiKernelManager) SetMultiHopConfigManager(mhcm *MultiHopConfigManager) {
	m.multiHopConfigManager = mhcm
}

// PendingInterconnectRequest 表示一个待审批的内核互联请求
type PendingInterconnectRequest struct {
	RequestID         string
	RequesterKernelID string
	Address           string
	MainPort          int
	KernelPort        int
	CaCertificate     []byte
	Timestamp         int64
	Status            string // "pending", "approved", "rejected"
}

// PendingHopInfo 表示一个等待审批的 hop 信息（由发起方记录）
type PendingHopInfo struct {
	RouteName  string // 所属路由名称
	HopIndex   int    // hop 序号（从1开始）
	HopTotal   int    // 总 hop 数
	ToKernelID string // 目标内核 ID
	ToAddress  string // 目标地址
	ToPort     int    // 目标端口
	RequestID  string // 原始请求 ID
	Approved   bool   // 是否已收到批准通知
	ApprovedBy string // 批准方内核 ID
}

// MultiHopSession 表示一个活跃的多跳路由建立会话（由发起方持有）
type MultiHopSession struct {
	RouteName   string
	RouteConfig *MultiHopConfigFile
	Hops       map[int]*PendingHopInfo // key: hopIndex, value: hop info
	AllDone    chan struct{}          // 所有 hop 都建立完成后关闭
	Done       bool                   // 是否已完成
	Mu         sync.Mutex
}

// AddPendingRequest 添加一个待审批请求
func (m *MultiKernelManager) AddPendingRequest(req *PendingInterconnectRequest) {
	m.pendingRequestsMu.Lock()
	defer m.pendingRequestsMu.Unlock()
	m.pendingRequests[req.RequestID] = req
}

// ListPendingRequests 列出所有待审批请求
func (m *MultiKernelManager) ListPendingRequests() []*PendingInterconnectRequest {
	m.pendingRequestsMu.RLock()
	defer m.pendingRequestsMu.RUnlock()
	list := make([]*PendingInterconnectRequest, 0, len(m.pendingRequests))
	for _, r := range m.pendingRequests {
		list = append(list, r)
	}
	return list
}

// ApprovePendingRequest 批准指定请求（会发起到请求内核的连接）
func (m *MultiKernelManager) ApprovePendingRequest(requestID string) error {
	// 移出并获取请求信息
	m.pendingRequestsMu.Lock()
	req, exists := m.pendingRequests[requestID]
	if !exists {
		m.pendingRequestsMu.Unlock()
		return fmt.Errorf("pending request %s not found", requestID)
	}
	delete(m.pendingRequests, requestID)
	m.pendingRequestsMu.Unlock()

	// 记录互联批准存证
	if m.auditLog != nil {
		m.auditLog.SubmitBasicEvidence(
			m.config.KernelID,
			evidence.EventTypeInterconnectApproved,
			"", // 无频道ID
			"", // 无业务数据哈希
			evidence.DirectionOutgoing,
			req.RequesterKernelID,
		)
	}

	// 在批准前先检查是否已经知晓该内核（避免重复通知）
	m.kernelsMu.RLock()
	_, alreadyKnown := m.kernels[req.RequesterKernelID]
	m.kernelsMu.RUnlock()
	if alreadyKnown {
		// 确保已有持久连接；如果没有则尝试建立
		if err := m.connectToKernelInternal(req.RequesterKernelID, req.Address, req.KernelPort, false); err != nil {
			return fmt.Errorf("failed to ensure connection to requester kernel %s: %w", req.RequesterKernelID, err)
		}
		// 广播已知内核给该内核
		go m.BroadcastKnownKernels(req.RequesterKernelID)
		return nil
	}

	// 先通知请求方"已批准"，调用对方的 RegisterKernel 并带上 interconnect_approve 标记，
	// 让请求方在自己的服务端直接把批准方加入已知内核列表（避免再次创建 pending）。
	// 同时发送 NotifyMultiHopApproved RPC，让请求方知道该 hop 已批准并触发自动重连。
	if err := m.notifyRequesterApproveAndMultiHopApproved(req, requestID); err != nil {
		return fmt.Errorf("failed to notify requester %s of approve: %w", req.RequesterKernelID, err)
	}

	// 然后由本端建立到请求方的持久连接（不再作为 interconnect_request）
	if err := m.connectToKernelInternal(req.RequesterKernelID, req.Address, req.KernelPort, false); err != nil {
		return fmt.Errorf("failed to connect to requester kernel %s: %w", req.RequesterKernelID, err)
	}

	// 广播本内核已知的其他内核给新连接的内核
	go m.BroadcastKnownKernels(req.RequesterKernelID)

	return nil
}

// notifyRequesterApprove 向请求方发送批准通知（使用 RegisterKernel + metadata interconnect_approve）
// 这是一个内部辅助方法，仅发送 IsInterconnectApprove=true 的注册请求
func (m *MultiKernelManager) notifyRequesterApprove(req *PendingInterconnectRequest) error {
	return m.sendInterconnectApprove(req)
}

// sendInterconnectApprove 建立到请求方的连接并发送 IsInterconnectApprove=true 的注册请求
func (m *MultiKernelManager) sendInterconnectApprove(req *PendingInterconnectRequest) error {
	cert, err := tls.LoadX509KeyPair(m.config.KernelCertPath, m.config.KernelKeyPath)
	if err != nil {
		return fmt.Errorf("failed to load certificates: %w", err)
	}

	caCertPool := x509.NewCertPool()
	ownCACert, err := os.ReadFile(m.config.CACertPath)
	if err != nil {
		return fmt.Errorf("failed to read own CA certificate: %w", err)
	}
	if !caCertPool.AppendCertsFromPEM(ownCACert) {
		return fmt.Errorf("failed to append own CA certificate")
	}

	peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", req.RequesterKernelID)
	if peerCert, err := os.ReadFile(peerCACertPath); err == nil {
		_ = caCertPool.AppendCertsFromPEM(peerCert)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
		MinVersion:   tls.VersionTLS13,
	}
	creds := credentials.NewTLS(tlsConfig)

	targetAddr := fmt.Sprintf("%s:%d", req.Address, req.KernelPort)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(m.config.ConnectTimeout)*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, targetAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return fmt.Errorf("failed to dial requester %s: %w", req.RequesterKernelID, err)
	}
	defer conn.Close()

	client := pb.NewKernelServiceClient(conn)
	ownCACertData, _ := os.ReadFile(m.config.CACertPath)

	approveReq := &pb.RegisterKernelRequest{
		KernelId:      m.config.KernelID,
		Address:       m.config.Address,
		Port:          int32(m.config.Port),
		PublicKey:     "",
		CaCertificate: ownCACertData,
		IsInterconnectApprove: true,
		Timestamp:     time.Now().Unix(),
	}

	_, err = client.RegisterKernel(context.Background(), approveReq)
	if err != nil {
		return fmt.Errorf("approve RPC failed: %w", err)
	}
	return nil
}

// notifyRequesterApproveAndMultiHopApproved 发送互联批准 + 多跳审批通知
// 批准方调用此方法通知发起方该 hop 已批准，发起方收到后将自动重连
func (m *MultiKernelManager) notifyRequesterApproveAndMultiHopApproved(req *PendingInterconnectRequest, requestID string) error {
	// 1. 先发送 IsInterconnectApprove=true 的注册请求
	if err := m.sendInterconnectApprove(req); err != nil {
		return fmt.Errorf("failed to send interconnect approve: %w", err)
	}

	// 2. 然后发送 NotifyMultiHopApproved RPC，携带 hop 元信息
	cert, err := tls.LoadX509KeyPair(m.config.KernelCertPath, m.config.KernelKeyPath)
	if err != nil {
		return fmt.Errorf("failed to load certificates: %w", err)
	}

	caCertPool := x509.NewCertPool()
	ownCACert, err := os.ReadFile(m.config.CACertPath)
	if err != nil {
		return fmt.Errorf("failed to read own CA certificate: %w", err)
	}
	if !caCertPool.AppendCertsFromPEM(ownCACert) {
		return fmt.Errorf("failed to append own CA certificate")
	}

	peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", req.RequesterKernelID)
	if peerCert, err := os.ReadFile(peerCACertPath); err == nil {
		_ = caCertPool.AppendCertsFromPEM(peerCert)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
		MinVersion:   tls.VersionTLS13,
	}
	creds := credentials.NewTLS(tlsConfig)

	targetAddr := fmt.Sprintf("%s:%d", req.Address, req.KernelPort)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(m.config.ConnectTimeout)*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, targetAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		log.Printf("[WARN] Failed to dial for NotifyMultiHopApproved: %v (register already sent)", err)
		return nil
	}
	defer conn.Close()

	client := pb.NewKernelServiceClient(conn)

	notifyReq := &pb.NotifyMultiHopApprovedRequest{
		ApprovedKernelId: req.RequesterKernelID,
		ApproverKernelId: m.config.KernelID,
		RequestId:       requestID,
		HopIndex:        0,
		HopTotal:        0,
		ApprovedAt:       time.Now().Unix(),
	}

	resp, err := client.NotifyMultiHopApproved(context.Background(), notifyReq)
	if err != nil {
		log.Printf("[WARN] NotifyMultiHopApproved RPC failed: %v (register already sent)", err)
		return nil
	}
	if !resp.Success {
		log.Printf("[WARN] NotifyMultiHopApproved returned failure: %s", resp.Message)
	}
	return nil
}

// BroadcastKnownKernels 向指定内核广播本内核已知的内核列表
// 这用于在新内核连接后，让它了解网络中其他内核的信息
func (m *MultiKernelManager) BroadcastKnownKernels(targetKernelID string) error {
	m.kernelsMu.RLock()
	kernelInfo, exists := m.kernels[targetKernelID]
	if !exists {
		m.kernelsMu.RUnlock()
		return fmt.Errorf("kernel %s not found", targetKernelID)
	}

	// 检查客户端是否可用
	if kernelInfo.Client == nil {
		m.kernelsMu.RUnlock()
		return fmt.Errorf("kernel %s client not available", targetKernelID)
	}

	// 构建已知内核列表
	knownKernels := make([]*pb.KernelInfo, 0)
	for _, k := range m.kernels {
		if k.KernelID == targetKernelID {
			continue // 跳过目标内核自己
		}
		knownKernels = append(knownKernels, &pb.KernelInfo{
			KernelId:      k.KernelID,
			Address:       k.Address,
			Port:          int32(k.MainPort), // 使用主端口
			Status:        k.Status,
			LastHeartbeat: k.LastHeartbeat,
			PublicKey:     k.PublicKey,
		})
	}
	m.kernelsMu.RUnlock()

	if len(knownKernels) == 0 {
		return nil
	}

	// 发送同步请求
	req := &pb.SyncKnownKernelsRequest{
		SourceKernelId: m.config.KernelID,
		KnownKernels:   knownKernels,
		SyncType:       "full",
	}

	resp, err := kernelInfo.Client.SyncKnownKernels(context.Background(), req)
	if err != nil {
		return fmt.Errorf("failed to broadcast known kernels to %s: %w", targetKernelID, err)
	}

	log.Printf("Broadcasting %d known kernels to %s, peer added %d new kernels",
		len(knownKernels), targetKernelID, len(resp.NewlyKnownKernels))

	// 如果对方返回了新内核，尝试连接到它们
	for _, newKernel := range resp.NewlyKnownKernels {

		// 检查是否已经在本地列表中
		m.kernelsMu.Lock()
		existing, alreadyKnown := m.kernels[newKernel.KernelId]
		m.kernelsMu.Unlock()

		if alreadyKnown {
			// 已经存在，尝试确保连接存在
			if existing.conn == nil || existing.Client == nil {
				// 没有有效连接，尝试重建
				mainPort := int(newKernel.Port)
				kernelPort := mainPort + 2
				if err := m.connectToKernelInternal(newKernel.KernelId, newKernel.Address, kernelPort, false); err != nil {
					log.Printf("[WARN] Failed to reconnect to kernel %s: %v", newKernel.KernelId, err)
				}
			}
			continue
		}

		// 尝试建立连接
		mainPort := int(newKernel.Port)
		kernelPort := mainPort + 2
		if err := m.connectToKernelInternal(newKernel.KernelId, newKernel.Address, kernelPort, false); err != nil {
			// 如果是 already connected 错误，忽略
			if strings.Contains(err.Error(), "already connected") {
			} else {
				log.Printf("[WARN] Failed to connect to new kernel %s: %v", newKernel.KernelId, err)
			}
		}
	}

	return nil
}

// StartKernelServer 启动内核间通信服务器
func (m *MultiKernelManager) StartKernelServer() error {
	address := fmt.Sprintf("%s:%d", m.config.Address, m.config.KernelPort)

	// 创建TLS配置用于内核间通信
	cert, err := tls.LoadX509KeyPair(m.config.KernelCertPath, m.config.KernelKeyPath)
	if err != nil {
		return fmt.Errorf("failed to load kernel certificates: %w", err)
	}

	// 创建证书池，包含自己的CA和所有已知对等内核的CA
	caCertPool := x509.NewCertPool()

	// 添加自己的CA证书
	ownCACert, err := os.ReadFile(m.config.CACertPath)
	if err != nil {
		return fmt.Errorf("failed to read own CA certificate: %w", err)
	}
	if !caCertPool.AppendCertsFromPEM(ownCACert) {
		return fmt.Errorf("failed to append own CA certificate")
	}

	// 添加所有已知对等内核的CA证书
	m.kernelsMu.RLock()
	for kernelID := range m.kernels {
		peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", kernelID)
		if peerCACert, err := os.ReadFile(peerCACertPath); err == nil {
			if !caCertPool.AppendCertsFromPEM(peerCACert) {
			}
		}
	}
	m.kernelsMu.RUnlock()

	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caCertPool,
		RootCAs:      caCertPool,
	})

	server := grpc.NewServer(grpc.Creds(creds))

	kernelService := NewKernelServiceServer(m, m.channelManager, m.registry, m.notificationManager, m.auditLog, m.businessChainManager)
	pb.RegisterKernelServiceServer(server, kernelService)

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to listen on kernel port: %w", err)
	}

	log.Printf("[INFO] Kernel-to-kernel server started on %s", address)

	if err := server.Serve(listener); err != nil {
		return fmt.Errorf("failed to serve kernel server: %w", err)
	}

	return nil
}

// connectToKernelInternal 执行连接并注册，interconnectRequest 表示是否把本次注册作为“互联请求”发送给目标（带 metadata）
func (m *MultiKernelManager) connectToKernelInternal(kernelID, address string, port int, interconnectRequest bool) error {
	m.kernelsMu.Lock()
	defer m.kernelsMu.Unlock()

	// 检查是否已经有有效的连接（Client 和 conn 都存在）
	if existingInfo, exists := m.kernels[kernelID]; exists {
		if existingInfo.Client != nil && existingInfo.conn != nil {
			return fmt.Errorf("already connected to kernel %s", kernelID)
		}
		// 内核存在但连接无效，删除后重新建立
		delete(m.kernels, kernelID)
	}

	// 创建TLS配置
	cert, err := tls.LoadX509KeyPair(m.config.KernelCertPath, m.config.KernelKeyPath)
	if err != nil {
		return fmt.Errorf("failed to load certificates: %w", err)
	}

	// 创建证书池，包含自己的CA和可能的对等CA
	caCertPool := x509.NewCertPool()

	// 添加自己的CA证书
	ownCACert, err := os.ReadFile(m.config.CACertPath)
	if err != nil {
		return fmt.Errorf("failed to read own CA certificate: %w", err)
	}
	if !caCertPool.AppendCertsFromPEM(ownCACert) {
		return fmt.Errorf("failed to append own CA certificate")
	}

	// 检查是否已经有对等内核的CA证书，如果有也添加
	peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", kernelID)
	if peerCACert, err := os.ReadFile(peerCACertPath); err == nil {
		if !caCertPool.AppendCertsFromPEM(peerCACert) {
		}
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
		// 对于内核间通信，我们需要客户端认证
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  caCertPool,
	}

	creds := credentials.NewTLS(tlsConfig)

	// 连接到目标内核
	targetAddr := fmt.Sprintf("%s:%d", address, port)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(m.config.ConnectTimeout)*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, targetAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return fmt.Errorf("failed to connect to kernel %s: %w", kernelID, err)
	}

	client := pb.NewKernelServiceClient(conn)

	// 读取自己的CA证书
	caCertData, err := os.ReadFile(m.config.CACertPath)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to read CA certificate: %w", err)
	}

	// 注册自己到目标内核
	registerReq := &pb.RegisterKernelRequest{
		KernelId:     m.config.KernelID,
		Address:      m.config.Address,
		Port:         int32(m.config.Port),
		PublicKey:    "", // TODO: 读取公钥
		CaCertificate: caCertData, // 发送自己的CA证书
		Timestamp:    time.Now().Unix(),
		IsInterconnectRequest: interconnectRequest,
	}

	resp, err := client.RegisterKernel(context.Background(), registerReq)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to register with kernel %s: %w", kernelID, err)
	}

	// 如果是发起互联请求，记录存证
	if interconnectRequest && m.auditLog != nil {
		m.auditLog.SubmitBasicEvidence(
			m.config.KernelID,
			evidence.EventTypeInterconnectRequested,
			"",
			"", // 无业务数据哈希
			evidence.DirectionOutgoing,
			kernelID,
		)
	}

	// 如果目标返回了一个 interconnect_request_id，说明它把请求作为待审批处理，先不算最终连接
	if resp.Message != "" && strings.Contains(resp.Message, "interconnect_request_id:") {
		// 保存对方的CA证书（如果提供）
		if len(resp.PeerCaCertificate) > 0 {
			peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", kernelID)
		if err := os.WriteFile(peerCACertPath, resp.PeerCaCertificate, 0644); err != nil {
		}
		}

		// 从 message 中提取 request id 并返回特定错误，供调用处区分 pending 状态
		parts := strings.Split(resp.Message, ";")
		requestID := ""
		for _, p := range parts {
			if strings.HasPrefix(p, "interconnect_request_id:") {
				requestID = strings.TrimPrefix(p, "interconnect_request_id:")
				break
			}
		}

		// Close connection (no final registration)
		conn.Close()
		if requestID != "" {
			return fmt.Errorf("interconnect_pending:%s", requestID)
		}
		return fmt.Errorf("interconnect_pending")
	}

	if !resp.Success {
		// 如果目标已经把我们注册过（可能是并发或之前已注册），当作成功继续建立连接
		if strings.Contains(strings.ToLower(resp.Message), "kernel already registered") {
			log.Printf("Warning: target kernel %s reports already registered: %s — proceeding to establish connection", kernelID, resp.Message)
			// 即使对方返回已注册，也要保存对方返回的CA证书
			if len(resp.PeerCaCertificate) > 0 {
				peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", kernelID)
				if err := os.WriteFile(peerCACertPath, resp.PeerCaCertificate, 0644); err != nil {
				} else {
				}
			}
		} else {
			conn.Close()
			return fmt.Errorf("registration rejected by kernel %s: %s", kernelID, resp.Message)
		}
	} else {
		// 保存对方的CA证书
		if len(resp.PeerCaCertificate) > 0 {
			peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", kernelID)
		if err := os.WriteFile(peerCACertPath, resp.PeerCaCertificate, 0644); err != nil {
		}
		}
	}

	// 保存内核信息
	// port 是内核间通信端口，mainPort = kernelPort - 2（假设标准配置）
	mainPort := port - 2
	kernelInfo := &KernelInfo{
		KernelID:      kernelID,
		Address:       address,
		Port:          port,          // 内核间通信端口
		MainPort:      mainPort,      // 主服务器端口（IdentityService等）
		Status:        "active",
		LastHeartbeat: time.Now().Unix(),
		Client:        client,
		conn:          conn,
	}

	m.kernels[kernelID] = kernelInfo

	// 启动心跳goroutine
	go m.kernelHeartbeat(kernelID)

	log.Printf("[OK] Connected to kernel %s at %s:%d", kernelID, address, port)

	// 广播本内核已知的其他内核给新连接的内核
	go m.BroadcastKnownKernels(kernelID)

	// 如果对方之前不知道本内核已知的其他内核，需要反向同步
	// 这样可以实现：当 A 通知 B 有关 C 的信息后，B 会通知 A 有关 D 的信息
	go m.SyncPeerKernels(kernelID)

	return nil
}

// ConnectToKernel 连接到另一个内核
// port 参数应该是目标内核的内核通信端口 (kernel_port)
func (m *MultiKernelManager) ConnectToKernel(kernelID, address string, port int, routeName ...string) error {
	m.kernelsMu.Lock()
	defer m.kernelsMu.Unlock()

	// 检查是否已经连接
	if _, exists := m.kernels[kernelID]; exists {
		return fmt.Errorf("already connected to kernel %s", kernelID)
	}

	// 如果指定了路由名称，先建立多跳路由
	if len(routeName) > 0 && routeName[0] != "" {
		// 检查是否配置了多跳配置管理器
		if m.multiHopConfigManager == nil {
			return fmt.Errorf("multi-hop config manager not initialized, cannot use route")
		}

		routeCfg, err := m.multiHopConfigManager.LoadConfig(routeName[0])
		if err != nil {
			return fmt.Errorf("failed to load route config: %w", err)
		}

		// 验证路由是否包含目标内核
		hopFound := false
		for _, hop := range routeCfg.Hops {
			if hop.ToKernel == kernelID {
				hopFound = true
				break
			}
		}
		if !hopFound {
			return fmt.Errorf("route %s does not include target kernel %s", routeName[0], kernelID)
		}

		// 连接多跳路由
		if err := m.ConnectMultiHopRoute(routeCfg); err != nil {
			return fmt.Errorf("failed to connect multi-hop route: %w", err)
		}
	}

	// 创建TLS配置
	cert, err := tls.LoadX509KeyPair(m.config.KernelCertPath, m.config.KernelKeyPath)
	if err != nil {
		return fmt.Errorf("failed to load certificates: %w", err)
	}

	// 创建证书池，包含自己的CA和可能的对等CA
	caCertPool := x509.NewCertPool()

	// 添加自己的CA证书
	ownCACert, err := os.ReadFile(m.config.CACertPath)
	if err != nil {
		return fmt.Errorf("failed to read own CA certificate: %w", err)
	}
	if !caCertPool.AppendCertsFromPEM(ownCACert) {
		return fmt.Errorf("failed to append own CA certificate")
	}

	// 检查是否已经有对等内核的CA证书，如果有也添加
	peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", kernelID)
	if peerCACert, err := os.ReadFile(peerCACertPath); err == nil {
		if !caCertPool.AppendCertsFromPEM(peerCACert) {
		}
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
		// 对于内核间通信，我们需要客户端认证
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  caCertPool,
	}

	creds := credentials.NewTLS(tlsConfig)

	// 连接到目标内核
	targetAddr := fmt.Sprintf("%s:%d", address, port)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(m.config.ConnectTimeout)*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, targetAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return fmt.Errorf("failed to connect to kernel %s: %w", kernelID, err)
	}

	client := pb.NewKernelServiceClient(conn)

	// 读取自己的CA证书
	caCertData, err := os.ReadFile(m.config.CACertPath)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to read CA certificate: %w", err)
	}

	// 注册自己到目标内核
	registerReq := &pb.RegisterKernelRequest{
		KernelId:     m.config.KernelID,
		Address:      m.config.Address,
		Port:         int32(m.config.Port),
		PublicKey:    "", // TODO: 读取公钥
		CaCertificate: caCertData, // 发送自己的CA证书
		IsInterconnectRequest: true,
		Timestamp:    time.Now().Unix(),
	}

	resp, err := client.RegisterKernel(context.Background(), registerReq)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to register with kernel %s: %w", kernelID, err)
	}

	// 如果是发起互联请求，记录存证
	if m.auditLog != nil {
		m.auditLog.SubmitBasicEvidence(
			m.config.KernelID,
			evidence.EventTypeInterconnectRequested,
			"",
			"", // 无业务数据哈希
			evidence.DirectionOutgoing,
			kernelID,
		)
	}

	// 如果目标返回了一个 interconnect_request_id，说明它把请求作为待审批处理，先不算最终连接
	if resp.Message != "" && strings.Contains(resp.Message, "interconnect_request_id:") {
		// 保存对方的CA证书（如果提供）
		if len(resp.PeerCaCertificate) > 0 {
			peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", kernelID)
			if err := os.WriteFile(peerCACertPath, resp.PeerCaCertificate, 0644); err != nil {
			} else {
			}
		}

		// 从 message 中提取 request id 并返回特定错误，供调用处区分 pending 状态
		parts := strings.Split(resp.Message, ";")
		requestID := ""
		for _, p := range parts {
			if strings.HasPrefix(p, "interconnect_request_id:") {
				requestID = strings.TrimPrefix(p, "interconnect_request_id:")
				break
			}
		}

		// Close connection (no final registration)
		conn.Close()
		if requestID != "" {
			return fmt.Errorf("interconnect_pending:%s", requestID)
		}
		return fmt.Errorf("interconnect_pending")
	}

	if !resp.Success {
		conn.Close()
		return fmt.Errorf("registration rejected by kernel %s: %s", kernelID, resp.Message)
	}

	// 保存对方的CA证书
	if len(resp.PeerCaCertificate) > 0 {
		peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", kernelID)
		if err := os.WriteFile(peerCACertPath, resp.PeerCaCertificate, 0644); err != nil {
		} else {
		}
	}

	// 保存内核信息
	// port 是内核通信端口，mainPort = kernelPort - 2（假设标准配置）
	mainPort := port - 2
	kernelInfo := &KernelInfo{
		KernelID:      kernelID,
		Address:       address,
		Port:          port,          // 内核间通信端口
		MainPort:      mainPort,      // 主服务器端口（IdentityService等）
		Status:        "active",
		LastHeartbeat: time.Now().Unix(),
		Client:        client,
		conn:          conn,
	}

	m.kernels[kernelID] = kernelInfo

	// 启动心跳goroutine
	go m.kernelHeartbeat(kernelID)

	log.Printf("[OK] Connected to kernel %s at %s:%d", kernelID, address, port)
	return nil
}

// ConnectToKernelViaRoute 通过多跳路由连接到目标内核
// 只提供路由名称，目标内核信息从路由配置中获取
func (m *MultiKernelManager) ConnectToKernelViaRoute(routeName string) error {
	if m.multiHopConfigManager == nil {
		return fmt.Errorf("multi-hop config manager not initialized")
	}

	// 加载路由配置
	routeCfg, err := m.multiHopConfigManager.LoadConfig(routeName)
	if err != nil {
		return fmt.Errorf("failed to load route config: %w", err)
	}

	if len(routeCfg.Hops) == 0 {
		return fmt.Errorf("route %s has no hops", routeName)
	}

	// 获取最后一跳的信息（目标内核）
	lastHop := routeCfg.Hops[len(routeCfg.Hops)-1]
	targetKernelID := lastHop.ToKernel
	targetAddress := lastHop.ToAddress
	targetPort := lastHop.ToPort

	log.Printf("=== Connecting via route: %s ===", routeName)
	log.Printf("Target kernel: %s (%s:%d)", targetKernelID, targetAddress, targetPort)

	// 调用 ConnectMultiHopRoute 建立多跳连接
	if err := m.ConnectMultiHopRoute(routeCfg); err != nil {
		return fmt.Errorf("failed to connect multi-hop route: %w", err)
	}

	return nil
}

// DisconnectFromKernel 断开与内核的连接
func (m *MultiKernelManager) DisconnectFromKernel(kernelID string) error {
	m.kernelsMu.Lock()
	defer m.kernelsMu.Unlock()

	kernelInfo, exists := m.kernels[kernelID]
	if !exists {
		return fmt.Errorf("not connected to kernel %s", kernelID)
	}

	kernelInfo.conn.Close()
	delete(m.kernels, kernelID)

	log.Printf("[OK] Disconnected from kernel %s", kernelID)
	return nil
}

// EnsureKernelConnected 确保与指定内核的连接已建立
// 如果内核已存在于 kernels map 中但没有有效的 Client/conn，则尝试建立连接
func (m *MultiKernelManager) EnsureKernelConnected(kernelID string) error {
	m.kernelsMu.Lock()
	defer m.kernelsMu.Unlock()

	kernelInfo, exists := m.kernels[kernelID]
	if !exists {
		return fmt.Errorf("kernel %s not found in known kernels", kernelID)
	}

	// 如果已经有有效的连接，直接返回
	if kernelInfo.Client != nil && kernelInfo.conn != nil {
		return nil
	}

	// 需要建立连接
	log.Printf("[WARN]️ Kernel %s has no active connection (Client=%v, conn=%v), attempting to connect...",
		kernelID, kernelInfo.Client != nil, kernelInfo.conn != nil)

	// 检查是否有必要的地址信息
	if kernelInfo.Address == "" {
		return fmt.Errorf("kernel %s has no address information", kernelID)
	}

	// 计算内核间通信端口 (mainPort + 2)
	kernelPort := kernelInfo.MainPort + 2
	if kernelInfo.Port > 0 {
		kernelPort = kernelInfo.Port
	}

	// 创建TLS配置
	cert, err := tls.LoadX509KeyPair(m.config.KernelCertPath, m.config.KernelKeyPath)
	if err != nil {
		return fmt.Errorf("failed to load certificates: %w", err)
	}

	// 创建证书池，包含自己的CA和可能的对等CA
	caCertPool := x509.NewCertPool()

	// 添加自己的CA证书
	ownCACert, err := os.ReadFile(m.config.CACertPath)
	if err != nil {
		return fmt.Errorf("failed to read own CA certificate: %w", err)
	}
	if !caCertPool.AppendCertsFromPEM(ownCACert) {
		return fmt.Errorf("failed to append own CA certificate")
	}

	// 检查是否已经有对等内核的CA证书，如果有也添加
	peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", kernelID)
	if peerCACert, err := os.ReadFile(peerCACertPath); err == nil {
		if !caCertPool.AppendCertsFromPEM(peerCACert) {
		}
	} else {
		log.Printf("No peer CA certificate for %s, will attempt insecure connection", kernelID)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
		ClientAuth:    tls.RequireAndVerifyClientCert,
		ClientCAs:    caCertPool,
	}

	creds := credentials.NewTLS(tlsConfig)

	// 连接到目标内核
	targetAddr := fmt.Sprintf("%s:%d", kernelInfo.Address, kernelPort)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(m.config.ConnectTimeout)*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, targetAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return fmt.Errorf("failed to connect to kernel %s at %s: %w", kernelID, targetAddr, err)
	}

	client := pb.NewKernelServiceClient(conn)

	// 更新 kernelInfo
	kernelInfo.Client = client
	kernelInfo.conn = conn
	kernelInfo.Status = "active"
	kernelInfo.LastHeartbeat = time.Now().Unix()

	log.Printf("[OK] Successfully connected to kernel %s at %s", kernelID, targetAddr)
	return nil
}

// ListKnownKernels 列出已知内核
func (m *MultiKernelManager) ListKnownKernels() []*KernelInfo {
	m.kernelsMu.RLock()
	defer m.kernelsMu.RUnlock()

	kernels := make([]*KernelInfo, 0, len(m.kernels))
	for _, kernel := range m.kernels {
		kernels = append(kernels, kernel)
	}

	return kernels
}

// GetConnectedKernelCount 获取已连接内核数量
func (m *MultiKernelManager) GetConnectedKernelCount() int {
	m.kernelsMu.RLock()
	defer m.kernelsMu.RUnlock()
	return len(m.kernels)
}

// GetKernelID 获取本内核ID
func (m *MultiKernelManager) GetKernelID() string {
	return m.config.KernelID
}

// ForwardAckToKernel 向指定内核发送 ForwardAckNotification RPC
func (m *MultiKernelManager) ForwardAckToKernel(targetKernelID string, req *pb.ForwardAckNotificationRequest) error {
	m.kernelsMu.RLock()
	kernelInfo, exists := m.kernels[targetKernelID]
	if !exists || kernelInfo == nil || kernelInfo.Client == nil {
		m.kernelsMu.RUnlock()
		// 尝试建立连接
		m.kernelsMu.RUnlock()
		m.kernelsMu.Lock()
		kernelInfo, exists = m.kernels[targetKernelID]
		if !exists {
			m.kernelsMu.Unlock()
			return fmt.Errorf("kernel %s not found in known kernels", targetKernelID)
		}
		// 尝试建立连接
		mainPort := kernelInfo.MainPort
		kernelPort := mainPort + 2
		m.kernelsMu.Unlock()

		if err := m.createKernelClient(targetKernelID, kernelInfo.Address, kernelPort); err != nil {
			return fmt.Errorf("failed to create client for kernel %s: %w", targetKernelID, err)
		}

		m.kernelsMu.RLock()
		kernelInfo, _ = m.kernels[targetKernelID]
		m.kernelsMu.RUnlock()
	}

	client := kernelInfo.Client
	m.kernelsMu.RUnlock()

	if client == nil {
		return fmt.Errorf("kernel %s client not available", targetKernelID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.ForwardAckNotification(ctx, req)
	if err != nil {
		return fmt.Errorf("ForwardAckNotification RPC failed: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("ForwardAckNotification returned failure: %s", resp.Message)
	}

	return nil
}

// AddRemotePermissionRequest 添加同步过来的权限请求
func (m *MultiKernelManager) AddRemotePermissionRequest(req *RemotePermissionRequest) {
	m.remotePermissionRequestsMu.Lock()
	defer m.remotePermissionRequestsMu.Unlock()

	// 检查是否已存在（根据 requestID）
	channelReqs := m.remotePermissionRequests[req.ChannelID]
	for _, existing := range channelReqs {
		if existing.RequestID == req.RequestID {
			// 已存在，更新状态
			existing.Status = req.Status
			return
		}
	}
	// 添加新请求
	m.remotePermissionRequests[req.ChannelID] = append(channelReqs, req)
}

// GetRemotePermissionRequests 获取指定频道的远程权限请求
func (m *MultiKernelManager) GetRemotePermissionRequests(channelID string) []*RemotePermissionRequest {
	m.remotePermissionRequestsMu.RLock()
	defer m.remotePermissionRequestsMu.RUnlock()
	return m.remotePermissionRequests[channelID]
}

// connectToKernelIdentityService 连接到内核的IdentityService（主服务器端口）
func (m *MultiKernelManager) connectToKernelIdentityService(kernel *KernelInfo) (*grpc.ClientConn, error) {
	// 创建TLS配置，同时包含自己的CA和对等内核的CA
	cert, err := tls.LoadX509KeyPair(m.config.KernelCertPath, m.config.KernelKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load client certificates: %w", err)
	}

	caCertPool := x509.NewCertPool()

	// 添加自己的CA证书
	ownCACert, err := os.ReadFile(m.config.CACertPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read own CA certificate: %w", err)
	}
	if !caCertPool.AppendCertsFromPEM(ownCACert) {
		return nil, fmt.Errorf("failed to append own CA certificate")
	}

	// 添加对等内核的CA证书（如果存在）
	peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", kernel.KernelID)
	peerCACertExists := false
	if peerCACert, err := os.ReadFile(peerCACertPath); err == nil {
		if !caCertPool.AppendCertsFromPEM(peerCACert) {
		} else {
			peerCACertExists = true
		}
	} else {
		log.Printf("Warning: peer CA certificate not found for kernel %s: %v", kernel.KernelID, err)
	}

	// 构建TLS配置
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
		ServerName:   "", // 使用IP地址，不验证服务器名称
		MinVersion:   tls.VersionTLS13,
	}

	// 如果没有对端CA证书，尝试不使用服务器证书验证的方式连接
	// 这是为了支持动态发现的内核之间的通信
	if !peerCACertExists {
		log.Printf("Attempting insecure connection to kernel %s (no peer CA available)", kernel.KernelID)
		// 使用 InsecureSkipVerify 允许连接到没有预共享CA的内核
		// 注意：这仍然要求客户端提供证书进行双向认证
		insecureCertPool := x509.NewCertPool()
		if !insecureCertPool.AppendCertsFromPEM(ownCACert) {
			return nil, fmt.Errorf("failed to append own CA certificate")
		}
		tlsConfig.RootCAs = insecureCertPool
		// 设置 ServerName 以匹配服务器证书的CN
		tlsConfig.ServerName = "trusted-data-space-kernel"
	}

	creds := credentials.NewTLS(tlsConfig)

	// 连接到目标内核的主服务器端口（而不是kernel_port）
	targetAddr := fmt.Sprintf("%s:%d", kernel.Address, kernel.MainPort) // kernel.MainPort是目标内核的主服务器端口
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(m.config.ConnectTimeout)*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, targetAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to kernel %s identity service: %w", kernel.KernelID, err)
	}

	return conn, nil
}

// createKernelClient 为已注册的内核创建持久客户端连接（用于 ForwardData 等调用）
func (m *MultiKernelManager) createKernelClient(kernelID, address string, port int) error {
	
	m.kernelsMu.Lock()
	defer m.kernelsMu.Unlock()

	// 检查是否已存在连接
	if existing, exists := m.kernels[kernelID]; exists && existing.Client != nil {
		return nil
	}

	
	// 创建TLS配置
	cert, err := tls.LoadX509KeyPair(m.config.KernelCertPath, m.config.KernelKeyPath)
	if err != nil {
		log.Printf("[ERROR] Failed to load client certificates for %s: %v", kernelID, err)
		return fmt.Errorf("failed to load client certificates: %w", err)
	}

	caCertPool := x509.NewCertPool()

	// 添加自己的CA证书
	ownCACert, err := os.ReadFile(m.config.CACertPath)
	if err != nil {
		log.Printf("[ERROR] Failed to read own CA certificate for %s: %v", kernelID, err)
		return fmt.Errorf("failed to read own CA certificate: %w", err)
	}
	if !caCertPool.AppendCertsFromPEM(ownCACert) {
		return fmt.Errorf("failed to append own CA certificate")
	}

	// 添加对等内核的CA证书
	peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", kernelID)
	if peerCert, err := os.ReadFile(peerCACertPath); err != nil {
		log.Printf("[WARN] Peer CA certificate not found for %s: %v (trying without it)", kernelID, err)
	} else {
		if !caCertPool.AppendCertsFromPEM(peerCert) {
			log.Printf("[WARN] Failed to append peer CA certificate for %s", kernelID)
		}
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
		ServerName:   "",
		MinVersion:   tls.VersionTLS13,
	}

	creds := credentials.NewTLS(tlsConfig)

	// 连接到目标内核的主服务器端口
	targetAddr := fmt.Sprintf("%s:%d", address, port)
	
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(m.config.ConnectTimeout)*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, targetAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		log.Printf("[ERROR] Failed to connect to kernel %s: %v", kernelID, err)
		return fmt.Errorf("failed to connect to kernel %s: %w", kernelID, err)
	}

	client := pb.NewKernelServiceClient(conn)

	// 更新内核信息
	if existing, exists := m.kernels[kernelID]; exists {
		existing.conn = conn
		existing.Client = client
		existing.LastHeartbeat = time.Now().Unix()
	} else {
		// 如果内核信息不存在，创建一个新的
		m.kernels[kernelID] = &KernelInfo{
			KernelID:      kernelID,
			Address:       address,
			Port:          port,
			MainPort:      port,
			Status:        "active",
			LastHeartbeat: time.Now().Unix(),
			conn:          conn,
			Client:        client,
		}
	}

	return nil
}

// CollectAllConnectors 收集所有连接内核的连接器信息（只收集公开的连接器）
func (m *MultiKernelManager) CollectAllConnectors() ([]*pb.ConnectorInfo, error) {
	var allConnectors []*pb.ConnectorInfo

	// 添加本地连接器（只添加公开的）
	localConnectors := m.registry.ListExposedConnectors()
	for _, conn := range localConnectors {
		allConnectors = append(allConnectors, &pb.ConnectorInfo{
			ConnectorId:   conn.ConnectorID,
			EntityType:    conn.EntityType,
			PublicKey:     conn.PublicKey,
			Status:        string(conn.Status),
			LastHeartbeat: conn.LastHeartbeat.Unix(),
			RegisteredAt:  conn.RegisteredAt.Unix(),
			KernelId:      m.config.KernelID, // 本地连接器标记为本内核
		})
	}

	// 从所有连接的内核收集连接器信息
	m.kernelsMu.RLock()
	kernels := make([]*KernelInfo, 0, len(m.kernels))
	for _, kernel := range m.kernels {
		kernels = append(kernels, kernel)
	}
	m.kernelsMu.RUnlock()

	for _, kernel := range kernels {

		// 为每个内核创建到其主服务器端口的新连接（用于访问IdentityService）
		identityConn, err := m.connectToKernelIdentityService(kernel)
		if err != nil {
			log.Printf("Failed to connect to identity service of kernel %s: %v", kernel.KernelID, err)
			continue
		}


		// 使用Identity服务发现连接器
		client := pb.NewIdentityServiceClient(identityConn)
		req := &pb.DiscoverRequest{
			RequesterId: m.config.KernelID,
		}

		resp, err := client.DiscoverConnectors(context.Background(), req)
		identityConn.Close() // 使用完后关闭连接

		if err != nil {
			log.Printf("Failed to discover connectors from kernel %s: %v", kernel.KernelID, err)
			continue
		}


		// 添加远程连接器信息
		for _, remoteConn := range resp.Connectors {
			// 标记为远程内核的连接器
			if remoteConn.KernelId == "" {
				remoteConn.KernelId = kernel.KernelID
			}
			allConnectors = append(allConnectors, remoteConn)
		}
	}

	return allConnectors, nil
}

// SyncConnectorInfo 同步连接器信息
func (m *MultiKernelManager) SyncConnectorInfo(targetKernelID string) error {
	connectors := m.registry.ListConnectors()

	req := &pb.SyncConnectorInfoRequest{
		SourceKernelId: m.config.KernelID,
		Connectors:     make([]*pb.ConnectorInfo, len(connectors)),
		SyncType:       "incremental",
	}

	for i, conn := range connectors {
		req.Connectors[i] = &pb.ConnectorInfo{
			ConnectorId:   conn.ConnectorID,
			EntityType:    conn.EntityType,
			PublicKey:     conn.PublicKey,
			Status:        string(conn.Status),
			LastHeartbeat: conn.LastHeartbeat.Unix(),
			RegisteredAt:  conn.RegisteredAt.Unix(),
			KernelId:      m.config.KernelID,
		}
	}

	if targetKernelID != "" {
		// 同步到指定内核
		m.kernelsMu.RLock()
		kernelInfo, exists := m.kernels[targetKernelID]
		m.kernelsMu.RUnlock()

		if !exists {
			return fmt.Errorf("not connected to kernel %s", targetKernelID)
		}

		_, err := kernelInfo.Client.SyncConnectorInfo(context.Background(), req)
		return err
	} else {
		// 广播到所有已连接的内核
		m.kernelsMu.RLock()
		kernels := make([]*KernelInfo, 0, len(m.kernels))
		for _, kernel := range m.kernels {
			kernels = append(kernels, kernel)
		}
		m.kernelsMu.RUnlock()

		for _, kernel := range kernels {
			go func(k *KernelInfo) {
				_, err := k.Client.SyncConnectorInfo(context.Background(), req)
				if err != nil {
					log.Printf("Failed to sync connectors with kernel %s: %v", k.KernelID, err)
				}
			}(kernel)
		}
	}

	return nil
}

// ForwardData 转发数据到其他内核
func (m *MultiKernelManager) ForwardData(targetKernelID string, dataPacket *pb.DataPacket, isFinal bool) error {
	m.kernelsMu.RLock()
	kernelInfo, exists := m.kernels[targetKernelID]
	m.kernelsMu.RUnlock()

	if !exists || kernelInfo == nil {
		return fmt.Errorf("not connected to kernel %s", targetKernelID)
	}

	// If client or conn is nil, attempt a best-effort reconnect to avoid panic.
	if kernelInfo.Client == nil || kernelInfo.conn == nil {
		log.Printf("[WARN] Kernel %s client/conn nil, attempting reconnect", targetKernelID)

		// Close and remove any stale entry before reconnecting.
		m.kernelsMu.Lock()
		if k, ok := m.kernels[targetKernelID]; ok {
			if k.conn != nil {
				_ = k.conn.Close()
			}
			delete(m.kernels, targetKernelID)
		}
		m.kernelsMu.Unlock()

		// Try to reconnect using the last-known address/port from the stale kernelInfo.
		if err := m.connectToKernelInternal(targetKernelID, kernelInfo.Address, kernelInfo.Port, false); err != nil {
			return fmt.Errorf("failed to reconnect to kernel %s: %w", targetKernelID, err)
		}

		// Re-fetch kernel info
		m.kernelsMu.RLock()
		kernelInfo, exists = m.kernels[targetKernelID]
		m.kernelsMu.RUnlock()
		if !exists || kernelInfo == nil || kernelInfo.Client == nil {
			return fmt.Errorf("kernel %s client not available after reconnect", targetKernelID)
		}
	}

	req := &pb.ForwardDataRequest{
		SourceKernelId: m.config.KernelID,
		TargetKernelId: targetKernelID,
		ChannelId:      dataPacket.ChannelId,
		DataPacket:     dataPacket,
		IsFinal:        isFinal,
	}

	
	_, err := kernelInfo.Client.ForwardData(context.Background(), req)
	if err != nil {
		log.Printf("[WARN] ForwardData RPC to %s failed: %v", targetKernelID, err)
	}
	return err
}

// kernelHeartbeat 内核心跳
func (m *MultiKernelManager) kernelHeartbeat(kernelID string) {
	ticker := time.NewTicker(time.Duration(m.config.HeartbeatInterval) * time.Second)
	defer ticker.Stop()

	for m.running {
		select {
		case <-ticker.C:
			m.sendHeartbeat(kernelID)
		}
	}
}

// sendHeartbeat 发送心跳
func (m *MultiKernelManager) sendHeartbeat(kernelID string) {
	m.kernelsMu.RLock()
	kernelInfo, exists := m.kernels[kernelID]
	m.kernelsMu.RUnlock()

	if !exists {
		return
	}

	req := &pb.KernelHeartbeatRequest{
		KernelId: m.config.KernelID,
		Timestamp: time.Now().Unix(),
		Stats: map[string]int32{
			"connectors": int32(len(m.registry.ListConnectors())),
			"channels":   int32(len(m.channelManager.ListChannels())),
		},
	}

	resp, err := kernelInfo.Client.KernelHeartbeat(context.Background(), req)
	if err != nil {
		log.Printf("Heartbeat failed for kernel %s: %v", kernelID, err)
		// TODO: 处理连接失败，可能需要重连
		return
	}

	// 更新最后心跳时间
	m.kernelsMu.Lock()
	if kernelInfo, exists := m.kernels[kernelID]; exists {
		kernelInfo.LastHeartbeat = time.Now().Unix()
	}
	m.kernelsMu.Unlock()

	// 处理其他内核的状态更新
	for _, update := range resp.Updates {
		log.Printf("Kernel %s status update: %s -> %s",
			update.KernelId, update.KernelId, update.Status)
	}
}

// SyncKnownKernelsToKernel 向指定内核发送本内核已知的内核列表
// 这用于当本内核了解到新内核后，主动让对方了解本内核已知的其他内核
func (m *MultiKernelManager) SyncKnownKernelsToKernel(kernelID string, address string, port int) {
	log.Printf("[INFO] Syncing known kernels to %s at %s:%d", kernelID, address, port)

	// 构建已知内核列表
	m.kernelsMu.RLock()
	knownKernels := make([]*pb.KernelInfo, 0)
	// 首先添加自己（让对方知道自己）
	knownKernels = append(knownKernels, &pb.KernelInfo{
		KernelId:      m.config.KernelID,
		Address:       m.config.Address,
		Port:          int32(m.config.Port),
		Status:        "active",
		LastHeartbeat: time.Now().Unix(),
		PublicKey:     "",
	})
	// 然后添加本内核已知的其他内核
	for _, k := range m.kernels {
		if k.KernelID == kernelID {
			continue // 跳过目标内核自己
		}
		knownKernels = append(knownKernels, &pb.KernelInfo{
			KernelId:      k.KernelID,
			Address:       k.Address,
			Port:          int32(k.MainPort),
			Status:        k.Status,
			LastHeartbeat: k.LastHeartbeat,
			PublicKey:     k.PublicKey,
		})
	}
	m.kernelsMu.RUnlock()

	if len(knownKernels) == 0 {
		log.Printf("No known kernels to sync to %s", kernelID)
		return
	}

	// 创建到目标内核的临时连接
	cert, err := tls.LoadX509KeyPair(m.config.KernelCertPath, m.config.KernelKeyPath)
	if err != nil {
		return
	}

	caCertPool := x509.NewCertPool()
	ownCACert, err := os.ReadFile(m.config.CACertPath)
	if err != nil {
		return
	}
	if !caCertPool.AppendCertsFromPEM(ownCACert) {
		return
	}

	// 尝试读取对端 CA
	peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", kernelID)
	if peerCACert, err := os.ReadFile(peerCACertPath); err == nil {
		caCertPool.AppendCertsFromPEM(peerCACert)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
		MinVersion:   tls.VersionTLS13,
	}
	creds := credentials.NewTLS(tlsConfig)

	targetAddr := fmt.Sprintf("%s:%d", address, port)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(m.config.ConnectTimeout)*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, targetAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		log.Printf("[WARN] Failed to connect to %s for sync: %v", kernelID, err)
		return
	}
	defer conn.Close()

	client := pb.NewKernelServiceClient(conn)

	req := &pb.SyncKnownKernelsRequest{
		SourceKernelId: m.config.KernelID,
		KnownKernels:   knownKernels,
		SyncType:       "full",
	}

	resp, err := client.SyncKnownKernels(ctx, req)
	if err != nil {
		log.Printf("[WARN] Failed to sync to %s: %v", kernelID, err)
		return
	}

	// 如果对方返回了新内核，尝试连接到它们
	for _, newKernel := range resp.NewlyKnownKernels {
		// 跳过自己
		if newKernel.KernelId == m.config.KernelID {
			continue
		}

		m.kernelsMu.Lock()
		existing, alreadyKnown := m.kernels[newKernel.KernelId]
		m.kernelsMu.Unlock()

		if alreadyKnown {
			if existing.conn == nil || existing.Client == nil {
				mainPort := int(newKernel.Port)
				kernelPort := mainPort + 2
				_ = m.connectToKernelInternal(newKernel.KernelId, newKernel.Address, kernelPort, false)
			}
			continue
		}

		mainPort := int(newKernel.Port)
		kernelPort := mainPort + 2
		if err := m.connectToKernelInternal(newKernel.KernelId, newKernel.Address, kernelPort, false); err != nil {
			if !strings.Contains(err.Error(), "already connected") {
				log.Printf("[WARN] Failed to connect to kernel %s: %v", newKernel.KernelId, err)
			}
		}
	}
}

// SyncPeerKernels 向指定内核同步本内核已知的内核信息
// 这用于当本内核通过其他内核了解到新内核后，反向让对方了解本内核已知的其他内核
// 例如：A 通知 B 有关 C 的信息 -> B 连接到 C -> B 通知 A 有关 D 的信息
func (m *MultiKernelManager) SyncPeerKernels(targetKernelID string) {
	// 首先尝试获取读锁来检查内核是否存在
	m.kernelsMu.RLock()
	kernelInfo, exists := m.kernels[targetKernelID]
	if !exists || kernelInfo == nil || kernelInfo.Client == nil {
		m.kernelsMu.RUnlock()
		// 内核不存在或没有有效连接，尝试建立连接
		m.kernelsMu.Lock()
		kernelInfo, exists = m.kernels[targetKernelID]
		if !exists {
			m.kernelsMu.Unlock()
			return
		}
		// 尝试建立连接
		mainPort := kernelInfo.MainPort
		kernelPort := mainPort + 2
		m.kernelsMu.Unlock()

		if err := m.connectToKernelInternal(targetKernelID, kernelInfo.Address, kernelPort, false); err != nil {
			if !strings.Contains(err.Error(), "already connected") {
				log.Printf("[WARN] Failed to connect to %s for sync: %v", targetKernelID, err)
			}
		}
		// 连接后重新获取信息
		m.kernelsMu.RLock()
		kernelInfo, exists = m.kernels[targetKernelID]
		if !exists || kernelInfo == nil || kernelInfo.Client == nil {
			m.kernelsMu.RUnlock()
			return
		}
	}

	// 收集本内核已知的内核中，对方可能不知道的
	kernelsToSync := make([]*pb.KernelInfo, 0)
	for _, k := range m.kernels {
		if k.KernelID == targetKernelID {
			continue
		}
		// 跳过那些已经有连接的内核（对方应该已经知道了）
		if k.conn != nil && k.Client != nil {
			continue
		}
		kernelsToSync = append(kernelsToSync, &pb.KernelInfo{
			KernelId:      k.KernelID,
			Address:       k.Address,
			Port:          int32(k.MainPort),
			Status:        k.Status,
			LastHeartbeat: k.LastHeartbeat,
			PublicKey:     k.PublicKey,
		})
	}
	m.kernelsMu.RUnlock()

	if len(kernelsToSync) == 0 {
		return
	}

	log.Printf("Syncing %d peer kernels to %s", len(kernelsToSync), targetKernelID)

	// 发送同步请求
	req := &pb.SyncKnownKernelsRequest{
		SourceKernelId: m.config.KernelID,
		KnownKernels:   kernelsToSync,
		SyncType:       "incremental",
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(m.config.ConnectTimeout)*time.Second)
	defer cancel()

	resp, err := kernelInfo.Client.SyncKnownKernels(ctx, req)
	if err != nil {
		log.Printf("[WARN] Failed to sync peer kernels to %s: %v", targetKernelID, err)
		return
	}

	// 如果对方返回了新内核，尝试连接到它们
	for _, newKernel := range resp.NewlyKnownKernels {
		m.kernelsMu.Lock()
		existing, alreadyKnown := m.kernels[newKernel.KernelId]
		m.kernelsMu.Unlock()

		if alreadyKnown {
			if existing.conn == nil || existing.Client == nil {
				mainPort := int(newKernel.Port)
				kernelPort := mainPort + 2
				_ = m.connectToKernelInternal(newKernel.KernelId, newKernel.Address, kernelPort, false)
			}
			continue
		}

		mainPort := int(newKernel.Port)
		kernelPort := mainPort + 2
		if err := m.connectToKernelInternal(newKernel.KernelId, newKernel.Address, kernelPort, false); err != nil {
			if !strings.Contains(err.Error(), "already connected") {
				log.Printf("[WARN] Failed to connect to kernel %s: %v", newKernel.KernelId, err)
			}
		}
	}
}

// ========== 多跳链路自动建立机制 ==========

// OnMultiHopApprovedHandler 初始化多跳审批回调（由 main.go 在启动时调用）
// 该回调会在 kernel_service.go 的 NotifyMultiHopApproved 中被调用
func (m *MultiKernelManager) InitMultiHopApprovedCallback() {
	m.OnMultiHopApproved = func(notification *pb.NotifyMultiHopApprovedRequest) {
		// 找到对应的待审批 hop 并触发重连
		m.pendingHopsMu.Lock()
		hopInfo, exists := m.pendingHops[notification.RequestId]
		if !exists {
			log.Printf("[WARN] No pending hop found for request_id=%s", notification.RequestId)
			m.pendingHopsMu.Unlock()
			return
		}
		// 标记为已批准
		hopInfo.Approved = true
		hopInfo.ApprovedBy = notification.ApproverKernelId
		requestID := notification.RequestId
		m.pendingHopsMu.Unlock()

		// 立即尝试重连该 hop（不阻塞 RPC 响应）
		go func() {
			// 再次尝试连接，此时目标已批准，应该能成功
			if err := m.connectToKernelInternal(hopInfo.ToKernelID, hopInfo.ToAddress, hopInfo.ToPort, false); err != nil {
				if strings.Contains(err.Error(), "already connected") {
				} else {
					log.Printf("[WARN] Retry failed for %s: %v", hopInfo.ToKernelID, err)
					// 清理该 hop 的待审批记录
					m.pendingHopsMu.Lock()
					delete(m.pendingHops, requestID)
					m.pendingHopsMu.Unlock()
					return
				}
			} else {
			}

			// 清理该 hop 的待审批记录
			m.pendingHopsMu.Lock()
			delete(m.pendingHops, requestID)
			m.pendingHopsMu.Unlock()

		// 检查该路由的所有 hop 是否都已建立完成
		m.checkMultiHopSessionComplete(hopInfo.RouteName)
		}()
	}
}

// InitHopEstablishedCallback 初始化 Hop 建立完成回调
// 当收到 HopEstablished RPC 时，触发自动建立到目标内核的连接
func (m *MultiKernelManager) InitHopEstablishedCallback() {
	m.OnHopEstablished = func(req *pb.HopEstablishedRequest) {
		log.Printf("[INFO] HopEstablished callback triggered: source=%s, target=%s, hop=%s",
			req.SourceKernelId, req.TargetKernelId, req.HopId)

		// 触发重连到目标内核
		go func() {
			// 尝试连接到目标内核
			if err := m.connectToKernelInternal(req.TargetKernelId, req.TargetAddress, int(req.TargetPort), false); err != nil {
				if strings.Contains(err.Error(), "already connected") {
					log.Printf("[INFO] Already connected to %s via hop %s", req.TargetKernelId, req.HopId)
				} else {
					log.Printf("[WARN] Failed to connect to %s via hop %s: %v", req.TargetKernelId, req.HopId, err)
				}
				return
			}
			log.Printf("[OK] Successfully connected to %s via hop %s", req.TargetKernelId, req.HopId)
		}()
	}
}

// checkMultiHopSessionComplete 检查多跳会话是否全部完成
func (m *MultiKernelManager) checkMultiHopSessionComplete(routeName string) {
	m.multiHopSessionsMu.Lock()
	defer m.multiHopSessionsMu.Unlock()

	session, exists := m.multiHopSessions[routeName]
	if !exists {
		return
	}

	session.Mu.Lock()
	defer session.Mu.Unlock()

	// 检查所有 hop 是否都已连接
	allDone := true
	for _, hop := range session.Hops {
		m.kernelsMu.RLock()
		kernelInfo, connected := m.kernels[hop.ToKernelID]
		connected = connected && kernelInfo != nil && kernelInfo.conn != nil && kernelInfo.Client != nil
		m.kernelsMu.RUnlock()
		if !connected {
			allDone = false
			break
		}
	}

	if allDone {
		session.Done = true
		close(session.AllDone)
		// 清理会话
		delete(m.multiHopSessions, routeName)
	}
}

// RegisterPendingHop 注册一个待审批的 hop
func (m *MultiKernelManager) RegisterPendingHop(session *MultiHopSession, hopIndex int, hopInfo *PendingHopInfo) {
	m.pendingHopsMu.Lock()
	m.pendingHops[hopInfo.RequestID] = hopInfo
	m.pendingHopsMu.Unlock()

	session.Mu.Lock()
	session.Hops[hopIndex] = hopInfo
	session.Mu.Unlock()
}

// Shutdown 关闭多内核管理器
func (m *MultiKernelManager) Shutdown() {
	m.running = false

	m.kernelsMu.Lock()
	defer m.kernelsMu.Unlock()

	for kernelID, kernelInfo := range m.kernels {
		kernelInfo.conn.Close()
		log.Printf("Closed connection to kernel %s", kernelID)
	}

	m.kernels = make(map[string]*KernelInfo)
}

// ConnectMultiHopRoute 根据多跳链路配置建立连接
// 该方法会按照配置中的每一跳依次建立连接
// 如果遇到待审批的 hop，会自动注册并等待审批通知，无需手动重试
func (m *MultiKernelManager) ConnectMultiHopRoute(config *MultiHopConfigFile) error {
	if config == nil {
		return fmt.Errorf("config cannot be nil")
	}

	if len(config.Hops) == 0 {
		return fmt.Errorf("no hops in configuration")
	}

	log.Printf("=== Establishing multi-hop route: %s ===", config.RouteName)
	log.Printf("Route: %s", config.Description)

	// 创建多跳会话，用于追踪所有待审批的 hop
	session := &MultiHopSession{
		RouteName:   config.RouteName,
		RouteConfig: config,
		Hops:        make(map[int]*PendingHopInfo),
		AllDone:     make(chan struct{}),
		Done:        false,
	}

	// 注册会话
	m.multiHopSessionsMu.Lock()
	m.multiHopSessions[config.RouteName] = session
	m.multiHopSessionsMu.Unlock()

	// 用于统计有多少个待审批的 hop
	pendingHopCount := 0

	// 依次建立每一跳的连接
	for i, hop := range config.Hops {
		hopNum := i + 1
		log.Printf("--- Hop %d: %s -> %s (%s:%d) ---",
			hopNum, hop.FromKernel, hop.ToKernel, hop.ToAddress, hop.ToPort)

		// 检查是否需要连接（仅当未连接时）
		m.kernelsMu.RLock()
		existingKernel, alreadyConnected := m.kernels[hop.ToKernel]
		m.kernelsMu.RUnlock()

		if alreadyConnected && existingKernel.conn != nil && existingKernel.Client != nil {
			continue
		}

		// 尝试连接到目标内核
		// 注意：这里使用 interconnectRequest=true，因为需要对方审批才能建立连接
		if err := m.connectToKernelInternal(hop.ToKernel, hop.ToAddress, hop.ToPort, true); err != nil {
			// 检查是否是"已连接"错误
			if strings.Contains(err.Error(), "already connected") {
				continue
			}

			// 检查是否是待审批错误
			if strings.HasPrefix(err.Error(), "interconnect_pending:") {
				requestID := strings.TrimPrefix(err.Error(), "interconnect_pending:")

				// 注册待审批的 hop 到会话中，以便收到批准通知后自动重连
				hopInfo := &PendingHopInfo{
					RouteName:  config.RouteName,
					HopIndex:   hopNum,
					HopTotal:   len(config.Hops),
					ToKernelID: hop.ToKernel,
					ToAddress:  hop.ToAddress,
					ToPort:     hop.ToPort,
					RequestID:  requestID,
					Approved:   false,
				}
				m.RegisterPendingHop(session, hopNum, hopInfo)
				pendingHopCount++
				continue
			}

			log.Printf("  [ERROR] Failed to connect to %s: %v", hop.ToKernel, err)
			return fmt.Errorf("hop %d: failed to connect to %s: %w", hopNum, hop.ToKernel, err)
		}

	}

	// 如果有待审批请求，不再返回错误，而是启动后台等待
	if pendingHopCount > 0 {
		log.Printf("   Please run 'approve-request <request_id>' on each target kernel.")
		// 启动后台 goroutine 等待所有审批
		go m.waitForMultiHopApprovals(session, pendingHopCount)
		// 立即返回，让用户可以继续其他操作
		return nil
	}

	log.Printf("=== Multi-hop route %s established ===", config.RouteName)
	return nil
}

// waitForMultiHopApprovals 等待所有待审批的 hop 完成批准
// 当所有 hop 都建立完成后，记录日志并清理会话
func (m *MultiKernelManager) waitForMultiHopApprovals(session *MultiHopSession, expectedCount int) {
	// 等待会话完成（所有 hop 都建立）或超时
	timeout := time.After(120 * time.Second) // 2 分钟超时

	select {
	case <-session.AllDone:
	case <-timeout:
		// 清理会话
		m.multiHopSessionsMu.Lock()
		delete(m.multiHopSessions, session.RouteName)
		m.multiHopSessionsMu.Unlock()
	}
}

// ConnectAllEnabledRoutes 连接所有已启用的多跳路由
func (m *MultiKernelManager) ConnectAllEnabledRoutes(configManager *MultiHopConfigManager) error {
	if configManager == nil {
		return fmt.Errorf("config manager cannot be nil")
	}

	enabledConfigs := configManager.GetEnabledConfigs()

	if len(enabledConfigs) == 0 {
		log.Printf("No enabled multi-hop routes to connect")
		return nil
	}

	log.Printf("=== Connecting %d enabled multi-hop routes ===", len(enabledConfigs))

	successCount := 0
	failedCount := 0

	for _, config := range enabledConfigs {

		if err := m.ConnectMultiHopRoute(config); err != nil {
			failedCount++
			continue
		}

		successCount++
	}

	log.Printf("=== Multi-hop route connection summary: %d succeeded, %d failed ===", successCount, failedCount)

	if failedCount > 0 {
		return fmt.Errorf("%d routes failed to establish", failedCount)
	}

	return nil
}

// GetMultiHopRouteInfo 获取多跳路由信息
func (m *MultiKernelManager) GetMultiHopRouteInfo(config *MultiHopConfigFile) string {
	if config == nil {
		return "nil config"
	}

	info := fmt.Sprintf("Route: %s (%s)\n", config.RouteName, config.Name)
	info += fmt.Sprintf("Description: %s\n", config.Description)
	info += fmt.Sprintf("Hops: %d\n", len(config.Hops))
	info += "Path:\n"

	for i, hop := range config.Hops {
		// 检查连接状态
		m.kernelsMu.RLock()
		kernelInfo, connected := m.kernels[hop.ToKernel]
		m.kernelsMu.RUnlock()

		status := "[ERROR] Not connected"
		if connected && kernelInfo != nil && kernelInfo.conn != nil && kernelInfo.Client != nil {
			status = "[OK] Connected"
		}

		info += fmt.Sprintf("  %d. %s -> %s [%s:%d] [%s]\n",
			i+1, hop.FromKernel, hop.ToKernel, hop.ToAddress, hop.ToPort, status)
	}

	return info
}

// ValidateMultiHopConfig 验证多跳配置是否适用于当前内核
func (m *MultiKernelManager) ValidateMultiHopConfig(config *MultiHopConfigFile) error {
	if config == nil {
		return fmt.Errorf("config cannot be nil")
	}

	if len(config.Hops) == 0 {
		return fmt.Errorf("no hops in configuration")
	}

	// 验证第一跳的源是否是当前内核
	firstHop := config.Hops[0]
	if firstHop.FromKernel != m.config.KernelID {
		return fmt.Errorf("first hop from_kernel (%s) does not match current kernel (%s)",
			firstHop.FromKernel, m.config.KernelID)
	}

	// 验证每一跳的连续性
	for i := 1; i < len(config.Hops); i++ {
		prevHop := config.Hops[i-1]
		currHop := config.Hops[i]

		if prevHop.ToKernel != currHop.FromKernel {
			return fmt.Errorf("hop %d: from_kernel (%s) does not match previous hop's to_kernel (%s)",
				i+1, currHop.FromKernel, prevHop.ToKernel)
		}
	}

	return nil
}
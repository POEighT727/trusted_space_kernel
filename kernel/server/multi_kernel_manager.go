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
)

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

	kernels   map[string]*KernelInfo
	kernelsMu sync.RWMutex
	pendingRequests   map[string]*PendingInterconnectRequest
	pendingRequestsMu sync.RWMutex

	running bool
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
		running:        true,
	}

	return manager, nil
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

	// 在批准前先检查是否已经知晓该内核（避免重复通知）
	m.kernelsMu.RLock()
	_, alreadyKnown := m.kernels[req.RequesterKernelID]
	m.kernelsMu.RUnlock()
	if alreadyKnown {
		log.Printf("Requester %s already known locally, skipping approve notification", req.RequesterKernelID)
		// 确保已有持久连接；如果没有则尝试建立
		if err := m.connectToKernelInternal(req.RequesterKernelID, req.Address, req.KernelPort, false); err != nil {
			return fmt.Errorf("failed to ensure connection to requester kernel %s: %w", req.RequesterKernelID, err)
		}
		return nil
	}

	// 先通知请求方“已批准”，调用对方的 RegisterKernel 并带上 interconnect_approve 标记，
	// 让请求方在自己的服务端直接把批准方加入已知内核列表（避免再次创建 pending）。
	if err := m.notifyRequesterApprove(req); err != nil {
		return fmt.Errorf("failed to notify requester %s of approve: %w", req.RequesterKernelID, err)
	}

	// 然后由本端建立到请求方的持久连接（不再作为 interconnect_request）
	if err := m.connectToKernelInternal(req.RequesterKernelID, req.Address, req.KernelPort, false); err != nil {
		return fmt.Errorf("failed to connect to requester kernel %s: %w", req.RequesterKernelID, err)
	}

	return nil
}

// notifyRequesterApprove 向请求方发送批准通知（使用 RegisterKernel + metadata interconnect_approve）
func (m *MultiKernelManager) notifyRequesterApprove(req *PendingInterconnectRequest) error {
	// 建立到请求方的临时 TLS 连接（用于发送 approve 通知）
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

	// 如果我们已经有对端 CA，加入以便验证对端证书
	peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", req.RequesterKernelID)
	if peerCACert, err := os.ReadFile(peerCACertPath); err == nil {
		_ = caCertPool.AppendCertsFromPEM(peerCACert)
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

	// 读取自己的 CA 证书 以便对方保存
	ownCACertData, _ := os.ReadFile(m.config.CACertPath)

	approveReq := &pb.RegisterKernelRequest{
		KernelId:      m.config.KernelID,
		Address:       m.config.Address,
		Port:          int32(m.config.Port),
		PublicKey:     "",
		CaCertificate: ownCACertData,
		Metadata:      map[string]string{"interconnect_approve": "true"},
		Timestamp:     time.Now().Unix(),
	}

	_, err = client.RegisterKernel(context.Background(), approveReq)
	if err != nil {
		return fmt.Errorf("approve RPC failed: %w", err)
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
				log.Printf("Warning: failed to append peer CA certificate for %s", kernelID)
			} else {
				log.Printf("Loaded peer CA certificate for %s", kernelID)
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

	kernelService := NewKernelServiceServer(m, m.channelManager, m.registry)
	pb.RegisterKernelServiceServer(server, kernelService)

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to listen on kernel port: %w", err)
	}

	log.Printf("🔗 Kernel-to-kernel server started on %s", address)

	if err := server.Serve(listener); err != nil {
		return fmt.Errorf("failed to serve kernel server: %w", err)
	}

	return nil
}

// connectToKernelInternal 执行连接并注册，interconnectRequest 表示是否把本次注册作为“互联请求”发送给目标（带 metadata）
func (m *MultiKernelManager) connectToKernelInternal(kernelID, address string, port int, interconnectRequest bool) error {
	m.kernelsMu.Lock()
	defer m.kernelsMu.Unlock()

	// 检查是否已经连接
	if _, exists := m.kernels[kernelID]; exists {
		return fmt.Errorf("already connected to kernel %s", kernelID)
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
				log.Printf("Warning: failed to append peer CA certificate for %s", kernelID)
			} else {
				// suppressed detailed peer CA log
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
	}
	if interconnectRequest {
		registerReq.Metadata = map[string]string{"interconnect_request": "true"}
	}

	resp, err := client.RegisterKernel(context.Background(), registerReq)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to register with kernel %s: %w", kernelID, err)
	}

	// 如果目标返回了一个 interconnect_request_id，说明它把请求作为待审批处理，先不算最终连接
	if resp.Message != "" && strings.Contains(resp.Message, "interconnect_request_id:") {
		// 保存对方的CA证书（如果提供）
		if len(resp.PeerCaCertificate) > 0 {
			peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", kernelID)
			if err := os.WriteFile(peerCACertPath, resp.PeerCaCertificate, 0644); err != nil {
				log.Printf("Warning: failed to save peer CA certificate for %s: %v", kernelID, err)
			} else {
				// suppressed detailed CA saved log
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

		log.Printf("Interconnect request pending for kernel %s: %s", kernelID, resp.Message)
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
		} else {
			conn.Close()
			return fmt.Errorf("registration rejected by kernel %s: %s", kernelID, resp.Message)
		}
	}

	// 保存对方的CA证书
		if len(resp.PeerCaCertificate) > 0 {
			peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", kernelID)
			if err := os.WriteFile(peerCACertPath, resp.PeerCaCertificate, 0644); err != nil {
				log.Printf("Warning: failed to save peer CA certificate for %s: %v", kernelID, err)
			} else {
				// suppressed detailed CA saved log
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

	log.Printf("✅ Connected to kernel %s at %s:%d", kernelID, address, port)
	return nil
}

 
// ConnectToKernel 连接到另一个内核
// port 参数应该是目标内核的内核通信端口 (kernel_port)
func (m *MultiKernelManager) ConnectToKernel(kernelID, address string, port int) error {
	m.kernelsMu.Lock()
	defer m.kernelsMu.Unlock()

	// 检查是否已经连接
	if _, exists := m.kernels[kernelID]; exists {
		return fmt.Errorf("already connected to kernel %s", kernelID)
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
			log.Printf("Warning: failed to append peer CA certificate for %s", kernelID)
		} else {
			log.Printf("Using existing peer CA certificate for %s", kernelID)
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
		Metadata:     map[string]string{"interconnect_request": "true"},
		Timestamp:    time.Now().Unix(),
	}

	resp, err := client.RegisterKernel(context.Background(), registerReq)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to register with kernel %s: %w", kernelID, err)
	}

	// 如果目标返回了一个 interconnect_request_id，说明它把请求作为待审批处理，先不算最终连接
	if resp.Message != "" && strings.Contains(resp.Message, "interconnect_request_id:") {
		// 保存对方的CA证书（如果提供）
		if len(resp.PeerCaCertificate) > 0 {
			peerCACertPath := fmt.Sprintf("certs/peer-%s-ca.crt", kernelID)
			if err := os.WriteFile(peerCACertPath, resp.PeerCaCertificate, 0644); err != nil {
				log.Printf("Warning: failed to save peer CA certificate for %s: %v", kernelID, err)
			} else {
				log.Printf("Saved peer CA certificate for kernel %s (pending approval)", kernelID)
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

		log.Printf("Interconnect request pending for kernel %s: %s", kernelID, resp.Message)
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
			log.Printf("Warning: failed to save peer CA certificate for %s: %v", kernelID, err)
		} else {
			log.Printf("Saved peer CA certificate for kernel %s", kernelID)
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

	log.Printf("✅ Connected to kernel %s at %s:%d", kernelID, address, port)
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

	log.Printf("✅ Disconnected from kernel %s", kernelID)
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
	if peerCACert, err := os.ReadFile(peerCACertPath); err == nil {
		if !caCertPool.AppendCertsFromPEM(peerCACert) {
			log.Printf("Warning: failed to append peer CA certificate for %s", kernel.KernelID)
		} else {
			log.Printf("Using peer CA certificate for kernel %s", kernel.KernelID)
		}
	} else {
		log.Printf("Warning: peer CA certificate not found for kernel %s: %v", kernel.KernelID, err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
		ServerName:   "", // 使用IP地址，不验证服务器名称
		MinVersion:   tls.VersionTLS13,
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

// CollectAllConnectors 收集所有连接内核的连接器信息
func (m *MultiKernelManager) CollectAllConnectors() ([]*pb.ConnectorInfo, error) {
	var allConnectors []*pb.ConnectorInfo

	// 添加本地连接器
	localConnectors := m.registry.ListConnectors()
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
		log.Printf("Attempting to collect connectors from kernel %s at %s:%d", kernel.KernelID, kernel.Address, kernel.MainPort)

		// 为每个内核创建到其主服务器端口的新连接（用于访问IdentityService）
		identityConn, err := m.connectToKernelIdentityService(kernel)
		if err != nil {
			log.Printf("Failed to connect to identity service of kernel %s: %v", kernel.KernelID, err)
			continue
		}

		log.Printf("Successfully connected to identity service of kernel %s", kernel.KernelID)

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

		log.Printf("Successfully discovered %d connectors from kernel %s", len(resp.Connectors), kernel.KernelID)

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
func (m *MultiKernelManager) ForwardData(targetKernelID string, dataPacket *pb.DataPacket) error {
	m.kernelsMu.RLock()
	kernelInfo, exists := m.kernels[targetKernelID]
	m.kernelsMu.RUnlock()

	if !exists {
		return fmt.Errorf("not connected to kernel %s", targetKernelID)
	}

	req := &pb.ForwardDataRequest{
		SourceKernelId: m.config.KernelID,
		TargetKernelId: targetKernelID,
		ChannelId:      dataPacket.ChannelId,
		DataPacket:     dataPacket,
	}

	_, err := kernelInfo.Client.ForwardData(context.Background(), req)
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

package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "github.com/trusted-space/kernel/proto/kernel/v1"
	"github.com/trusted-space/kernel/kernel/circulation"
	"github.com/trusted-space/kernel/kernel/control"
	"github.com/trusted-space/kernel/kernel/security"
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
		running:        true,
	}

	return manager, nil
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

// ConnectToKernel 连接到另一个内核
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
		Timestamp:    time.Now().Unix(),
	}

	resp, err := client.RegisterKernel(context.Background(), registerReq)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to register with kernel %s: %w", kernelID, err)
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
	kernelInfo := &KernelInfo{
		KernelID:      kernelID,
		Address:       address,
		Port:          port,          // 内核间通信端口
		MainPort:      m.config.Port, // 主服务器端口
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
	// 使用security包的客户端TLS配置来确保配置正确
	creds, err := security.NewClientTransportCredentials(
		m.config.CACertPath,
		m.config.KernelCertPath,
		m.config.KernelKeyPath,
		"", // serverName 为空，因为我们使用IP地址
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create client credentials: %w", err)
	}

	// 连接到目标内核的主服务器端口（而不是kernel_port）
	targetAddr := fmt.Sprintf("%s:%d", kernel.Address, kernel.MainPort) // kernel.MainPort是主服务器端口
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

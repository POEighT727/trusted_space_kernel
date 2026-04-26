package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"gopkg.in/yaml.v3"

	pb "github.com/trusted-space/kernel/proto/kernel/v1"
	"github.com/trusted-space/kernel/kernel/circulation"
	"github.com/trusted-space/kernel/kernel/control"
	"github.com/trusted-space/kernel/kernel/database"
	"github.com/trusted-space/kernel/kernel/evidence"
	"github.com/trusted-space/kernel/kernel/operator_peer"
	"github.com/trusted-space/kernel/kernel/security"
	"github.com/trusted-space/kernel/kernel/server"
	"github.com/trusted-space/kernel/kernel/tempchat"
)

// SeedKernelConfig 种子内核配置
type SeedKernelConfig struct {
	KernelID string `yaml:"kernel_id"`
	Address  string `yaml:"address"`
	Port     int    `yaml:"port"`
}

// Config 内核配置
type Config struct {
	Kernel struct {
		ID          string `yaml:"id"`
		Type        string `yaml:"type"`
		Description string `yaml:"description"`
	} `yaml:"kernel"`

	Server struct {
		Address string `yaml:"address"`
		Port    int    `yaml:"port"`
	} `yaml:"server"`

	MultiKernel struct {
		SeedKernels       []SeedKernelConfig `yaml:"seed_kernels"`
		KernelPort        int  `yaml:"kernel_port"`
		HeartbeatInterval int  `yaml:"heartbeat_interval"`
		ConnectTimeout    int  `yaml:"connect_timeout"`
		MaxRetries        int  `yaml:"max_retries"`
	} `yaml:"multi_kernel"`

	Security struct {
		RootCACertPath  string `yaml:"root_ca_cert_path"` // 根CA证书路径
		CACertPath      string `yaml:"ca_cert_path"`     // 中间CA证书路径
		CAKeyPath       string `yaml:"ca_key_path"`     // CA私钥路径
		ServerCertPath string `yaml:"server_cert_path"`
		ServerKeyPath  string `yaml:"server_key_path"`
		// 内核间通信证书
		KernelCertPath string `yaml:"kernel_cert_path"`
		KernelKeyPath  string `yaml:"kernel_key_path"`
	} `yaml:"security"`

	Evidence struct {
		Persistent  bool   `yaml:"persistent"`
		LogFilePath string `yaml:"log_file_path"`
	} `yaml:"evidence"`

	Database struct {
		Enabled  bool   `yaml:"enabled"`
		Host     string `yaml:"host"`
		Port     int    `yaml:"port"`
		User     string `yaml:"user"`
		Password string `yaml:"password"`
		Database string `yaml:"database"`
	} `yaml:"database"`

	Channel struct {
		Evidence struct {
			DefaultMode          string `yaml:"default_mode"`
			DefaultStrategy      string `yaml:"default_strategy"`
			DefaultConnectorID   string `yaml:"default_connector_id"`
			DefaultBackupEnabled bool   `yaml:"default_backup_enabled"`
			DefaultRetentionDays int    `yaml:"default_retention_days"`
			DefaultCompressData  bool   `yaml:"default_compress_data"`
		} `yaml:"evidence"`
	} `yaml:"channel"`

	TempChat struct {
		Enabled           bool `yaml:"enabled"`
		Port              int  `yaml:"port"`
		SessionTimeout    int  `yaml:"session_timeout"`
		HeartbeatInterval int  `yaml:"heartbeat_interval"`
		SyncInterval      int  `yaml:"sync_interval"`
	} `yaml:"tempchat"`

	// P2P 运维方直连配置
	P2P struct {
		Enabled     bool   `yaml:"enabled"`
		ListenAddr string `yaml:"listen_addr"`
	} `yaml:"p2p"`

	// Bootstrap 一次性注册码配置
	Bootstrap struct {
		Enabled              bool   `yaml:"enabled"`
		DefaultExpiry        string `yaml:"default_expiry,omitempty"`   // 如 "168h"
		TokenConfigFile      string `yaml:"token_config_file,omitempty"` // token 配置文件路径
	} `yaml:"bootstrap"`
}

func main() {
	// 解析命令行参数
	configPath := flag.String("config", "config/kernel.yaml", "path to config file")
	daemon := flag.Bool("daemon", false, "run in daemon/background mode without interactive shell")

	// 默认存证配置相关参数（仅支持内核内置存证）
	defaultEvidenceMode := flag.String("default-evidence-mode", "none", "default evidence mode (none, internal)")
	defaultEvidenceStrategy := flag.String("default-evidence-strategy", "all", "default evidence strategy (all, data, control, important)")
	defaultEvidenceRetention := flag.Int("default-evidence-retention", 30, "default evidence retention days")
	defaultEvidenceCompress := flag.Bool("default-evidence-compress", true, "compress evidence data by default")

	flag.Parse()

	// 处理子命令
	if flag.NArg() > 0 {
		command := flag.Arg(0)
		switch command {
		case "token":
			handleTokenCommand(flag.Args()[1:], configPath)
			return
		}
	}

	// 加载配置
	config, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 设置默认值
	if config.Kernel.ID == "" {
		config.Kernel.ID = "kernel-1"
	}
	if config.Kernel.Type == "" {
		config.Kernel.Type = "primary"
	}
	if config.MultiKernel.KernelPort == 0 {
		config.MultiKernel.KernelPort = config.Server.Port + 2
	}
	if config.MultiKernel.HeartbeatInterval == 0 {
		config.MultiKernel.HeartbeatInterval = 30
	}
	if config.MultiKernel.ConnectTimeout == 0 {
		config.MultiKernel.ConnectTimeout = 10
	}
	if config.MultiKernel.MaxRetries == 0 {
		config.MultiKernel.MaxRetries = 3
	}
	if config.Security.KernelCertPath == "" {
		config.Security.KernelCertPath = config.Security.ServerCertPath
	}
	if config.Security.KernelKeyPath == "" {
		config.Security.KernelKeyPath = config.Security.ServerKeyPath
	}

	// TempChat 配置默认值
	if config.TempChat.Port == 0 {
		config.TempChat.Port = config.Server.Port + 1 // 临时通信服务端口默认为主端口+1
	}
	if config.TempChat.SessionTimeout == 0 {
		config.TempChat.SessionTimeout = 60
	}
	if config.TempChat.HeartbeatInterval == 0 {
		config.TempChat.HeartbeatInterval = 15
	}
	if config.TempChat.SyncInterval == 0 {
		config.TempChat.SyncInterval = 30
	}

	// P2P 配置默认值
	if config.P2P.ListenAddr == "" {
		// 默认使用 kernel_port+3（P2P端口约定为kernel端口+3）
		config.P2P.ListenAddr = fmt.Sprintf("0.0.0.0:%d", config.MultiKernel.KernelPort+3)
	}

	// 使用配置文件中的值覆盖命令行参数（如果配置文件中有设置）
	if config.Channel.Evidence.DefaultMode != "" {
		*defaultEvidenceMode = config.Channel.Evidence.DefaultMode
	}
	if config.Channel.Evidence.DefaultStrategy != "" {
		*defaultEvidenceStrategy = config.Channel.Evidence.DefaultStrategy
	}
	if config.Channel.Evidence.DefaultRetentionDays > 0 {
		*defaultEvidenceRetention = config.Channel.Evidence.DefaultRetentionDays
	}
	*defaultEvidenceCompress = config.Channel.Evidence.DefaultCompressData

	// Bootstrap 配置默认值和解析
	bootstrapTokenConfigFile := "./config/bootstrap_tokens.yaml"
	if config.Bootstrap.TokenConfigFile != "" {
		bootstrapTokenConfigFile = config.Bootstrap.TokenConfigFile
	}

	// 初始化 TokenManager
	tokenManager := control.NewTokenManager(bootstrapTokenConfigFile)
	// 加载 token 配置
	if err := tokenManager.LoadConfig(); err != nil {
		log.Printf("[WARN] Failed to load bootstrap token config: %v", err)
	}

	// 如果配置了 default_expiry，覆盖默认值
	if config.Bootstrap.DefaultExpiry != "" {
		if dur, err := time.ParseDuration(config.Bootstrap.DefaultExpiry); err == nil {
			// 使用 Setter 方法设置默认有效期
			tokenManager.SetDefaultExpiryDuration(dur)
		} else {
			log.Printf("[WARN] Invalid bootstrap default_expiry: %v", err)
		}
	}

	// 初始化组件
	log.Println("[INFO] Initializing kernel components...")

	// 1. 身份注册表
	registry := control.NewRegistry()
	registry.StartHealthCheck()
	log.Println("[OK] Registry initialized")

	// 2. 频道管理器
	channelManager := circulation.NewChannelManager()
	channelManager.SetKernelID(config.Kernel.ID) // 设置当前内核ID
	channelManager.StartCleanupRoutine()
	channelManager.StartBufferCleanupRoutine() // 启动连接器缓冲清理协程

	// 初始化频道配置管理器
	configManager, err := circulation.NewChannelConfigManager("./channel_configs")
	if err != nil {
		log.Fatalf("Failed to create channel config manager: %v", err)
	}
	channelManager.SetConfigManager(configManager)

	// 设置默认存证配置（当频道未指定配置文件时使用，仅支持内核内置存证）
	defaultEvidenceConfig := &circulation.EvidenceConfig{
		Mode:          circulation.EvidenceMode(*defaultEvidenceMode),
		Strategy:      circulation.EvidenceStrategy(*defaultEvidenceStrategy),
		RetentionDays: *defaultEvidenceRetention,
		CompressData:  *defaultEvidenceCompress,
	}

	if err := channelManager.SetDefaultEvidenceConfig(defaultEvidenceConfig); err != nil {
		log.Fatalf("Failed to set default evidence config: %v", err)
	}

	log.Println("[OK] Channel manager initialized")

	// 4. 数据库管理器（如果启用）
	var dbManager *database.DBManager
	var evidenceStore evidence.EvidenceStore

	if config.Database.Enabled {
		dbConfig := database.MySQLConfig{
			Host:     config.Database.Host,
			Port:     config.Database.Port,
			User:     config.Database.User,
			Password: config.Database.Password,
			Database: config.Database.Database,
		}

		dbManager, err = database.NewDBManager(dbConfig)
		if err != nil {
			log.Fatalf("Failed to initialize database: %v", err)
		}
		defer dbManager.Close()

		// 创建证据存储
		evidenceStore = database.NewMySQLEvidenceStore(dbManager.GetDB())
		log.Println("[OK] Database initialized")
	}

	// 5. 审计日志
	auditLogConfig := evidence.AuditLogConfig{
		Persistent:     config.Evidence.Persistent,
		LogFilePath:    config.Evidence.LogFilePath,
		Store:          evidenceStore,
		ChannelManager: channelManager,
		UseMemoryCache: !config.Database.Enabled, // 如果使用数据库，不需要内存缓存
		SignRecordFunc: security.SignRecordHash, // 每条 evidence_record 的 RSA 签名
	}

	auditLog, err := evidence.NewAuditLogWithConfig(auditLogConfig)
	if err != nil {
		log.Fatalf("Failed to initialize audit log: %v", err)
	}
	defer auditLog.Close()
	log.Println("[OK] Audit log initialized")

	// 6. CA 服务（用于动态签发证书）
	ca, err := security.NewCA(config.Security.CACertPath, config.Security.CAKeyPath)
	if err != nil {
		log.Fatalf("Failed to initialize CA: %v", err)
	}
	log.Println("[OK] CA initialized")

	// 7. 多内核管理器（核心组件，总是启用）
	log.Println("[INFO] Initializing multi-kernel manager...")

	kernelConfig := &server.KernelConfig{
		KernelID:          config.Kernel.ID,
		KernelType:        config.Kernel.Type,
		Description:       config.Kernel.Description,
		Address:           config.Server.Address,
		Port:              config.Server.Port,
		KernelPort:        config.MultiKernel.KernelPort,
		CACertPath:        config.Security.CACertPath,
		RootCACertPath:     config.Security.RootCACertPath,
		IntermediateCAPath: config.Security.CACertPath,
		KernelCertPath:    config.Security.KernelCertPath,
		KernelKeyPath:     config.Security.KernelKeyPath,
		HeartbeatInterval: config.MultiKernel.HeartbeatInterval,
		ConnectTimeout:    config.MultiKernel.ConnectTimeout,
		MaxRetries:        config.MultiKernel.MaxRetries,
	}

	multiKernelManager, err := server.NewMultiKernelManager(kernelConfig, registry, channelManager)
	if err != nil {
		log.Fatalf("Failed to initialize multi-kernel manager: %v", err)
	}

	// 8. 多跳链路配置管理器
	log.Println("[INFO] Initializing multi-hop config manager...")
	multiHopConfigManager, err := server.NewMultiHopConfigManager("./kernel_configs")
	if err != nil {
		log.Printf("[WARN] Failed to initialize multi-hop config manager: %v", err)
		// 不致命，继续运行
		multiHopConfigManager = nil
	} else {
		log.Printf("[OK] Multi-hop config manager initialized with %d routes",
			len(multiHopConfigManager.ListConfigs()))
		// 注意：不再自动连接多跳路由，由用户通过 connect-kernel -route 命令手动指定

		// 将多跳配置管理器注入到多内核管理器
		multiKernelManager.SetMultiHopConfigManager(multiHopConfigManager)
	}

	// 初始化多跳审批回调，使发起方能在收到审批通知后自动重连
	multiKernelManager.InitMultiHopApprovedCallback()

	// 初始化 Hop 建立完成回调，使发起方能在收到 HopEstablished RPC 后自动重连
	multiKernelManager.InitHopEstablishedCallback()

	// 将跨内核数据转发回调注入到 ChannelManager（当检测到目标为 kernel:connector 时调用）
	channelManager.SetForwardToKernel(func(kernelID string, packet *circulation.DataPacket, isFinal bool) error {
		currentKernelID := config.Kernel.ID

		// 记录 kernel→kernel DATA_SEND 证据（每个业务数据包，非 ACK，非流结束包）
		// 原因：kernel→kernel 的证据在 ForwardData RPC 返回前记录，避免 ACK 包 FlowId 丢失导致跳过
		if !packet.IsAck && !isFinal && packet.DataHash != "" {
			metadata := map[string]string{
				"data_category": "business",
			}
			if _, err := auditLog.SubmitBasicEvidenceWithMetadata(
				currentKernelID,               // source: 当前内核
				evidence.EventTypeDataSend,    // event: DATA_SEND
				packet.ChannelID,               // channel
				packet.DataHash,                // data_hash
				evidence.DirectionInternal,     // direction
				kernelID,                       // target: 目标内核
				packet.FlowID,                  // flow_id
				metadata,                       // metadata
			); err != nil {
				log.Printf("[WARN] Failed to submit kernel->kernel DATA_SEND evidence: %v", err)
			}
		}

		pbPacket := &pb.DataPacket{
			ChannelId:         packet.ChannelID,
			SequenceNumber:    packet.SequenceNumber,
			Payload:           packet.Payload,
			Signature:         packet.Signature,
			Timestamp:         packet.Timestamp,
			SenderId:          packet.SenderID,
			TargetIds:         packet.TargetIDs,
			FlowId:            packet.FlowID,
			IsFinal:           isFinal,
			DataHash: packet.DataHash,
			IsAck:    packet.IsAck,
			// Signature 字段在下面单独赋值，传递上一跳 kernel 的签名
		}
		// Signature 已合并：直接传递上一跳 kernel 的签名，下一跳读取 GetSignature() 即可
		
		return multiKernelManager.ForwardData(kernelID, pbPacket, isFinal)
	})

	// 设置多跳路由回调，用于获取下一跳内核信息
	if multiHopConfigManager != nil {
		channelManager.SetGetNextHopKernel(func(currentKernelID, targetKernelID string) (string, string, int, int, int, bool) {
			return multiHopConfigManager.GetNextHop(currentKernelID, targetKernelID)
		})
		// 设置反向路由回调，用于获取上一跳内核信息（ACK 反向转发用）
		channelManager.SetGetPreviousHopKernel(func(sourceKernelID string) (string, string, int, bool) {
			return multiHopConfigManager.GetPreviousHop(sourceKernelID)
		})
	}

	// 连接种子内核
	for _, seed := range config.MultiKernel.SeedKernels {
		go func(seedConfig SeedKernelConfig) {
			if err := multiKernelManager.ConnectToKernel(seedConfig.KernelID, seedConfig.Address, seedConfig.Port); err != nil {
				log.Printf("[WARN] Failed to connect to seed kernel %s: %v", seedConfig.KernelID, err)
			}
		}(seed)
	}

	// 将 AuditLog 注入到 MultiKernelManager（必须在 StartKernelServer 之前，因为 goroutine 会立即创建 KernelServiceServer）
	multiKernelManager.SetAuditLog(auditLog)

	// 注册业务数据哈希链服务（只依赖 dbManager，可以提前创建）
	businessChainService := server.NewBusinessChainServiceServer(dbManager)
	multiKernelManager.SetBusinessChainManager(businessChainService.Manager())

	// 启动内核间通信服务器（goroutine，监听 kernel-to-kernel 端口）
	go func() {
		if err := multiKernelManager.StartKernelServer(); err != nil {
			log.Printf("[ERROR] Failed to start kernel server: %v", err)
		}
	}()

	log.Println("[OK] Multi-kernel manager initialized")

	// 9. 临时通信管理器（运维方功能）
	var tempChatManager *tempchat.TempChatManager
	if config.TempChat.Enabled {
		log.Println("[INFO] Initializing TempChat manager (ops mode)...")

		tempChatMgrConfig := &tempchat.TempChatConfig{
			HeartbeatInterval: config.TempChat.HeartbeatInterval,
			SessionTimeout:     config.TempChat.SessionTimeout,
			SyncInterval:      config.TempChat.SyncInterval,
		}

		tempChatManager = tempchat.NewTempChatManager(config.Kernel.ID, tempChatMgrConfig)
		log.Printf("[OK] TempChat manager initialized (port: %d)", config.TempChat.Port)
	}

	// 10. P2P运维方直连管理器
	var p2pManager *operator_peer.P2PManager
	if config.P2P.Enabled {
		log.Println("[INFO] Initializing P2P operator peer manager...")

		p2pMgrConfig := operator_peer.DefaultP2PManagerConfig()
		p2pMgrConfig.KernelID = config.Kernel.ID
		p2pMgrConfig.ListenAddr = config.P2P.ListenAddr
		p2pMgrConfig.LocalKernelAddr = config.Server.Address
		p2pMgrConfig.LocalKernelPort = config.MultiKernel.KernelPort

		var err error
		p2pManager, err = operator_peer.NewP2PManager(p2pMgrConfig)
		if err != nil {
			log.Fatalf("Failed to initialize P2P manager: %v", err)
		}

		// 设置TempChatManager引用（用于获取本地连接器信息）
		if tempChatManager != nil {
			p2pManager.SetTempChatManager(tempChatManager)
		}

		// 将 MultiKernelManager 注入 P2PManager（提供按需连接时所需的地址信息）
		if multiKernelManager != nil {
			multiKernelManager.SetP2PManager(p2pManager)
		}

		// 设置消息投递回调（将P2P接收到的消息投递到本地连接器）
		p2pManager.SetOnMessageDelivered(func(senderKernelID, senderID, receiverID string, payload []byte, flowID string) error {
			// 解析真正的连接器ID（格式为 "kernelID:connectorID"，或直接是 "connectorID"）
			connectorID := receiverID
			if idx := strings.LastIndex(receiverID, ":"); idx >= 0 {
				connectorID = receiverID[idx+1:]
			}

			// 通过TempChatManager投递消息
			if tempChatManager != nil {
				msg := &pb.TempMessage{
					MessageId:      uuid.New().String(),
					SenderId:       senderID,
					ReceiverId:     connectorID,
					Payload:        payload,
					Timestamp:      time.Now().Unix(),
					FlowId:        flowID,
					SourceKernelId: senderKernelID,
				}
				return tempChatManager.DeliverMessage(connectorID, msg)
			}
			return fmt.Errorf("tempChatManager not available")
		})

		// 设置跨内核消息转发回调（将TempChat消息转发到P2P）
		if tempChatManager != nil {
			tempChatManager.SetRelayHandler(func(receiverKernelID, receiverID string, payload []byte) error {
				// 按需建立 P2P 连接（如果尚未连接）
				if p2pManager != nil {
					if err := p2pManager.EnsurePeerConnected(receiverKernelID, "TempChat-relay"); err != nil {
						log.Printf("[P2P] Failed to ensure peer connected for relay: %v", err)
						// P2P 连接失败时静默降级，不阻塞 TempChat 消息（可扩展为 gRPC 回退）
						return nil
					}
				}

				relayMsg := &operator_peer.RelayMessagePayload{
					SenderID:       tempChatManager.GetKernelID(),
					SenderKernelID: tempChatManager.GetKernelID(),
					ReceiverID:     receiverKernelID + ":" + receiverID,
					Payload:        payload,
					Timestamp:      time.Now().Unix(),
				}
				return p2pManager.RelayMessage(receiverKernelID, relayMsg)
			})
		}

		// 启动P2P服务器
		if err := p2pManager.Start(); err != nil {
			log.Fatalf("Failed to start P2P manager: %v", err)
		}

		log.Printf("[OK] P2P manager started on %s", config.P2P.ListenAddr)

		// P2P 连接不再在启动时预连接，而是按需建立：
		// 当内核需要与某个远程内核通信时（TempChat relay 或 sync），
		// 由 EnsurePeerConnected() 动态建立连接
		// 这样避免了内核启动时不知道将要与哪些内核互联的问题
		// (config.P2P.Peers 配置保留，仅作参考/记录用途)
	}

	// Build the peer-cert getter for dynamic TLS. It reads from in-memory
	// map (freshest certs) and falls back to disk (peer-*.crt files saved by RegisterKernelCA).
	peerCertGetter := func(kernelID string) []byte {
		multiKernelManager.PendingPeerCACertsMu().RLock()
		defer multiKernelManager.PendingPeerCACertsMu().RUnlock()
		if kernelID == "" {
			for _, pem := range multiKernelManager.PendingPeerCACerts() {
				if len(pem) > 0 {
					return pem
				}
			}
			if entries, err := os.ReadDir("certs"); err == nil {
				for _, entry := range entries {
					if entry.IsDir() {
						continue
					}
					if !strings.HasPrefix(entry.Name(), "peer-") || !strings.HasSuffix(entry.Name(), ".crt") {
						continue
					}
					path := filepath.Join("certs", entry.Name())
					if pem, err := os.ReadFile(path); err == nil && len(pem) > 0 {
						return pem
					}
				}
			}
			return nil
		}
		if pem, ok := multiKernelManager.PendingPeerCACerts()[kernelID]; ok && len(pem) > 0 {
			return pem
		}
		path := fmt.Sprintf("certs/peer-%s-ca.crt", kernelID)
		if pem, err := os.ReadFile(path); err == nil && len(pem) > 0 {
			return pem
		}
		return nil
	}

	// Use DynamicTLSCredentials so the main gRPC server dynamically loads
	// peer intermediate CA certs (received via RegisterKernelCA) before each
	// TLS handshake. Without this, inter-kernel connections to port 50051 would
	// fail because the server only trusts its own CA at startup.
	creds, err := security.NewDynamicServerTransportCredentials(
		config.Security.ServerCertPath, config.Security.ServerKeyPath,
		config.Security.RootCACertPath, config.Security.CACertPath, "certs",
		peerCertGetter,
	)
	if err != nil {
		log.Fatalf("Failed to setup dynamic mTLS: %v", err)
	}
	log.Println("[OK] Dynamic mTLS configured")

	// Bootstrap server still uses static mtlsConfig for simplicity.
	mtlsConfig := &security.MTLSConfig{
		RootCACertPath:    config.Security.RootCACertPath,
		IntermediateCAPath: config.Security.CACertPath,
		ServerCertPath:    config.Security.ServerCertPath,
		ServerKeyPath:     config.Security.ServerKeyPath,
	}

	// 创建 gRPC 服务器
	grpcServer := grpc.NewServer(
		grpc.Creds(creds),
		grpc.MaxRecvMsgSize(10*1024*1024), // 10MB
		grpc.MaxSendMsgSize(10*1024*1024),
	)

	// 注册服务
	channelService := server.NewChannelServiceServer(channelManager, registry, auditLog, multiKernelManager, dbManager)
	
	// 设置权限变更回调：当权限被批准时，通知被添加的连接器（含跨内核转发）
	channelManager.SetPermissionChangeCallback(func(channelID, connectorID, changeType string) {
		// 获取频道信息
		channel, err := channelManager.GetChannel(channelID)
		if err != nil {
			log.Printf("[WARN] Permission change callback: failed to get channel %s: %v", channelID, err)
			return
		}

		// 构建通知消息，携带 ACCEPTED 状态便于对端内核直接激活频道
		notification := &pb.ChannelNotification{
			ChannelId:         channelID,
			CreatorId:         channel.CreatorID,
			SenderIds:         channel.SenderIDs,
			ReceiverIds:       channel.ReceiverIDs,
			Encrypted:         channel.Encrypted,
			DataTopic:         channel.DataTopic,
			CreatedAt:         channel.CreatedAt.Unix(),
			NegotiationStatus: pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED,
		}

		// NotifyParticipant 支持本地和跨内核通知（kernelID:connectorID 格式）
		if err := channelService.NotifyParticipant(connectorID, notification); err != nil {
			log.Printf("[WARN] Permission change callback: failed to notify connector %s: %v", connectorID, err)
		}
	})
	
	// 设置频道更新后通知回调：当远端内核同步的 channel_update 添加了本地接收者时，通知它们
	channelManager.SetChannelUpdateNotifyCallback(func(channelID string, addedLocalReceivers []string) {
		ch, err := channelManager.GetChannel(channelID)
		if err != nil {
			log.Printf("[WARN] Channel update callback: channel %s not found: %v", channelID, err)
			return
		}
		notification := &pb.ChannelNotification{
			ChannelId:         channelID,
			CreatorId:         ch.CreatorID,
			SenderIds:         ch.SenderIDs,
			ReceiverIds:       ch.ReceiverIDs,
			Encrypted:         ch.Encrypted,
			DataTopic:         ch.DataTopic,
			CreatedAt:         ch.CreatedAt.Unix(),
			NegotiationStatus: pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED,
		}
		for _, connID := range addedLocalReceivers {
			if err := channelService.NotificationManager.Notify(connID, notification); err != nil {
				log.Printf("[WARN] Channel update callback: failed to notify receiver %s: %v", connID, err)
			}
		}
	})

	pb.RegisterChannelServiceServer(grpcServer, channelService)

	identityService := server.NewIdentityServiceServer(registry, auditLog, ca, channelManager, channelService.NotificationManager, multiKernelManager, tokenManager)
	pb.RegisterIdentityServiceServer(grpcServer, identityService)

	evidenceService := server.NewEvidenceServiceServer(auditLog, channelManager)
	pb.RegisterEvidenceServiceServer(grpcServer, evidenceService)

	// 将业务哈希链服务注入到 ChannelService
	channelService.SetBusinessChainService(businessChainService)

	// 将 ChannelService 的 NotificationManager 注入到 MultiKernelManager，供内核间服务使用
	multiKernelManager.SetNotificationManager(channelService.NotificationManager)
	// 注册内核间通信服务（多内核网络核心服务）
	kernelService := server.NewKernelServiceServer(multiKernelManager, channelManager, registry, channelService.NotificationManager, auditLog, businessChainService.Manager())
	pb.RegisterKernelServiceServer(grpcServer, kernelService)
	log.Println("[OK] Kernel-to-kernel service registered")

	// 注册临时通信服务（运维方功能）- 使用独立服务器
	var tempChatServer *grpc.Server
	if tempChatManager != nil {
		tempChatServer = grpc.NewServer(
			grpc.Creds(creds),
			grpc.MaxRecvMsgSize(10*1024*1024),
			grpc.MaxSendMsgSize(10*1024*1024),
		)
		tempChatService := tempchat.NewTempChatServiceServer(tempChatManager)
		pb.RegisterTempChatServiceServer(tempChatServer, tempChatService)
		log.Println("[OK] TempChat service registered")
	}

	log.Println("[OK] gRPC services registered")

	// 创建引导服务（允许无证书连接，用于首次注册）
	bootstrapCreds, err := security.NewBootstrapServerTransportCredentials(mtlsConfig)
	if err != nil {
		log.Fatalf("Failed to setup bootstrap TLS: %v", err)
	}
	
	bootstrapServer := grpc.NewServer(
		grpc.Creds(bootstrapCreds),
		grpc.MaxRecvMsgSize(10*1024*1024),
		grpc.MaxSendMsgSize(10*1024*1024),
	)
	
	// 注册引导服务（只包含RegisterConnector方法）
	pb.RegisterIdentityServiceServer(bootstrapServer, identityService)
	
	// 启动引导服务器（使用不同的端口，例如主端口+1）
	bootstrapPort := config.Server.Port + 1
	bootstrapAddress := fmt.Sprintf("%s:%d", config.Server.Address, bootstrapPort)
	bootstrapListener, err := net.Listen("tcp", bootstrapAddress)
	if err != nil {
		log.Fatalf("Failed to listen on bootstrap port: %v", err)
	}
	
	go func() {
		log.Printf("[INFO] Bootstrap server started on %s (for certificate registration)", bootstrapAddress)
		if err := bootstrapServer.Serve(bootstrapListener); err != nil {
			log.Printf("[ERROR] Bootstrap server error: %v", err)
		}
	}()

	// 启动主服务器（mTLS）
	address := fmt.Sprintf("%s:%d", config.Server.Address, config.Server.Port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	// 启动独立 TempChat 服务器
	if tempChatServer != nil {
		tempChatAddr := fmt.Sprintf("%s:%d", config.Server.Address, config.TempChat.Port)
		tempChatListener, err := net.Listen("tcp", tempChatAddr)
		if err != nil {
			log.Fatalf("Failed to listen on TempChat port: %v", err)
		}
		go func() {
			log.Printf("[INFO] TempChat server started on %s", tempChatAddr)
			if err := tempChatServer.Serve(tempChatListener); err != nil {
				log.Printf("[ERROR] TempChat server error: %v", err)
			}
		}()
	}

	// 优雅关闭
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		log.Println("[INFO] Shutting down gracefully...")
		bootstrapServer.GracefulStop()
		grpcServer.GracefulStop()
		if tempChatServer != nil {
			tempChatServer.GracefulStop()
		}
		auditLog.Close()
		log.Println("[OK] Shutdown complete")
		os.Exit(0)
	}()

	log.Printf("[INFO] Trusted Data Space Kernel started on %s", address)

	// 默认进入交互模式，除非指定-daemon参数
	if *daemon {
		log.Println("[INFO] Running in daemon mode (background service only)...")
		log.Println("[INFO] Waiting for connector connections...")
	} else {
		log.Println("[INFO] Starting interactive management console...")
		log.Println("[OK] gRPC server is running in the background")
		log.Println("[OK] Interactive commands are enabled")
		log.Println("[OK] Ready to accept connector connections")
		go runInteractiveKernelShell(config, channelManager, registry, multiKernelManager, multiHopConfigManager, p2pManager)
	}

	if err := grpcServer.Serve(listener); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}

// runInteractiveKernelShell 运行交互式内核命令行
func runInteractiveKernelShell(config *Config, channelManager *circulation.ChannelManager,
	registry *control.Registry, multiKernelManager *server.MultiKernelManager,
	multiHopConfigManager *server.MultiHopConfigManager,
	p2pManager *operator_peer.P2PManager) {

	kernelID := config.Kernel.ID
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("Trusted Data Space Kernel - Interactive Management Console")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Kernel ID: %s\n", kernelID)
	fmt.Println("Multi-kernel: enabled (default)")
	fmt.Println("gRPC Server: Running (accepting connector connections)")
	if p2pManager != nil {
		fmt.Println("P2P Operator: enabled")
	}
	fmt.Println("Management: Interactive commands enabled")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("Type 'help' for available commands or 'status' for kernel status")
	fmt.Println()

	for {
		fmt.Printf("[%s] > ", kernelID)

		if !scanner.Scan() {
			break
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}

		command := parts[0]
		args := parts[1:]

		switch command {
		case "help", "h":
			printKernelHelp()
		case "status":
			handleKernelStatus(config, channelManager, registry, multiKernelManager, p2pManager)
		case "connectors", "cs":
			handleKernelConnectors(registry, multiKernelManager)
		case "channels", "ch":
			handleKernelChannels(channelManager)
		case "kernels", "ks":
			handleKernelList(multiKernelManager)
		case "connect-kernel":
			handleConnectKernel(multiKernelManager, args)
		case "pending-requests":
			handleListPendingRequests(multiKernelManager)
		case "approve-request":
			handleApproveRequest(multiKernelManager, args)
		case "disconnect-kernel":
			handleDisconnectKernel(multiKernelManager, args)
		case "routes", "rt":
			handleListRoutes(multiHopConfigManager, multiKernelManager)
		case "connect-route":
			handleConnectRoute(multiKernelManager, multiHopConfigManager, args)
		case "load-route":
			handleLoadRoute(multiHopConfigManager, args)
		case "enable-route":
			handleEnableRoute(multiHopConfigManager, args)
		case "disable-route":
			handleDisableRoute(multiHopConfigManager, args)
		case "route-info":
			handleRouteInfo(multiKernelManager, multiHopConfigManager, args)
		case "peers", "ps":
			handleP2PPeers(p2pManager)
		case "connect-peer":
			handleConnectPeer(p2pManager, args)
		case "disconnect-peer":
			handleDisconnectPeer(p2pManager, args)
		case "exit", "quit", "q":
			fmt.Println("Shutting down kernel...")
			os.Exit(0)
		default:
			fmt.Printf("Unknown command: %s. Type 'help' for available commands\n", command)
		}
	}
}

// printKernelHelp 打印内核命令帮助
func printKernelHelp() {
	fmt.Println("Kernel Management Commands:")
	fmt.Println("  status                        - Show kernel status and statistics")
	fmt.Println("  connectors, cs                - List all connectors (local + connected kernels)")
	fmt.Println("  channels, ch                  - List all channels in this kernel")
	fmt.Println("  kernels, ks                   - List all known kernels (multi-kernel mode)")
	fmt.Println("  connect-kernel <id> <addr> <port> [-route <route_name>]")
	fmt.Println("                                - Initiate inter-kernel connection (with optional multi-hop route)")
	fmt.Println("  disconnect-kernel <kernel_id> - Disconnect from a kernel")
	fmt.Println("  pending-requests              - List pending interconnect requests awaiting approval")
	fmt.Println("  approve-request <request_id>  - Approve a pending interconnect request (establish connection)")
	fmt.Println()
	fmt.Println("P2P Operator Peer Commands:")
	fmt.Println("  peers, ps                     - List connected P2P peers")
	fmt.Println("  connect-peer <kernel_id> <addr> <port>")
	fmt.Println("                                - Connect to a P2P peer (operator)")
	fmt.Println("  disconnect-peer <kernel_id>    - Disconnect from a P2P peer")
	fmt.Println()
	fmt.Println("Multi-Hop Route Commands:")
	fmt.Println("  routes, rt                    - List all configured multi-hop routes")
	fmt.Println("  load-route <filename>         - Load a multi-hop route from config file")
	fmt.Println("  connect-route <route_name>    - Connect a specific multi-hop route")
	fmt.Println("  route-info <route_name>       - Show detailed info of a route and connection status")
	fmt.Println("  enable-route <route_name>     - Enable a route for auto-connect")
	fmt.Println("  disable-route <route_name>    - Disable a route for auto-connect")
	fmt.Println()
	fmt.Println("Notes:")
	fmt.Println("  - Typical connect example: connect-kernel kernel-2 192.168.202.136 50053")
	fmt.Println("    This sends an interconnect request to the target; it returns a request id.")
	fmt.Println("  - For multi-hop connection: connect-kernel kernel-3 192.168.202.140 50053 -route route-kernel-1-to-kernel-3-via-kernel-2")
	fmt.Println("    This connects to kernel-3 through the specified multi-hop route.")
	fmt.Println("  - On the target kernel, run `pending-requests` to view request ids, then")
	fmt.Println("    `approve-request <id>` to approve and establish the connection.")
	fmt.Println()
	fmt.Println("  - connectors/ks/cs collect information across connected kernels;")
	fmt.Println("    after approval, run `ks` to confirm the peer kernel is known.")
	fmt.Println()
	fmt.Println("  - Multi-hop routes enable data forwarding through intermediate kernels.")
	fmt.Println("    Use `routes` to see configured routes, `route-info <name>` for details.")
	fmt.Println("    Use `-route <route_name>` with `connect-kernel` to specify the route.")
	fmt.Println()
	fmt.Println("  - P2P peers enable direct TCP connections between operators for faster")
	fmt.Println("    connector list sync and message relay.")
	fmt.Println()
	fmt.Println("  help, h                       - Show this help message")
	fmt.Println("  exit, quit, q                 - Exit the kernel")
	fmt.Println()
}

// handleKernelStatus 处理状态查询命令
func handleKernelStatus(config *Config, channelManager *circulation.ChannelManager,
	registry *control.Registry, multiKernelManager *server.MultiKernelManager,
	p2pManager *operator_peer.P2PManager) {

	fmt.Println("=== Kernel Status ===")
	fmt.Printf("Kernel ID: %s\n", config.Kernel.ID)
	fmt.Printf("Type: %s\n", config.Kernel.Type)
	fmt.Printf("Address: %s:%d\n", config.Server.Address, config.Server.Port)
	fmt.Println("Multi-kernel: enabled (default)")

	// 连接器统计
	connectorCount := len(registry.ListConnectors())
	fmt.Printf("Connectors: %d\n", connectorCount)

	// 频道统计
	channelCount := len(channelManager.ListChannels())
	fmt.Printf("Channels: %d\n", channelCount)

	// 多内核信息
	kernelCount := multiKernelManager.GetConnectedKernelCount()
	fmt.Printf("Connected Kernels: %d\n", kernelCount)

	// P2P对等方信息
	if p2pManager != nil && p2pManager.IsRunning() {
		peerCount := p2pManager.GetPeerCount()
		fmt.Printf("P2P Peers: %d\n", peerCount)
	}

	fmt.Println()
}

// handleP2PPeers 处理P2P对等方列表命令
func handleP2PPeers(p2pManager *operator_peer.P2PManager) {
	if p2pManager == nil || !p2pManager.IsRunning() {
		fmt.Println("P2P mode not enabled")
		return
	}

	peers := p2pManager.ListPeers()
	if len(peers) == 0 {
		fmt.Println("No P2P peers connected")
		return
	}

	fmt.Println("=== P2P Peers ===")
	fmt.Println(strings.Repeat("-", 70))
	fmt.Printf("%-25s %-20s %-15s\n", "Kernel ID", "Address", "Status")
	fmt.Println(strings.Repeat("-", 70))

	for _, peer := range peers {
		fmt.Printf("%-25s %-20s %-15s\n", peer.KernelID, fmt.Sprintf("%s:%d", peer.Address, peer.Port), peer.Status)
	}
	fmt.Println()
}

// handleConnectPeer 处理连接P2P对等方命令
func handleConnectPeer(p2pManager *operator_peer.P2PManager, args []string) {
	if p2pManager == nil || !p2pManager.IsRunning() {
		fmt.Println("P2P mode not enabled")
		return
	}

	if len(args) < 3 {
		fmt.Println("Usage: connect-peer <kernel_id> <address> <port>")
		return
	}

	kernelID := args[0]
	address := args[1]
	port, err := strconv.Atoi(args[2])
	if err != nil {
		fmt.Printf("Invalid port: %s\n", args[2])
		return
	}

	if err := p2pManager.ConnectPeer(kernelID, address, port); err != nil {
		fmt.Printf("Failed to connect: %v\n", err)
		return
	}

	fmt.Printf("Connecting to peer %s at %s:%d...\n", kernelID, address, port)
}

// handleDisconnectPeer 处理断开P2P对等方命令
func handleDisconnectPeer(p2pManager *operator_peer.P2PManager, args []string) {
	if p2pManager == nil || !p2pManager.IsRunning() {
		fmt.Println("P2P mode not enabled")
		return
	}

	if len(args) < 1 {
		fmt.Println("Usage: disconnect-peer <kernel_id>")
		return
	}

	kernelID := args[0]
	if err := p2pManager.DisconnectPeer(kernelID); err != nil {
		fmt.Printf("Failed to disconnect: %v\n", err)
		return
	}

	fmt.Printf("Disconnected from peer %s\n", kernelID)
}

// handleKernelConnectors 处理连接器列表命令
func handleKernelConnectors(registry *control.Registry, multiKernelManager *server.MultiKernelManager) {
	var connectors []*pb.ConnectorInfo

	// 获取本内核ID
	localKernelID := ""
	if multiKernelManager != nil {
		localKernelID = multiKernelManager.GetKernelID()
	}

	// 记录本地连接器ID，用于去重
	localConnectorIDs := make(map[string]bool)

	// 始终显示本地所有连接器（无论是否公开）
		localConnectors := registry.ListConnectors()
		for _, conn := range localConnectors {
		localConnectorIDs[conn.ConnectorID] = true
			connectors = append(connectors, &pb.ConnectorInfo{
				ConnectorId:   conn.ConnectorID,
				EntityType:    conn.EntityType,
				PublicKey:     conn.PublicKey,
				Status:        string(conn.Status),
				LastHeartbeat: conn.LastHeartbeat.Unix(),
				RegisteredAt:  conn.RegisteredAt.Unix(),
			KernelId:      "local",
		})
	}

	// 如果有连接的其他内核，再添加远端公开的连接器（过滤掉本内核的）
	if multiKernelManager != nil && multiKernelManager.GetConnectedKernelCount() > 0 {
		remoteConnectors, err := multiKernelManager.CollectAllConnectors()
		if err != nil {
			fmt.Printf("Failed to collect remote connectors: %v\n", err)
		} else {
			// 过滤掉本内核的连接器和已经显示的本地连接器
			for _, rc := range remoteConnectors {
				// 跳过本内核的连接器
				if rc.KernelId == localKernelID {
					continue
				}
				// 跳过已经在本地显示的连接器（防止重复）
				if localConnectorIDs[rc.ConnectorId] {
					continue
				}
				connectors = append(connectors, rc)
			}
		}
	}

	if len(connectors) == 0 {
		fmt.Println("No connectors registered")
		return
	}

	fmt.Println("=== Registered Connectors ===")
	fmt.Println(strings.Repeat("-", 100))
	fmt.Printf("%-20s %-15s %-10s %-20s %-15s\n", "Connector ID", "Entity Type", "Status", "Last Heartbeat", "Kernel")
	fmt.Println(strings.Repeat("-", 100))

	for _, c := range connectors {
		lastHeartbeat := time.Unix(c.LastHeartbeat, 0)
		timeStr := time.Since(lastHeartbeat).Round(time.Second).String()

		kernelID := c.KernelId
		if kernelID == "" {
			kernelID = "local"
		}

		fmt.Printf("%-20s %-15s %-10s %-20s %-15s\n",
			c.ConnectorId,
			c.EntityType,
			c.Status,
			timeStr+" ago",
			kernelID)
	}
	fmt.Println()
}

// handleKernelChannels 处理频道列表命令
func handleKernelChannels(channelManager *circulation.ChannelManager) {
	channels := channelManager.ListChannels()

	if len(channels) == 0 {
		fmt.Println("No channels created")
		return
	}

	fmt.Println("=== Active Channels ===")
	fmt.Println(strings.Repeat("-", 100))
	fmt.Printf("%-40s %-20s %-15s %-10s\n", "Channel ID", "Data Topic", "Status", "Participants")
	fmt.Println(strings.Repeat("-", 100))

	for _, ch := range channels {
		participantCount := len(ch.SenderIDs) + len(ch.ReceiverIDs)
		fmt.Printf("%-40s %-20s %-15s %-10d\n",
			ch.ChannelID,
			ch.DataTopic,
			ch.Status,
			participantCount)
	}
	fmt.Println()
}

// handleKernelList 处理内核列表命令
func handleKernelList(multiKernelManager *server.MultiKernelManager) {
	if multiKernelManager == nil {
		fmt.Println("Multi-kernel mode not enabled")
		return
	}

	kernels := multiKernelManager.ListKnownKernels()

	if len(kernels) == 0 {
		fmt.Println("No other kernels known")
		return
	}

	fmt.Println("=== Known Kernels ===")
	fmt.Println(strings.Repeat("-", 80))
	fmt.Printf("%-20s %-20s %-10s %-15s\n", "Kernel ID", "Address", "Status", "Last Heartbeat")
	fmt.Println(strings.Repeat("-", 80))

	for _, k := range kernels {
		lastHeartbeat := time.Unix(k.LastHeartbeat, 0)
		timeStr := time.Since(lastHeartbeat).Round(time.Second).String()
		fmt.Printf("%-20s %-20s %-10s %-15s\n",
			k.KernelID,
			fmt.Sprintf("%s:%d", k.Address, k.Port),
			k.Status,
			timeStr+" ago")
	}
	fmt.Println()
}

// handleConnectKernel 处理连接内核命令
func handleConnectKernel(multiKernelManager *server.MultiKernelManager, args []string) {
	if multiKernelManager == nil {
		fmt.Println("Multi-kernel mode not enabled")
		return
	}

	var kernelID, address string
	var port int
	var routeName string

	// 检查是否只使用路由参数（--route 或 -r）
	if len(args) == 2 && (args[0] == "--route" || args[0] == "-r") {
		routeName = args[1]
	} else if len(args) == 3 {
		// 直接连接模式
		kernelID = args[0]
		address = args[1]
		var err error
		port, err = strconv.Atoi(args[2])
		if err != nil {
			fmt.Printf("Invalid port: %s\n", args[2])
			return
		}
	} else if len(args) == 4 && (args[1] == "--route" || args[1] == "-r") {
		// 指定了目标内核和路由
		kernelID = args[0]
		address = args[2]
		var err error
		port, err = strconv.Atoi(args[3])
		if err != nil {
			fmt.Printf("Invalid port: %s\n", args[3])
			return
		}
		routeName = args[3] // 这里会覆盖，需要重新处理
	} else if len(args) == 5 && (args[3] == "--route" || args[3] == "-r") {
		// 完整格式：<kernel_id> <address> <port> -route <route_name>
		kernelID = args[0]
		address = args[1]
		var err error
		port, err = strconv.Atoi(args[2])
		if err != nil {
			fmt.Printf("Invalid port: %s\n", args[2])
			return
		}
		routeName = args[4]
	} else {
		fmt.Println("Usage:")
		fmt.Println("  connect-kernel <kernel_id> <address> <port>              - Direct connection")
		fmt.Println("  connect-kernel --route <route_name>                      - Connect via multi-hop route")
		fmt.Println("  connect-kernel <kernel_id> <address> <port> --route <route_name> - Connect to specific target via route")
		return
	}

	if routeName != "" {
		fmt.Printf("Connecting via multi-hop route: %s\n", routeName)
		if err := multiKernelManager.ConnectToKernelViaRoute(routeName); err != nil {
			// 检查是否是待审批错误，格式: "multi-hop pending: ..."
			if strings.Contains(err.Error(), "multi-hop pending:") {
				errMsg := err.Error()
				fmt.Printf("\n[WARN]  Multi-hop connection requires approval(s):\n\n")
				fmt.Printf("  %s\n\n", errMsg)
				fmt.Printf("Required actions:\n")
				fmt.Printf("1. Go to each target kernel and run: approve-request <request_id>\n")
				fmt.Printf("2. After all approvals, re-run: connect-kernel --route %s\n\n", routeName)
				return
			}
			// 检查是否是待审批错误，格式: "hop X/Y pending: ..."
			if strings.Contains(err.Error(), "hop ") && strings.Contains(err.Error(), "pending") {
				errMsg := err.Error()
				fmt.Printf("\n[WARN]  Multi-hop connection requires approval(s):\n\n")
				fmt.Printf("  %s\n\n", errMsg)
				fmt.Printf("Required actions:\n")
				fmt.Printf("1. Go to the intermediate/target kernel(s) and approve the request(s)\n")
				fmt.Printf("2. Re-run: connect-kernel --route %s\n\n", routeName)
				return
			}
			// 特殊处理 interconnect pending 错误
			if strings.HasPrefix(err.Error(), "interconnect_pending:") {
				requestID := strings.TrimPrefix(err.Error(), "interconnect_pending:")
				fmt.Printf("Interconnect request sent (request id: %s). Waiting for approval on the target kernel.\n", requestID)
				return
			}
			fmt.Printf("Failed to connect: %v\n", err)
			return
		}
		fmt.Printf("Successfully connected via route: %s\n", routeName)
		return
	}

	fmt.Printf("Connecting to kernel %s at %s:%d...\n", kernelID, address, port)

	if err := multiKernelManager.ConnectToKernel(kernelID, address, port); err != nil {
		// 特殊处理 interconnect pending 错误
		if strings.HasPrefix(err.Error(), "interconnect_pending:") {
			requestID := strings.TrimPrefix(err.Error(), "interconnect_pending:")
			fmt.Printf("Interconnect request sent (request id: %s). Waiting for approval on the target kernel.\n", requestID)
			return
		}
		fmt.Printf("Failed to connect: %v\n", err)
		return
	}

	fmt.Printf("Successfully connected to kernel %s\n", kernelID)
}

// handleDisconnectKernel 处理断开内核连接命令
func handleDisconnectKernel(multiKernelManager *server.MultiKernelManager, args []string) {
	if multiKernelManager == nil {
		fmt.Println("Multi-kernel mode not enabled")
		return
	}

	if len(args) != 1 {
		fmt.Println("Usage: disconnect-kernel <kernel_id>")
		return
	}

	kernelID := args[0]

	fmt.Printf("Disconnecting from kernel %s...\n", kernelID)

	if err := multiKernelManager.DisconnectFromKernel(kernelID); err != nil {
		fmt.Printf("Failed to disconnect: %v\n", err)
		return
	}

	fmt.Printf("Successfully disconnected from kernel %s\n", kernelID)
}

// handleListPendingRequests 列出待审批的内核互联请求
func handleListPendingRequests(multiKernelManager *server.MultiKernelManager) {
	if multiKernelManager == nil {
		fmt.Println("Multi-kernel mode not enabled")
		return
	}
	pending := multiKernelManager.ListPendingRequests()
	if len(pending) == 0 {
		fmt.Println("No pending interconnect requests")
		return
	}
	fmt.Println("=== Pending Interconnect Requests ===")
	fmt.Println(strings.Repeat("-", 80))
	fmt.Printf("%-40s %-20s %-15s %-10s\n", "Request ID", "Requester Kernel", "Address", "Status")
	fmt.Println(strings.Repeat("-", 80))
	for _, r := range pending {
		kp := r.KernelPort
		if kp == 0 {
			kp = r.MainPort + 2
		}
		fmt.Printf("%-40s %-20s %-15s %-10s\n", r.RequestID, r.RequesterKernelID, fmt.Sprintf("%s:%d", r.Address, kp), r.Status)
	}
	fmt.Println()
}

// handleApproveRequest 批准指定的互联请求
func handleApproveRequest(multiKernelManager *server.MultiKernelManager, args []string) {
	if multiKernelManager == nil {
		fmt.Println("Multi-kernel mode not enabled")
		return
	}
	if len(args) != 1 {
		fmt.Println("Usage: approve-request <request_id>")
		return
	}
	requestID := args[0]
	fmt.Printf("Approving interconnect request %s...\n", requestID)
	if err := multiKernelManager.ApprovePendingRequest(requestID); err != nil {
		fmt.Printf("Failed to approve request: %v\n", err)
		return
	}
	fmt.Printf("Approved and connected for request %s\n", requestID)
}

// handleSyncConnectors 处理同步连接器命令

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return &config, nil
}

// handleListRoutes 处理列出所有多跳路由命令
func handleListRoutes(configManager *server.MultiHopConfigManager, multiKernelManager *server.MultiKernelManager) {
	if configManager == nil {
		fmt.Println("Multi-hop config manager not initialized")
		return
	}

	configs := configManager.ListConfigs()
	if len(configs) == 0 {
		fmt.Println("No multi-hop routes configured")
		return
	}

	fmt.Println("=== Multi-Hop Routes ===")
	fmt.Println(strings.Repeat("-", 100))
	fmt.Printf("%-45s %-20s %-10s %-10s\n", "Route Name", "Hops", "Enabled", "Status")
	fmt.Println(strings.Repeat("-", 100))

	for _, config := range configs {
		// 检查连接状态
		connectedHops := 0

		// 获取已连接的内核列表
		knownKernels := multiKernelManager.ListKnownKernels()
		connectedKernelIDs := make(map[string]bool)
		for _, k := range knownKernels {
			connectedKernelIDs[k.KernelID] = true
		}

		for _, hop := range config.Hops {
			if connectedKernelIDs[hop.ToKernel] {
				connectedHops++
			}
		}

		status := "inactive"
		if connectedHops == len(config.Hops) && len(config.Hops) > 0 {
			status = "active"
		} else if connectedHops > 0 {
			status = "partial"
		}

		enabledStr := "false"
		if config.Enabled {
			enabledStr = "true"
		}

		fmt.Printf("%-45s %-20d %-10s %-10s\n",
			config.RouteName,
			len(config.Hops),
			enabledStr,
			status)
	}
	fmt.Println()
}

// handleLoadRoute 处理加载多跳路由配置命令
func handleLoadRoute(configManager *server.MultiHopConfigManager, args []string) {
	if configManager == nil {
		fmt.Println("Multi-hop config manager not initialized")
		return
	}

	if len(args) != 1 {
		fmt.Println("Usage: load-route <filename>")
		return
	}

	filename := args[0]
	fmt.Printf("Loading multi-hop route from %s...\n", filename)

	// 读取并解析配置文件
	data, err := os.ReadFile(filename)
	if err != nil {
		fmt.Printf("Failed to read file: %v\n", err)
		return
	}

	var config server.MultiHopConfigFile
	if err := json.Unmarshal(data, &config); err != nil {
		fmt.Printf("Failed to parse config: %v\n", err)
		return
	}

	// 验证配置
	if err := configManager.ValidateConfig(&config); err != nil {
		fmt.Printf("Invalid config: %v\n", err)
		return
	}

	// 保存配置
	if err := configManager.SaveConfig(&config); err != nil {
		fmt.Printf("Failed to save config: %v\n", err)
		return
	}

	fmt.Printf("[OK] Multi-hop route '%s' loaded successfully\n", config.RouteName)
	fmt.Printf("  Name: %s\n", config.Name)
	fmt.Printf("  Description: %s\n", config.Description)
	fmt.Printf("  Hops: %d\n", len(config.Hops))

	for i, hop := range config.Hops {
		fmt.Printf("    Hop %d: %s(%s:%d) -> %s\n",
			i+1, hop.FromKernel, hop.FromAddress, hop.ToPort, hop.ToKernel)
	}
}

// handleConnectRoute 处理连接指定多跳路由命令
func handleConnectRoute(multiKernelManager *server.MultiKernelManager, configManager *server.MultiHopConfigManager, args []string) {
	if configManager == nil {
		fmt.Println("Multi-hop config manager not initialized")
		return
	}

	if len(args) != 1 {
		fmt.Println("Usage: connect-route <route_name>")
		return
	}

	routeName := args[0]
	fmt.Printf("Connecting multi-hop route: %s\n", routeName)

	// 加载配置
	config, err := configManager.LoadConfig(routeName)
	if err != nil {
		fmt.Printf("Failed to load route: %v\n", err)
		return
	}

	// 验证配置
	if err := multiKernelManager.ValidateMultiHopConfig(config); err != nil {
		fmt.Printf("Route validation failed: %v\n", err)
		return
	}

	// 建立连接
	if err := multiKernelManager.ConnectMultiHopRoute(config); err != nil {
		fmt.Printf("Failed to connect route: %v\n", err)
		return
	}

	fmt.Printf("[OK] Multi-hop route '%s' connected successfully\n", routeName)
}

// handleEnableRoute 处理启用多跳路由命令
func handleEnableRoute(configManager *server.MultiHopConfigManager, args []string) {
	if configManager == nil {
		fmt.Println("Multi-hop config manager not initialized")
		return
	}

	if len(args) != 1 {
		fmt.Println("Usage: enable-route <route_name>")
		return
	}

	routeName := args[0]
	config, err := configManager.LoadConfig(routeName)
	if err != nil {
		fmt.Printf("Failed to load route: %v\n", err)
		return
	}

	config.Enabled = true
	if err := configManager.SaveConfig(config); err != nil {
		fmt.Printf("Failed to save config: %v\n", err)
		return
	}

	fmt.Printf("[OK] Route '%s' enabled\n", routeName)
}

// handleDisableRoute 处理禁用多跳路由命令
func handleDisableRoute(configManager *server.MultiHopConfigManager, args []string) {
	if configManager == nil {
		fmt.Println("Multi-hop config manager not initialized")
		return
	}

	if len(args) != 1 {
		fmt.Println("Usage: disable-route <route_name>")
		return
	}

	routeName := args[0]
	config, err := configManager.LoadConfig(routeName)
	if err != nil {
		fmt.Printf("Failed to load route: %v\n", err)
		return
	}

	config.Enabled = false
	if err := configManager.SaveConfig(config); err != nil {
		fmt.Printf("Failed to save config: %v\n", err)
		return
	}

	fmt.Printf("[OK] Route '%s' disabled\n", routeName)
}

// handleRouteInfo 处理显示路由详细信息命令
func handleRouteInfo(multiKernelManager *server.MultiKernelManager, configManager *server.MultiHopConfigManager, args []string) {
	if configManager == nil {
		fmt.Println("Multi-hop config manager not initialized")
		return
	}

	if len(args) != 1 {
		fmt.Println("Usage: route-info <route_name>")
		return
	}

	routeName := args[0]
	config, err := configManager.LoadConfig(routeName)
	if err != nil {
		fmt.Printf("Failed to load route: %v\n", err)
		return
	}

	// 显示路由详细信息
	info := multiKernelManager.GetMultiHopRouteInfo(config)
	fmt.Println("=== Route Information ===")
	fmt.Print(info)
	fmt.Println()

	// 显示当前内核连接状态
	fmt.Println("=== Current Kernel Connections ===")
	knownKernels := multiKernelManager.ListKnownKernels()
	if len(knownKernels) == 0 {
		fmt.Println("No connected kernels")
	} else {
		for _, k := range knownKernels {
			fmt.Printf("  %s: %s:%d (%s)\n", k.KernelID, k.Address, k.Port, k.Status)
		}
	}
}

// handleTokenCommand 处理 token 子命令
func handleTokenCommand(args []string, configPath *string) {
	// 重新解析子命令参数
	tokenCmd := flag.NewFlagSet("token", flag.ExitOnError)
	generateCmd := tokenCmd.Bool("generate", false, "generate a new bootstrap token")
	listCmd := tokenCmd.Bool("list", false, "list all bootstrap tokens")
	revokeCmd := tokenCmd.Bool("revoke", false, "revoke a bootstrap token")
	connectorID := tokenCmd.String("connector-id", "", "bind token to a specific connector ID")
	expiry := tokenCmd.String("expiry", "", "token expiry duration (e.g. 168h, 7d)")
	tokenCode := tokenCmd.String("code", "", "token code to revoke")

	tokenCmd.Parse(args)

	// 加载配置
	config, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// 初始化 TokenManager
	bootstrapTokenConfigFile := "./config/bootstrap_tokens.yaml"
	if config.Bootstrap.TokenConfigFile != "" {
		bootstrapTokenConfigFile = config.Bootstrap.TokenConfigFile
	}

	tokenManager := control.NewTokenManager(bootstrapTokenConfigFile)
	if err := tokenManager.LoadConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load bootstrap token config: %v\n", err)
		os.Exit(1)
	}

	// 设置默认过期时间
	if config.Bootstrap.DefaultExpiry != "" {
		if dur, err := time.ParseDuration(config.Bootstrap.DefaultExpiry); err == nil {
			tokenManager.SetDefaultExpiryDuration(dur)
		}
	}

	// 执行子命令
	switch {
	case *generateCmd:
		// 生成新 Token
		var expiryDur *time.Duration
		if *expiry != "" {
			dur, err := time.ParseDuration(*expiry)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Invalid expiry duration: %v\n", err)
				os.Exit(1)
			}
			expiryDur = &dur
		}

		token, err := tokenManager.GenerateToken(*connectorID, expiryDur)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to generate token: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("[OK] Bootstrap token generated:\n")
		fmt.Printf("  Code:        %s\n", token.Code)
		fmt.Printf("  ConnectorID: %s\n", token.ConnectorID)
		fmt.Printf("  Status:      %s\n", token.Status)
		fmt.Printf("  CreatedAt:   %s\n", token.CreatedAt.Format(time.RFC3339))
		if token.ExpiresAt != nil {
			fmt.Printf("  ExpiresAt:   %s\n", token.ExpiresAt.Format(time.RFC3339))
		} else {
			fmt.Printf("  ExpiresAt:   never\n")
		}

	case *listCmd:
		// 列出所有 Token
		tokens := tokenManager.ListTokens()
		if len(tokens) == 0 {
			fmt.Println("No bootstrap tokens found")
			return
		}

		fmt.Printf("%-25s %-20s %-10s %-20s %s\n", "Code", "ConnectorID", "Status", "CreatedAt", "ExpiresAt")
		fmt.Println(strings.Repeat("-", 100))

		for _, t := range tokens {
			expiresAt := "never"
			if t.ExpiresAt != nil {
				expiresAt = t.ExpiresAt.Format(time.RFC3339)
			}

		status := t.Status
		if t.IsValid() {
			status = "valid"
		} else if t.Status == control.BootstrapTokenStatusUsed {
			status = "used"
		} else if t.Status == control.BootstrapTokenStatusExpired {
			status = "expired"
		} else if t.Status == control.BootstrapTokenStatusRevoked {
			status = "revoked"
		}

			fmt.Printf("%-25s %-20s %-10s %-20s %s\n",
				t.Code[:20]+"...", // 截断显示
				t.ConnectorID,
				status,
				t.CreatedAt.Format("2006-01-02 15:04:05"),
				expiresAt,
			)
		}
		fmt.Printf("\nTotal: %d token(s)\n", len(tokens))

	case *revokeCmd:
		// 撤销 Token
		if *tokenCode == "" {
			fmt.Fprintf(os.Stderr, "Error: --code is required for revoke command\n")
			tokenCmd.Usage()
			os.Exit(1)
		}

		if err := tokenManager.RevokeToken(*tokenCode); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to revoke token: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("[OK] Token %s has been revoked\n", *tokenCode)

	default:
		tokenCmd.Usage()
		fmt.Println("\nExamples:")
		fmt.Println("  kernel token -generate -connector-id connector-A")
		fmt.Println("  kernel token -generate -connector-id connector-B -expiry 48h")
		fmt.Println("  kernel token -list")
		fmt.Println("  kernel token -revoke -code TSK-BOOT-XXXXXXXX-XXXX")
	}
}

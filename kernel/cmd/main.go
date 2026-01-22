package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"gopkg.in/yaml.v3"

	pb "github.com/trusted-space/kernel/proto/kernel/v1"
	"github.com/trusted-space/kernel/kernel/circulation"
	"github.com/trusted-space/kernel/kernel/control"
	"github.com/trusted-space/kernel/kernel/database"
	"github.com/trusted-space/kernel/kernel/evidence"
	"github.com/trusted-space/kernel/kernel/security"
	"github.com/trusted-space/kernel/kernel/server"
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
		CACertPath     string `yaml:"ca_cert_path"`
		CAKeyPath      string `yaml:"ca_key_path"`      // CA私钥路径
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

	Policy struct {
		DefaultAllow bool `yaml:"default_allow"`
	} `yaml:"policy"`

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
}

func main() {
	// 解析命令行参数
	configPath := flag.String("config", "config/kernel.yaml", "path to config file")
	daemon := flag.Bool("daemon", false, "run in daemon/background mode without interactive shell")

	// 默认存证配置相关参数（带默认值，会被配置文件覆盖）
	defaultEvidenceMode := flag.String("default-evidence-mode", "none", "default evidence mode (none, internal, external, hybrid)")
	defaultEvidenceStrategy := flag.String("default-evidence-strategy", "all", "default evidence strategy (all, data, control, important)")
	defaultEvidenceConnector := flag.String("default-evidence-connector", "", "default external evidence connector ID")
	defaultEvidenceBackup := flag.Bool("default-evidence-backup", false, "enable backup evidence by default")
	defaultEvidenceRetention := flag.Int("default-evidence-retention", 30, "default evidence retention days")
	defaultEvidenceCompress := flag.Bool("default-evidence-compress", true, "compress evidence data by default")

	flag.Parse()

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

	// 使用配置文件中的值覆盖命令行参数（如果配置文件中有设置）
	if config.Channel.Evidence.DefaultMode != "" {
		*defaultEvidenceMode = config.Channel.Evidence.DefaultMode
	}
	if config.Channel.Evidence.DefaultStrategy != "" {
		*defaultEvidenceStrategy = config.Channel.Evidence.DefaultStrategy
	}
	if config.Channel.Evidence.DefaultConnectorID != "" {
		*defaultEvidenceConnector = config.Channel.Evidence.DefaultConnectorID
	}
	if config.Channel.Evidence.DefaultRetentionDays > 0 {
		*defaultEvidenceRetention = config.Channel.Evidence.DefaultRetentionDays
	}
	*defaultEvidenceBackup = config.Channel.Evidence.DefaultBackupEnabled
	*defaultEvidenceCompress = config.Channel.Evidence.DefaultCompressData

	// 初始化组件
	log.Println("Initializing kernel components...")

	// 1. 身份注册表
	registry := control.NewRegistry()
	registry.StartHealthCheck()
	log.Println("✓ Registry initialized")

	// 2. 权限策略引擎
	policyEngine := control.NewPolicyEngine(config.Policy.DefaultAllow)
	policyEngine.LoadDefaultRules()
	log.Println("✓ Policy engine initialized")

	// 3. 频道管理器
	channelManager := circulation.NewChannelManager()
	channelManager.StartCleanupRoutine()
	channelManager.StartBufferCleanupRoutine() // 启动连接器缓冲清理协程

	// 初始化频道配置管理器
	configManager, err := circulation.NewChannelConfigManager("./channel_configs")
	if err != nil {
		log.Fatalf("Failed to create channel config manager: %v", err)
	}
	channelManager.SetConfigManager(configManager)

	// 设置默认存证配置（当频道未指定配置文件时使用）
	defaultEvidenceConfig := &circulation.EvidenceConfig{
		Mode:           circulation.EvidenceMode(*defaultEvidenceMode),
		Strategy:       circulation.EvidenceStrategy(*defaultEvidenceStrategy),
		ConnectorID:    *defaultEvidenceConnector,
		BackupEnabled:  *defaultEvidenceBackup,
		RetentionDays:  *defaultEvidenceRetention,
		CompressData:   *defaultEvidenceCompress,
		CustomSettings: make(map[string]string),
	}

	if err := channelManager.SetDefaultEvidenceConfig(defaultEvidenceConfig); err != nil {
		log.Fatalf("Failed to set default evidence config: %v", err)
	}

	// 启动存证连接器心跳检查
	channelManager.StartEvidenceConnectorHeartbeatCheck()

	log.Println("✓ Channel manager initialized")

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
		log.Println("✓ Database initialized")
	}

	// 5. 审计日志
	auditLogConfig := evidence.AuditLogConfig{
		Persistent:     config.Evidence.Persistent,
		LogFilePath:    config.Evidence.LogFilePath,
		Store:          evidenceStore,
		ChannelManager: channelManager,
		UseMemoryCache: !config.Database.Enabled, // 如果使用数据库，不需要内存缓存
	}

	auditLog, err := evidence.NewAuditLogWithConfig(auditLogConfig)
	if err != nil {
		log.Fatalf("Failed to initialize audit log: %v", err)
	}
	defer auditLog.Close()
	log.Println("✓ Audit log initialized")

	// 6. CA 服务（用于动态签发证书）
	ca, err := security.NewCA(config.Security.CACertPath, config.Security.CAKeyPath)
	if err != nil {
		log.Fatalf("Failed to initialize CA: %v", err)
	}
	log.Println("✓ CA initialized")

	// 7. 多内核管理器（核心组件，总是启用）
	log.Println("正在初始化多内核管理器...")

	kernelConfig := &server.KernelConfig{
		KernelID:          config.Kernel.ID,
		KernelType:        config.Kernel.Type,
		Description:       config.Kernel.Description,
		Address:           config.Server.Address,
		Port:              config.Server.Port,
		KernelPort:        config.MultiKernel.KernelPort,
		CACertPath:        config.Security.CACertPath,
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

	// 连接种子内核
	for _, seed := range config.MultiKernel.SeedKernels {
		go func(seedConfig SeedKernelConfig) {
			if err := multiKernelManager.ConnectToKernel(seedConfig.KernelID, seedConfig.Address, seedConfig.Port); err != nil {
				log.Printf("Failed to connect to seed kernel %s: %v", seedConfig.KernelID, err)
			}
		}(seed)
	}

	// 启动内核间通信服务器
	go func() {
		if err := multiKernelManager.StartKernelServer(); err != nil {
			log.Printf("Failed to start kernel server: %v", err)
		}
	}()

	log.Println("✓ Multi-kernel manager initialized")

	// 8. mTLS 配置
	mtlsConfig := &security.MTLSConfig{
		CACertPath:     config.Security.CACertPath,
		ServerCertPath: config.Security.ServerCertPath,
		ServerKeyPath:  config.Security.ServerKeyPath,
	}

	creds, err := security.NewServerTransportCredentials(mtlsConfig)
	if err != nil {
		log.Fatalf("Failed to setup mTLS: %v", err)
	}
	log.Println("✓ mTLS configured")

	// 创建 gRPC 服务器
	grpcServer := grpc.NewServer(
		grpc.Creds(creds),
		grpc.MaxRecvMsgSize(10*1024*1024), // 10MB
		grpc.MaxSendMsgSize(10*1024*1024),
	)

	// 注册服务
	channelService := server.NewChannelServiceServer(channelManager, policyEngine, registry, auditLog, multiKernelManager)
	pb.RegisterChannelServiceServer(grpcServer, channelService)

	identityService := server.NewIdentityServiceServer(registry, auditLog, ca, channelManager, channelService.NotificationManager, multiKernelManager)
	pb.RegisterIdentityServiceServer(grpcServer, identityService)

	evidenceService := server.NewEvidenceServiceServer(auditLog, channelManager)
	pb.RegisterEvidenceServiceServer(grpcServer, evidenceService)

	// 注册内核间通信服务（多内核网络核心服务）
	kernelService := server.NewKernelServiceServer(multiKernelManager, channelManager, registry)
	pb.RegisterKernelServiceServer(grpcServer, kernelService)
	log.Println("✓ Kernel-to-kernel service registered")

	log.Println("✓ gRPC services registered")

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
		log.Printf("🔓 Bootstrap server started on %s (for certificate registration)", bootstrapAddress)
		if err := bootstrapServer.Serve(bootstrapListener); err != nil {
			log.Printf("Bootstrap server error: %v", err)
		}
	}()

	// 启动主服务器（mTLS）
	address := fmt.Sprintf("%s:%d", config.Server.Address, config.Server.Port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	// 优雅关闭
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		log.Println("\nShutting down gracefully...")
		bootstrapServer.GracefulStop()
		grpcServer.GracefulStop()
		auditLog.Close()
		log.Println("Shutdown complete")
		os.Exit(0)
	}()

	log.Printf("🚀 Trusted Data Space Kernel started on %s", address)

	// 默认进入交互模式，除非指定-daemon参数
	if *daemon {
		log.Println("Running in daemon mode (background service only)...")
		log.Println("Waiting for connector connections...")
	} else {
		log.Println("Starting interactive management console...")
		log.Println("✓ gRPC server is running in the background")
		log.Println("✓ Interactive commands are enabled")
		log.Println("✓ Ready to accept connector connections")
		go runInteractiveKernelShell(config, channelManager, registry, multiKernelManager)
	}

	if err := grpcServer.Serve(listener); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}

// runInteractiveKernelShell 运行交互式内核命令行
func runInteractiveKernelShell(config *Config, channelManager *circulation.ChannelManager,
	registry *control.Registry, multiKernelManager *server.MultiKernelManager) {

	kernelID := config.Kernel.ID
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("🚀 Trusted Data Space Kernel - Interactive Management Console")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Kernel ID: %s\n", kernelID)
	fmt.Println("Multi-kernel: enabled (default)")
	fmt.Println("gRPC Server: Running (accepting connector connections)")
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
			handleKernelStatus(config, channelManager, registry, multiKernelManager)
		case "connectors", "cs":
			handleKernelConnectors(registry, multiKernelManager)
		case "channels", "ch":
			handleKernelChannels(channelManager)
		case "kernels", "ks":
			handleKernelList(multiKernelManager)
		case "connect-kernel":
			handleConnectKernel(multiKernelManager, args)
		case "disconnect-kernel":
			handleDisconnectKernel(multiKernelManager, args)
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
	fmt.Println("  status              - Show kernel status and statistics")
	fmt.Println("  connectors, cs      - List all connectors (local + connected kernels)")
	fmt.Println("  channels, ch        - List all channels in this kernel")
	fmt.Println("  kernels, ks         - List all known kernels (multi-kernel mode)")
	fmt.Println("  connect-kernel <kernel_id> <address> <port> - Connect to another kernel")
	fmt.Println("  disconnect-kernel <kernel_id> - Disconnect from a kernel")
	fmt.Println("  help, h             - Show this help message")
	fmt.Println("  exit, quit, q       - Exit the kernel")
	fmt.Println()
}

// handleKernelStatus 处理状态查询命令
func handleKernelStatus(config *Config, channelManager *circulation.ChannelManager,
	registry *control.Registry, multiKernelManager *server.MultiKernelManager) {

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

	fmt.Println()
}

// handleKernelConnectors 处理连接器列表命令
func handleKernelConnectors(registry *control.Registry, multiKernelManager *server.MultiKernelManager) {
	var connectors []*pb.ConnectorInfo
	var err error

	// 如果有连接的其他内核，收集所有连接器的信息
	if multiKernelManager != nil && multiKernelManager.GetConnectedKernelCount() > 0 {
		connectors, err = multiKernelManager.CollectAllConnectors()
		if err != nil {
			fmt.Printf("Failed to collect connectors: %v\n", err)
			return
		}
	} else {
		// 只有本地连接器
		localConnectors := registry.ListConnectors()
		for _, conn := range localConnectors {
			connectors = append(connectors, &pb.ConnectorInfo{
				ConnectorId:   conn.ConnectorID,
				EntityType:    conn.EntityType,
				PublicKey:     conn.PublicKey,
				Status:        string(conn.Status),
				LastHeartbeat: conn.LastHeartbeat.Unix(),
				RegisteredAt:  conn.RegisteredAt.Unix(),
				KernelId:      "", // 本地连接器
			})
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

	if len(args) != 3 {
		fmt.Println("Usage: connect-kernel <kernel_id> <address> <port>")
		return
	}

	kernelID := args[0]
	address := args[1]
	port, err := strconv.Atoi(args[2])
	if err != nil {
		fmt.Printf("Invalid port: %s\n", args[2])
		return
	}

	fmt.Printf("Connecting to kernel %s at %s:%d...\n", kernelID, address, port)

	if err := multiKernelManager.ConnectToKernel(kernelID, address, port); err != nil {
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


package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

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

// Config 内核配置
type Config struct {
	Server struct {
		Address string `yaml:"address"`
		Port    int    `yaml:"port"`
	} `yaml:"server"`

	Security struct {
		CACertPath     string `yaml:"ca_cert_path"`
		CAKeyPath      string `yaml:"ca_key_path"`      // CA私钥路径
		ServerCertPath string `yaml:"server_cert_path"`
		ServerKeyPath  string `yaml:"server_key_path"`
	} `yaml:"security"`

	Evidence struct {
		Persistent  bool   `yaml:"persistent"`
		LogFilePath string `yaml:"log_file_path"`
		UseDatabase bool   `yaml:"use_database"`
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

	// 7. mTLS 配置
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
	channelService := server.NewChannelServiceServer(channelManager, policyEngine, registry, auditLog)
	pb.RegisterChannelServiceServer(grpcServer, channelService)

	identityService := server.NewIdentityServiceServer(registry, auditLog, ca, channelManager, channelService.NotificationManager)
	pb.RegisterIdentityServiceServer(grpcServer, identityService)

	evidenceService := server.NewEvidenceServiceServer(auditLog, channelManager)
	pb.RegisterEvidenceServiceServer(grpcServer, evidenceService)

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
	log.Println("Waiting for connector connections...")

	if err := grpcServer.Serve(listener); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}

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


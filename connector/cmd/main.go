package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/trusted-space/kernel/connector/client"
	pb "github.com/trusted-space/kernel/proto/kernel/v1"
)

// Config 连接器配置
type Config struct {
	Connector struct {
		ID               string `yaml:"id"`
		EntityType       string `yaml:"entity_type"`
		PublicKey        string `yaml:"public_key"`
		Exposed          bool   `yaml:"expose_to_others"` // 是否向其他内核公开自己的信息，默认为true
		DataCatalog      []string `yaml:"data_catalog"`   // 数据类型/分类目录（支持直接内联列表）
		DataCatalogFile  string   `yaml:"data_catalog_file"` // 数据目录文件路径（支持从文件加载，JSON 或纯文本，每行一个）
	} `yaml:"connector"`

	Kernel struct {
		ID      string `yaml:"id"`
		Address string `yaml:"address"`
		Port    int    `yaml:"port"`
	} `yaml:"kernel"`

	Security struct {
		CACertPath     string `yaml:"ca_cert_path"`
		ClientCertPath string `yaml:"client_cert_path"`
		ClientKeyPath  string `yaml:"client_key_path"`
		ServerName     string `yaml:"server_name"`
	} `yaml:"security"`

	Channel struct {
		ConfigDir string `yaml:"config_dir"` // 频道配置文件目录
	} `yaml:"channel"`

	Database struct {
		Enabled  bool   `yaml:"enabled"`  // 是否启用数据库
		Host     string `yaml:"host"`
		Port     int    `yaml:"port"`
		User     string `yaml:"user"`
		Password string `yaml:"password"`
		Database string `yaml:"database"`
	} `yaml:"database"`
}

func main() {
	// 解析命令行参数
	configPath := flag.String("config", "config/connector.yaml", "path to config file")
	flag.Parse()

	// 加载配置
	config, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 检查证书是否存在，如果不存在则进行首次注册
	if _, err := os.Stat(config.Security.ClientCertPath); os.IsNotExist(err) {
		fmt.Println("未找到证书文件，开始首次注册...")
		
		// 读取配置模板（如果存在）
		configYAML := ""
		if configData, err := os.ReadFile(*configPath); err == nil {
			configYAML = string(configData)
		}
		
		// 连接到引导端点（主端口+1）
		bootstrapPort := config.Kernel.Port + 1
		bootstrapAddr := fmt.Sprintf("%s:%d", config.Kernel.Address, bootstrapPort)
		
		fmt.Printf("正在连接到引导服务器 %s...\n", bootstrapAddr)
		
		// 注册并获取证书
		certPEM, keyPEM, caCertPEM, err := client.RegisterConnector(
			bootstrapAddr,
			config.Connector.ID,
			config.Connector.EntityType,
			config.Connector.PublicKey,
			configYAML,
		)
		if err != nil {
			log.Fatalf("注册失败: %v", err)
		}
		
		// 保存证书文件
		if err := os.WriteFile(config.Security.ClientCertPath, certPEM, 0600); err != nil {
			log.Fatalf("保存证书失败: %v", err)
		}
		if err := os.WriteFile(config.Security.ClientKeyPath, keyPEM, 0600); err != nil {
			log.Fatalf("保存私钥失败: %v", err)
		}
		if err := os.WriteFile(config.Security.CACertPath, caCertPEM, 0644); err != nil {
			log.Fatalf("保存CA证书失败: %v", err)
		}
		
		fmt.Println("[OK] 证书已保存")
	}

	// 创建连接器
	serverAddr := fmt.Sprintf("%s:%d", config.Kernel.Address, config.Kernel.Port)

	// 确定是否公开连接器信息，默认为 true（不传或传 nil 时为 true）
	// 如果配置文件中显式设置为 false，则为 false
	var exposed *bool
	if !config.Connector.Exposed {
		// 用户显式设置为 false
		exposedVal := false
		exposed = &exposedVal
	}
	// 如果 config.Connector.Exposed 为 true（或未设置默认值），则 exposed 保持为 nil，表示默认公开

	connector, err := client.NewConnector(&client.Config{
		ConnectorID:    config.Connector.ID,
		EntityType:     config.Connector.EntityType,
		PublicKey:      config.Connector.PublicKey,
		ServerAddr:     serverAddr,
		CACertPath:     config.Security.CACertPath,
		ClientCertPath: config.Security.ClientCertPath,
		ClientKeyPath:  config.Security.ClientKeyPath,
		ServerName:     config.Security.ServerName,
		KernelID: func() string {
			if config.Kernel.ID != "" {
				return config.Kernel.ID
			}
			// fallback to environment variable if not set in config
			return os.Getenv("KERNEL_ID")
		}(),
		Exposed: exposed,
		DataCatalog: config.Connector.DataCatalog,
		// 数据库配置
		DatabaseEnabled:  config.Database.Enabled,
		DatabaseHost:     config.Database.Host,
		DatabasePort:     config.Database.Port,
		DatabaseUser:     config.Database.User,
		DatabasePassword: config.Database.Password,
		DatabaseName:     config.Database.Database,
	})
	if err != nil {
		log.Fatalf("Failed to create connector: %v", err)
	}
	defer connector.Close()

	// 创建频道配置文件目录
	channelConfigDir := config.Channel.ConfigDir
	if channelConfigDir == "" {
		// 默认使用相对路径: ./channels/{connector_id}
		channelConfigDir = fmt.Sprintf("./channels/%s", config.Connector.ID)
	}

	if err := os.MkdirAll(channelConfigDir, 0755); err != nil {
		log.Fatalf("Failed to create channel config directory %s: %v", channelConfigDir, err)
	}
	fmt.Printf("[OK] 频道配置目录已创建: %s\n", channelConfigDir)

	// 将配置目录路径存储在连接器中（如果需要的话）
	// 这里可以考虑将channelConfigDir传递给连接器客户端

	// 连接到内核
	fmt.Printf("正在连接到内核 %s...\n", serverAddr)
	if err := connector.Connect(); err != nil {
		log.Fatalf("连接失败: %v", err)
	}

	fmt.Printf("[OK] 连接成功！连接器ID: %s\n", config.Connector.ID)
	
	// 启动自动通知监听（所有连接器都会自动等待频道创建通知）
	fmt.Println("正在启动自动通知监听...")
	if err := connector.StartAutoNotificationListener(func(notification *pb.ChannelNotification) {
		// 通知回调：根据协商状态显示不同消息
		switch notification.NegotiationStatus {
		case pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_PROPOSED:
			fmt.Printf("\n[INFO] 收到频道提议通知:\n")
		case pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED:
			fmt.Printf("\n[INFO] 频道已正式创建并激活:\n")
		case pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_REJECTED:
			fmt.Printf("\n[ERROR] 频道提议已被拒绝:\n")
		default:
			fmt.Printf("\n[INFO] 收到频道通知:\n")
		}

		fmt.Printf("   频道ID: %s\n", notification.ChannelId)
		fmt.Printf("   创建者: %s\n", notification.CreatorId)
		fmt.Printf("   发送方: %v\n", notification.SenderIds)
		fmt.Printf("   接收方: %v\n", notification.ReceiverIds)
		fmt.Printf("   加密: %v\n", notification.Encrypted)
		fmt.Printf("   数据主题: %s\n", notification.DataTopic)
		fmt.Printf("   创建时间: %s\n", time.Unix(notification.CreatedAt, 0).Format("2006-01-02 15:04:05"))

		// 显示存证配置（如果有）
		if notification.EvidenceConfig != nil {
			fmt.Printf("   存证方式: %s\n", notification.EvidenceConfig.Mode)
			fmt.Printf("   存证策略: %s\n", notification.EvidenceConfig.Strategy)
			if notification.EvidenceConfig.ConnectorId != "" {
				fmt.Printf("   外部存证连接器: %s\n", notification.EvidenceConfig.ConnectorId)
			}
			if notification.EvidenceConfig.BackupEnabled {
				fmt.Printf("   备份存证: 启用\n")
			}
			fmt.Printf("   保留天数: %d天\n", notification.EvidenceConfig.RetentionDays)
			if notification.EvidenceConfig.CompressData {
				fmt.Printf("   数据压缩: 启用\n")
			}
		}
	}); err != nil {
		log.Printf("[WARN] 启动自动通知监听失败: %v", err)
	} else {
		fmt.Println("[OK] 自动通知监听已启动（连接器状态: active，将自动订阅频道）")
	}
	
	fmt.Println("[OK] 已进入交互模式，输入 'help' 查看可用命令")
	fmt.Println()

	// 优雅关闭
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// 启动交互式命令行
	go runInteractiveShell(connector, config)

	// 等待信号
	<-sigChan
	fmt.Println("\n正在关闭连接器...")
}

// runInteractiveShell 运行交互式命令行
func runInteractiveShell(connector *client.Connector, config *Config) {
	connectorID := config.Connector.ID

	// 获取默认配置目录
	defaultConfigDir := config.Channel.ConfigDir
	if defaultConfigDir == "" {
		defaultConfigDir = fmt.Sprintf("./channels/%s", config.Connector.ID)
	}

	scanner := bufio.NewScanner(os.Stdin)
	
	for {
		fmt.Print(fmt.Sprintf("[%s] > ", connectorID))
		
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
			printHelp(defaultConfigDir)
		case "list", "ls":
			handleList(connector)
		case "info":
			handleInfo(connector, args)
		case "create":
			defaultConfigDir := config.Channel.ConfigDir
			if defaultConfigDir == "" {
				defaultConfigDir = fmt.Sprintf("./channels/%s", config.Connector.ID)
			}
			handleCreateChannel(connector, args, defaultConfigDir)
		case "accept":
			handleAcceptProposal(connector, args)
		case "reject":
			handleRejectProposal(connector, args)
		case "propose":
			fmt.Println("[FAILED] 'propose' command is deprecated - use 'create' command instead")
		case "sendto":
			handleSendTo(connector, args)
		case "subscribe", "sub":
			handleSubscribe(connector, args)
		case "receive", "recv":
			fmt.Println("[FAILED] 'receive' command is deprecated - use 'subscribe' command instead")
		case "channels", "ch":
			handleChannels(connector)
		case "request-permission":
			handleRequestPermission(connector, args)
		case "approve-permission":
			handleApprovePermission(connector, args)
		case "reject-permission":
			handleRejectPermission(connector, args)
		case "list-permissions":
			handleListPermissions(connector, args)
		case "query-evidence", "query":
			handleQueryEvidence(connector, args)
		case "status":
			handleStatus(connector, args)
		case "exit", "quit", "q":
			fmt.Println("退出连接器...")
			os.Exit(0)
	default:
			fmt.Printf("未知命令: %s。输入 'help' 查看可用命令\n", command)
		}
	}
}

// printHelp 打印帮助信息
func printHelp(defaultConfigDir string) {
	fmt.Println("可用命令:")
	fmt.Println("  list, ls              - 列出空间中的所有连接器")
	fmt.Println("  info <connector_id>   - 查看指定连接器的详细信息")
	fmt.Println("  create --config <config_file> | --sender <sender_ids> --receiver <receiver_ids> [--approver <approver_id>] [--reason <reason>] - 创建频道")
	fmt.Printf("    支持两种方式：\n")
	fmt.Printf("    1. 从配置文件创建: create --config channel-config.json (在 %s 中查找)\n", defaultConfigDir)
	fmt.Println("    2. 手动指定参数: create --sender connector-A --receiver connector-B --reason \"data exchange\"")
    fmt.Println("    发起频道创建提议，支持多个发送方和接收方，需要所有参与方确认后才能使用")
    fmt.Printf("    示例: create --config channel-simple-config.json\n")
    fmt.Println("    示例: create --sender connector-A,connector-B --receiver connector-C --reason \"group chat\"")
	fmt.Println("  accept <channel_id> <proposal_id> - 接受频道提议")
	fmt.Println("    示例: accept channel-123 proposal-456")
	fmt.Println("  reject <channel_id> <proposal_id> [--reason <reason>] - 拒绝频道提议")
	fmt.Println("    示例: reject channel-123 proposal-456 --reason \"not authorized\"")
	fmt.Println("  sendto <channel_id> [file_path] - 向频道发送数据或文件")
    fmt.Println("    如果提供file_path且文件存在，则发送文件；否则进入文本发送模式")
    fmt.Println("    注意: 单对单模式下，数据会自动发送给频道的接收方")
    fmt.Println("    示例: sendto channel-123                    (文本模式)")
    fmt.Println("    示例: sendto channel-123 /path/to/file.txt  (发送文件)")
	fmt.Println("  subscribe <channel_id> [--role <sender|receiver>] [--reason <reason>] [output_dir] - 订阅频道")
    fmt.Println("    频道外连接器：申请加入频道指定角色（需要审批通过后才能订阅）")
    fmt.Println("    频道内连接器：直接订阅频道开始接收数据")
    fmt.Println("    自动识别文件传输数据包和普通文本数据包，文件保存到output_dir（默认: ./received）")
	fmt.Println("  channels, ch          - 查看当前连接器参与的频道信息")
	fmt.Println("  request-permission <channel_id> <change_type> <target_id> <reason> - 申请权限变更")
	fmt.Println("    change_type: add_sender, remove_sender, add_receiver, remove_receiver")
	fmt.Println("    示例: request-permission channel-123 add_sender connector-X \"需要发送数据\"")
	fmt.Println("  approve-permission <channel_id> <request_id> - 批准权限变更或订阅申请（仅频道参与者可用）")
	fmt.Println("    示例: approve-permission channel-123 req-456")
	fmt.Println("  reject-permission <channel_id> <request_id> <reason> - 拒绝权限变更或订阅申请（仅频道参与者可用）")
	fmt.Println("    示例: reject-permission channel-123 req-456 \"权限不足\"")
	fmt.Println("  list-permissions <channel_id> - 查看频道的权限变更请求")
	fmt.Println("  query-evidence, query [--channel <channel_id>] [--connector <connector_id>] [--flow <flow_id>] [--limit <num>] - 查询存证记录")
    fmt.Println("    查询证据记录，支持按频道、连接器、业务流程过滤")
    fmt.Println("    示例: query-evidence --channel channel-123 --limit 10")
    fmt.Println("    示例: query-evidence --connector connector-A")
    fmt.Println("    示例: query-evidence --flow flow-uuid-123")
	fmt.Println("  status [active|inactive|closed] - 查看或设置连接器状态")
	fmt.Println("  help, h               - 显示此帮助信息")
	fmt.Println("  exit, quit, q         - 退出连接器")
	fmt.Println("")
	fmt.Println("频道协商流程:")
	fmt.Println("  1. 创建者发起频道提议: create --sender A,B --receiver C,D --reason \"...\"")
	fmt.Println("  2. 所有发送方和接收方都需要确认:")
	fmt.Println("     - 每个参与方确认: accept <channel_id> <proposal_id>")
	fmt.Println("     - 任意方拒绝: reject <channel_id> <proposal_id> --reason \"...\"")
	fmt.Println("  3. 所有参与方确认后频道激活，可使用: sendto <channel_id>")
}

// handleList 处理列出连接器命令
func handleList(connector *client.Connector) {
	fmt.Println("正在查询空间中的连接器...")

	connectors, err := connector.DiscoverConnectors("")
	if err != nil {
		fmt.Printf("[FAILED] 查询失败: %v\n", err)
		return
	}

	// 尝试查询跨内核连接器
	crossConnectors, kernels, err := connector.DiscoverCrossKernelConnectors("", "")
	if err == nil && len(crossConnectors) > 0 {
		// 合并连接器列表
		allConnectors := append(connectors, crossConnectors...)
		connectors = allConnectors
		fmt.Printf("[OK] 包含 %d 个跨内核连接器\n", len(crossConnectors))
	}

	if len(connectors) == 0 {
		fmt.Println("空间中没有其他连接器")
		return
	}

	fmt.Printf("\n找到 %d 个连接器:\n", len(connectors))
	fmt.Println(strings.Repeat("-", 90))
	fmt.Printf("%-20s %-15s %-10s %-15s %-20s\n", "连接器ID", "实体类型", "状态", "内核ID", "最后心跳")
	fmt.Println(strings.Repeat("-", 90))

	for _, c := range connectors {
		lastHeartbeat := time.Unix(c.LastHeartbeat, 0)
		timeStr := time.Since(lastHeartbeat).Round(time.Second).String()
		kernelID := c.KernelId
		if kernelID == "" {
			kernelID = "本内核"
		}
		fmt.Printf("%-20s %-15s %-10s %-15s %-20s\n",
			c.ConnectorId,
			c.EntityType,
			c.Status,
			kernelID,
			timeStr+"前")
	}

	if len(kernels) > 0 {
		fmt.Printf("\n发现的内核 (%d 个):\n", len(kernels))
		fmt.Println(strings.Repeat("-", 60))
		fmt.Printf("%-20s %-20s %-10s\n", "内核ID", "地址", "状态")
		fmt.Println(strings.Repeat("-", 60))
		for _, k := range kernels {
			fmt.Printf("%-20s %-20s %-10s\n",
				k.KernelId,
				fmt.Sprintf("%s:%d", k.Address, k.Port),
				k.Status)
		}
	}

	fmt.Println(strings.Repeat("-", 90))
}

// handleInfo 处理查看连接器信息命令
func handleInfo(connector *client.Connector, args []string) {
	if len(args) == 0 {
		fmt.Println("用法: info <connector_id>")
		return
	}

	connectorID := args[0]
	fmt.Printf("正在查询连接器 %s 的信息...\n", connectorID)

	info, err := connector.GetConnectorInfo(connectorID)
	if err != nil {
		fmt.Printf("[FAILED] 查询失败: %v\n", err)
		return
	}

	fmt.Println("\n连接器信息:")
	fmt.Println(strings.Repeat("-", 50))
	fmt.Printf("连接器ID:     %s\n", info.ConnectorId)
	fmt.Printf("实体类型:     %s\n", info.EntityType)
	fmt.Printf("状态:         %s\n", info.Status)
	fmt.Printf("公钥:         %s\n", truncateString(info.PublicKey, 50))
	fmt.Printf("数据目录:     %s\n", formatDataCatalog(info.DataCatalog))
	fmt.Printf("信息暴露:     %s\n", mapExposedToText(info.Exposed))
	fmt.Printf("最后心跳:     %s\n", time.Unix(info.LastHeartbeat, 0).Format("2006-01-02 15:04:05"))
	fmt.Printf("注册时间:     %s\n", time.Unix(info.RegisteredAt, 0).Format("2006-01-02 15:04:05"))
	fmt.Println(strings.Repeat("-", 50))
}

// formatDataCatalog 格式化数据目录显示
func formatDataCatalog(catalog []string) string {
	if len(catalog) == 0 {
		return "(未设置)"
	}
	return strings.Join(catalog, ", ")
}

// mapExposedToText 将 exposed 状态转换为可读文本
func mapExposedToText(exposed bool) string {
	if exposed {
		return "是 (向其他内核公开)"
	}
	return "否 (不向其他内核公开)"
}

// handleCreateChannel 处理创建频道命令
func handleCreateChannel(connector *client.Connector, args []string, defaultConfigDir string) {
	if len(args) < 2 {
		fmt.Println("[FAILED] 参数错误: create --config <config_file> | --sender <sender_ids> --receiver <receiver_ids> [--approver <approver_id>] [--reason <reason>]")
		fmt.Println("   支持两种方式：")
		fmt.Printf("   1. 从配置文件: create --config channel-config.json (在 %s 中查找)\n", defaultConfigDir)
		fmt.Println("   2. 手动指定: create --sender connector-A --receiver connector-B --reason \"data exchange\"")
		return
	}

	// 检查是否使用配置文件模式
	var configFile string
	var useConfigFile bool

	// 快速检查第一个参数是否是--config
	if len(args) >= 2 && args[0] == "--config" {
		useConfigFile = true
		if len(args) >= 2 {
			configFile = args[1]
		} else {
			fmt.Println("[FAILED] --config 参数需要提供配置文件路径")
			return
		}
	}

	if useConfigFile {
		// 从配置文件创建频道
		handleCreateChannelFromConfigFile(connector, configFile, defaultConfigDir)
	} else {
		// 使用命令行参数创建频道
		handleCreateChannelFromArgs(connector, args)
	}
}

// handleCreateChannelFromConfigFile 从配置文件创建频道
func handleCreateChannelFromConfigFile(connector *client.Connector, configFile string, defaultConfigDir string) {
	// 如果配置文件路径不包含目录分隔符，则在默认配置目录中查找
	if !filepath.IsAbs(configFile) && !strings.Contains(configFile, string(filepath.Separator)) {
		configFile = filepath.Join(defaultConfigDir, configFile)
	}

	// 检查文件是否存在
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		fmt.Printf("[FAILED] 配置文件不存在: %s\n", configFile)
		return
	}

	fmt.Printf("正在从配置文件创建频道: %s...\n", configFile)

	// 读取配置文件内容以判断是否包含跨内核参与者
	data, err := os.ReadFile(configFile)
	if err != nil {
		fmt.Printf("[FAILED] 读取配置文件失败: %v\n", err)
		return
	}

	// 解析为简化结构（包含 evidence_config）
	var cfg struct {
		SenderIDs      []string `json:"sender_ids"`
		ReceiverIDs    []string `json:"receiver_ids"`
		DataTopic       string   `json:"data_topic"`
		Encrypted      bool      `json:"encrypted"`
		ChannelName    string   `json:"channel_name"`
		CreatorID      string   `json:"creator_id"`
		EvidenceConfig *struct {
			Mode           string `json:"mode"`
			Strategy       string `json:"strategy"`
			ConnectorID    string `json:"connector_id"`
			BackupEnabled  bool   `json:"backup_enabled"`
			RetentionDays  int    `json:"retention_days"`
			CompressData   bool   `json:"compress_data"`
		} `json:"evidence_config"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		// 尝试用 yaml 解析（兼容）
		if err2 := yaml.Unmarshal(data, &cfg); err2 != nil {
			// 无法解析，回退到原有方法
			config, err := connector.CreateChannelFromConfig(configFile)
			if err != nil {
				fmt.Printf("[FAILED] 创建频道失败: %v\n", err)
				return
			}
			fmt.Printf("[OK] 频道创建提议已提交\n")
			fmt.Printf("  频道名称: %s\n", config.ChannelName)
			fmt.Printf("  创建者: %s\n", config.CreatorID)
			fmt.Printf("  发送方: %v\n", config.SenderIDs)
			fmt.Printf("  接收方: %v\n", config.ReceiverIDs)
			fmt.Printf("  数据主题: %s\n", config.DataTopic)
			fmt.Printf("  加密: %v\n", config.Encrypted)
			fmt.Println("  创建者已自动接受，等待其他参与方确认后频道将自动激活...")
			return
		}
	}

	containsRemote := func(ids []string) bool {
		for _, id := range ids {
			if strings.Contains(id, ":") {
				return true
			}
		}
		return false
	}

	if containsRemote(cfg.SenderIDs) || containsRemote(cfg.ReceiverIDs) {
		// 使用跨内核创建
		fmt.Println("检测到跨内核参与者，使用跨内核协商路径创建频道...")

		// 转换 evidence_config 为 protobuf 格式
		var evidenceConfig *pb.EvidenceConfig
		if cfg.EvidenceConfig != nil {
			evidenceConfig = &pb.EvidenceConfig{
				Mode:          cfg.EvidenceConfig.Mode,
				Strategy:      cfg.EvidenceConfig.Strategy,
				ConnectorId:   cfg.EvidenceConfig.ConnectorID,
				BackupEnabled: cfg.EvidenceConfig.BackupEnabled,
				RetentionDays: int32(cfg.EvidenceConfig.RetentionDays),
				CompressData:  cfg.EvidenceConfig.CompressData,
			}
		}

		chID, err := connector.CreateCrossKernelChannel(cfg.SenderIDs, cfg.ReceiverIDs, cfg.DataTopic, cfg.Encrypted, fmt.Sprintf("Create from config: %s", configFile), evidenceConfig)
		if err != nil {
			fmt.Printf("[FAILED] 跨内核频道创建失败: %v\n", err)
			return
		}
		fmt.Printf("[OK] 跨内核频道已创建: %s\n", chID)
		return
	}

	// 否则使用原有本地提议流程
	config, err := connector.CreateChannelFromConfig(configFile)
	if err != nil {
		fmt.Printf("[FAILED] 创建频道失败: %v\n", err)
		return
	}

	fmt.Printf("[OK] 频道创建提议已提交\n")
	fmt.Printf("  频道名称: %s\n", config.ChannelName)
	fmt.Printf("  创建者: %s\n", config.CreatorID)
	fmt.Printf("  发送方: %v\n", config.SenderIDs)
	fmt.Printf("  接收方: %v\n", config.ReceiverIDs)
	fmt.Printf("  数据主题: %s\n", config.DataTopic)
	fmt.Printf("  加密: %v\n", config.Encrypted)
	fmt.Println("  创建者已自动接受，等待其他参与方确认后频道将自动激活...")
}

// handleCreateChannelFromArgs 从命令行参数创建频道
func handleCreateChannelFromArgs(connector *client.Connector, args []string) {
	var senderIDs []string
	var receiverIDs []string
	approverID := "" // 默认为空，表示使用创建者作为批准者
	reason := "channel proposal"

	// 解析参数
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--sender":
			if i+1 < len(args) {
				// 支持逗号分隔的多个发送方ID
				senderList := strings.Split(args[i+1], ",")
				for _, sender := range senderList {
					sender = strings.TrimSpace(sender)
					if sender != "" {
						senderIDs = append(senderIDs, sender)
					}
				}
				i += 2
			} else {
				fmt.Println("[FAILED] --sender 参数需要提供发送方ID")
				return
			}
		case "--receiver":
			if i+1 < len(args) {
				// 支持逗号分隔的多个接收方ID
				receiverList := strings.Split(args[i+1], ",")
				for _, receiver := range receiverList {
					receiver = strings.TrimSpace(receiver)
					if receiver != "" {
						receiverIDs = append(receiverIDs, receiver)
					}
				}
				i += 2
			} else {
				fmt.Println("[FAILED] --receiver 参数需要提供接收方ID")
				return
			}
		case "--approver":
			if i+1 < len(args) {
				approverID = args[i+1]
				i += 2
			} else {
				fmt.Println("[FAILED] --approver 参数需要提供批准者ID")
				return
			}
		case "--reason":
			if i+1 < len(args) {
				reason = strings.Join(args[i+1:], " ")
				i = len(args) // 结束解析
			} else {
				fmt.Println("[FAILED] --reason 参数需要提供理由")
				return
			}
		default:
			fmt.Printf("[FAILED] 未知参数: %s\n", args[i])
			fmt.Println("   支持的参数: --sender, --receiver, --approver, --reason")
			return
		}
	}

	if len(senderIDs) == 0 || len(receiverIDs) == 0 {
		fmt.Println("[FAILED] 必须至少指定一个 --sender 和一个 --receiver 参数")
		return
	}

	// 去重
	senderIDs = removeDuplicates(senderIDs)
	receiverIDs = removeDuplicates(receiverIDs)

	fmt.Printf("正在提议创建频道，发送方: %v, 接收方: %v...\n", senderIDs, receiverIDs)
	if approverID != "" {
		fmt.Printf("权限批准者: %s\n", approverID)
	} else {
		fmt.Println("权限批准者: 默认(创建者)")
	}

	// 检查是否包含跨内核参与者（格式 kernel_id:connector_id）
	containsRemote := func(ids []string) bool {
		for _, id := range ids {
			if strings.Contains(id, ":") {
				return true
			}
		}
		return false
	}

	if containsRemote(senderIDs) || containsRemote(receiverIDs) {
		// 使用跨内核创建流程：通过 KernelService.CreateCrossKernelChannel 发起
		fmt.Println("检测到跨内核参与者，使用跨内核协商路径创建频道...")
		chID, err := connector.CreateCrossKernelChannel(senderIDs, receiverIDs, "", true, reason, nil)
		if err != nil {
			fmt.Printf("[FAILED] 跨内核频道创建失败: %v\n", err)
			return
		}
		fmt.Printf("[OK] 跨内核频道已创建: %s\n", chID)
		// 本地记录将在通知中异步完成
	} else {
		channelID, proposalID, err := connector.ProposeChannel(senderIDs, receiverIDs, "", approverID, reason)
		if err != nil {
			fmt.Printf("[FAILED] 提议创建频道失败: %v\n", err)
			return
		}

		fmt.Printf("[OK] 频道提议创建成功\n")
		fmt.Printf("  频道ID: %s\n", channelID)
		fmt.Printf("  提议ID: %s\n", proposalID)
		// 显示需要哪些参与方确认（创建者自动接受，不需要确认）
		fmt.Println("  需要以下参与方确认:")
		selfID := connector.GetID()
		hasOthers := false
		for _, senderID := range senderIDs {
			if senderID != selfID { // 创建者自己不需要确认
				fmt.Printf("    - 发送方 %s 需要确认\n", senderID)
				hasOthers = true
			}
		}
		for _, receiverID := range receiverIDs {
			fmt.Printf("    - 接收方 %s 需要确认\n", receiverID)
			hasOthers = true
		}
		if !hasOthers {
			fmt.Println("    - 无（所有参与方都是创建者自己）")
		}
		fmt.Println("  创建者已自动接受，等待其他参与方确认后频道将自动激活...")
	}
}


// handleAcceptProposal 处理接受提议命令
func handleAcceptProposal(connector *client.Connector, args []string) {
	if len(args) != 2 {
		fmt.Println("[FAILED] 参数错误: accept <channel_id> <proposal_id>")
		return
	}

	channelID := args[0]
	proposalID := args[1]

	fmt.Printf("正在接受频道提议: %s...\n", channelID)

	err := connector.AcceptChannelProposal(channelID, proposalID)
		if err != nil {
		fmt.Printf("[FAILED] 接受频道提议失败: %v\n", err)
		return
	}

	fmt.Printf("[OK] 已确认频道提议 %s\n", proposalID)
	fmt.Println("  等待其他参与方确认，频道将自动激活...")
}

// handleRejectProposal 处理拒绝提议命令
func handleRejectProposal(connector *client.Connector, args []string) {
	if len(args) < 2 {
		fmt.Println("[FAILED] 参数错误: reject <channel_id> <proposal_id> [--reason <reason>]")
		return
	}

	channelID := args[0]
	proposalID := args[1]
	reason := "no reason provided"

	// 解析可选的 --reason 参数
	if len(args) >= 4 && args[2] == "--reason" {
		reason = strings.Join(args[3:], " ")
	}

	fmt.Printf("正在拒绝频道提议: %s...\n", channelID)

	err := connector.RejectChannelProposal(channelID, proposalID, reason)
	if err != nil {
		fmt.Printf("[FAILED] 拒绝频道提议失败: %v\n", err)
		return
	}

	fmt.Printf("[OK] 频道提议已拒绝，频道将被关闭: %s\n", reason)
}

// handleCreateChannel removed - direct channel creation is not allowed

// handleSendTo 处理向已存在频道发送数据命令（频道内所有参与者可互相发送）
// 用法: sendto <channel_id> [file_path] [@receiver_id]
// 如果提供file_path且文件存在，则发送文件；否则进入文本发送模式
func handleSendTo(connector *client.Connector, args []string) {
	if len(args) == 0 {
		fmt.Println("用法: sendto <channel_id> [file_path] [@receiver_id]")
		fmt.Println("说明: 向已存在的频道发送数据或文件")
		fmt.Println("  - 如果提供file_path且文件存在，则发送文件")
		fmt.Println("  - 否则进入文本发送模式")
		fmt.Println("示例: sendto channel-123                    (文本模式)")
		fmt.Println("示例: sendto channel-123 /path/to/file.txt  (发送文件)")
		fmt.Println("示例: sendto channel-123 /path/to/file.txt @connector-B  (发送文件到指定接收者)")
		return
	}

	channelID := args[0]

	// 获取频道信息并验证提议状态
	fmt.Printf("正在验证频道 %s 的状态...\n", channelID)
	channelInfo, err := connector.GetChannelInfo(channelID)
	if err != nil {
		fmt.Printf("[FAILED] 获取频道信息失败: %v\n", err)
		return
	}

	if !channelInfo.Found {
		fmt.Printf("[FAILED] 频道 %s 不存在\n", channelID)
		return
	}

	// 检查协商状态
	if channelInfo.NegotiationStatus != pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_ACCEPTED {
		fmt.Printf("[FAILED] 无法发送数据到频道 %s\n", channelID)
		fmt.Printf("   频道协商状态: %s\n", channelInfo.NegotiationStatus.String())
		switch channelInfo.NegotiationStatus {
		case pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_PROPOSED:
			fmt.Println("   原因: 频道提议尚未被所有参与方接受")
			if channelInfo.ProposalId != "" {
				fmt.Printf("   提议ID: %s\n", channelInfo.ProposalId)
			}
			fmt.Println("   解决方法: 请等待所有参与方使用 'accept <channel_id> <proposal_id>' 接受提议")
		case pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_REJECTED:
			fmt.Println("   原因: 频道提议已被拒绝")
		case pb.ChannelNegotiationStatus_NEGOTIATION_STATUS_UNKNOWN:
			fmt.Println("   原因: 频道状态未知")
		}
		return
	}

	// 检查频道状态
	if channelInfo.Status != "active" {
		fmt.Printf("[FAILED] 频道 %s 当前状态为 %s，无法发送数据\n", channelID, channelInfo.Status)
		return
	}

	// 检查发送方权限（当前连接器必须是频道的发送方）
	connectorID := connector.GetID()
	isSender := false
	for _, senderID := range channelInfo.SenderIds {
		// 去掉 kernel 前缀再比较（格式: "kernelID:connectorID" 或直接是 "connectorID"）
		actualID := senderID
		if idx := strings.LastIndex(senderID, ":"); idx != -1 {
			actualID = senderID[idx+1:]
		}
		if actualID == connectorID {
			isSender = true
			break
		}
	}

	if !isSender {
		fmt.Printf("[FAILED] 权限不足: 连接器 %s 不是频道 %s 的发送方\n", connectorID, channelID)
		fmt.Printf("   频道发送方: %v\n", channelInfo.SenderIds)
		fmt.Printf("   频道接收方: %v\n", channelInfo.ReceiverIds)
		fmt.Println("   解决方法: 只有发送方可以向频道发送数据")
		return
	}

	fmt.Printf("[OK] 频道状态验证通过，可以发送数据\n")

	// 检查第二个参数是否是文件路径
	if len(args) >= 2 {
		filePath := args[1]
		
		// 检查是否是文件路径（不以@开头，且文件存在）
		if !strings.HasPrefix(filePath, "@") {
			if fileInfo, err := os.Stat(filePath); err == nil && !fileInfo.IsDir() {
				// 是文件，发送文件
				var targetIDs []string
				// 解析目标接收者（从第三个参数开始）
				for i := 2; i < len(args); i++ {
					if strings.HasPrefix(args[i], "@") {
						targetID := strings.TrimPrefix(args[i], "@")
						targetIDs = append(targetIDs, targetID)
					}
				}

				fmt.Printf("正在发送文件: %s 到频道 %s...\n", filePath, channelID)
				if len(targetIDs) > 0 {
					fmt.Printf("目标接收者: %v\n", targetIDs)
				} else {
					fmt.Println("目标接收者: 所有订阅者（广播）")
				}

				if err := connector.SendFile(channelID, filePath, targetIDs); err != nil {
					fmt.Printf("[FAILED] 发送文件失败: %v\n", err)
					return
				}

				fmt.Printf("[OK] 文件发送成功: %s\n", filePath)
				return
			}
		}
	}

	// 文本发送模式
	fmt.Printf("正在连接到频道 %s...\n", channelID)

	// 启动实时发送器
	sender, err := connector.StartRealtimeSend(channelID)
	if err != nil {
		fmt.Printf("[FAILED] 连接频道失败: %v\n", err)
		return
	}
	defer sender.Close()

	fmt.Println("实时发送模式：输入数据后按回车立即发送")
	fmt.Println("发送格式: <数据> (广播给所有订阅者) 或 @<接收者ID> <数据> (指定接收者)")
	fmt.Println("输入 'END' 结束发送")

	fmt.Println("[OK] 实时发送已就绪，开始输入数据...")

	// 读取并实时发送数据
	scanner := bufio.NewScanner(os.Stdin)
	packetCount := 0
	
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "END" {
			break
		}
		if line == "" {
			continue // 跳过空行
		}

		// 检查是否指定了目标接收者（格式: @receiver_id data）
		var targetIDs []string
		var data string
		
		if strings.HasPrefix(line, "@") {
			// 解析目标接收者
			parts := strings.Fields(line)
			if len(parts) < 2 {
				fmt.Println("[FAILED] 格式错误，正确格式: @<接收者ID> <数据>")
				continue
			}
			
			// 提取所有以@开头的接收者ID
			for i, part := range parts {
				if strings.HasPrefix(part, "@") {
					targetID := strings.TrimPrefix(part, "@")
					targetIDs = append(targetIDs, targetID)
				} else {
					// 剩余部分作为数据
					data = strings.Join(parts[i:], " ")
					break
				}
			}
			
			if len(data) == 0 {
				fmt.Println("[FAILED] 格式错误，需要提供数据内容")
				continue
			}
		} else {
			// 没有指定接收者，广播给所有订阅者
			data = line
			targetIDs = []string{} // 空列表表示广播
		}

		// 立即发送这一行
		if err := sender.SendLineTo([]byte(data), targetIDs); err != nil {
			fmt.Printf("[FAILED] 发送失败: %v\n", err)
			continue
		}

		packetCount++
		if len(targetIDs) > 0 {
			fmt.Printf("[OK] [%d] 已发送到 %v: %s\n", packetCount, targetIDs, data)
		} else {
			fmt.Printf("[OK] [%d] 已广播: %s\n", packetCount, data)
		}
	}

	if packetCount == 0 {
		fmt.Println("没有发送任何数据")
		return
	}

	fmt.Printf("[OK] 共发送 %d 条数据\n", packetCount)
}

// handleSubscribe 处理订阅频道命令
func handleSubscribe(connector *client.Connector, args []string) {
	if len(args) == 0 {
		fmt.Println("[FAILED] 请指定要订阅的频道ID")
		fmt.Println("用法: subscribe <channel_id> [--role <sender|receiver>] [--reason <reason>] [--output <dir>]")
		return
	}

	// 如果指定了channel_id，检查是否需要申请加入或直接订阅
	channelID := args[0]

	// 解析命令行参数
	var role string
	var reason string = "request to join channel"
	outputDir := "./received"

	i := 1
	for i < len(args) {
		switch args[i] {
		case "--role":
			if i+1 < len(args) {
				role = args[i+1]
				if role != "sender" && role != "receiver" {
					fmt.Printf("[FAILED] 无效的角色: %s (必须是 'sender' 或 'receiver')\n", role)
					return
				}
				i += 2
			} else {
				fmt.Println("[FAILED] --role 参数需要提供角色值")
				return
			}
		case "--reason":
			if i+1 < len(args) {
				reason = strings.Join(args[i+1:], " ")
				i = len(args) // 结束解析
			} else {
				fmt.Println("[FAILED] --reason 参数需要提供理由")
				return
			}
		case "--output":
			if i+1 < len(args) {
				outputDir = args[i+1]
				i += 2
			} else {
				fmt.Println("[FAILED] --output 参数需要提供目录路径")
				return
			}
		default:
			// 如果不是以--开头，当作output_dir
			if !strings.HasPrefix(args[i], "--") {
				outputDir = args[i]
				i++
			} else {
				fmt.Printf("[FAILED] 未知参数: %s\n", args[i])
				return
			}
		}
	}

	// 创建输出目录
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Printf("[WARN] 创建接收目录失败: %v\n", err)
	}

	// 检查是否已经是频道参与者
	isParticipant, err := connector.IsChannelParticipant(channelID)
	if err != nil {
		fmt.Printf("[FAILED] 检查频道参与状态失败: %v\n", err)
		return
	}

	if isParticipant {
		// 已经是频道参与者，直接订阅
		fmt.Printf("[OK] 您已经是频道 %s 的参与者，正在订阅...\n", channelID)
	} else {
		// 不是频道参与者，需要申请加入
		if role == "" {
			fmt.Printf("[FAILED] 您不是频道 %s 的参与者，请指定要申请的角色\n", channelID)
			fmt.Println("   使用: subscribe <channel_id> --role <sender|receiver> [--reason <reason>] [--output <dir>]")
			return
		}

		fmt.Printf("📝 申请加入频道 %s 作为 %s...\n", channelID, role)
		fmt.Printf("   理由: %s\n", reason)

		// 发送加入申请
		err := connector.RequestChannelAccess(channelID, role, reason)
		if err != nil {
			fmt.Printf("[FAILED] 申请加入频道失败: %v\n", err)
			return
		}

		fmt.Println("[OK] 加入申请已发送，等待审批...")
		fmt.Println("   审批通过后您将自动获得订阅权限")
		return
	}

	// 创建文件接收器
	fileReceiver := client.NewFileReceiver(outputDir, func(filePath, fileHash string) {
		fmt.Printf("\n[OK] 文件接收并保存成功:\n")
		fmt.Printf("  文件路径: %s\n", filePath)
		fmt.Printf("  文件哈希: %s\n", fileHash)
	})

	fmt.Printf("正在订阅频道 %s...\n", channelID)
	fmt.Printf("文件将保存到: %s\n", outputDir)

	// 获取频道信息并记录到本地
	go func() {
		channelInfo, err := connector.GetChannelInfo(channelID)
		if err == nil && channelInfo != nil && channelInfo.Found {
			// 记录频道信息到本地
			connector.RecordChannelFromNotification(&pb.ChannelNotification{
				ChannelId:         channelInfo.ChannelId,
				CreatorId:         channelInfo.CreatorId,
				SenderIds:         channelInfo.SenderIds,
				ReceiverIds:       channelInfo.ReceiverIds,
				Encrypted:         channelInfo.Encrypted,
				DataTopic:         channelInfo.DataTopic,
				CreatedAt:         channelInfo.CreatedAt,
			})
		}
	}()

	// 在goroutine中接收数据
	go func() {
		err := connector.ReceiveData(channelID, func(packet *pb.DataPacket) error {
			// 检查是否是文件传输数据包
			if client.IsFileTransferPacket(packet.Payload) {
				// 处理文件传输数据包
				if err := fileReceiver.HandleFilePacket(packet); err != nil {
					log.Printf("[WARN] 处理文件数据包失败: %v", err)
				}
			} else if client.IsControlMessage(packet.Payload) {
				// 处理控制消息
				if err := handleControlMessage(packet); err != nil {
					log.Printf("[WARN] 处理控制消息失败: %v", err)
				}
			} else {
				// 普通数据包，显示文本
				senderInfo := ""
				if packet.SenderId != "" {
					senderInfo = fmt.Sprintf("来自 %s, ", packet.SenderId)
				}
				fmt.Printf("[DATA] [序列号: %d] %s数据: %s\n", packet.SequenceNumber, senderInfo, string(packet.Payload))
			}
			return nil
		})

		if err != nil {
			fmt.Printf("[FAILED] 接收失败: %v\n", err)
		} else {
			fmt.Printf("[OK] 频道 %s 已关闭\n", channelID)
		}
	}()

	fmt.Println("[OK] 已订阅，等待数据... (输入任意命令继续)")
}

// handleStatus 处理状态命令
func handleStatus(connector *client.Connector, args []string) {
	if len(args) == 0 {
		// 查看当前状态
		status := connector.GetStatus()
		fmt.Printf("当前连接器状态: %s\n", status)
		fmt.Println("说明:")
		fmt.Println("  - active: 连接器处于活跃状态，收到频道创建通知时会自动订阅")
		fmt.Println("  - inactive: 连接器处于非活跃状态，收到通知但不会自动订阅，需要手动订阅")
		fmt.Println("  - closed: 连接器已关闭")
		return
	}

	// 设置状态
	newStatus := args[0]
	if err := connector.SetStatus(client.ConnectorStatus(newStatus)); err != nil {
		fmt.Printf("[FAILED] 设置状态失败: %v\n", err)
		fmt.Println("可用状态: active, inactive, closed")
		return
	}

	fmt.Printf("[OK] 连接器状态已设置为: %s\n", newStatus)
	if newStatus == "active" {
		fmt.Println("  → 连接器将自动订阅新创建的频道")
	} else {
		fmt.Println("  → 连接器不会自动订阅，需要手动使用 'subscribe <channel_id>' 订阅")
	}
}

// truncateString 截断字符串
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// handleRequestPermission 处理申请权限变更命令
func handleRequestPermission(connector *client.Connector, args []string) {
	if len(args) != 4 {
		fmt.Println("[FAILED] 参数错误: request-permission <channel_id> <change_type> <target_id> <reason>")
		fmt.Println("   change_type: add_sender, remove_sender, add_receiver, remove_receiver")
		return
	}

	channelID := args[0]
	changeType := args[1]
	targetID := args[2]
	reason := args[3]

	// 验证change_type
	validTypes := map[string]bool{
		"add_sender":      true,
		"remove_sender":   true,
		"add_receiver":    true,
		"remove_receiver": true,
	}
	if !validTypes[changeType] {
		fmt.Printf("[FAILED] 无效的变更类型: %s\n", changeType)
		fmt.Println("   支持的类型: add_sender, remove_sender, add_receiver, remove_receiver")
		return
	}

	fmt.Printf("正在申请权限变更: 频道 %s, 类型 %s, 目标 %s...\n", channelID, changeType, targetID)

	resp, err := connector.RequestPermissionChange(channelID, changeType, targetID, reason)
	if err != nil {
		fmt.Printf("[FAILED] 申请失败: %v\n", err)
		return
	}

	if !resp.Success {
		fmt.Printf("[FAILED] 申请失败: %s\n", resp.Message)
		return
	}

	fmt.Printf("[OK] 权限变更申请已提交\n")
	fmt.Printf("  请求ID: %s\n", resp.RequestId)
	fmt.Println("  等待批准者审批...")
}

// handleApprovePermission 处理批准权限变更/订阅申请命令
func handleApprovePermission(connector *client.Connector, args []string) {
	if len(args) != 2 {
		fmt.Println("[FAILED] 参数错误: approve-permission <channel_id> <request_id>")
		return
	}

	channelID := args[0]
	requestID := args[1]

	fmt.Printf("正在批准请求: %s...\n", requestID)

	// 首先尝试批准权限变更（这是主要的处理方式）
	resp2, err2 := connector.ApprovePermissionChange(channelID, requestID)
	if err2 != nil {
		// 如果权限变更批准失败，尝试批准订阅申请（作为备用方案）
		resp, err := connector.ApproveChannelSubscription(channelID, requestID)
		if err == nil && resp.Success {
			fmt.Printf("[OK] 订阅申请已批准: %s\n", requestID)
			fmt.Println("  申请者现在可以订阅该频道")
			return
		}
		// 两个都失败了，显示权限变更的错误
		fmt.Printf("[FAILED] 批准失败: %v\n", err2)
		return
	}

	if !resp2.Success {
		// 权限变更批准失败，尝试订阅申请
		resp, err := connector.ApproveChannelSubscription(channelID, requestID)
		if err == nil && resp.Success {
			fmt.Printf("[OK] 订阅申请已批准: %s\n", requestID)
			fmt.Println("  申请者现在可以订阅该频道")
			return
		}
		// 两个都失败了，显示权限变更的错误消息
		fmt.Printf("[FAILED] 批准失败: %s\n", resp2.Message)
		return
	}

	fmt.Printf("[OK] 权限变更已批准: %s\n", requestID)
}

// handleRejectPermission 处理拒绝权限变更/订阅申请命令
func handleRejectPermission(connector *client.Connector, args []string) {
	if len(args) < 3 {
		fmt.Println("[FAILED] 参数错误: reject-permission <channel_id> <request_id> <reason>")
		return
	}

	channelID := args[0]
	requestID := args[1]
	reason := strings.Join(args[2:], " ")

	fmt.Printf("正在拒绝请求: %s...\n", requestID)

	// 首先尝试拒绝订阅申请（如果适用）
	resp, err := connector.RejectChannelSubscription(channelID, requestID, reason)
	if err == nil && resp.Success {
		fmt.Printf("[OK] 订阅申请已拒绝: %s\n", requestID)
		fmt.Printf("  拒绝理由: %s\n", reason)
		return
	}

	// 如果订阅申请拒绝失败，尝试拒绝权限变更
	resp2, err2 := connector.RejectPermissionChange(channelID, requestID, reason)
	if err2 != nil {
		fmt.Printf("[FAILED] 拒绝失败: %v\n", err2)
		return
	}

	if !resp2.Success {
		fmt.Printf("[FAILED] 拒绝失败: %s\n", resp2.Message)
		return
	}

	fmt.Printf("[OK] 权限变更已拒绝: %s\n", requestID)
	fmt.Printf("  拒绝理由: %s\n", reason)
}

// handleListPermissions 处理查看权限变更请求命令
func handleListPermissions(connector *client.Connector, args []string) {
	if len(args) != 1 {
		fmt.Println("[FAILED] 参数错误: list-permissions <channel_id>")
		return
	}

	channelID := args[0]

	fmt.Printf("正在获取频道 %s 的权限变更请求...\n", channelID)

	resp, err := connector.GetPermissionRequests(channelID)
	if err != nil {
		fmt.Printf("[FAILED] 获取失败: %v\n", err)
		return
	}

	if !resp.Success {
		fmt.Printf("[FAILED] 获取失败: %s\n", resp.Message)
		return
	}

	if len(resp.Requests) == 0 {
		fmt.Println("当前频道没有权限变更请求")
		return
	}

	fmt.Println("\n权限变更请求列表:")
	fmt.Println(strings.Repeat("-", 100))
	fmt.Printf("%-36s %-15s %-15s %-12s %-10s\n", "请求ID", "请求者", "变更类型", "目标ID", "状态")
	fmt.Println(strings.Repeat("-", 100))

	for _, req := range resp.Requests {
		fmt.Printf("%-36s %-15s %-15s %-12s %-10s\n",
			req.RequestId, req.RequesterId, req.ChangeType, req.TargetId, req.Status)

		if req.Reason != "" {
			fmt.Printf("  理由: %s\n", req.Reason)
		}
		if req.ApprovedBy != "" {
			fmt.Printf("  批准者: %s\n", req.ApprovedBy)
		}
		if req.RejectReason != "" {
			fmt.Printf("  拒绝理由: %s\n", req.RejectReason)
		}
		fmt.Printf("  创建时间: %s\n", time.Unix(req.CreatedAt, 0).Format("2006-01-02 15:04:05"))
		if req.ApprovedAt > 0 {
			fmt.Printf("  批准时间: %s\n", time.Unix(req.ApprovedAt, 0).Format("2006-01-02 15:04:05"))
		}
		fmt.Println(strings.Repeat("-", 100))
	}
}

// handleQueryEvidence 处理查询存证记录命令
func handleQueryEvidence(connector *client.Connector, args []string) {
	// 解析命令行参数
	var channelID, connectorID, flowID string
	var limit int32 = 50 // 默认查询50条

	i := 0
	for i < len(args) {
		switch args[i] {
		case "--channel":
			if i+1 < len(args) {
				channelID = strings.TrimSpace(args[i+1])
				i += 2
			} else {
				fmt.Println("[FAILED] --channel 参数需要提供频道ID")
				return
			}
		case "--connector":
			if i+1 < len(args) {
				connectorID = strings.TrimSpace(args[i+1])
				i += 2
			} else {
				fmt.Println("[FAILED] --connector 参数需要提供连接器ID")
				return
			}
		case "--flow":
			if i+1 < len(args) {
				flowID = strings.TrimSpace(args[i+1])
				i += 2
			} else {
				fmt.Println("[FAILED] --flow 参数需要提供业务流程ID")
				return
			}
		case "--limit":
			if i+1 < len(args) {
				limitStr := strings.TrimSpace(args[i+1])
				if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
					limit = int32(l)
				} else {
					fmt.Printf("[FAILED] 无效的limit值: %s\n", limitStr)
					return
				}
				i += 2
			} else {
				fmt.Println("[FAILED] --limit 参数需要提供数字")
				return
			}
		default:
			fmt.Printf("[FAILED] 未知参数: %s\n", args[i])
			fmt.Println("用法: query-evidence [--channel <channel_id>] [--connector <connector_id>] [--flow <flow_id>] [--limit <num>]")
			return
		}
	}

	// 至少需要指定一个查询条件
	if channelID == "" && connectorID == "" && flowID == "" {
		fmt.Println("[FAILED] 至少需要指定--channel、--connector或--flow参数")
		fmt.Println("用法: query-evidence [--channel <channel_id>] [--connector <connector_id>] [--flow <flow_id>] [--limit <num>]")
		fmt.Println("示例: query-evidence --channel channel-123 --limit 10")
		fmt.Println("示例: query-evidence --connector connector-A")
		fmt.Println("示例: query-evidence --flow flow-uuid-123")
		return
	}

	fmt.Printf("正在查询存证记录...\n")
	if channelID != "" {
		fmt.Printf("  频道ID: %s\n", channelID)
	}
	if connectorID != "" {
		fmt.Printf("  连接器ID: %s\n", connectorID)
	}
	if flowID != "" {
		fmt.Printf("  业务流程ID: %s\n", flowID)
	}
	fmt.Printf("  限制数量: %d\n", limit)

	// 根据查询类型调用不同的接口
	var records []*pb.EvidenceRecord
	var err error
	if flowID != "" {
		records, err = connector.QueryEvidenceByFlowID(flowID)
	} else {
		records, err = connector.QueryEvidence(channelID, connectorID, limit)
	}
	if err != nil {
		fmt.Printf("[FAILED] 查询失败: %v\n", err)
		return
	}

	if len(records) == 0 {
		fmt.Println("没有找到匹配的存证记录")
		return
	}

	fmt.Printf("\n找到 %d 条存证记录:\n", len(records))
	fmt.Println(strings.Repeat("=", 120))
	fmt.Printf("%-40s %-20s %-15s %-10s %-25s\n",
		"存证交易ID", "事件类型", "连接器ID", "频道ID", "存储时间")
	fmt.Println(strings.Repeat("=", 120))

	for _, record := range records {
		// 截断交易ID显示前16位
		shortTxID := record.EvidenceTxId
		if len(shortTxID) > 16 {
			shortTxID = shortTxID[:16] + "..."
		}

		// 格式化存储时间
		storedTime := time.Unix(record.StoredTimestamp, 0).Format("2006-01-02 15:04:05")

		fmt.Printf("%-40s %-20s %-15s %-10s %-25s\n",
			shortTxID,
			record.Evidence.EventType,
			record.Evidence.ConnectorId,
			record.Evidence.ChannelId,
			storedTime)

		// 显示数据哈希（截断）
		if record.Evidence.DataHash != "" {
			dataHashShort := record.Evidence.DataHash
			if len(dataHashShort) > 32 {
				dataHashShort = dataHashShort[:32] + "..."
			}
			fmt.Printf("  数据哈希: %s\n", dataHashShort)
		}

		// 显示元数据
		if len(record.Evidence.Metadata) > 0 {
			fmt.Printf("  元数据: ")
			var metadataStrings []string
			for k, v := range record.Evidence.Metadata {
				metadataStrings = append(metadataStrings, fmt.Sprintf("%s=%s", k, v))
			}
			fmt.Printf("%s\n", strings.Join(metadataStrings, ", "))
		}

		fmt.Println(strings.Repeat("-", 120))
	}
}

// handleChannels 查看当前连接器参与的频道信息
func handleChannels(connector *client.Connector) {
	localChannels := connector.ListLocalChannels()
	if len(localChannels) == 0 {
		fmt.Println("当前没有记录到任何频道（可能尚未创建或收到频道通知）。")
		return
	}

	fmt.Println("当前参与的频道列表：")
	fmt.Println(strings.Repeat("-", 80))
	fmt.Printf("%-40s %-20s %-10s\n", "频道ID", "数据主题", "创建者")
	fmt.Println(strings.Repeat("-", 80))
	
	for _, localCh := range localChannels {
		// 实时从内核获取最新的频道信息
		channelInfo, err := connector.GetChannelInfo(localCh.ChannelID)
		if err != nil || channelInfo == nil || !channelInfo.Found {
			// 如果获取失败，使用本地缓存的信息
			fmt.Printf("%-40s %-20s %-10s\n", localCh.ChannelID, localCh.DataTopic, localCh.CreatorID)
			fmt.Printf("  参与者: %v (本地缓存，可能已过时)\n", localCh.Participants)
			if localCh.CreatedAt > 0 {
				fmt.Printf("  创建时间: %s\n", time.Unix(localCh.CreatedAt, 0).Format("2006-01-02 15:04:05"))
			}
		} else {
			// 使用最新的频道信息
			fmt.Printf("%-40s %-20s %-10s\n", channelInfo.ChannelId, channelInfo.DataTopic, channelInfo.CreatorId)
			fmt.Printf("  发送方: %v\n", channelInfo.SenderIds)
			fmt.Printf("  接收方: %v\n", channelInfo.ReceiverIds)
			fmt.Printf("  加密: %v\n", channelInfo.Encrypted)
			fmt.Printf("  状态: %s\n", channelInfo.Status)
			if channelInfo.CreatedAt > 0 {
				fmt.Printf("  创建时间: %s\n", time.Unix(channelInfo.CreatedAt, 0).Format("2006-01-02 15:04:05"))
			}
			if channelInfo.LastActivity > 0 {
				fmt.Printf("  最后活动: %s\n", time.Unix(channelInfo.LastActivity, 0).Format("2006-01-02 15:04:05"))
			}
			
			// 更新本地缓存
			connector.RecordChannelFromNotification(&pb.ChannelNotification{
				ChannelId:         channelInfo.ChannelId,
				CreatorId:         channelInfo.CreatorId,
				SenderIds:         channelInfo.SenderIds,
				ReceiverIds:       channelInfo.ReceiverIds,
				Encrypted:        channelInfo.Encrypted,
				DataTopic:        channelInfo.DataTopic,
				CreatedAt:        channelInfo.CreatedAt,
			})
		}
		fmt.Println(strings.Repeat("-", 80))
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

	// 加载数据目录（支持文件引用）
	dataCatalog := loadDataCatalog(&config)
	if len(dataCatalog) > 0 {
		config.Connector.DataCatalog = dataCatalog
	}

	return &config, nil
}

// loadDataCatalog 加载数据目录
// 优先使用内联列表，否则尝试从文件加载
// 支持 JSON 数组格式和纯文本格式（每行一个数据项）
func loadDataCatalog(config *Config) []string {
	// 优先使用内联列表
	if len(config.Connector.DataCatalog) > 0 {
		return config.Connector.DataCatalog
	}

	// 尝试从文件加载
	if config.Connector.DataCatalogFile == "" {
		return nil
	}

	filePath := config.Connector.DataCatalogFile
	data, err := os.ReadFile(filePath)
	if err != nil {
		log.Printf("[WARN] Failed to read data catalog file %s: %v", filePath, err)
		return nil
	}

	trimmed := strings.TrimSpace(string(data))

	// 尝试 JSON 数组格式
	if strings.HasPrefix(trimmed, "[") {
		var items []string
		if err := json.Unmarshal(data, &items); err != nil {
			log.Printf("[WARN] Failed to parse data catalog JSON from %s: %v", filePath, err)
			return nil
		}
		return items
	}

	// 纯文本格式：每行一个数据项
	var items []string
	scanner := bufio.NewScanner(strings.NewReader(trimmed))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			items = append(items, line)
		}
	}
	return items
}

// removeDuplicates 移除字符串切片中的重复元素，保持原有顺序
func removeDuplicates(slice []string) []string {
	keys := make(map[string]bool)
	var result []string
	for _, item := range slice {
		if !keys[item] {
			keys[item] = true
			result = append(result, item)
		}
	}
	return result
}

// ControlMessage 控制消息结构（与kernel中的定义保持一致）
type ControlMessage struct {
	MessageType string    `json:"message_type"`
	Timestamp   time.Time `json:"timestamp"`
	SenderID    string    `json:"sender_id"`

	// 权限变更相关字段
	PermissionRequest *PermissionRequestMessage `json:"permission_request,omitempty"`
	PermissionResult  *PermissionResultMessage  `json:"permission_result,omitempty"`

	// 频道提议相关字段
	ChannelProposal *ChannelProposalMessage `json:"channel_proposal,omitempty"`
}

// PermissionRequestMessage 权限变更请求消息
type PermissionRequestMessage struct {
	RequestID  string `json:"request_id"`
	ChannelID  string `json:"channel_id"`
	ChangeType string `json:"change_type"`
	TargetID   string `json:"target_id"`
	Reason     string `json:"reason"`
}

// PermissionResultMessage 权限变更结果消息
type PermissionResultMessage struct {
	RequestID    string `json:"request_id"`
	ChannelID    string `json:"channel_id"`
	Action       string `json:"action"`
	ApproverID   string `json:"approver_id"`
	RejectReason string `json:"reject_reason,omitempty"`
}

// ChannelProposalMessage 频道提议消息
type ChannelProposalMessage struct {
	ProposalID  string   `json:"proposal_id"`
	ChannelID   string   `json:"channel_id"`
	CreatorID   string   `json:"creator_id"`
	SenderIDs   []string `json:"sender_ids"`
	ReceiverIDs []string `json:"receiver_ids"`
	DataTopic   string   `json:"data_topic"`
	Reason      string   `json:"reason"`
}

// handleControlMessage 处理控制消息
func handleControlMessage(packet *pb.DataPacket) error {
	var message ControlMessage
	if err := json.Unmarshal(packet.Payload, &message); err != nil {
		return fmt.Errorf("failed to unmarshal control message: %w", err)
	}

	switch message.MessageType {
	case "permission_request":
		if message.PermissionRequest != nil {
			fmt.Printf("🔐 [控制消息] 权限变更请求:\n")
			fmt.Printf("   请求ID: %s\n", message.PermissionRequest.RequestID)
			fmt.Printf("   频道ID: %s\n", message.PermissionRequest.ChannelID)
			fmt.Printf("   变更类型: %s\n", message.PermissionRequest.ChangeType)
			fmt.Printf("   目标ID: %s\n", message.PermissionRequest.TargetID)
			fmt.Printf("   请求者: %s\n", message.SenderID)
			if message.PermissionRequest.Reason != "" {
				fmt.Printf("   理由: %s\n", message.PermissionRequest.Reason)
			}
		}

	case "permission_result":
		if message.PermissionResult != nil {
			action := "批准"
			if message.PermissionResult.Action == "rejected" {
				action = "拒绝"
			}
			fmt.Printf("✅ [控制消息] 权限变更%s:\n", action)
			fmt.Printf("   请求ID: %s\n", message.PermissionResult.RequestID)
			fmt.Printf("   频道ID: %s\n", message.PermissionResult.ChannelID)
			fmt.Printf("   批准者: %s\n", message.PermissionResult.ApproverID)
			if message.PermissionResult.RejectReason != "" {
				fmt.Printf("   拒绝理由: %s\n", message.PermissionResult.RejectReason)
			}
		}

	case "channel_proposal":
		if message.ChannelProposal != nil {
			fmt.Printf("[INFO] [控制消息] 频道提议广播:\n")
			fmt.Printf("   提议ID: %s\n", message.ChannelProposal.ProposalID)
			fmt.Printf("   频道ID: %s\n", message.ChannelProposal.ChannelID)
			fmt.Printf("   创建者: %s\n", message.ChannelProposal.CreatorID)
			fmt.Printf("   发送方: %v\n", message.ChannelProposal.SenderIDs)
			fmt.Printf("   接收方: %v\n", message.ChannelProposal.ReceiverIDs)
			if message.ChannelProposal.Reason != "" {
				fmt.Printf("   理由: %s\n", message.ChannelProposal.Reason)
			}
		}

	default:
		fmt.Printf("[WARN] [控制消息] 未知类型: %s\n", message.MessageType)
	}

	return nil
}


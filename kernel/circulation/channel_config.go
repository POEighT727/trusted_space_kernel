package circulation

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ChannelConfigFile 频道配置文件结构
type ChannelConfigFile struct {
	ChannelName    string          `json:"channel_name"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	CreatorID      string          `json:"creator_id"`
	ApproverID     string          `json:"approver_id"`
	SenderIDs      []string        `json:"sender_ids"`
	ReceiverIDs    []string        `json:"receiver_ids"`
	DataTopic      string          `json:"data_topic"`
	Encrypted      bool            `json:"encrypted"`
	EvidenceConfig *EvidenceConfig `json:"evidence_config"`
	CreatedAt      *time.Time      `json:"created_at,omitempty"`
	UpdatedAt      *time.Time      `json:"updated_at,omitempty"`
	Version        int             `json:"version"`
}

// ChannelConfigManager 频道配置管理器
type ChannelConfigManager struct {
	configDir     string        // 配置文件目录
	configs       map[string]*ChannelConfigFile // 内存中的配置缓存
	mu            sync.RWMutex  // 读写锁
	defaultConfig *EvidenceConfig // 默认存证配置
}

// NewChannelConfigManager 创建新的频道配置管理器
func NewChannelConfigManager(configDir string) (*ChannelConfigManager, error) {
	if configDir == "" {
		configDir = "./channel_configs" // 默认配置目录
	}

	// 创建配置目录
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create config directory: %v", err)
	}

	manager := &ChannelConfigManager{
		configDir: configDir,
		configs:   make(map[string]*ChannelConfigFile),
		defaultConfig: &EvidenceConfig{
			Mode:           EvidenceModeNone,
			Strategy:       EvidenceStrategyAll,
			BackupEnabled:  false,
			RetentionDays:  30,
			CompressData:   true,
			CustomSettings: make(map[string]string),
		},
	}

	// 加载现有配置
	if err := manager.loadAllConfigs(); err != nil {
		log.Printf("⚠ Failed to load existing configs: %v", err)
	}

	return manager, nil
}

// SaveConfig 保存频道配置到文件
func (cm *ChannelConfigManager) SaveConfig(config *ChannelConfigFile) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if config.ChannelName == "" {
		return fmt.Errorf("channel name cannot be empty")
	}

	// 更新时间戳和版本
	now := time.Now()
	config.UpdatedAt = &now
	if config.Version == 0 {
		config.Version = 1
		config.CreatedAt = &now
	} else {
		config.Version++
	}

	// 序列化为JSON
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %v", err)
	}

	// 写入文件
	filename := fmt.Sprintf("%s.json", config.ChannelName)
	filepath := filepath.Join(cm.configDir, filename)

	if err := ioutil.WriteFile(filepath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %v", err)
	}

	// 更新内存缓存
	cm.configs[config.ChannelName] = config

	log.Printf("✓ Channel config saved: %s", config.ChannelName)
	return nil
}

// LoadConfig 从文件加载频道配置
func (cm *ChannelConfigManager) LoadConfig(channelID string) (*ChannelConfigFile, error) {
	cm.mu.RLock()
	if config, exists := cm.configs[channelID]; exists {
		cm.mu.RUnlock()
		return config, nil
	}
	cm.mu.RUnlock()

	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 从文件加载
	filename := fmt.Sprintf("%s.json", channelID)
	filepath := filepath.Join(cm.configDir, filename)

	data, err := ioutil.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %v", err)
	}

	var config ChannelConfigFile
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %v", err)
	}

	// 缓存到内存
	cm.configs[channelID] = &config

	return &config, nil
}

// DeleteConfig 删除频道配置
func (cm *ChannelConfigManager) DeleteConfig(channelID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	filename := fmt.Sprintf("%s.json", channelID)
	filepath := filepath.Join(cm.configDir, filename)

	// 删除文件
	if err := os.Remove(filepath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete config file: %v", err)
	}

	// 从内存中删除
	delete(cm.configs, channelID)

	log.Printf("✓ Channel config deleted: %s", channelID)
	return nil
}

// ListConfigs 列出所有频道配置
func (cm *ChannelConfigManager) ListConfigs() []*ChannelConfigFile {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	configs := make([]*ChannelConfigFile, 0, len(cm.configs))
	for _, config := range cm.configs {
		configs = append(configs, config)
	}

	return configs
}

// CreateConfigFromChannel 从Channel创建配置文件
func (cm *ChannelConfigManager) CreateConfigFromChannel(channel *Channel, name, description string) *ChannelConfigFile {
	now := time.Now()
	config := &ChannelConfigFile{
		ChannelName:    channel.ChannelName,
		Name:           name,
		Description:    description,
		CreatorID:      channel.CreatorID,
		ApproverID:     channel.ApproverID,
		SenderIDs:      make([]string, len(channel.SenderIDs)),
		ReceiverIDs:    make([]string, len(channel.ReceiverIDs)),
		DataTopic:      channel.DataTopic,
		Encrypted:      channel.Encrypted,
		EvidenceConfig: channel.EvidenceConfig,
		CreatedAt:      &channel.CreatedAt,
		UpdatedAt:      &now,
		Version:        1,
	}

	// 复制切片
	copy(config.SenderIDs, channel.SenderIDs)
	copy(config.ReceiverIDs, channel.ReceiverIDs)

	return config
}

// CreateChannelFromConfig 从配置文件创建Channel
func (cm *ChannelConfigManager) CreateChannelFromConfig(config *ChannelConfigFile) *Channel {
	// 处理创建时间
	createdAt := time.Now()
	if config.CreatedAt != nil {
		createdAt = *config.CreatedAt
	}

	channel := &Channel{
		ChannelID:      uuid.New().String(),
		ChannelName:    config.ChannelName,
		CreatorID:      config.CreatorID,
		ApproverID:     config.ApproverID,
		SenderIDs:      make([]string, len(config.SenderIDs)),
		ReceiverIDs:    make([]string, len(config.ReceiverIDs)),
		Encrypted:      config.Encrypted,
		DataTopic:      config.DataTopic,
		Status:         ChannelStatusActive,
		CreatedAt:      createdAt,
		LastActivity:   time.Now(),
		EvidenceConfig: config.EvidenceConfig,
		dataQueue:      make(chan *DataPacket, 1000),
		subscribers:    make(map[string]chan *DataPacket),
		buffer:         make([]*DataPacket, 0),
		maxBufferSize:  10000,
		permissionRequests: make([]*PermissionChangeRequest, 0),
	}

	// 复制切片
	copy(channel.SenderIDs, config.SenderIDs)
	copy(channel.ReceiverIDs, config.ReceiverIDs)

	return channel
}

// SetDefaultEvidenceConfig 设置默认存证配置
func (cm *ChannelConfigManager) SetDefaultEvidenceConfig(config *EvidenceConfig) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if config != nil {
		cm.defaultConfig = config
		log.Printf("✓ Default evidence config updated")
	}
}

// GetDefaultEvidenceConfig 获取默认存证配置
func (cm *ChannelConfigManager) GetDefaultEvidenceConfig() *EvidenceConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	// 返回配置的副本以避免外部修改
	defaultConfig := &EvidenceConfig{
		Mode:           cm.defaultConfig.Mode,
		Strategy:       cm.defaultConfig.Strategy,
		ConnectorID:    cm.defaultConfig.ConnectorID,
		BackupEnabled:  cm.defaultConfig.BackupEnabled,
		RetentionDays:  cm.defaultConfig.RetentionDays,
		CompressData:   cm.defaultConfig.CompressData,
		CustomSettings: make(map[string]string),
	}

	for k, v := range cm.defaultConfig.CustomSettings {
		defaultConfig.CustomSettings[k] = v
	}

	return defaultConfig
}

// loadAllConfigs 加载所有配置文件到内存
func (cm *ChannelConfigManager) loadAllConfigs() error {
	files, err := ioutil.ReadDir(cm.configDir)
	if err != nil {
		return err
	}

	loadedCount := 0
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}

		channelID := file.Name()[:len(file.Name())-5] // 移除.json后缀
		if _, err := cm.LoadConfig(channelID); err != nil {
			log.Printf("⚠ Failed to load config for channel %s: %v", channelID, err)
			continue
		}
		loadedCount++
	}

	if loadedCount > 0 {
		log.Printf("✓ Loaded %d channel configs from %s", loadedCount, cm.configDir)
	}
	return nil
}

// ValidateEvidenceConfig 验证存证配置
func (cm *ChannelConfigManager) ValidateEvidenceConfig(config *EvidenceConfig) error {
	if config == nil {
		return nil // nil配置是允许的
	}

	// 验证存证方式
	switch config.Mode {
	case EvidenceModeNone, EvidenceModeInternal, EvidenceModeExternal, EvidenceModeHybrid:
		// 有效的存证方式
	default:
		return fmt.Errorf("invalid evidence mode: %s", config.Mode)
	}

	// 验证存证策略
	switch config.Strategy {
	case EvidenceStrategyAll, EvidenceStrategyData, EvidenceStrategyControl, EvidenceStrategyImportant:
		// 有效的存证策略
	default:
		return fmt.Errorf("invalid evidence strategy: %s", config.Strategy)
	}

	// 如果使用外部存证连接器，检查连接器ID
	if config.Mode == EvidenceModeExternal || config.Mode == EvidenceModeHybrid {
		if config.ConnectorID == "" {
			return fmt.Errorf("connector ID is required for external evidence mode")
		}
	}

	// 验证保留天数
	if config.RetentionDays <= 0 {
		config.RetentionDays = 30 // 设置默认值
	}

	return nil
}

// GetConfigDir 获取配置目录路径
func (cm *ChannelConfigManager) GetConfigDir() string {
	return cm.configDir
}

// CleanupOldConfigs 清理旧的配置文件（保留最近的版本）
func (cm *ChannelConfigManager) CleanupOldConfigs(maxVersions int) error {
	if maxVersions <= 0 {
		maxVersions = 5 // 默认保留5个版本
	}

	// 按频道分组配置文件
	channelFiles := make(map[string][]os.FileInfo)

	files, err := ioutil.ReadDir(cm.configDir)
	if err != nil {
		return err
	}

	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		channelID := file.Name()[:len(file.Name())-5]
		channelFiles[channelID] = append(channelFiles[channelID], file)
	}

	cleanupCount := 0
	for channelID, files := range channelFiles {
		if len(files) > maxVersions {
			channelCleanupCount := 0
			// 按修改时间排序（最新的在前）
			for i := 0; i < len(files)-1; i++ {
				for j := i + 1; j < len(files); j++ {
					if files[i].ModTime().Before(files[j].ModTime()) {
						files[i], files[j] = files[j], files[i]
					}
				}
			}

			// 删除多余的文件
			for i := maxVersions; i < len(files); i++ {
				filepath := filepath.Join(cm.configDir, files[i].Name())
				if err := os.Remove(filepath); err != nil {
					log.Printf("⚠ Failed to remove old config file %s: %v", filepath, err)
				} else {
					channelCleanupCount++
					cleanupCount++
				}
			}

			if channelCleanupCount > 0 {
				log.Printf("🧹 Cleaned up %d old config files for channel %s", channelCleanupCount, channelID)
			}
		}
	}

	if cleanupCount > 0 {
		log.Printf("🧹 Total cleaned up %d old channel config files", cleanupCount)
	}

	return nil
}

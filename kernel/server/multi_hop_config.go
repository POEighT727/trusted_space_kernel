package server

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

// MultiHopConfigFile 多跳链路配置文件结构
type MultiHopConfigFile struct {
	RouteName  string         `json:"route_name"`
	Name       string         `json:"name"`
	Description string        `json:"description"`
	CreatorID  string         `json:"creator_id"`
	Hops       []MultiHop     `json:"hops"`
	Enabled    bool           `json:"enabled"`
	CreatedAt  *time.Time    `json:"created_at,omitempty"`
	UpdatedAt  *time.Time    `json:"updated_at,omitempty"`
	Version    int           `json:"version"`
}

// MultiHop 单跳配置
type MultiHop struct {
	HopID       int    `json:"hop_id"`
	FromKernel  string `json:"from_kernel"`
	ToKernel    string `json:"to_kernel"`
	ToAddress   string `json:"to_address"`
	ToPort      int    `json:"to_port"`
	AutoConnect bool   `json:"auto_connect"`
}

// MultiHopRoute 多跳路由（运行时使用）
type MultiHopRoute struct {
	RouteID    string
	Config     *MultiHopConfigFile
	Status     string // "pending", "active", "inactive", "failed"
	CreatedAt  time.Time
	UpdatedAt time.Time
}

// MultiHopConfigManager 多跳链路配置管理器
type MultiHopConfigManager struct {
	configDir     string
	configs       map[string]*MultiHopConfigFile
	routes        map[string]*MultiHopRoute
	mu            sync.RWMutex
}

// NewMultiHopConfigManager 创建新的多跳链路配置管理器
func NewMultiHopConfigManager(configDir string) (*MultiHopConfigManager, error) {
	if configDir == "" {
		configDir = "./kernel_configs" // 默认配置目录
	}

	// 创建配置目录
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create config directory: %v", err)
	}

	manager := &MultiHopConfigManager{
		configDir: configDir,
		configs:   make(map[string]*MultiHopConfigFile),
		routes:    make(map[string]*MultiHopRoute),
	}

	// 加载现有配置
	if err := manager.loadAllConfigs(); err != nil {
		log.Printf("⚠ Failed to load existing multi-hop configs: %v", err)
	}

	return manager, nil
}

// SaveConfig 保存多跳链路配置到文件
func (m *MultiHopConfigManager) SaveConfig(config *MultiHopConfigFile) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if config.RouteName == "" {
		return fmt.Errorf("route name cannot be empty")
	}

	// 验证跳配置
	if len(config.Hops) == 0 {
		return fmt.Errorf("at least one hop is required")
	}

	// 验证每一跳的配置
	for i, hop := range config.Hops {
		if hop.FromKernel == "" {
			return fmt.Errorf("hop %d: from_kernel cannot be empty", i+1)
		}
		if hop.ToKernel == "" {
			return fmt.Errorf("hop %d: to_kernel cannot be empty", i+1)
		}
		if hop.ToAddress == "" {
			return fmt.Errorf("hop %d: to_address cannot be empty", i+1)
		}
		if hop.ToPort == 0 {
			return fmt.Errorf("hop %d: to_port cannot be zero", i+1)
		}
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
	filename := fmt.Sprintf("%s.json", config.RouteName)
	filepath := filepath.Join(m.configDir, filename)

	if err := ioutil.WriteFile(filepath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %v", err)
	}

	// 更新内存缓存
	m.configs[config.RouteName] = config

	log.Printf("✓ Multi-hop route config saved: %s", config.RouteName)
	return nil
}

// LoadConfig 从文件加载多跳链路配置
func (m *MultiHopConfigManager) LoadConfig(routeName string) (*MultiHopConfigFile, error) {
	m.mu.RLock()
	if config, exists := m.configs[routeName]; exists {
		m.mu.RUnlock()
		return config, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	// 从文件加载
	filename := fmt.Sprintf("%s.json", routeName)
	filepath := filepath.Join(m.configDir, filename)

	data, err := ioutil.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %v", err)
	}

	var config MultiHopConfigFile
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %v", err)
	}

	// 缓存到内存
	m.configs[routeName] = &config

	return &config, nil
}

// DeleteConfig 删除多跳链路配置
func (m *MultiHopConfigManager) DeleteConfig(routeName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	filename := fmt.Sprintf("%s.json", routeName)
	filepath := filepath.Join(m.configDir, filename)

	// 删除文件
	if err := os.Remove(filepath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete config file: %v", err)
	}

	// 从内存中删除
	delete(m.configs, routeName)
	delete(m.routes, routeName)

	log.Printf("✓ Multi-hop route config deleted: %s", routeName)
	return nil
}

// ListConfigs 列出所有多跳链路配置
func (m *MultiHopConfigManager) ListConfigs() []*MultiHopConfigFile {
	m.mu.RLock()
	defer m.mu.RUnlock()

	configs := make([]*MultiHopConfigFile, 0, len(m.configs))
	for _, config := range m.configs {
		configs = append(configs, config)
	}

	return configs
}

// ListRoutes 列出所有活跃的路由
func (m *MultiHopConfigManager) ListRoutes() []*MultiHopRoute {
	m.mu.RLock()
	defer m.mu.RUnlock()

	routes := make([]*MultiHopRoute, 0, len(m.routes))
	for _, route := range m.routes {
		routes = append(routes, route)
	}

	return routes
}

// GetRoute 获取指定路由
func (m *MultiHopConfigManager) GetRoute(routeName string) (*MultiHopRoute, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	route, exists := m.routes[routeName]
	return route, exists
}

// CreateRouteFromConfig 从配置文件创建路由
func (m *MultiHopConfigManager) CreateRouteFromConfig(config *MultiHopConfigFile) *MultiHopRoute {
	now := time.Now()
	route := &MultiHopRoute{
		RouteID:    uuid.New().String(),
		Config:     config,
		Status:     "pending",
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	m.mu.Lock()
	m.routes[config.RouteName] = route
	m.mu.Unlock()

	return route
}

// UpdateRouteStatus 更新路由状态
func (m *MultiHopConfigManager) UpdateRouteStatus(routeName string, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	route, exists := m.routes[routeName]
	if !exists {
		return fmt.Errorf("route %s not found", routeName)
	}

	route.Status = status
	route.UpdatedAt = time.Now()

	log.Printf("✓ Route %s status updated to: %s", routeName, status)
	return nil
}

// GetConfigDir 获取配置目录路径
func (m *MultiHopConfigManager) GetConfigDir() string {
	return m.configDir
}

// loadAllConfigs 加载所有配置文件到内存
func (m *MultiHopConfigManager) loadAllConfigs() error {
	files, err := ioutil.ReadDir(m.configDir)
	if err != nil {
		return err
	}

	loadedCount := 0
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}

		routeName := file.Name()[:len(file.Name())-5] // 移除.json后缀
		if _, err := m.LoadConfig(routeName); err != nil {
			log.Printf("⚠ Failed to load config for route %s: %v", routeName, err)
			continue
		}
		loadedCount++
	}

	if loadedCount > 0 {
		log.Printf("✓ Loaded %d multi-hop route configs from %s", loadedCount, m.configDir)
	}
	return nil
}

// ValidateConfig 验证多跳链路配置
func (m *MultiHopConfigManager) ValidateConfig(config *MultiHopConfigFile) error {
	if config == nil {
		return fmt.Errorf("config cannot be nil")
	}

	if config.RouteName == "" {
		return fmt.Errorf("route name cannot be empty")
	}

	if len(config.Hops) == 0 {
		return fmt.Errorf("at least one hop is required")
	}

	// 验证跳的顺序
	prevKernel := ""
	for i, hop := range config.Hops {
		if hop.HopID != i+1 {
			return fmt.Errorf("hop %d: hop_id should be %d", i+1, i+1)
		}

		// 验证跳的连续性
		if prevKernel != "" && hop.FromKernel != prevKernel {
			return fmt.Errorf("hop %d: from_kernel (%s) should match previous hop's to_kernel (%s)",
				i+1, hop.FromKernel, prevKernel)
		}

		// 验证端口
		if hop.ToPort <= 0 || hop.ToPort > 65535 {
			return fmt.Errorf("hop %d: invalid port %d", i+1, hop.ToPort)
		}

		prevKernel = hop.ToKernel
	}

	return nil
}

// GetEnabledConfigs 获取所有启用的配置
func (m *MultiHopConfigManager) GetEnabledConfigs() []*MultiHopConfigFile {
	m.mu.RLock()
	defer m.mu.RUnlock()

	configs := make([]*MultiHopConfigFile, 0)
	for _, config := range m.configs {
		if config.Enabled {
			configs = append(configs, config)
		}
	}

	return configs
}

// GetRoutePath 获取路由的完整路径描述
func (m *MultiHopConfigManager) GetRoutePath(routeName string) string {
	config, err := m.LoadConfig(routeName)
	if err != nil {
		return ""
	}

	path := ""
	for i, hop := range config.Hops {
		if i > 0 {
			path += " → "
		}
		path += hop.FromKernel + "(" + hop.ToAddress + ":" + fmt.Sprintf("%d", hop.ToPort) + ")"
		path += "→" + hop.ToKernel
	}

	return path
}

// GetNextHop 获取从指定内核到目标内核的下一跳信息
// 返回下一跳内核ID、地址、端口，以及当前是第几跳、总共多少跳
func (m *MultiHopConfigManager) GetNextHop(currentKernelID, targetKernelID string) (nextKernelID, nextAddress string, nextPort int, hopIndex, totalHops int, found bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	log.Printf("🔍 DEBUG GetNextHop: looking for path from %s to %s", currentKernelID, targetKernelID)

	// 遍历所有路由配置
	for _, config := range m.configs {
		log.Printf("🔍 DEBUG GetNextHop: checking route %s, enabled=%v", config.RouteName, config.Enabled)
		if !config.Enabled {
			continue
		}

		totalHops = len(config.Hops)

		// 查找当前内核所在的位置
		for i, hop := range config.Hops {
			log.Printf("🔍 DEBUG GetNextHop: hop[%d]: from=%s, to=%s", i, hop.FromKernel, hop.ToKernel)
			if hop.FromKernel == currentKernelID {
				// 如果当前内核就是要到达的目标内核
				if hop.ToKernel == targetKernelID {
					// 当前跳直接到目标，返回当前跳的 ToKernel
					log.Printf("🔍 DEBUG GetNextHop: FOUND - direct hop from %s to %s", currentKernelID, targetKernelID)
					return hop.ToKernel, hop.ToAddress, hop.ToPort, i + 1, totalHops, true
				}

				// 检查目标是否在后续路径中
				for j := i + 1; j < len(config.Hops); j++ {
					if config.Hops[j].ToKernel == targetKernelID {
						// 找到路径！返回当前跳的下一跳（即当前跳的 ToKernel）
						log.Printf("🔍 DEBUG GetNextHop: FOUND - multi-hop via %s", hop.ToKernel)
						return hop.ToKernel, hop.ToAddress, hop.ToPort, i + 1, totalHops, true
					}
				}
			}
		}
	}

	log.Printf("🔍 DEBUG GetNextHop: NO PATH FOUND")
	return "", "", 0, 0, 0, false
}

// GetRouteForKernelPair 获取两个内核之间的路由配置
// 返回路由名称和跳数信息
func (m *MultiHopConfigManager) GetRouteForKernelPair(currentKernelID, targetKernelID string) (routeName string, hopIndex, totalHops int, found bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, config := range m.configs {
		if !config.Enabled {
			continue
		}

		totalHops = len(config.Hops)

		// 查找当前内核到目标内核的路径
		for i, hop := range config.Hops {
			if hop.FromKernel == currentKernelID {
				// 检查目标是否在后续路径中
				for j := i + 1; j < len(config.Hops); j++ {
					if config.Hops[j].ToKernel == targetKernelID {
						return config.RouteName, i + 1, totalHops, true
					}
				}

				// 如果当前内核就是目标
				if hop.ToKernel == targetKernelID {
					return config.RouteName, i + 1, totalHops, true
				}
			}
		}
	}

	return "", 0, 0, false
}

package control

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// BootstrapTokenStatus Token 状态
type BootstrapTokenStatus string

const (
	BootstrapTokenStatusValid     BootstrapTokenStatus = "valid"     // 有效未使用
	BootstrapTokenStatusUsed      BootstrapTokenStatus = "used"      // 已使用
	BootstrapTokenStatusExpired   BootstrapTokenStatus = "expired"   // 已过期
	BootstrapTokenStatusRevoked   BootstrapTokenStatus = "revoked"   // 已撤销
)

// BootstrapToken 一次性注册令牌
type BootstrapToken struct {
	Code        string                  `yaml:"code"`         // Token 字符串
	ConnectorID string                  `yaml:"connector_id"` // 绑定的连接器ID
	Status      BootstrapTokenStatus   `yaml:"status"`       // 状态：valid/used/expired/revoked
	UsedAt      *time.Time             `yaml:"used_at,omitempty"`      // 使用时间
	UsedBy      string                 `yaml:"used_by,omitempty"`      // 实际使用的连接器ID（应与 ConnectorID 一致）
	CreatedAt   time.Time              `yaml:"created_at"`    // 创建时间
	ExpiresAt   *time.Time             `yaml:"expires_at,omitempty"`   // 过期时间（nil 表示永不过期）
}

// IsValid 检查 Token 是否有效（未使用、未过期、未撤销）
func (t *BootstrapToken) IsValid() bool {
	if t.Status != BootstrapTokenStatusValid {
		return false
	}
	if t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt) {
		return false
	}
	return true
}

// BootstrapConfig Bootstrap 配置
type BootstrapConfig struct {
	Enabled      bool              `yaml:"enabled"`       // 是否启用 Token 验证
	DefaultExpiry string           `yaml:"default_expiry,omitempty"` // 默认有效期字符串（如 "168h"）
	DefaultExpiryDuration time.Duration `yaml:"-"` // 解析后的 duration
	Tokens       []*BootstrapToken `yaml:"tokens"`        // Token 列表
}

// NewBootstrapConfig 创建默认配置
func NewBootstrapConfig() *BootstrapConfig {
	return &BootstrapConfig{
		Enabled:       true,
		DefaultExpiry: "168h", // 默认 7 天
		Tokens:        make([]*BootstrapToken, 0),
	}
}

// GenerateBootstrapTokenCode 生成随机的 Token 字符串
// 格式：TSK-BOOT-{8位随机hex}-{时间戳后4位hex}
func GenerateBootstrapTokenCode() (string, error) {
	// 生成 4 字节随机数 (8 位 hex 字符)
	randomBytes := make([]byte, 4)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("failed to generate random token: %w", err)
	}
	randomHex := hex.EncodeToString(randomBytes)

	// 取时间戳后 4 字节（8 位 hex）
	timestampHex := fmt.Sprintf("%08x", time.Now().UnixNano()/1e6%0x100000000)[:8]

	return fmt.Sprintf("TSK-BOOT-%s-%s", randomHex, timestampHex), nil
}

// TokenManager Token 管理器
type TokenManager struct {
	config *BootstrapConfig
	configFile string
	mu     sync.RWMutex
}

// NewTokenManager 创建 Token 管理器
func NewTokenManager(configFile string) *TokenManager {
	return &TokenManager{
		config:     NewBootstrapConfig(),
		configFile: configFile,
	}
}

// LoadConfig 从 YAML 文件加载配置
func (m *TokenManager) LoadConfig() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.configFile)
	if err != nil {
		// 文件不存在，使用默认配置
		if os.IsNotExist(err) {
			m.config = NewBootstrapConfig()
			return nil
		}
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg BootstrapConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse config YAML: %w", err)
	}

	// 解析 DefaultExpiry
	if cfg.DefaultExpiry != "" {
		dur, err := time.ParseDuration(cfg.DefaultExpiry)
		if err != nil {
			return fmt.Errorf("invalid default_expiry format: %w", err)
		}
		cfg.DefaultExpiryDuration = dur
	} else {
		cfg.DefaultExpiryDuration = 168 * time.Hour // 默认 7 天
	}

	// 确保 Tokens 不为 nil
	if cfg.Tokens == nil {
		cfg.Tokens = make([]*BootstrapToken, 0)
	}

	m.config = &cfg
	return nil
}

// SetDefaultExpiryDuration 设置默认有效期（用于从外部配置覆盖）
func (m *TokenManager) SetDefaultExpiryDuration(dur time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config.DefaultExpiryDuration = dur
}

// SaveConfig 保存配置到 YAML 文件
func (m *TokenManager) SaveConfig() error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	data, err := yaml.Marshal(m.config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(m.configFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// GenerateToken 生成新的 Bootstrap Token
func (m *TokenManager) GenerateToken(connectorID string, expiresIn *time.Duration) (*BootstrapToken, error) {
	m.mu.Lock()

	code, err := GenerateBootstrapTokenCode()
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}

	now := time.Now()
	token := &BootstrapToken{
		Code:        code,
		ConnectorID: connectorID,
		Status:      BootstrapTokenStatusValid,
		CreatedAt:   now,
	}

	// 设置过期时间
	if expiresIn != nil && *expiresIn > 0 {
		expiry := now.Add(*expiresIn)
		token.ExpiresAt = &expiry
	} else if m.config.DefaultExpiryDuration > 0 {
		expiry := now.Add(m.config.DefaultExpiryDuration)
		token.ExpiresAt = &expiry
	}

	// 检查是否已存在相同 code 的 token（极小概率冲突，重试）
	for _, existing := range m.config.Tokens {
		if existing.Code == code {
			m.mu.Unlock()
			// 递归重试
			return m.GenerateToken(connectorID, expiresIn)
		}
	}

	m.config.Tokens = append(m.config.Tokens, token)

	// 复制配置用于持久化（在锁内快照）
	configToSave := m.config
	m.mu.Unlock()

	// 锁外持久化，避免死锁
	if err := m.saveConfig(configToSave); err != nil {
		return nil, err
	}

	return token, nil
}

// saveConfig 内部无锁版本，用于持久化（由持有锁的方法调用）
func (m *TokenManager) saveConfig(config *BootstrapConfig) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(m.configFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// ValidateToken 验证 Token 是否有效
func (m *TokenManager) ValidateToken(code, connectorID string) (*BootstrapToken, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.config.Enabled {
		return nil, fmt.Errorf("bootstrap token validation is disabled")
	}

	for _, token := range m.config.Tokens {
		if token.Code == code {
			if !token.IsValid() {
				return nil, fmt.Errorf("token is invalid or expired")
			}
			// 检查绑定的 connector_id
			if token.ConnectorID != "" && token.ConnectorID != connectorID {
				return nil, fmt.Errorf("token is bound to a different connector: %s", token.ConnectorID)
			}
			return token, nil
		}
	}

	return nil, fmt.Errorf("token not found")
}

// MarkTokenUsed 标记 Token 已使用
func (m *TokenManager) MarkTokenUsed(code, usedBy string) error {
	m.mu.Lock()

	token := m.findTokenByCodeLocked(code)
	if token == nil {
		m.mu.Unlock()
		return fmt.Errorf("token not found")
	}

	if !token.IsValid() {
		m.mu.Unlock()
		return fmt.Errorf("token is invalid or already used")
	}

	now := time.Now()
	token.Status = BootstrapTokenStatusUsed
	token.UsedAt = &now
	token.UsedBy = usedBy

	// 复制配置用于持久化
	configToSave := m.config
	m.mu.Unlock()

	// 锁外持久化，避免死锁
	if err := m.saveConfig(configToSave); err != nil {
		return err
	}

	return nil
}

// ListTokens 列出所有 Token
func (m *TokenManager) ListTokens() []*BootstrapToken {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// 返回副本，避免外部修改
	result := make([]*BootstrapToken, len(m.config.Tokens))
	for i, token := range m.config.Tokens {
		result[i] = token
	}
	return result
}

// RevokeToken 撤销 Token
func (m *TokenManager) RevokeToken(code string) error {
	m.mu.Lock()

	token := m.findTokenByCodeLocked(code)
	if token == nil {
		m.mu.Unlock()
		return fmt.Errorf("token not found")
	}

	if token.Status == BootstrapTokenStatusUsed {
		m.mu.Unlock()
		return fmt.Errorf("token already used")
	}

	token.Status = BootstrapTokenStatusRevoked
	token.ExpiresAt = nil // 撤销后立即失效

	// 复制配置用于持久化
	configToSave := m.config
	m.mu.Unlock()

	// 锁外持久化，避免死锁
	if err := m.saveConfig(configToSave); err != nil {
		return err
	}

	return nil
}

// findTokenByCodeLocked 在锁内查找 Token（调用者必须持有锁）
func (m *TokenManager) findTokenByCodeLocked(code string) *BootstrapToken {
	for _, token := range m.config.Tokens {
		if token.Code == code {
			return token
		}
	}
	return nil
}

// IsEnabled 检查是否启用 Token 验证
func (m *TokenManager) IsEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.Enabled
}

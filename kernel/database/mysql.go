package database

import (
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// MySQLConfig MySQL配置
type MySQLConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
}

// DBManager 数据库管理器
type DBManager struct {
	db   *sql.DB
	mu   sync.RWMutex
	conf MySQLConfig
}

// NewDBManager 创建数据库管理器
func NewDBManager(conf MySQLConfig) (*DBManager, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		conf.User, conf.Password, conf.Host, conf.Port, conf.Database)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// 测试连接
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// 设置连接池
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(5 * time.Minute)

	manager := &DBManager{
		db:   db,
		conf: conf,
	}

	// 初始化表结构
	if err := manager.initTables(); err != nil {
		return nil, fmt.Errorf("failed to init tables: %w", err)
	}

	log.Println("✓ MySQL database connected successfully")
	return manager, nil
}

// initTables 初始化表结构
func (m *DBManager) initTables() error {
	// 创建证据记录表
	evidenceTableSQL := `
	CREATE TABLE IF NOT EXISTS evidence_records (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		tx_id VARCHAR(36) NOT NULL COMMENT '事务ID',
		connector_id VARCHAR(100) NOT NULL COMMENT '连接器ID',
		event_type VARCHAR(50) NOT NULL COMMENT '事件类型',
		channel_id VARCHAR(36) NOT NULL COMMENT '频道ID',
		data_hash VARCHAR(128) DEFAULT '' COMMENT '数据哈希',
		signature TEXT COMMENT '数字签名',
		timestamp TIMESTAMP(6) NOT NULL COMMENT '时间戳',
		metadata JSON COMMENT '元数据',
		record_hash VARCHAR(128) NOT NULL COMMENT '记录哈希',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
		INDEX idx_tx_id (tx_id),
		INDEX idx_connector_id (connector_id),
		INDEX idx_event_type (event_type),
		INDEX idx_channel_id (channel_id),
		INDEX idx_timestamp (timestamp),
		UNIQUE KEY uk_tx_event (tx_id, event_type)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='证据记录表';`

	if _, err := m.db.Exec(evidenceTableSQL); err != nil {
		return fmt.Errorf("failed to create evidence_records table: %w", err)
	}

	// 创建证据分发表
	deliveryTableSQL := `
	CREATE TABLE IF NOT EXISTS evidence_delivery (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		record_id BIGINT NOT NULL COMMENT '证据记录ID',
		connector_id VARCHAR(100) NOT NULL COMMENT '接收连接器ID',
		channel_id VARCHAR(36) NOT NULL COMMENT '证据频道ID',
		status ENUM('pending', 'delivered', 'failed') DEFAULT 'pending' COMMENT '分发状态',
		attempts INT DEFAULT 0 COMMENT '尝试次数',
		last_attempt TIMESTAMP NULL COMMENT '最后尝试时间',
		error_message TEXT COMMENT '错误信息',
		delivered_at TIMESTAMP NULL COMMENT '分发成功时间',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
		INDEX idx_record_id (record_id),
		INDEX idx_connector_id (connector_id),
		INDEX idx_status (status),
		FOREIGN KEY (record_id) REFERENCES evidence_records(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='证据分发表';`

	if _, err := m.db.Exec(deliveryTableSQL); err != nil {
		return fmt.Errorf("failed to create evidence_delivery table: %w", err)
	}

	// 创建证据验证表
	verificationTableSQL := `
	CREATE TABLE IF NOT EXISTS evidence_verification (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		record_id BIGINT NOT NULL COMMENT '证据记录ID',
		connector_id VARCHAR(100) NOT NULL COMMENT '验证连接器ID',
		verification_type VARCHAR(50) NOT NULL COMMENT '验证类型',
		is_valid BOOLEAN NOT NULL COMMENT '是否有效',
		error_message TEXT COMMENT '验证错误信息',
		verified_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP COMMENT '验证时间',
		INDEX idx_record_id (record_id),
		INDEX idx_connector_id (connector_id),
		INDEX idx_verification_type (verification_type),
		FOREIGN KEY (record_id) REFERENCES evidence_records(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='证据验证表';`

	if _, err := m.db.Exec(verificationTableSQL); err != nil {
		return fmt.Errorf("failed to create evidence_verification table: %w", err)
	}

	return nil
}

// GetDB 获取数据库连接
func (m *DBManager) GetDB() *sql.DB {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.db
}

// Close 关闭数据库连接
func (m *DBManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db != nil {
		return m.db.Close()
	}
	return nil
}

// HealthCheck 健康检查
func (m *DBManager) HealthCheck() error {
	return m.db.Ping()
}

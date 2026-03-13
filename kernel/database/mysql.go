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
	// 创建证据记录表（内核级别存证）
	evidenceTableSQL := `
	CREATE TABLE IF NOT EXISTS evidence_records (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		event_id VARCHAR(36) NOT NULL COMMENT '事件实例ID',
		event_type VARCHAR(50) NOT NULL COMMENT '事件类型',
		timestamp TIMESTAMP(6) NOT NULL COMMENT '时间戳',
		source_id VARCHAR(100) NOT NULL COMMENT '事件来源ID（连接器或内核）',
		target_id VARCHAR(100) DEFAULT '' COMMENT '目标ID（直接下一跳：内核或连接器）',
		channel_id VARCHAR(36) DEFAULT '' COMMENT '频道ID',
		data_hash VARCHAR(128) DEFAULT '' COMMENT '数据哈希',
		tx_id VARCHAR(36) DEFAULT '' COMMENT '业务流程ID（用于跨内核关联）',
		signature TEXT COMMENT '内核数字签名',
		hash VARCHAR(128) NOT NULL COMMENT '记录内容哈希',
		prev_hash VARCHAR(128) DEFAULT '' COMMENT '上一条记录的哈希（哈希链）',
		metadata JSON COMMENT '元数据',
		INDEX idx_event_id (event_id),
		INDEX idx_event_type (event_type),
		INDEX idx_source_id (source_id),
		INDEX idx_target_id (target_id),
		INDEX idx_channel_id (channel_id),
		INDEX idx_tx_id (tx_id),
		INDEX idx_timestamp (timestamp)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='内核存证记录表';`

	if _, err := m.db.Exec(evidenceTableSQL); err != nil {
		return fmt.Errorf("failed to create evidence_records table: %w", err)
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

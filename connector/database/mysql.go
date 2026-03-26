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

	log.Println("[OK] Connector MySQL database connected successfully")
	return manager, nil
}

// initTables 初始化表结构
func (m *DBManager) initTables() error {
	// 创建业务数据哈希链表
	businessChainTableSQL := `
	CREATE TABLE IF NOT EXISTS business_data_chain (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		channel_id VARCHAR(36) NOT NULL,
		data_hash VARCHAR(128) NOT NULL DEFAULT '' COMMENT '当前业务数据包的哈希（ACK记录为空）',
		prev_data_hash VARCHAR(128) NOT NULL DEFAULT '' COMMENT '前一个数据包的哈希',
		prev_signature TEXT NOT NULL COMMENT '上一跳的RSA签名（ACK记录中为上一跳ACK签名）',
		signature TEXT NOT NULL COMMENT '当前节点对data_hash的RSA签名',
		timestamp DATETIME(6) NOT NULL COMMENT '时间戳',
		INDEX idx_channel_id (channel_id),
		INDEX idx_data_hash (data_hash)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='业务数据哈希链表';`

	if _, err := m.db.Exec(businessChainTableSQL); err != nil {
		return fmt.Errorf("failed to create business_data_chain table: %w", err)
	}

	// 迁移：如果现有表的 data_hash / prev_data_hash 列为 NOT NULL 且无默认值，修改为允许空字符串
	// 这是为了支持 ACK 记录（data_hash 为空）
	alterSQLs := []string{
		`ALTER TABLE business_data_chain MODIFY COLUMN data_hash VARCHAR(128) NOT NULL DEFAULT ''`,
		`ALTER TABLE business_data_chain MODIFY COLUMN prev_data_hash VARCHAR(128) NOT NULL DEFAULT ''`,
	}
	for _, stmt := range alterSQLs {
		m.db.Exec(stmt)
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

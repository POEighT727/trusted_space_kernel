package database

import (
	"database/sql"
	"fmt"
	"time"
)

// BusinessChainRecord 业务数据哈希链记录
type BusinessChainRecord struct {
	ID            int64
	ConnectorID   string
	ChannelID     string
	DataHash      string // 业务数据哈希，ACK记录为空
	PrevHash      string // 前一个数据包的哈希（prev_data_hash）
	PrevSignature string // 上一跳（kernel/connector）的 RSA 签名
	Signature     string // 当前节点对 data_hash 的 RSA 签名
	Timestamp     time.Time
}

// BusinessChainStore 业务数据哈希链存储接口
type BusinessChainStore interface {
	// InsertRecord 插入新记录
	InsertRecord(record *BusinessChainRecord) (int64, error)
	// GetLastRecord 获取指定频道的最新记录
	GetLastRecord(channelID string) (*BusinessChainRecord, error)
	// GetLastRecordByConnector 获取指定连接器和频道的最新记录
	GetLastRecordByConnector(connectorID, channelID string) (*BusinessChainRecord, error)
	// GetRecords 获取指定频道的记录列表
	GetRecords(channelID string, limit int) ([]*BusinessChainRecord, error)
	// GetRecordCount 获取指定频道的记录数量
	GetRecordCount(channelID string) (int, error)
	// GetAckRecords 获取指定频道的ACK记录（data_hash为空）
	GetAckRecords(channelID string) ([]*BusinessChainRecord, error)
}

// MySQLBusinessChainStore MySQL实现
type MySQLBusinessChainStore struct {
	db *sql.DB
}

// NewMySQLBusinessChainStore 创建MySQL存储
func NewMySQLBusinessChainStore(db *sql.DB) *MySQLBusinessChainStore {
	return &MySQLBusinessChainStore{db: db}
}

// InsertRecord 插入新记录
func (s *MySQLBusinessChainStore) InsertRecord(record *BusinessChainRecord) (int64, error) {
	

	query := `
		INSERT INTO business_data_chain (connector_id, channel_id, data_hash, prev_data_hash, prev_signature, signature, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	result, err := s.db.Exec(query,
		record.ConnectorID,
		record.ChannelID,
		record.DataHash,
		record.PrevHash,
		record.PrevSignature,
		record.Signature,
		record.Timestamp,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert chain record: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get last insert id: %w", err)
	}

	return id, nil
}

// GetLastRecord 获取指定频道的最新记录（排除ACK，即data_hash非空）
func (s *MySQLBusinessChainStore) GetLastRecord(channelID string) (*BusinessChainRecord, error) {
	query := `
		SELECT id, connector_id, channel_id, data_hash, prev_data_hash, prev_signature, signature, timestamp
		FROM business_data_chain
		WHERE channel_id = ? AND data_hash != ''
		ORDER BY id DESC
		LIMIT 1
	`
	row := s.db.QueryRow(query, channelID)

	var record BusinessChainRecord
	err := row.Scan(
		&record.ID,
		&record.ConnectorID,
		&record.ChannelID,
		&record.DataHash,
		&record.PrevHash,
		&record.PrevSignature,
		&record.Signature,
		&record.Timestamp,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query last record: %w", err)
	}

	return &record, nil
}

// GetLastRecordByConnector 获取指定连接器和频道的最新记录（排除ACK）
func (s *MySQLBusinessChainStore) GetLastRecordByConnector(connectorID, channelID string) (*BusinessChainRecord, error) {
	query := `
		SELECT id, connector_id, channel_id, data_hash, prev_data_hash, prev_signature, signature, timestamp
		FROM business_data_chain
		WHERE connector_id = ? AND channel_id = ? AND data_hash != ''
		ORDER BY id DESC
		LIMIT 1
	`
	row := s.db.QueryRow(query, connectorID, channelID)

	var record BusinessChainRecord
	err := row.Scan(
		&record.ID,
		&record.ConnectorID,
		&record.ChannelID,
		&record.DataHash,
		&record.PrevHash,
		&record.PrevSignature,
		&record.Signature,
		&record.Timestamp,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query last record: %w", err)
	}

	return &record, nil
}

// GetRecords 获取指定频道的记录列表
func (s *MySQLBusinessChainStore) GetRecords(channelID string, limit int) ([]*BusinessChainRecord, error) {
	query := `
		SELECT id, connector_id, channel_id, data_hash, prev_data_hash, prev_signature, signature, timestamp
		FROM business_data_chain
		WHERE channel_id = ?
		ORDER BY id ASC
		LIMIT ?
	`
	rows, err := s.db.Query(query, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query records: %w", err)
	}
	defer rows.Close()

	var records []*BusinessChainRecord
	for rows.Next() {
		var record BusinessChainRecord
		err := rows.Scan(
			&record.ID,
			&record.ConnectorID,
			&record.ChannelID,
			&record.DataHash,
			&record.PrevHash,
			&record.PrevSignature,
			&record.Signature,
			&record.Timestamp,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan record: %w", err)
		}
		records = append(records, &record)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return records, nil
}

// GetRecordCount 获取指定频道的记录数量
func (s *MySQLBusinessChainStore) GetRecordCount(channelID string) (int, error) {
	query := `SELECT COUNT(*) FROM business_data_chain WHERE channel_id = ?`
	var count int
	err := s.db.QueryRow(query, channelID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count records: %w", err)
	}
	return count, nil
}

// GetAckRecords 获取指定频道的ACK记录（data_hash为空）
func (s *MySQLBusinessChainStore) GetAckRecords(channelID string) ([]*BusinessChainRecord, error) {
	query := `
		SELECT id, connector_id, channel_id, data_hash, prev_data_hash, prev_signature, signature, timestamp
		FROM business_data_chain
		WHERE channel_id = ? AND data_hash = ''
		ORDER BY id ASC
	`
	rows, err := s.db.Query(query, channelID)
	if err != nil {
		return nil, fmt.Errorf("failed to query ack records: %w", err)
	}
	defer rows.Close()

	var records []*BusinessChainRecord
	for rows.Next() {
		var record BusinessChainRecord
		err := rows.Scan(
			&record.ID,
			&record.ConnectorID,
			&record.ChannelID,
			&record.DataHash,
			&record.PrevHash,
			&record.PrevSignature,
			&record.Signature,
			&record.Timestamp,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan ack record: %w", err)
		}
		records = append(records, &record)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return records, nil
}

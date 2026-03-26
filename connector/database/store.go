package database

import (
	"database/sql"
	"fmt"
	"time"
)

// BusinessChainRecord 业务数据哈希链记录
type BusinessChainRecord struct {
	ID          int64
	ChannelID   string
	DataHash    string // 业务数据哈希，ACK记录为空
	PrevHash    string
	PrevSignature string // 上一跳的RSA签名
	Signature   string
	Timestamp   time.Time
}

// Store 数据存储接口
type Store interface {
	// AppendChainRecord 追加哈希链记录
	AppendChainRecord(record *BusinessChainRecord) (int64, error)
	// GetLastRecordByChannel 获取指定频道的最新记录（排除ACK）
	GetLastRecordByChannel(channelID string) (*BusinessChainRecord, error)
	// GetRecordsByChannel 获取指定频道的所有记录
	GetRecordsByChannel(channelID string) ([]*BusinessChainRecord, error)
	// GetRecordByID 根据ID获取记录
	GetRecordByID(id int64) (*BusinessChainRecord, error)
}

// MySQLStore MySQL存储实现
type MySQLStore struct {
	db *sql.DB
}

// NewMySQLStore 创建MySQL存储
func NewMySQLStore(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: db}
}

// AppendChainRecord 追加哈希链记录
func (s *MySQLStore) AppendChainRecord(record *BusinessChainRecord) (int64, error) {
	query := `
		INSERT INTO business_data_chain (channel_id, data_hash, prev_data_hash, prev_signature, signature, timestamp)
		VALUES (?, ?, ?, ?, ?, ?)
	`
	result, err := s.db.Exec(query,
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

// GetLastRecordByChannel 获取指定频道的最新记录（排除ACK，即data_hash非空）
func (s *MySQLStore) GetLastRecordByChannel(channelID string) (*BusinessChainRecord, error) {
	query := `
		SELECT id, channel_id, data_hash, prev_data_hash, prev_signature, signature, timestamp
		FROM business_data_chain
		WHERE channel_id = ? AND data_hash != ''
		ORDER BY id DESC
		LIMIT 1
	`
	row := s.db.QueryRow(query, channelID)

	var record BusinessChainRecord
	err := row.Scan(
		&record.ID,
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

// GetRecordsByChannel 获取指定频道的所有记录
func (s *MySQLStore) GetRecordsByChannel(channelID string) ([]*BusinessChainRecord, error) {
	query := `
		SELECT id, channel_id, data_hash, prev_data_hash, prev_signature, signature, timestamp
		FROM business_data_chain
		WHERE channel_id = ?
		ORDER BY id ASC
	`
	rows, err := s.db.Query(query, channelID)
	if err != nil {
		return nil, fmt.Errorf("failed to query records: %w", err)
	}
	defer rows.Close()

	var records []*BusinessChainRecord
	for rows.Next() {
		var record BusinessChainRecord
		err := rows.Scan(
			&record.ID,
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

// GetRecordByID 根据ID获取记录
func (s *MySQLStore) GetRecordByID(id int64) (*BusinessChainRecord, error) {
	query := `
		SELECT id, channel_id, data_hash, prev_data_hash, prev_signature, signature, timestamp
		FROM business_data_chain
		WHERE id = ?
	`
	row := s.db.QueryRow(query, id)

	var record BusinessChainRecord
	err := row.Scan(
		&record.ID,
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
		return nil, fmt.Errorf("failed to query record: %w", err)
	}

	return &record, nil
}

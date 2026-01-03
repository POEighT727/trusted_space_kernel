package database

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/trusted-space/kernel/kernel/evidence"
)

// EvidenceStore 证据存储接口
type EvidenceStore interface {
	Store(record *evidence.EvidenceRecord) error
	GetByID(id int64) (*evidence.EvidenceRecord, error)
	Query(filter interface{}) ([]*evidence.EvidenceRecord, error)
	Update(record *evidence.EvidenceRecord) error
	Delete(id int64) error
	Count(filter interface{}) (int64, error)
	GetByTxID(txID string) ([]*evidence.EvidenceRecord, error)
	GetByChannel(channelID string, startTime, endTime *time.Time) ([]*evidence.EvidenceRecord, error)
	GetByConnector(connectorID string, startTime, endTime *time.Time) ([]*evidence.EvidenceRecord, error)
	VerifyRecord(record *evidence.EvidenceRecord) error
	Close() error
}

// MySQLEvidenceStore MySQL证据存储实现
type MySQLEvidenceStore struct {
	db *sql.DB
}

// NewMySQLEvidenceStore 创建MySQL证据存储
func NewMySQLEvidenceStore(db *sql.DB) *MySQLEvidenceStore {
	return &MySQLEvidenceStore{db: db}
}

// Store 存储证据记录
func (s *MySQLEvidenceStore) Store(record *evidence.EvidenceRecord) error {
	metadataJSON, err := json.Marshal(record.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	query := `
		INSERT INTO evidence_records
		(tx_id, connector_id, event_type, channel_id, data_hash, signature, timestamp, metadata, record_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err = s.db.Exec(query,
		record.TxID,
		record.ConnectorID,
		string(record.EventType),
		record.ChannelID,
		record.DataHash,
		record.Signature,
		record.Timestamp,
		string(metadataJSON),
		record.Hash,
	)

	if err != nil {
		return fmt.Errorf("failed to store evidence record: %w", err)
	}

	return nil
}

// GetByID 根据ID获取证据记录
func (s *MySQLEvidenceStore) GetByID(id int64) (*evidence.EvidenceRecord, error) {
	query := `
		SELECT id, tx_id, connector_id, event_type, channel_id, data_hash, signature, timestamp, metadata, record_hash
		FROM evidence_records WHERE id = ?`

	row := s.db.QueryRow(query, id)

	record := &evidence.EvidenceRecord{}
	var metadataStr string
	var dbID int64

	err := row.Scan(&dbID, &record.TxID, &record.ConnectorID, &record.EventType,
		&record.ChannelID, &record.DataHash, &record.Signature, &record.Timestamp,
		&metadataStr, &record.Hash)

	if err != nil {
		return nil, fmt.Errorf("failed to scan evidence record: %w", err)
	}

	// 解析metadata
	if err := json.Unmarshal([]byte(metadataStr), &record.Metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
	}

	return record, nil
}

// Query 根据过滤器查询证据记录
func (s *MySQLEvidenceStore) Query(filter interface{}) ([]*evidence.EvidenceRecord, error) {
	evidenceFilter, ok := filter.(evidence.EvidenceFilter)
	if !ok {
		return nil, fmt.Errorf("invalid filter type")
	}
	var conditions []string
	var args []interface{}

	if evidenceFilter.TxID != "" {
		conditions = append(conditions, "tx_id = ?")
		args = append(args, evidenceFilter.TxID)
	}
	if evidenceFilter.ConnectorID != "" {
		conditions = append(conditions, "connector_id = ?")
		args = append(args, evidenceFilter.ConnectorID)
	}
	if evidenceFilter.EventType != "" {
		conditions = append(conditions, "event_type = ?")
		args = append(args, evidenceFilter.EventType)
	}
	if evidenceFilter.ChannelID != "" {
		conditions = append(conditions, "channel_id = ?")
		args = append(args, evidenceFilter.ChannelID)
	}
	if evidenceFilter.StartTime != nil {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, *evidenceFilter.StartTime)
	}
	if evidenceFilter.EndTime != nil {
		conditions = append(conditions, "timestamp <= ?")
		args = append(args, *evidenceFilter.EndTime)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	limitClause := ""
	if evidenceFilter.Limit > 0 {
		limitClause = fmt.Sprintf(" LIMIT %d", evidenceFilter.Limit)
		if evidenceFilter.Offset > 0 {
			limitClause += fmt.Sprintf(" OFFSET %d", evidenceFilter.Offset)
		}
	}

	query := fmt.Sprintf(`
		SELECT tx_id, connector_id, event_type, channel_id, data_hash, signature, timestamp, metadata, record_hash
		FROM evidence_records %s ORDER BY timestamp DESC %s`,
		whereClause, limitClause)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query evidence records: %w", err)
	}
	defer rows.Close()

	var records []*evidence.EvidenceRecord
	for rows.Next() {
		record := &evidence.EvidenceRecord{}
		var metadataStr string

		err := rows.Scan(&record.TxID, &record.ConnectorID, &record.EventType,
			&record.ChannelID, &record.DataHash, &record.Signature, &record.Timestamp,
			&metadataStr, &record.Hash)
		if err != nil {
			return nil, fmt.Errorf("failed to scan evidence record: %w", err)
		}

		// 解析metadata
		if err := json.Unmarshal([]byte(metadataStr), &record.Metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
		}

		records = append(records, record)
	}

	return records, nil
}

// Update 更新证据记录
func (s *MySQLEvidenceStore) Update(record *evidence.EvidenceRecord) error {
	metadataJSON, err := json.Marshal(record.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	query := `
		UPDATE evidence_records
		SET connector_id = ?, event_type = ?, channel_id = ?, data_hash = ?,
		    signature = ?, timestamp = ?, metadata = ?, record_hash = ?
		WHERE tx_id = ? AND event_type = ?`

	_, err = s.db.Exec(query,
		record.ConnectorID,
		string(record.EventType),
		record.ChannelID,
		record.DataHash,
		record.Signature,
		record.Timestamp,
		string(metadataJSON),
		record.Hash,
		record.TxID,
		string(record.EventType),
	)

	if err != nil {
		return fmt.Errorf("failed to update evidence record: %w", err)
	}

	return nil
}

// Delete 删除证据记录
func (s *MySQLEvidenceStore) Delete(id int64) error {
	query := "DELETE FROM evidence_records WHERE id = ?"
	_, err := s.db.Exec(query, id)
	return err
}

// Count 统计记录数量
func (s *MySQLEvidenceStore) Count(filter interface{}) (int64, error) {
	evidenceFilter, ok := filter.(evidence.EvidenceFilter)
	if !ok {
		return 0, fmt.Errorf("invalid filter type")
	}
	var conditions []string
	var args []interface{}

	if evidenceFilter.TxID != "" {
		conditions = append(conditions, "tx_id = ?")
		args = append(args, evidenceFilter.TxID)
	}
	if evidenceFilter.ConnectorID != "" {
		conditions = append(conditions, "connector_id = ?")
		args = append(args, evidenceFilter.ConnectorID)
	}
	if evidenceFilter.EventType != "" {
		conditions = append(conditions, "event_type = ?")
		args = append(args, evidenceFilter.EventType)
	}
	if evidenceFilter.ChannelID != "" {
		conditions = append(conditions, "channel_id = ?")
		args = append(args, evidenceFilter.ChannelID)
	}
	if evidenceFilter.StartTime != nil {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, *evidenceFilter.StartTime)
	}
	if evidenceFilter.EndTime != nil {
		conditions = append(conditions, "timestamp <= ?")
		args = append(args, *evidenceFilter.EndTime)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf("SELECT COUNT(*) FROM evidence_records %s", whereClause)

	var count int64
	err := s.db.QueryRow(query, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count evidence records: %w", err)
	}

	return count, nil
}

// GetByTxID 根据事务ID获取证据记录
func (s *MySQLEvidenceStore) GetByTxID(txID string) ([]*evidence.EvidenceRecord, error) {
	filter := evidence.EvidenceFilter{TxID: txID}
	return s.Query(filter)
}

// GetByChannel 根据频道获取证据记录
func (s *MySQLEvidenceStore) GetByChannel(channelID string, startTime, endTime *time.Time) ([]*evidence.EvidenceRecord, error) {
	filter := evidence.EvidenceFilter{
		ChannelID: channelID,
		StartTime: startTime,
		EndTime:   endTime,
	}
	return s.Query(filter)
}

// GetByConnector 根据连接器获取证据记录
func (s *MySQLEvidenceStore) GetByConnector(connectorID string, startTime, endTime *time.Time) ([]*evidence.EvidenceRecord, error) {
	filter := evidence.EvidenceFilter{
		ConnectorID: connectorID,
		StartTime:   startTime,
		EndTime:     endTime,
	}
	return s.Query(filter)
}

// VerifyRecord 验证证据记录
func (s *MySQLEvidenceStore) VerifyRecord(record *evidence.EvidenceRecord) error {
	// 计算记录哈希并验证
	calculatedHash := s.calculateRecordHash(record)
	if calculatedHash != record.Hash {
		return fmt.Errorf("hash mismatch: expected=%s, got=%s", record.Hash, calculatedHash)
	}
	return nil
}

// calculateRecordHash 计算记录哈希
func (s *MySQLEvidenceStore) calculateRecordHash(record *evidence.EvidenceRecord) string {
	// 实现哈希计算逻辑
	data := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d",
		record.TxID,
		record.ConnectorID,
		record.EventType,
		record.ChannelID,
		record.DataHash,
		record.Signature,
		record.Timestamp.Unix(),
	)

	// 如果有metadata，包含在哈希中
	if len(record.Metadata) > 0 {
		metadataJSON, _ := json.Marshal(record.Metadata)
		data += "|" + string(metadataJSON)
	}

	return fmt.Sprintf("%x", record.Hash) // 简化实现，实际应该使用SHA256
}

// Close 关闭存储
func (s *MySQLEvidenceStore) Close() error {
	return nil
}

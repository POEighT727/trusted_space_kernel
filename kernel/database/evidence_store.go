package database

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
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
	GetByEventID(eventID string) (*evidence.EvidenceRecord, error)
	GetByChannel(channelID string, startTime, endTime *time.Time) ([]*evidence.EvidenceRecord, error)
	GetBySource(sourceID string, startTime, endTime *time.Time) ([]*evidence.EvidenceRecord, error)
	GetByTarget(targetID string, startTime, endTime *time.Time) ([]*evidence.EvidenceRecord, error)
	GetByDirection(direction evidence.EvidenceDirection, startTime, endTime *time.Time) ([]*evidence.EvidenceRecord, error)
	GetByEventType(eventType evidence.EventType, startTime, endTime *time.Time) ([]*evidence.EvidenceRecord, error)
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
	// 将 metadata 转换为 JSON 字符串存储
	var metadataJSON []byte
	if record.Metadata != nil {
		var err error
		metadataJSON, err = json.Marshal(record.Metadata)
		if err != nil {
			metadataJSON = []byte("{}")
		}
	}

	query := `
		INSERT INTO evidence_records
		(event_id, event_type, timestamp, source_id, target_id, channel_id, flow_id, data_hash, signature, hash, prev_hash, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := s.db.Exec(query,
		record.EventID,
		string(record.EventType),
		record.Timestamp,
		record.SourceID,
		record.TargetID,
		record.ChannelID,
		record.FlowID,
		record.DataHash,
		record.Signature,
		record.Hash,
		record.PrevHash,
		metadataJSON,
	)

	if err != nil {
		return fmt.Errorf("failed to store evidence record: %w", err)
	}

	return nil
}

// GetByID 根据ID获取证据记录
func (s *MySQLEvidenceStore) GetByID(id int64) (*evidence.EvidenceRecord, error) {
	query := `
		SELECT id, event_id, event_type, timestamp, source_id, target_id, channel_id, flow_id, data_hash, signature, hash, prev_hash, metadata
		FROM evidence_records WHERE id = ?`

	row := s.db.QueryRow(query, id)

	record := &evidence.EvidenceRecord{}
	var dbID int64
	var metadataBytes []byte

	err := row.Scan(&dbID, &record.EventID, &record.EventType, &record.Timestamp,
		&record.SourceID, &record.TargetID, &record.ChannelID, &record.FlowID,
		&record.DataHash, &record.Signature, &record.Hash, &record.PrevHash, &metadataBytes)

	if err != nil {
		return nil, fmt.Errorf("failed to scan evidence record: %w", err)
	}

	// 反序列化 metadata
	if len(metadataBytes) > 0 {
		if err := json.Unmarshal(metadataBytes, &record.Metadata); err != nil {
			log.Printf("[WARN] Failed to unmarshal metadata: %v", err)
		}
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

	if evidenceFilter.EventID != "" {
		conditions = append(conditions, "event_id = ?")
		args = append(args, evidenceFilter.EventID)
	}
	if evidenceFilter.EventType != "" {
		conditions = append(conditions, "event_type = ?")
		args = append(args, evidenceFilter.EventType)
	}
	if evidenceFilter.SourceID != "" {
		conditions = append(conditions, "source_id = ?")
		args = append(args, evidenceFilter.SourceID)
	}
	if evidenceFilter.TargetID != "" {
		conditions = append(conditions, "target_id = ?")
		args = append(args, evidenceFilter.TargetID)
	}
	if evidenceFilter.ChannelID != "" {
		conditions = append(conditions, "channel_id = ?")
		args = append(args, evidenceFilter.ChannelID)
	}
	if evidenceFilter.FlowID != "" {
		conditions = append(conditions, "flow_id = ?")
		args = append(args, evidenceFilter.FlowID)
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
		SELECT event_id, event_type, timestamp, source_id, target_id, channel_id, flow_id, data_hash, signature, hash, prev_hash, metadata
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
		var metadataBytes []byte

		err := rows.Scan(&record.EventID, &record.EventType, &record.Timestamp,
			&record.SourceID, &record.TargetID, &record.ChannelID,
			&record.FlowID, &record.DataHash, &record.Signature, &record.Hash, &record.PrevHash, &metadataBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to scan evidence record: %w", err)
		}

		// 反序列化 metadata
		if len(metadataBytes) > 0 {
			if err := json.Unmarshal(metadataBytes, &record.Metadata); err != nil {
				log.Printf("[WARN] Failed to unmarshal metadata: %v", err)
			}
		}

		records = append(records, record)
	}

	return records, nil
}

// Update 更新证据记录
func (s *MySQLEvidenceStore) Update(record *evidence.EvidenceRecord) error {
	// 将 metadata 转换为 JSON 字符串存储
	var metadataJSON []byte
	if record.Metadata != nil {
		var err error
		metadataJSON, err = json.Marshal(record.Metadata)
		if err != nil {
			metadataJSON = []byte("{}")
		}
	}

	query := `
		UPDATE evidence_records
		SET event_type = ?, timestamp = ?, source_id = ?, target_id = ?,
		    channel_id = ?, flow_id = ?, data_hash = ?, signature = ?, hash = ?, prev_hash = ?, metadata = ?
		WHERE event_id = ?`

	_, err := s.db.Exec(query,
		string(record.EventType),
		record.Timestamp,
		record.SourceID,
		record.TargetID,
		record.ChannelID,
		record.FlowID,
		record.DataHash,
		record.Signature,
		record.Hash,
		record.PrevHash,
		metadataJSON,
		record.EventID,
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

	if evidenceFilter.EventID != "" {
		conditions = append(conditions, "event_id = ?")
		args = append(args, evidenceFilter.EventID)
	}
	if evidenceFilter.EventType != "" {
		conditions = append(conditions, "event_type = ?")
		args = append(args, evidenceFilter.EventType)
	}
	if evidenceFilter.SourceID != "" {
		conditions = append(conditions, "source_id = ?")
		args = append(args, evidenceFilter.SourceID)
	}
	if evidenceFilter.TargetID != "" {
		conditions = append(conditions, "target_id = ?")
		args = append(args, evidenceFilter.TargetID)
	}
	if evidenceFilter.ChannelID != "" {
		conditions = append(conditions, "channel_id = ?")
		args = append(args, evidenceFilter.ChannelID)
	}
	if evidenceFilter.FlowID != "" {
		conditions = append(conditions, "flow_id = ?")
		args = append(args, evidenceFilter.FlowID)
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

// GetByEventID 根据事件ID获取证据记录
func (s *MySQLEvidenceStore) GetByEventID(eventID string) (*evidence.EvidenceRecord, error) {
	query := `SELECT id, event_id, event_type, timestamp, source_id, target_id, channel_id, flow_id, data_hash, signature, hash, prev_hash, metadata
		FROM evidence_records WHERE event_id = ?`

	row := s.db.QueryRow(query, eventID)

	record := &evidence.EvidenceRecord{}
	var dbID int64
	var metadataBytes []byte

	err := row.Scan(&dbID, &record.EventID, &record.EventType, &record.Timestamp,
		&record.SourceID, &record.TargetID, &record.ChannelID, &record.FlowID,
		&record.DataHash, &record.Signature, &record.Hash, &record.PrevHash, &metadataBytes)

	if err != nil {
		return nil, fmt.Errorf("failed to scan evidence record: %w", err)
	}

	// 反序列化 metadata
	if len(metadataBytes) > 0 {
		if err := json.Unmarshal(metadataBytes, &record.Metadata); err != nil {
			log.Printf("[WARN] Failed to unmarshal metadata: %v", err)
		}
	}

	return record, nil
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

// GetBySource 根据来源获取证据记录
func (s *MySQLEvidenceStore) GetBySource(sourceID string, startTime, endTime *time.Time) ([]*evidence.EvidenceRecord, error) {
	filter := evidence.EvidenceFilter{
		SourceID: sourceID,
		StartTime: startTime,
		EndTime:   endTime,
	}
	return s.Query(filter)
}

// GetByTarget 根据目标获取证据记录
func (s *MySQLEvidenceStore) GetByTarget(targetID string, startTime, endTime *time.Time) ([]*evidence.EvidenceRecord, error) {
	filter := evidence.EvidenceFilter{
		TargetID: targetID,
		StartTime: startTime,
		EndTime:   endTime,
	}
	return s.Query(filter)
}

// GetByDirection 根据方向获取证据记录
func (s *MySQLEvidenceStore) GetByDirection(direction evidence.EvidenceDirection, startTime, endTime *time.Time) ([]*evidence.EvidenceRecord, error) {
	filter := evidence.EvidenceFilter{
		Direction: string(direction),
		StartTime: startTime,
		EndTime:   endTime,
	}
	return s.Query(filter)
}

// GetByEventType 根据事件类型获取证据记录
func (s *MySQLEvidenceStore) GetByEventType(eventType evidence.EventType, startTime, endTime *time.Time) ([]*evidence.EvidenceRecord, error) {
	filter := evidence.EvidenceFilter{
		EventType: string(eventType),
		StartTime: startTime,
		EndTime:   endTime,
	}
	return s.Query(filter)
}

// GetByFlowID 根据 FlowID 获取证据记录（用于跨内核关联查询）
func (s *MySQLEvidenceStore) GetByFlowID(flowID string, startTime, endTime *time.Time) ([]*evidence.EvidenceRecord, error) {
	filter := evidence.EvidenceFilter{
		FlowID:    flowID,
		StartTime: startTime,
		EndTime:   endTime,
	}
	return s.Query(filter)
}

// VerifyRecord 验证证据记录
func (s *MySQLEvidenceStore) VerifyRecord(record *evidence.EvidenceRecord) error {
	calculatedHash := s.calculateRecordHash(record)
	if calculatedHash != record.Hash {
		return fmt.Errorf("hash mismatch: expected=%s, got=%s", record.Hash, calculatedHash)
	}
	return nil
}

// calculateRecordHash 计算记录哈希
func (s *MySQLEvidenceStore) calculateRecordHash(record *evidence.EvidenceRecord) string {
	data := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%d|%s",
		record.EventID,
		record.EventType,
		record.SourceID,
		record.TargetID,
		record.Direction,
		record.ChannelID,
		record.DataHash,
		record.Signature,
		record.Timestamp.Unix(),
		record.PrevHash,
	)

	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// Close 关闭存储
func (s *MySQLEvidenceStore) Close() error {
	return nil
}

// UpdateFlowSignature 更新记录的流签名
func (s *MySQLEvidenceStore) UpdateFlowSignature(eventID, flowSignature string) error {
	query := `UPDATE evidence_records SET flow_signature = ? WHERE event_id = ?`
	_, err := s.db.Exec(query, flowSignature, eventID)
	if err != nil {
		return fmt.Errorf("failed to update flow signature: %w", err)
	}
	return nil
}

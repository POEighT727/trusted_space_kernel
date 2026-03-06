package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/trusted-space/kernel/kernel/circulation"
	"github.com/trusted-space/kernel/kernel/security"
)

// EventType 事件类型
type EventType string

const (
	// 数据传输事件
	EventTypeTransferStart        EventType = "TRANSFER_START"
	EventTypeTransferEnd          EventType = "TRANSFER_END"
	EventTypeDataReceived         EventType = "DATA_RECEIVED"
	EventTypeFileTransferStart    EventType = "FILE_TRANSFER_START"
	EventTypeFileTransferEnd      EventType = "FILE_TRANSFER_END"

	// 频道管理事件
	EventTypeChannelCreated       EventType = "CHANNEL_CREATED"
	EventTypeChannelClosed        EventType = "CHANNEL_CLOSED"
	EventTypeChannelActivated     EventType = "CHANNEL_ACTIVATED"
	EventTypeChannelSuspended     EventType = "CHANNEL_SUSPENDED"
	EventTypeChannelResumed       EventType = "CHANNEL_RESUMED"

	// 权限管理事件
	EventTypePermissionRequest    EventType = "PERMISSION_REQUEST"
	EventTypePermissionGranted    EventType = "PERMISSION_GRANTED"
	EventTypePermissionDenied     EventType = "PERMISSION_DENIED"
	EventTypePermissionRevoked    EventType = "PERMISSION_REVOKED"
	EventTypeRoleChanged          EventType = "ROLE_CHANGED"

	// 身份认证事件
	EventTypeAuthSuccess          EventType = "AUTH_SUCCESS"
	EventTypeAuthFail             EventType = "AUTH_FAIL"
	EventTypeAuthAttempt          EventType = "AUTH_ATTEMPT"
	EventTypeAuthLogout           EventType = "AUTH_LOGOUT"
	EventTypeTokenGenerated       EventType = "TOKEN_GENERATED"
	EventTypeTokenValidated       EventType = "TOKEN_VALIDATED"
	EventTypeTokenExpired         EventType = "TOKEN_EXPIRED"

	// 安全事件
	EventTypePolicyViolation      EventType = "POLICY_VIOLATION"
	EventTypeSecurityViolation    EventType = "SECURITY_VIOLATION"
	EventTypeDataTampering        EventType = "DATA_TAMPERING"
	EventTypeIntegrityCheckFail   EventType = "INTEGRITY_CHECK_FAIL"
	EventTypeSuspiciousActivity   EventType = "SUSPICIOUS_ACTIVITY"
	EventTypeAccessDenied         EventType = "ACCESS_DENIED"

	// 连接器事件
	EventTypeConnectorRegistered  EventType = "CONNECTOR_REGISTERED"
	EventTypeConnectorUnregistered EventType = "CONNECTOR_UNREGISTERED"
	EventTypeConnectorStatusChanged EventType = "CONNECTOR_STATUS_CHANGED"
	EventTypeConnectorHeartbeat   EventType = "CONNECTOR_HEARTBEAT"
	EventTypeConnectorOffline     EventType = "CONNECTOR_OFFLINE"
	EventTypeConnectorOnline      EventType = "CONNECTOR_ONLINE"

	// 证据相关事件
	EventTypeEvidenceGenerated    EventType = "EVIDENCE_GENERATED"
	EventTypeEvidenceVerified     EventType = "EVIDENCE_VERIFIED"
	EventTypeEvidenceIntegrityFail EventType = "EVIDENCE_INTEGRITY_FAIL"
	EventTypeEvidenceDistributed  EventType = "EVIDENCE_DISTRIBUTED"
	EventTypeEvidenceStored       EventType = "EVIDENCE_STORED"

	// 系统事件
	EventTypeSystemStartup        EventType = "SYSTEM_STARTUP"
	EventTypeSystemShutdown       EventType = "SYSTEM_SHUTDOWN"
	EventTypeConfigChanged        EventType = "CONFIG_CHANGED"
	EventTypeBackupCreated        EventType = "BACKUP_CREATED"
	EventTypeMaintenanceStart     EventType = "MAINTENANCE_START"
	EventTypeMaintenanceEnd       EventType = "MAINTENANCE_END"
)

// EvidenceRecord 存证记录
type EvidenceRecord struct {
	TxID        string            `json:"tx_id"`        // 业务流程ID (flow_id)
	ConnectorID string            `json:"connector_id"`
	EventType   EventType         `json:"event_type"`
	ChannelID   string            `json:"channel_id"`
	DataHash    string            `json:"data_hash"`
	Signature   string            `json:"signature"`
	Timestamp   time.Time         `json:"timestamp"`
	Metadata    map[string]string `json:"metadata"`
	Hash        string            `json:"hash"`        // 记录内容哈希，用于完整性验证
	EventID     string            `json:"event_id"`    // 事件实例ID (可选，用于区分同一流程中的不同事件)
}

// UnmarshalJSON 自定义JSON反序列化，支持向后兼容
func (e *EvidenceRecord) UnmarshalJSON(data []byte) error {
	// 定义临时结构体用于解析，支持旧的字段名
	type Alias EvidenceRecord
	aux := &struct {
		PrevHash   string `json:"prev_hash,omitempty"`   // 旧的链式哈希字段
		RecordHash string `json:"record_hash,omitempty"` // 旧的记录哈希字段
		*Alias
	}{
		Alias: (*Alias)(e),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	// 向后兼容处理：如果有旧的record_hash字段，使用它作为hash
	if aux.RecordHash != "" {
		e.Hash = aux.RecordHash
	}

	return nil
}

// AuditLog 溯源审计日志
type AuditLog struct {
	mu         sync.RWMutex
	store      EvidenceStore // 证据存储接口，支持文件或数据库存储

	// 内存缓存（可选，用于性能优化）
	records    []*EvidenceRecord
	indexByID  map[string]*EvidenceRecord // 按 TxID 索引
	indexByCh  map[string][]*EvidenceRecord // 按 ChannelID 索引
	indexByConn map[string][]*EvidenceRecord // 按 ConnectorID 索引

	// 文件存储（向下兼容）
	logFile    *os.File
	persistent bool

	// 分布式存证支持
	channelManager interface{} // ChannelManager 接口，用于通过频道传输存证数据
}

// EvidenceStore 证据存储接口
type EvidenceStore interface {
	Store(record *EvidenceRecord) error
	GetByID(id int64) (*EvidenceRecord, error)
	Query(filter interface{}) ([]*EvidenceRecord, error)
	Update(record *EvidenceRecord) error
	Delete(id int64) error
	Count(filter interface{}) (int64, error)
	GetByTxID(txID string) ([]*EvidenceRecord, error)
	GetByChannel(channelID string, startTime, endTime *time.Time) ([]*EvidenceRecord, error)
	GetByConnector(connectorID string, startTime, endTime *time.Time) ([]*EvidenceRecord, error)
	GetByChannelAndConnector(channelID, connectorID string, startTime, endTime *time.Time) ([]*EvidenceRecord, error)
	VerifyRecord(record *EvidenceRecord) error
	Close() error
}

// EvidenceFilter 证据查询过滤器
type EvidenceFilter struct {
	TxID        string
	ConnectorID string
	EventType   string
	ChannelID   string
	StartTime   *time.Time
	EndTime     *time.Time
	Limit       int
	Offset      int
}

// NewAuditLog 创建新的审计日志
// AuditLogConfig 审计日志配置
type AuditLogConfig struct {
	Persistent     bool
	LogFilePath    string
	Store          EvidenceStore // 如果提供，将使用数据库存储
	ChannelManager interface{}
	UseMemoryCache bool // 是否使用内存缓存
}

// NewAuditLog 创建审计日志（文件存储）
func NewAuditLog(persistent bool, logFilePath string, channelManager interface{}) (*AuditLog, error) {
	config := AuditLogConfig{
		Persistent:     persistent,
		LogFilePath:    logFilePath,
		ChannelManager: channelManager,
		UseMemoryCache: true,
	}
	return NewAuditLogWithConfig(config)
}

// NewAuditLogWithConfig 创建审计日志（支持数据库存储）
func NewAuditLogWithConfig(config AuditLogConfig) (*AuditLog, error) {
	al := &AuditLog{
		store:          config.Store,
		persistent:     config.Persistent,
		channelManager: config.ChannelManager,
	}

	// 初始化内存缓存（如果启用）
	if config.UseMemoryCache {
		al.records = make([]*EvidenceRecord, 0)
		al.indexByID = make(map[string]*EvidenceRecord)
		al.indexByCh = make(map[string][]*EvidenceRecord)
		al.indexByConn = make(map[string][]*EvidenceRecord)
	}

	// 如果使用文件存储
	if config.Persistent && config.LogFilePath != "" && config.Store == nil {
		// 打开或创建日志文件（追加模式）
		file, err := os.OpenFile(config.LogFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %w", err)
		}
		al.logFile = file

		// 加载现有日志
		if err := al.loadFromFile(config.LogFilePath); err != nil {
			return nil, fmt.Errorf("failed to load existing logs: %w", err)
		}
	}

	return al, nil
}

// SubmitEvidence 提交存证
func (al *AuditLog) SubmitEvidence(connectorID string, eventType EventType, channelID, dataHash string, metadata map[string]string) (*EvidenceRecord, error) {
	return al.SubmitEvidenceWithFlowID("", connectorID, eventType, channelID, dataHash, metadata)
}

// SubmitEvidenceWithFlowID 提交带有业务流程ID的存证
func (al *AuditLog) SubmitEvidenceWithFlowID(flowID, connectorID string, eventType EventType, channelID, dataHash string, metadata map[string]string) (*EvidenceRecord, error) {
	log.Printf("📝 EVIDENCE SUBMIT: %s, connector: %s, channel: %s, hash: %s, flow: %s", eventType, connectorID, channelID, dataHash, flowID)
	
	// 不需要锁，因为每个 SubmitEvidenceWithFlowID 调用都是独立的
	// 构建证据记录

	// 使用传入的flowID，如果为空则生成新的
	if flowID == "" {
		flowID = uuid.New().String()
	}

	// 生成事件实例ID（用于区分同一流程中的不同事件）
	eventID := uuid.New().String()

	// 创建临时记录用于签名（不包含TxID，因为TxID是动态生成的）
	tempTimestamp := time.Now()

	// 生成数字签名
	signature, err := al.generateEvidenceSignature(connectorID, string(eventType), channelID, dataHash, tempTimestamp.Unix())
	if err != nil {
		log.Printf("⚠️  Failed to generate signature for evidence: %v", err)
		// 如果签名生成失败，设置为空签名，但仍继续处理
		signature = ""
	}

	record := &EvidenceRecord{
		TxID:        flowID,      // 使用业务流程ID作为TxID
		ConnectorID: connectorID,
		EventType:   eventType,
		ChannelID:   channelID,
		DataHash:    dataHash,
		Signature:   signature,
		Timestamp:   tempTimestamp,
		Metadata:    metadata,
		EventID:     eventID,     // 事件实例ID
	}

	// 计算记录内容的哈希
	record.Hash = al.calculateRecordHash(record)

	// 如果使用数据库存储
	if al.store != nil {
		if err := al.store.Store(record); err != nil {
			log.Printf("⚠️ Failed to store evidence in database: %v", err)
			// 继续执行，不返回错误，确保其他存储方式仍然工作
		} else {
			log.Printf("✓ Evidence stored in database: %s", flowID)
		}
	}

	// 更新内存缓存（如果启用）
	if al.records != nil {
	al.records = append(al.records, record)
	al.indexByID[flowID] = record

	if channelID != "" {
		al.indexByCh[channelID] = append(al.indexByCh[channelID], record)
	}

	if connectorID != "" {
		al.indexByConn[connectorID] = append(al.indexByConn[connectorID], record)
	}
	}

	// 通过频道传输存证数据
	// 外部存证模式：只发送给指定外部存证连接器
	// 内部存证模式：广播给所有订阅者（分布式存储）
	if err := al.transmitEvidenceViaChannel(record); err != nil {
		log.Printf("⚠️ Failed to transmit evidence via channel: %v", err)
		// 不返回错误，因为证据已经存储，只是分发失败
	}

	// 持久化到文件（可选，本地备份）
	if al.persistent && al.logFile != nil {
		if err := al.writeRecordToFile(record); err != nil {
			log.Printf("⚠️ Failed to persist record to file: %v", err)
			// 不返回错误，因为主要存储已成功
		}
	}

	return record, nil
}

// calculateRecordHash 计算记录内容的哈希值
func (al *AuditLog) calculateRecordHash(record *EvidenceRecord) string {
	data := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d|%s",
		record.TxID,
		record.ConnectorID,
		record.EventType,
		record.ChannelID,
		record.DataHash,
		record.Signature,
		record.Timestamp.Unix(),
		record.EventID,
	)

	// 如果有metadata，包含在哈希中
	if len(record.Metadata) > 0 {
		metadataJSON, _ := json.Marshal(record.Metadata)
		data += "|" + string(metadataJSON)
	}

	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// QueryByTxID 根据业务流程ID查询所有相关事件
func (al *AuditLog) QueryByTxID(flowID string) ([]*EvidenceRecord, error) {
	// 如果使用数据库存储
	if al.store != nil {
		records, err := al.store.GetByTxID(flowID)
		if err != nil {
			return nil, fmt.Errorf("failed to query evidence from database: %w", err)
		}
		return records, nil
	}

	// 内存缓存查询 - 查找所有匹配的记录
	al.mu.RLock()
	defer al.mu.RUnlock()

	var records []*EvidenceRecord
	for _, record := range al.records {
		if record.TxID == flowID {
			records = append(records, record)
		}
	}

	return records, nil
}

// QueryByTxIDSingle 根据交易ID查询单个事件（向后兼容）
func (al *AuditLog) QueryByTxIDSingle(txID string) (*EvidenceRecord, error) {
	records, err := al.QueryByTxID(txID)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("evidence record not found for txID: %s", txID)
	}
	return records[0], nil
}

// QueryByChannel 根据频道 ID 查询
func (al *AuditLog) QueryByChannel(channelID string, startTime, endTime time.Time, limit int) []*EvidenceRecord {
	// 如果使用数据库存储
	if al.store != nil {
		var startTimePtr, endTimePtr *time.Time
		if !startTime.IsZero() {
			startTimePtr = &startTime
		}
		if !endTime.IsZero() {
			endTimePtr = &endTime
		}

		records, err := al.store.GetByChannel(channelID, startTimePtr, endTimePtr)
		if err != nil {
			log.Printf("⚠️ Failed to query evidence from database: %v", err)
			return []*EvidenceRecord{}
		}

		// 应用限制
		if limit > 0 && len(records) > limit {
			records = records[:limit]
		}

		return records
	}

	// 内存缓存查询
	al.mu.RLock()
	defer al.mu.RUnlock()

	records, exists := al.indexByCh[channelID]
	if !exists {
		return []*EvidenceRecord{}
	}

	// 过滤时间范围
	filtered := make([]*EvidenceRecord, 0)
	for _, record := range records {
		if (startTime.IsZero() || record.Timestamp.After(startTime)) &&
			(endTime.IsZero() || record.Timestamp.Before(endTime)) {
			filtered = append(filtered, record)
			if limit > 0 && len(filtered) >= limit {
				break
			}
		}
	}

	return filtered
}

// QueryByChannelAndConnector 根据频道和连接器ID查询证据记录
func (al *AuditLog) QueryByChannelAndConnector(channelID, connectorID string, startTime, endTime time.Time, limit int) []*EvidenceRecord {
	// 如果使用数据库存储
	if al.store != nil {
		var startTimePtr, endTimePtr *time.Time
		if !startTime.IsZero() {
			startTimePtr = &startTime
		}
		if !endTime.IsZero() {
			endTimePtr = &endTime
		}

		records, err := al.store.GetByChannelAndConnector(channelID, connectorID, startTimePtr, endTimePtr)
		if err != nil {
			log.Printf("⚠️ Failed to query evidence from database: %v", err)
			return []*EvidenceRecord{}
		}

		// 应用限制
		if limit > 0 && len(records) > limit {
			records = records[:limit]
		}

		return records
	}

	// 内存缓存查询
	al.mu.RLock()
	defer al.mu.RUnlock()

	records, exists := al.indexByCh[channelID]
	if !exists {
		return []*EvidenceRecord{}
	}

	// 过滤连接器和时间范围
	filtered := make([]*EvidenceRecord, 0)
	for _, record := range records {
		if record.ConnectorID == connectorID &&
			(startTime.IsZero() || record.Timestamp.After(startTime)) &&
			(endTime.IsZero() || record.Timestamp.Before(endTime)) {
			filtered = append(filtered, record)
			if limit > 0 && len(filtered) >= limit {
				break
			}
		}
	}

	return filtered
}

// QueryByConnector 根据连接器 ID 查询
func (al *AuditLog) QueryByConnector(connectorID string, startTime, endTime time.Time, limit int) []*EvidenceRecord {
	// 如果使用数据库存储
	if al.store != nil {
		var startTimePtr, endTimePtr *time.Time
		if !startTime.IsZero() {
			startTimePtr = &startTime
		}
		if !endTime.IsZero() {
			endTimePtr = &endTime
		}

		records, err := al.store.GetByConnector(connectorID, startTimePtr, endTimePtr)
		if err != nil {
			log.Printf("⚠️ Failed to query evidence from database: %v", err)
			return []*EvidenceRecord{}
		}

		// 应用限制
		if limit > 0 && len(records) > limit {
			records = records[:limit]
		}

		return records
	}

	// 内存缓存查询
	al.mu.RLock()
	defer al.mu.RUnlock()

	records, exists := al.indexByConn[connectorID]
	if !exists {
		return []*EvidenceRecord{}
	}

	// 过滤时间范围
	filtered := make([]*EvidenceRecord, 0)
	for _, record := range records {
		if (startTime.IsZero() || record.Timestamp.After(startTime)) &&
			(endTime.IsZero() || record.Timestamp.Before(endTime)) {
			filtered = append(filtered, record)
			if limit > 0 && len(filtered) >= limit {
				break
			}
		}
	}

	return filtered
}

// GetAllRecords 获取所有记录
func (al *AuditLog) GetAllRecords() []*EvidenceRecord {
	al.mu.RLock()
	defer al.mu.RUnlock()

	return al.records
}

// GetRecordCount 获取记录总数
func (al *AuditLog) GetRecordCount() int {
	al.mu.RLock()
	defer al.mu.RUnlock()

	return len(al.records)
}

// VerifyRecord 验证单个记录的完整性
func (al *AuditLog) VerifyRecord(record *EvidenceRecord) error {
		calculatedHash := al.calculateRecordHash(record)
	if calculatedHash != record.Hash {
		return fmt.Errorf("hash mismatch: expected=%s, got=%s", record.Hash, calculatedHash)
		}
	return nil
}

// writeRecordToFile 将记录写入文件
func (al *AuditLog) writeRecordToFile(record *EvidenceRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to marshal record: %w", err)
	}

	if _, err := al.logFile.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("failed to write to file: %w", err)
	}

	return al.logFile.Sync()
}

// loadFromFile 从文件加载日志
func (al *AuditLog) loadFromFile(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 文件不存在，不是错误
		}
		return err
	}

	if len(data) == 0 {
		return nil
	}

	// 按行解析
	lines := []byte{}
	for _, b := range data {
		if b == '\n' {
			if len(lines) > 0 {
				var record EvidenceRecord
				if err := json.Unmarshal(lines, &record); err == nil {
					al.records = append(al.records, &record)
					al.indexByID[record.TxID] = &record
					
					if record.ChannelID != "" {
						al.indexByCh[record.ChannelID] = append(al.indexByCh[record.ChannelID], &record)
					}
					
					if record.ConnectorID != "" {
						al.indexByConn[record.ConnectorID] = append(al.indexByConn[record.ConnectorID], &record)
					}
				}
			}
			lines = []byte{}
		} else {
			lines = append(lines, b)
		}
	}

	return nil
}

// transmitEvidenceViaChannel 通过统一频道传输存证数据
func (al *AuditLog) transmitEvidenceViaChannel(record *EvidenceRecord) error {
	if al.channelManager == nil {
		// 如果没有频道管理器，跳过频道传输
		return nil
	}

	// 如果存证记录关联了频道，直接通过该频道传输存证数据
	if record.ChannelID != "" {
		// 通过统一频道传输存证数据（使用存证消息类型）
		return al.sendEvidenceToChannel(record.ChannelID, record)
	}

	// 没有关联频道，跳过频道传输
	return nil
}

// sendEvidenceToChannel 将存证记录发送到指定的频道
func (al *AuditLog) sendEvidenceToChannel(channelID string, record *EvidenceRecord) error {
	cm, ok := al.channelManager.(*circulation.ChannelManager)
	if !ok {
		return fmt.Errorf("invalid channel manager type")
	}

	channel, err := cm.GetChannel(channelID)
	if err != nil {
		return fmt.Errorf("evidence channel not found: %w", err)
	}

	// 检查频道是否已激活
	if channel.Status != circulation.ChannelStatusActive {
		// 频道未激活，跳过频道传输（证据已存储在数据库中）
		log.Printf("ℹ️ Channel %s is not active (status: %s), skipping evidence transmission", channelID, channel.Status)
		return nil
	}

	// 序列化存证记录
	recordData, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to marshal evidence record: %w", err)
	}

	// 创建数据包
	sequenceNumber := int64(len(al.records)) // 使用记录总数作为序列号

	// 根据存证配置确定发送目标
	var targetIDs []string
	if channel.EvidenceConfig != nil {
		switch channel.EvidenceConfig.Mode {
		case circulation.EvidenceModeExternal:
			// 外部存证：只发送给外部存证连接器
			if channel.EvidenceConfig.ConnectorID != "" {
				targetIDs = []string{channel.EvidenceConfig.ConnectorID}
			} else {
				return fmt.Errorf("external evidence mode requires connector ID")
			}
		case circulation.EvidenceModeInternal:
			// 内部存证：广播给所有订阅者（用于分布式存储）
			targetIDs = []string{}
		case circulation.EvidenceModeHybrid:
			// 混合存证：发送给所有订阅者（包括外部存证连接器）
			targetIDs = []string{}
		default:
			// 默认广播
			targetIDs = []string{}
		}
	} else {
		// 没有存证配置，广播给所有订阅者
		targetIDs = []string{}
	}

	// 确定发送者ID：优先使用原始连接器，但如果不是发送方则使用创建者
	senderID := record.ConnectorID
	if !channel.CanSend(senderID) {
		// 如果原始连接器不是发送方，使用频道创建者作为发送者
		senderID = channel.CreatorID
		log.Printf("ℹ️ Using channel creator %s as sender for evidence (original sender %s is not authorized)", senderID, record.ConnectorID)
	}

	packet := &circulation.DataPacket{
		ChannelID:      channelID,
		SequenceNumber: sequenceNumber,
		Payload:        recordData,
		Signature:      "", // 存证数据由系统生成，暂时不需要签名
		Timestamp:      record.Timestamp.Unix(),
		SenderID:       senderID,
		TargetIDs:      targetIDs, // 根据存证模式确定发送目标
		MessageType:    circulation.MessageTypeEvidence, // 设置为存证消息类型
	}

	// 发送到频道
	return channel.PushData(packet)
}

// generateEvidenceSignature 生成存证记录的数字签名
func (al *AuditLog) generateEvidenceSignature(connectorID, eventType, channelID, dataHash string, timestamp int64) (string, error) {
	return security.GenerateEvidenceSignature(connectorID, eventType, channelID, dataHash, timestamp)
}

// Close 关闭审计日志
func (al *AuditLog) Close() error {
	if al.logFile != nil {
		return al.logFile.Close()
	}
	return nil
}


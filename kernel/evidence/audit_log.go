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

// EventType 事件类型（内核可直达的基本事件）
type EventType string

const (
	// 连接器事件
	EventTypeConnectorRegistered   EventType = "CONNECTOR_REGISTERED"   // 连接器注册
	EventTypeConnectorUnregistered EventType = "CONNECTOR_UNREGISTERED" // 连接器注销
	EventTypeConnectorOnline       EventType = "CONNECTOR_ONLINE"       // 连接器上线
	EventTypeConnectorOffline      EventType = "CONNECTOR_OFFLINE"      // 连接器离线
	EventTypeConnectorHeartbeat   EventType = "CONNECTOR_HEARTBEAT"   // 连接器心跳

	// 数据传输事件（兼容旧接口）
	EventTypeDataSend       EventType = "DATA_SEND"       // 数据发送
	EventTypeDataReceive    EventType = "DATA_RECEIVE"    // 数据接收
	EventTypeDataForward    EventType = "DATA_FORWARD"    // 数据转发（跨内核转发）
	EventTypeTransferStart  EventType = "TRANSFER_START"  // 传输开始（兼容旧接口）
	EventTypeTransferEnd    EventType = "TRANSFER_END"    // 传输结束（兼容旧接口）

	// 频道管理事件
	EventTypeChannelCreated EventType = "CHANNEL_CREATED" // 频道创建
	EventTypeChannelClosed  EventType = "CHANNEL_CLOSED"  // 频道关闭

	// 权限管理事件
	EventTypePermissionChange   EventType = "PERMISSION_CHANGE"   // 权限变更
	EventTypePermissionRequest EventType = "PERMISSION_REQUEST"   // 权限请求（兼容旧接口）
	EventTypePermissionGranted EventType = "PERMISSION_GRANTED"   // 权限授予（兼容旧接口）
	EventTypePermissionDenied  EventType = "PERMISSION_DENIED"    // 权限拒绝（兼容旧接口）
	EventTypePermissionRevoked EventType = "PERMISSION_REVOKED"  // 权限撤销（兼容旧接口）

	// 身份认证事件（兼容旧接口）
	EventTypeAuthSuccess      EventType = "AUTH_SUCCESS"      // 认证成功
	EventTypeAuthFail         EventType = "AUTH_FAIL"         // 认证失败
	EventTypeAuthAttempt      EventType = "AUTH_ATTEMPT"      // 认证尝试
	EventTypeAuthLogout       EventType = "AUTH_LOGOUT"       // 登出
	EventTypeTokenGenerated   EventType = "TOKEN_GENERATED"    // Token生成
	EventTypeTokenValidated   EventType = "TOKEN_VALIDATED"   // Token验证
	EventTypeTokenExpired     EventType = "TOKEN_EXPIRED"     // Token过期

	// 系统事件（兼容旧接口）
	EventTypeSystemStartup    EventType = "SYSTEM_STARTUP"    // 系统启动
	EventTypeSystemShutdown   EventType = "SYSTEM_SHUTDOWN"   // 系统关闭
	EventTypeConfigChanged   EventType = "CONFIG_CHANGED"    // 配置变更

	// 多内核互联事件
	EventTypeInterconnectRequested EventType = "INTERCONNECT_REQUESTED" // 内核互联请求
	EventTypeInterconnectApproved  EventType = "INTERCONNECT_APPROVED"  // 内核互联批准
	EventTypeInterconnectRejected EventType = "INTERCONNECT_REJECTED" // 内核互联拒绝

	// 其他兼容旧接口
	EventTypePolicyViolation    EventType = "POLICY_VIOLATION"     // 策略违规
	EventTypeConnectorStatusChanged EventType = "CONNECTOR_STATUS_CHANGED" // 连接器状态变更
)

// EvidenceDirection 存证方向
type EvidenceDirection string

const (
	DirectionOutgoing EvidenceDirection = "outgoing" // 发送方向：数据流出内核
	DirectionIncoming EvidenceDirection = "incoming" // 接收方向：数据流入内核
	DirectionInternal EvidenceDirection = "internal" // 内部事件：不涉及外部数据流
)

// EvidenceRecord 存证记录（内核级别，只记录内核可直达的信息）
type EvidenceRecord struct {
	EventID        string            `json:"event_id"`     // 事件实例ID
	EventType      EventType         `json:"event_type"`  // 事件类型
	Timestamp      time.Time         `json:"timestamp"`   // 时间戳
	SourceID       string            `json:"source_id"`   // 事件来源ID（连接器ID或内核ID）
	TargetID       string            `json:"target_id"`   // 目标ID（直接下一跳：内核或连接器）
	Direction      EvidenceDirection `json:"direction"`   // 存证方向
	ChannelID      string            `json:"channel_id"`  // 频道ID
	FlowID         string            `json:"flow_id"`     // 业务流程ID
	DataHash       string            `json:"data_hash"`   // 数据哈希
	Signature      string            `json:"signature"`   // 签名（流模式下仅在流结束时生成）
	Hash           string            `json:"hash"`        // 记录内容哈希，用于完整性验证
	PrevHash       string            `json:"prev_hash"`   // 上一条记录的哈希（哈希链）
	Metadata       map[string]string `json:"metadata,omitempty"` // 扩展元数据

	// 兼容旧接口的字段
	ConnectorID string `json:"connector_id,omitempty"` // 连接器ID（兼容旧接口）
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
	indexByCh  map[string][]*EvidenceRecord // 按 ChannelID 索引
	indexBySource map[string][]*EvidenceRecord // 按 SourceID 索引
	indexByTarget map[string][]*EvidenceRecord // 按 TargetID 索引
	indexByEventType map[EventType][]*EvidenceRecord // 按事件类型索引
	indexByFlowID map[string][]*EvidenceRecord // 按 FlowID 索引（用于跨内核关联）
	lastRecordHash string // 最新记录的哈希（用于哈希链）

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
	GetByEventID(eventID string) (*EvidenceRecord, error)
	GetByChannel(channelID string, startTime, endTime *time.Time) ([]*EvidenceRecord, error)
	GetBySource(sourceID string, startTime, endTime *time.Time) ([]*EvidenceRecord, error)
	GetByTarget(targetID string, startTime, endTime *time.Time) ([]*EvidenceRecord, error)
	GetByDirection(direction EvidenceDirection, startTime, endTime *time.Time) ([]*EvidenceRecord, error)
	GetByEventType(eventType EventType, startTime, endTime *time.Time) ([]*EvidenceRecord, error)
	GetByFlowID(flowID string, startTime, endTime *time.Time) ([]*EvidenceRecord, error)
	VerifyRecord(record *EvidenceRecord) error
	Close() error
}

// EvidenceFilter 证据查询过滤器
type EvidenceFilter struct {
	EventID     string
	EventType   string
	SourceID    string
	TargetID    string
	Direction   string
	ChannelID   string
	FlowID      string // 业务流程ID，用于跨内核关联查询
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
		al.indexByCh = make(map[string][]*EvidenceRecord)
		al.indexBySource = make(map[string][]*EvidenceRecord)
		al.indexByTarget = make(map[string][]*EvidenceRecord)
		al.indexByEventType = make(map[EventType][]*EvidenceRecord)
		al.indexByFlowID = make(map[string][]*EvidenceRecord)
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

// SubmitEvidence 提交存证（兼容旧接口，内部调用新版方法）
func (al *AuditLog) SubmitEvidence(connectorID string, eventType EventType, channelID, dataHash string) (*EvidenceRecord, error) {
	return al.SubmitBasicEvidence(connectorID, eventType, channelID, dataHash, DirectionInternal, "")
}

// SubmitEvidenceWithFlowID 提交带有业务流程ID的存证（兼容旧接口）
// flowID 参数保留以兼容旧接口，但不再用于记录中
func (al *AuditLog) SubmitEvidenceWithFlowID(flowID, connectorID string, eventType EventType, channelID, dataHash string) (*EvidenceRecord, error) {
	return al.SubmitBasicEvidence(connectorID, eventType, channelID, dataHash, DirectionInternal, "")
}

// SubmitBasicEvidence 提交基本事件存证
// sourceID: 事件来源ID（连接器ID或内核ID）
// eventType: 事件类型
// channelID: 频道ID（可选）
// dataHash: 数据哈希（可选）
// direction: 存证方向
// targetID: 目标ID（直接下一跳：内核或连接器）
func (al *AuditLog) SubmitBasicEvidence(sourceID string, eventType EventType, channelID, dataHash string, direction EvidenceDirection, targetID string) (*EvidenceRecord, error) {
	return al.SubmitBasicEvidenceWithMetadata(sourceID, eventType, channelID, dataHash, direction, targetID, "", map[string]string{"data_category": "control"})
}

// SubmitBasicEvidenceWithFlowID 提交带 flow_id 的基本事件存证
// flowID: 业务流程ID，用于跨内核关联
func (al *AuditLog) SubmitBasicEvidenceWithFlowID(sourceID string, eventType EventType, channelID, dataHash string, direction EvidenceDirection, targetID, flowID string) (*EvidenceRecord, error) {
	return al.SubmitBasicEvidenceWithMetadata(sourceID, eventType, channelID, dataHash, direction, targetID, flowID, nil)
}

// SubmitBasicEvidenceWithMetadata 提交带元数据的存证记录
func (al *AuditLog) SubmitBasicEvidenceWithMetadata(sourceID string, eventType EventType, channelID, dataHash string, direction EvidenceDirection, targetID, flowID string, metadata map[string]string) (*EvidenceRecord, error) {
	// 生成事件实例ID
	eventID := uuid.New().String()
	tempTimestamp := time.Now()

	// 注意：单条签名（Signature）在流签名模式下不再生成
	// 签名将在数据流结束时统一通过 SignFlow 方法生成
	signature := ""

	al.mu.Lock()
	prevHash := al.lastRecordHash
	al.mu.Unlock()

	record := &EvidenceRecord{
		EventID:   eventID,
		EventType: eventType,
		Timestamp: tempTimestamp,
		SourceID:  sourceID,
		TargetID:  targetID,
		Direction: direction,
		ChannelID: channelID,
		FlowID:    flowID,
		DataHash:  dataHash,
		Signature: signature,
		PrevHash:  prevHash,
	}

	// 设置元数据
	if metadata != nil {
		record.Metadata = metadata
	}

	// 计算记录内容的哈希
	record.Hash = al.calculateRecordHash(record)

	// 更新最后记录哈希（用于哈希链）
	al.mu.Lock()
	al.lastRecordHash = record.Hash
	al.mu.Unlock()

	// 存储到数据库
	if al.store != nil {
		var lastErr error
		for attempt := 1; attempt <= 3; attempt++ {
			if err := al.store.Store(record); err != nil {
				lastErr = err
				log.Printf("[WARN] Failed to store evidence in database (attempt %d/3): %v", attempt, err)
				if attempt < 3 {
					time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
					continue
				}
			} else {
				log.Printf("Evidence stored in database: %s", eventID)
				break
			}
		}
		if lastErr != nil {
			log.Printf("[WARN] Failed to store evidence in database after 3 attempts: %v", lastErr)
		}
	}

	// 更新内存缓存
	if al.records != nil {
		al.mu.Lock()
		al.records = append(al.records, record)

		if channelID != "" {
			al.indexByCh[channelID] = append(al.indexByCh[channelID], record)
		}

		if sourceID != "" {
			al.indexBySource[sourceID] = append(al.indexBySource[sourceID], record)
		}

		// 添加 flow_id 索引
		if flowID != "" {
			al.indexByFlowID[flowID] = append(al.indexByFlowID[flowID], record)
		}

		al.mu.Unlock()
	}

	return record, nil
}

// SubmitBasicEvidenceWithRetry 提交基本事件存证（带重试机制）
// maxRetries: 最大重试次数
func (al *AuditLog) SubmitBasicEvidenceWithRetry(sourceID string, eventType EventType, channelID, dataHash string, direction EvidenceDirection, targetID string, maxRetries int) (*EvidenceRecord, error) {
	// 生成事件实例ID
	eventID := uuid.New().String()
	tempTimestamp := time.Now()

	// 生成数字签名
	signature, err := al.generateEvidenceSignature(sourceID, string(eventType), channelID, dataHash, tempTimestamp.Unix())
	if err != nil {
		log.Printf("[WARN]  Failed to generate signature for evidence: %v", err)
		signature = ""
	}

	al.mu.Lock()
	prevHash := al.lastRecordHash
	al.mu.Unlock()

	record := &EvidenceRecord{
		EventID:   eventID,
		EventType: eventType,
		Timestamp: tempTimestamp,
		SourceID:  sourceID,
		TargetID:  targetID,
		Direction: direction,
		ChannelID: channelID,
		DataHash:  dataHash,
		Signature: signature,
		PrevHash:  prevHash,
	}

	// 计算记录内容的哈希
	record.Hash = al.calculateRecordHash(record)

	// 更新最后记录哈希（用于哈希链）
	al.mu.Lock()
	al.lastRecordHash = record.Hash
	al.mu.Unlock()

	// 如果使用数据库存储，添加重试机制
	if al.store != nil {
		var lastErr error
		for attempt := 1; attempt <= maxRetries; attempt++ {
			if err := al.store.Store(record); err != nil {
				lastErr = err
				log.Printf("[WARN] Failed to store evidence in database (attempt %d/%d): %v", attempt, maxRetries, err)
				if attempt < maxRetries {
					time.Sleep(time.Duration(attempt) * 500 * time.Millisecond) // 指数退避
					continue
				}
			} else {
				log.Printf("Evidence stored in database: %s", eventID)
				break
			}
		}
		if lastErr != nil {
			log.Printf("[WARN] Failed to store evidence in database after %d attempts: %v", maxRetries, lastErr)
		}
	}

	// 更新内存缓存（如果启用）
	if al.records != nil {
		al.mu.Lock()
		al.records = append(al.records, record)

		if channelID != "" {
			al.indexByCh[channelID] = append(al.indexByCh[channelID], record)
		}

		if sourceID != "" {
			al.indexBySource[sourceID] = append(al.indexBySource[sourceID], record)
		}

		if targetID != "" {
			al.indexByTarget[targetID] = append(al.indexByTarget[targetID], record)
		}

		al.indexByEventType[eventType] = append(al.indexByEventType[eventType], record)
		al.mu.Unlock()
	}

	// 持久化到文件（可选，本地备份）
	if al.persistent && al.logFile != nil {
		if err := al.writeRecordToFile(record); err != nil {
			log.Printf("[WARN] Failed to persist record to file: %v", err)
		}
	}

	return record, nil
}

// calculateRecordHash 计算记录内容的哈希值
func (al *AuditLog) calculateRecordHash(record *EvidenceRecord) string {
	data := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%d|%s",
		record.EventID,
		record.EventType,
		record.SourceID,
		record.TargetID,
		record.Direction,
		record.ChannelID,
		record.DataHash,
		record.Timestamp.Unix(),
		record.PrevHash,
	)

	// 计算哈希
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// QueryByEventID 根据事件ID查询
func (al *AuditLog) QueryByEventID(eventID string) (*EvidenceRecord, error) {
	if al.store != nil {
		record, err := al.store.GetByEventID(eventID)
		if err != nil {
			return nil, fmt.Errorf("failed to query evidence from database: %w", err)
		}
		return record, nil
	}

	// 内存缓存查询
	al.mu.RLock()
	defer al.mu.RUnlock()

	for _, record := range al.records {
		if record.EventID == eventID {
			return record, nil
		}
	}

	return nil, fmt.Errorf("evidence record not found for eventID: %s", eventID)
}

// QueryByChannelAndConnector 根据频道和连接器ID查询证据记录（兼容旧接口）
func (al *AuditLog) QueryByChannelAndConnector(channelID, connectorID string, startTime, endTime time.Time, limit int) []*EvidenceRecord {
	// 使用新版查询方法组合实现
	channelRecords := al.QueryByChannel(channelID, startTime, endTime, 0)
	sourceRecords := al.QueryBySource(connectorID, startTime, endTime, 0)

	// 合并结果
	records := make([]*EvidenceRecord, 0)
	seen := make(map[string]bool)
	for _, r := range channelRecords {
		if !seen[r.EventID] {
			records = append(records, r)
			seen[r.EventID] = true
		}
	}
	for _, r := range sourceRecords {
		if !seen[r.EventID] {
			records = append(records, r)
			seen[r.EventID] = true
		}
	}

	// 应用限制
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}

	return records
}

// QueryByConnector 根据连接器ID查询（兼容旧接口，查询source或target）
func (al *AuditLog) QueryByConnector(connectorID string, startTime, endTime time.Time, limit int) []*EvidenceRecord {
	sourceRecords := al.QueryBySource(connectorID, startTime, endTime, 0)
	targetRecords := al.QueryByTarget(connectorID, startTime, endTime, 0)

	// 合并结果
	records := make([]*EvidenceRecord, 0)
	seen := make(map[string]bool)
	for _, r := range sourceRecords {
		if !seen[r.EventID] {
			records = append(records, r)
			seen[r.EventID] = true
		}
	}
	for _, r := range targetRecords {
		if !seen[r.EventID] {
			records = append(records, r)
			seen[r.EventID] = true
		}
	}

	// 应用限制
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}

	return records
}

// QueryByChannel 根据频道 ID 查询
func (al *AuditLog) QueryByChannel(channelID string, startTime, endTime time.Time, limit int) []*EvidenceRecord {
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
			log.Printf("[WARN] Failed to query evidence from database: %v", err)
			return []*EvidenceRecord{}
		}

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

	return al.filterByTimeRange(records, startTime, endTime, limit)
}

// QueryByFlowID 根据 FlowID 查询存证记录（用于跨内核关联）
func (al *AuditLog) QueryByFlowID(flowID string, startTime, endTime time.Time, limit int) []*EvidenceRecord {
	if al.store != nil {
		var startTimePtr, endTimePtr *time.Time
		if !startTime.IsZero() {
			startTimePtr = &startTime
		}
		if !endTime.IsZero() {
			endTimePtr = &endTime
		}

		records, err := al.store.GetByFlowID(flowID, startTimePtr, endTimePtr)
		if err != nil {
			log.Printf("[WARN] Failed to query evidence by flow_id from database: %v", err)
			return []*EvidenceRecord{}
		}

		if limit > 0 && len(records) > limit {
			records = records[:limit]
		}

		return records
	}

	// 内存缓存查询
	al.mu.RLock()
	defer al.mu.RUnlock()

	records, exists := al.indexByFlowID[flowID]
	if !exists {
		return []*EvidenceRecord{}
	}

	return al.filterByTimeRange(records, startTime, endTime, limit)
}

// QueryBySource 根据来源ID查询
func (al *AuditLog) QueryBySource(sourceID string, startTime, endTime time.Time, limit int) []*EvidenceRecord {
	if al.store != nil {
		var startTimePtr, endTimePtr *time.Time
		if !startTime.IsZero() {
			startTimePtr = &startTime
		}
		if !endTime.IsZero() {
			endTimePtr = &endTime
		}

		records, err := al.store.GetBySource(sourceID, startTimePtr, endTimePtr)
		if err != nil {
			log.Printf("[WARN] Failed to query evidence from database: %v", err)
			return []*EvidenceRecord{}
		}

		if limit > 0 && len(records) > limit {
			records = records[:limit]
		}

		return records
	}

	// 内存缓存查询
	al.mu.RLock()
	defer al.mu.RUnlock()

	records, exists := al.indexBySource[sourceID]
	if !exists {
		return []*EvidenceRecord{}
	}

	return al.filterByTimeRange(records, startTime, endTime, limit)
}

// QueryByTarget 根据目标ID查询
func (al *AuditLog) QueryByTarget(targetID string, startTime, endTime time.Time, limit int) []*EvidenceRecord {
	if al.store != nil {
		var startTimePtr, endTimePtr *time.Time
		if !startTime.IsZero() {
			startTimePtr = &startTime
		}
		if !endTime.IsZero() {
			endTimePtr = &endTime
		}

		records, err := al.store.GetByTarget(targetID, startTimePtr, endTimePtr)
		if err != nil {
			log.Printf("[WARN] Failed to query evidence from database: %v", err)
			return []*EvidenceRecord{}
		}

		if limit > 0 && len(records) > limit {
			records = records[:limit]
		}

		return records
	}

	// 内存缓存查询
	al.mu.RLock()
	defer al.mu.RUnlock()

	records, exists := al.indexByTarget[targetID]
	if !exists {
		return []*EvidenceRecord{}
	}

	return al.filterByTimeRange(records, startTime, endTime, limit)
}

// QueryByDirection 根据方向查询
func (al *AuditLog) QueryByDirection(direction EvidenceDirection, startTime, endTime time.Time, limit int) []*EvidenceRecord {
	if al.store != nil {
		var startTimePtr, endTimePtr *time.Time
		if !startTime.IsZero() {
			startTimePtr = &startTime
		}
		if !endTime.IsZero() {
			endTimePtr = &endTime
		}

		records, err := al.store.GetByDirection(direction, startTimePtr, endTimePtr)
		if err != nil {
			log.Printf("[WARN] Failed to query evidence from database: %v", err)
			return []*EvidenceRecord{}
		}

		if limit > 0 && len(records) > limit {
			records = records[:limit]
		}

		return records
	}

	// 内存缓存查询
	al.mu.RLock()
	defer al.mu.RUnlock()

	var filtered []*EvidenceRecord
	for _, records := range al.indexByEventType {
		for _, record := range records {
			if record.Direction == direction {
				if (startTime.IsZero() || record.Timestamp.After(startTime)) &&
					(endTime.IsZero() || record.Timestamp.Before(endTime)) {
					filtered = append(filtered, record)
					if limit > 0 && len(filtered) >= limit {
						break
					}
				}
			}
		}
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}

	return filtered
}

// QueryByEventType 根据事件类型查询
func (al *AuditLog) QueryByEventType(eventType EventType, startTime, endTime time.Time, limit int) []*EvidenceRecord {
	if al.store != nil {
		var startTimePtr, endTimePtr *time.Time
		if !startTime.IsZero() {
			startTimePtr = &startTime
		}
		if !endTime.IsZero() {
			endTimePtr = &endTime
		}

		records, err := al.store.GetByEventType(eventType, startTimePtr, endTimePtr)
		if err != nil {
			log.Printf("[WARN] Failed to query evidence from database: %v", err)
			return []*EvidenceRecord{}
		}

		if limit > 0 && len(records) > limit {
			records = records[:limit]
		}

		return records
	}

	// 内存缓存查询
	al.mu.RLock()
	defer al.mu.RUnlock()

	records, exists := al.indexByEventType[eventType]
	if !exists {
		return []*EvidenceRecord{}
	}

	return al.filterByTimeRange(records, startTime, endTime, limit)
}

// filterByTimeRange 按时间范围过滤记录
func (al *AuditLog) filterByTimeRange(records []*EvidenceRecord, startTime, endTime time.Time, limit int) []*EvidenceRecord {
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

// SignFlow 对指定 flow_id 的所有记录生成流签名
// 在数据流结束时调用，获取链尾哈希并生成签名
func (al *AuditLog) SignFlow(flowID string) (*EvidenceRecord, error) {
	if flowID == "" {
		return nil, fmt.Errorf("flow_id is required")
	}

	// 获取该 flow_id 的所有记录
	records := al.QueryByFlowID(flowID, time.Time{}, time.Time{}, 0)
	if len(records) == 0 {
		return nil, fmt.Errorf("no records found for flow_id: %s", flowID)
	}

	// 获取链尾记录（最后一条）
	lastRecord := records[len(records)-1]
	chainTailHash := lastRecord.Hash

	// 生成流签名（对链尾哈希签名）
	timestamp := time.Now().Unix()
	signature, err := security.GenerateFlowSignature(chainTailHash, timestamp)
	if err != nil {
		return nil, fmt.Errorf("failed to generate flow signature: %w", err)
	}

	// 更新最后一条记录的 Signature
	lastRecord.Signature = signature

	// 如果有持久化存储，更新数据库
	if al.store != nil {
		if err := al.store.Update(lastRecord); err != nil {
			log.Printf("[WARN] Failed to update flow signature in database: %v", err)
		}
	}

	// 更新内存索引中的记录
	al.mu.Lock()
	if records, exists := al.indexByFlowID[flowID]; exists && len(records) > 0 {
		records[len(records)-1].Signature = signature
	}
	al.mu.Unlock()

	log.Printf("✅ Flow signature generated for flow_id: %s, chain_tail_hash: %s", flowID, chainTailHash)
	return lastRecord, nil
}

// VerifyFlow 验证指定 flow_id 的流签名
// 验证流程：获取链尾记录，验证 Signature 是否有效
func (al *AuditLog) VerifyFlow(flowID string) (bool, error) {
	if flowID == "" {
		return false, fmt.Errorf("flow_id is required")
	}

	// 获取该 flow_id 的所有记录
	records := al.QueryByFlowID(flowID, time.Time{}, time.Time{}, 0)
	if len(records) == 0 {
		return false, fmt.Errorf("no records found for flow_id: %s", flowID)
	}

	// 获取链尾记录
	lastRecord := records[len(records)-1]

	// 检查是否有流签名
	if lastRecord.Signature == "" {
		return false, fmt.Errorf("no flow signature found for flow_id: %s", flowID)
	}

	// 验证链尾哈希连续性（可选：验证整条链）
	if len(records) > 1 {
		for i := 1; i < len(records); i++ {
			if records[i].PrevHash != records[i-1].Hash {
				return false, fmt.Errorf("chain broken at record %d", i)
			}
		}
	}

	// 验证流签名
	chainTailHash := lastRecord.Hash
	timestamp := lastRecord.Timestamp.Unix()
	if err := security.VerifyFlowSignature(chainTailHash, timestamp, lastRecord.Signature); err != nil {
		return false, fmt.Errorf("flow signature verification failed: %w", err)
	}

	return true, nil
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
					al.mu.Lock()
					al.records = append(al.records, &record)

					if record.ChannelID != "" {
						al.indexByCh[record.ChannelID] = append(al.indexByCh[record.ChannelID], &record)
					}

					if record.SourceID != "" {
						al.indexBySource[record.SourceID] = append(al.indexBySource[record.SourceID], &record)
					}

					if record.TargetID != "" {
						al.indexByTarget[record.TargetID] = append(al.indexByTarget[record.TargetID], &record)
					}

					al.indexByEventType[record.EventType] = append(al.indexByEventType[record.EventType], &record)

					// 更新哈希链
					if record.PrevHash != "" {
						al.lastRecordHash = record.Hash
					}
					al.mu.Unlock()
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
// 内核存证通过频道传输，仅用于分布式存储，不涉及外部存证连接器
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
		return nil
	}

	// 序列化存证记录
	recordData, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to marshal evidence record: %w", err)
	}

	// 创建数据包（广播给所有订阅者）
	sequenceNumber := int64(len(al.records))

	packet := &circulation.DataPacket{
		ChannelID:      channelID,
		SequenceNumber: sequenceNumber,
		Payload:        recordData,
		Signature:      "",
		Timestamp:      record.Timestamp.Unix(),
		SenderID:       "kernel", // 内核作为发送者
		TargetIDs:      []string{}, // 广播给所有订阅者
		MessageType:    circulation.MessageTypeEvidence,
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


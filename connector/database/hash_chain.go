package database

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// BusinessChainManager 业务数据哈希链管理器
type BusinessChainManager struct {
	store Store
	mu    sync.RWMutex
}

// NewBusinessChainManager 创建哈希链管理器
func NewBusinessChainManager(store Store) *BusinessChainManager {
	return &BusinessChainManager{
		store: store,
	}
}

// BuildAndSignDataHash 构建并签名数据哈希
// 计算公式: SHA256(data + prev_data_hash)
// signature = RSA签名(data_hash + prev_signature)
func (m *BusinessChainManager) BuildAndSignDataHash(data []byte, channelID, prevSignature string) (dataHash string, signature string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	prevHash := ""
	record, err := m.store.GetLastRecordByChannel(channelID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get last record: %w", err)
	}
	if record != nil {
		prevHash = record.DataHash
	}

	hashInput := append(data, []byte(prevHash)...)
	hash := sha256.Sum256(hashInput)
	dataHash = hex.EncodeToString(hash[:])

	signatureData := dataHash + prevSignature
	signature = hex.EncodeToString([]byte(signatureData))

	newRecord := &BusinessChainRecord{
		ChannelID:     channelID,
		DataHash:      dataHash,
		PrevHash:      prevHash,
		PrevSignature: prevSignature,
		Signature:     signature,
		Timestamp:     time.Now(),
	}

	_, err = m.store.AppendChainRecord(newRecord)
	if err != nil {
		return "", "", fmt.Errorf("failed to save chain record: %w", err)
	}

	return dataHash, signature, nil
}

// ReceiveAndSign 接收数据并签名（接收方）
func (m *BusinessChainManager) ReceiveAndSign(data []byte, channelID, sourceSignature string) (signature string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	prevHash := ""
	record, err := m.store.GetLastRecordByChannel(channelID)
	if err != nil {
		return "", fmt.Errorf("failed to get last record: %w", err)
	}
	if record != nil {
		prevHash = record.DataHash
	}

	hashInput := append(data, []byte(prevHash)...)
	hash := sha256.Sum256(hashInput)
	dataHash := hex.EncodeToString(hash[:])

	signatureData := dataHash + sourceSignature
	signature = hex.EncodeToString([]byte(signatureData))

	newRecord := &BusinessChainRecord{
		ChannelID:     channelID,
		DataHash:      dataHash,
		PrevHash:      prevHash,
		PrevSignature: sourceSignature,
		Signature:     signature,
		Timestamp:     time.Now(),
	}

	_, err = m.store.AppendChainRecord(newRecord)
	if err != nil {
		return "", fmt.Errorf("failed to save chain record: %w", err)
	}
	return signature, nil
}

// HandleAck 处理ACK记录
// channelID: ACK所属频道
// prevSignature: 上一跳的签名（作为签名输入）
// signature: 当前节点对 prevSignature 的签名
func (m *BusinessChainManager) HandleAck(channelID, prevSignature, signature string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 去重：同一 (channelID, prevSignature) 组合只记录一次
	// 避免同一个 ACK 被多次调用 HandleAck（如 kernel-1 收到两个转发路径的 ACK）导致重复插入
	existingRecords, err := m.store.GetRecordsByChannel(channelID)
	if err == nil {
		for _, rec := range existingRecords {
			if rec.DataHash == "" && rec.PrevSignature == prevSignature {
				// 已存在相同 prevSignature 的 ACK 记录，跳过
				return nil
			}
		}
	}

	ackRecord := &BusinessChainRecord{
		ChannelID:     channelID,
		DataHash:      "", // ACK的data_hash为空
		PrevHash:      "",
		PrevSignature: prevSignature,
		Signature:     signature,
		Timestamp:     time.Now(),
	}

	_, err = m.store.AppendChainRecord(ackRecord)
	if err != nil {
		return fmt.Errorf("failed to save ack record: %w", err)
	}

	return nil
}

// GetLastDataHash 获取指定频道的最新data_hash
func (m *BusinessChainManager) GetLastDataHash(channelID string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	record, err := m.store.GetLastRecordByChannel(channelID)
	if err != nil {
		return "", fmt.Errorf("failed to get last record: %w", err)
	}
	if record == nil {
		return "", nil
	}
	return record.DataHash, nil
}

// VerifyChain 验证哈希链的完整性
func (m *BusinessChainManager) VerifyChain(channelID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	records, err := m.store.GetRecordsByChannel(channelID)
	if err != nil {
		return false, fmt.Errorf("failed to get records: %w", err)
	}

	if len(records) == 0 {
		return true, nil // 空链认为是有效的
	}

	// 验证每条记录
	for i, record := range records {
		// 跳过ACK记录（data_hash为空）
		if record.DataHash == "" {
			continue
		}

		// 跳过第一条记录
		if i == 0 {
			continue
		}

		// 找到前一条业务数据记录
		prevRecord := records[i-1]
		if prevRecord.DataHash == "" {
			// 上一条是ACK记录，找更前面的记录
			for j := i - 2; j >= 0; j-- {
				if records[j].DataHash != "" {
					prevRecord = records[j]
					break
				}
			}
		}

		// 验证 prev_hash
		if record.PrevHash != prevRecord.DataHash {
			return false, fmt.Errorf("hash chain broken at record %d: expected prev_hash=%s, got %s",
				record.ID, prevRecord.DataHash, record.PrevHash)
		}
	}

	return true, nil
}

// GetLastRecord 获取指定频道的最新记录（含 ACK，用于取 prevSignature）
func (m *BusinessChainManager) GetLastRecord(channelID string) (*BusinessChainRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.GetLastRecordWithAck(channelID)
}

// GetLastNonAckRecord 获取指定频道的最新业务记录（不含 ACK，用于取 prevHash）
func (m *BusinessChainManager) GetLastNonAckRecord(channelID string) (*BusinessChainRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.GetLastRecordByChannel(channelID)
}

// GetChainRecords 获取指定频道的所有记录（含 ACK）
func (m *BusinessChainManager) GetChainRecords(channelID string) ([]*BusinessChainRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.GetRecordsByChannel(channelID)
}

// RecordChainData 直接记录业务数据哈希链（由 connector 侧调用）
// dataHash: 当前数据包的 data_hash
// prevHash: 前一个数据包的 data_hash
// prevSignature: 前一个数据包的签名
// signature: 当前节点对 dataHash 的签名（可以为空）
func (m *BusinessChainManager) RecordChainData(channelID, dataHash, prevHash, prevSignature, signature string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	record := &BusinessChainRecord{
		ChannelID:     channelID,
		DataHash:      dataHash,
		PrevHash:      prevHash,
		PrevSignature: prevSignature,
		Signature:     signature,
		Timestamp:     time.Now(),
	}

	_, err := m.store.AppendChainRecord(record)
	if err != nil {
		return fmt.Errorf("failed to record chain data: %w", err)
	}
	return nil
}

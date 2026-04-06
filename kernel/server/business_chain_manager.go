package server

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/trusted-space/kernel/kernel/database"
	pb "github.com/trusted-space/kernel/proto/kernel/v1"
)

// BusinessChainManager 业务哈希链管理器（内核侧）
type BusinessChainManager struct {
	store *database.MySQLBusinessChainStore
}

// NewBusinessChainManager 创建业务哈希链管理器
func NewBusinessChainManager(dbManager *database.DBManager) *BusinessChainManager {
	var store *database.MySQLBusinessChainStore
	if dbManager != nil {
		store = database.NewMySQLBusinessChainStore(dbManager.GetDB())
	}
	return &BusinessChainManager{
		store: store,
	}
}

// RecordDataHash 记录业务数据哈希
// connectorID: 连接器ID（如 connector-A）
// channelID: 频道ID
// dataHash: 当前数据包的哈希
// prevHash: 前一个数据包的哈希
// prevSignature: 上一跳（connector/kernel）的签名
// signature: 当前节点（kernel）对 dataHash 的签名
func (m *BusinessChainManager) RecordDataHash(connectorID, channelID, dataHash, prevHash, prevSignature, signature string) error {
	if m.store == nil {
		return fmt.Errorf("store not initialized")
	}

	record := &database.BusinessChainRecord{
		ConnectorID:   connectorID,
		ChannelID:     channelID,
		DataHash:      dataHash,
		PrevHash:      prevHash,
		PrevSignature: prevSignature,
		Signature:    signature,
		Timestamp:    time.Now(),
	}

	_, err := m.store.InsertRecord(record)
	return err
}

// RecordAck 记录 ACK
// ACK 记录的 data_hash 为空，通过 prev_signature 和 signature 形成签名链
func (m *BusinessChainManager) RecordAck(connectorID, channelID, prevSignature, signature string) error {
	if m.store == nil {
		log.Printf("[WARN] RecordAck: store is nil, cannot record ACK")
		return fmt.Errorf("store not initialized")
	}

	record := &database.BusinessChainRecord{
		ConnectorID:   connectorID,
		ChannelID:     channelID,
		DataHash:      "", // ACK 记录为空
		PrevHash:     "",
		PrevSignature: prevSignature, // 上一跳的签名
		Signature:    signature,      // 当前节点对 prevSignature 的签名
		Timestamp:    time.Now(),
	}

	// 去重：同一 (channelID, prevSignature) 组合的 ACK 只记录一次
	// 防止 reverse routing 中 ACK 沿多条路径返回时导致重复插入
	existingRecords, err := m.store.GetRecords(channelID, 100)
	if err == nil {
		for _, rec := range existingRecords {
			if rec.DataHash == "" && rec.PrevSignature == prevSignature {
				return nil
			}
		}
	}

	id, err := m.store.InsertRecord(record)
	if err != nil {
		log.Printf("[WARN] RecordAck InsertRecord failed: %v", err)
	} else {
		log.Printf("[OK] RecordAck succeeded: id=%d, channelID=%s", id, channelID)
	}
	return err
}

// GetLastDataHash 获取指定频道的最新 data_hash
func (m *BusinessChainManager) GetLastDataHash(channelID string) (dataHash string, prevHash string, err error) {
	if m.store == nil {
		return "", "", fmt.Errorf("store not initialized")
	}

	record, err := m.store.GetLastRecord(channelID)
	if err != nil {
		return "", "", err
	}
	if record == nil {
		return "", "", nil
	}
	return record.DataHash, record.PrevHash, nil
}

// GetRecords 获取指定频道的记录列表（按 ID 升序）
func (m *BusinessChainManager) GetRecords(channelID string, limit int) ([]*database.BusinessChainRecord, error) {
	if m.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	return m.store.GetRecords(channelID, limit)
}

// BusinessChainServiceServer 业务数据哈希链服务实现
// 注意：data_hash 的计算和签名已集成到 StreamData 流式传输过程中，
// 此服务仅用于查询和同步操作。
type BusinessChainServiceServer struct {
	pb.UnimplementedBusinessChainServiceServer
	manager *BusinessChainManager
}

// NewBusinessChainServiceServer 创建业务哈希链服务
func NewBusinessChainServiceServer(dbManager *database.DBManager) *BusinessChainServiceServer {
	return &BusinessChainServiceServer{
		manager: NewBusinessChainManager(dbManager),
	}
}

// Manager 返回底层的 BusinessChainManager，供其他组件（如 KernelServiceServer）直接调用
func (s *BusinessChainServiceServer) Manager() *BusinessChainManager {
	return s.manager
}

// GetLastDataHash 获取最新 data_hash
func (s *BusinessChainServiceServer) GetLastDataHash(ctx context.Context, req *pb.GetLastDataHashRequest) (*pb.GetLastDataHashResponse, error) {
	if s.manager == nil || s.manager.store == nil {
		return &pb.GetLastDataHashResponse{
			Success:     false,
			LastDataHash: "",
			Message:     "business chain store not initialized",
		}, nil
	}

	lastHash, _, err := s.manager.GetLastDataHash(req.ChannelId)
	if err != nil {
		return &pb.GetLastDataHashResponse{
			Success:     false,
			LastDataHash: "",
			Message:     fmt.Sprintf("failed to get last hash: %v", err),
		}, nil
	}

	return &pb.GetLastDataHashResponse{
		Success:     true,
		LastDataHash: lastHash,
		Message:     "success",
	}, nil
}

// QueryChain 查询哈希链
func (s *BusinessChainServiceServer) QueryChain(ctx context.Context, req *pb.QueryChainRequest) (*pb.QueryChainResponse, error) {
	if s.manager == nil || s.manager.store == nil {
		return &pb.QueryChainResponse{
			Success: false,
			Message: "business chain store not initialized",
		}, nil
	}

	limit := int(req.Limit)
	if limit <= 0 {
		limit = 100
	}

	records, err := s.manager.store.GetRecords(req.ChannelId, limit)
	if err != nil {
		return &pb.QueryChainResponse{
			Success: false,
			Message: fmt.Sprintf("failed to get records: %v", err),
		}, nil
	}

	count, _ := s.manager.store.GetRecordCount(req.ChannelId)

	var pbRecords []*pb.ChainRecord
	for _, record := range records {
		pbRecords = append(pbRecords, &pb.ChainRecord{
			Id:            record.ID,
			ChannelId:     record.ChannelID,
			DataHash:      record.DataHash,
			PrevDataHash:  record.PrevHash,
			PrevSignature: record.PrevSignature,
			Signature:     record.Signature,
			Timestamp:     record.Timestamp.Unix(),
		})
	}

	return &pb.QueryChainResponse{
		Success:    true,
		Message:    "success",
		Records:    pbRecords,
		TotalCount: int32(count),
	}, nil
}

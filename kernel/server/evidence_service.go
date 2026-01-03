package server

import (
	"context"
	"fmt"
	"time"

	pb "github.com/trusted-space/kernel/proto/kernel/v1"
	"github.com/trusted-space/kernel/kernel/evidence"
	"github.com/trusted-space/kernel/kernel/security"
)

// EvidenceServiceServer 实现存证服务
type EvidenceServiceServer struct {
	pb.UnimplementedEvidenceServiceServer
	auditLog *evidence.AuditLog
}

// NewEvidenceServiceServer 创建存证服务
func NewEvidenceServiceServer(auditLog *evidence.AuditLog) *EvidenceServiceServer {
	return &EvidenceServiceServer{
		auditLog: auditLog,
	}
}

// NewEvidenceServiceServerWithChannel 创建支持分布式存证的存证服务
func NewEvidenceServiceServerWithChannel(channelManager interface{}, persistent bool, logFilePath string) (*EvidenceServiceServer, error) {
	auditLog, err := evidence.NewAuditLog(persistent, logFilePath, channelManager)
	if err != nil {
		return nil, err
	}

	return &EvidenceServiceServer{
		auditLog: auditLog,
	}, nil
}

// SubmitEvidence 提交存证
func (s *EvidenceServiceServer) SubmitEvidence(ctx context.Context, req *pb.EvidenceRequest) (*pb.EvidenceResponse, error) {
	// 验证提交者身份
	if err := security.VerifyConnectorID(ctx, req.ConnectorId); err != nil {
		return &pb.EvidenceResponse{
			Committed: false,
			Message:   fmt.Sprintf("authentication failed: %v", err),
		}, nil
	}

	// 解析事件类型
	eventType := evidence.EventType(req.EventType)

	// 提交存证
	record, err := s.auditLog.SubmitEvidence(
		req.ConnectorId,
		eventType,
		req.ChannelId,
		req.DataHash,
		req.Metadata,
	)
	if err != nil {
		return &pb.EvidenceResponse{
			Committed: false,
			Message:   fmt.Sprintf("failed to submit evidence: %v", err),
		}, nil
	}

	return &pb.EvidenceResponse{
		EvidenceTxId: record.TxID,
		Committed:    true,
		Message:      "evidence committed successfully",
		Timestamp:    record.Timestamp.Unix(),
	}, nil
}

// QueryEvidence 查询存证记录
func (s *EvidenceServiceServer) QueryEvidence(ctx context.Context, req *pb.QueryRequest) (*pb.QueryResponse, error) {
	// 验证查询者身份（从证书中提取）
	querierID, err := security.ExtractConnectorIDFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	var records []*evidence.EvidenceRecord

	// 根据不同条件查询
	if req.ChannelId != "" {
		// 按频道查询
		startTime := time.Unix(req.StartTimestamp, 0)
		endTime := time.Unix(req.EndTimestamp, 0)
		if req.EndTimestamp == 0 {
			endTime = time.Time{} // 零值表示不限制
		}
		
		limit := int(req.Limit)
		if limit == 0 {
			limit = 100 // 默认限制
		}

		records = s.auditLog.QueryByChannel(req.ChannelId, startTime, endTime, limit)
	} else if req.ConnectorId != "" {
		// 按连接器查询
		// 只能查询自己的记录（权限控制）
		if req.ConnectorId != querierID {
			return nil, fmt.Errorf("permission denied: can only query own records")
		}

		startTime := time.Unix(req.StartTimestamp, 0)
		endTime := time.Unix(req.EndTimestamp, 0)
		if req.EndTimestamp == 0 {
			endTime = time.Time{}
		}

		limit := int(req.Limit)
		if limit == 0 {
			limit = 100
		}

		records = s.auditLog.QueryByConnector(req.ConnectorId, startTime, endTime, limit)
	} else {
		// 无有效查询条件
		return nil, fmt.Errorf("must specify either channel_id or connector_id")
	}

	// 转换为 protobuf 格式
	pbRecords := make([]*pb.EvidenceRecord, len(records))
	for i, record := range records {
		pbRecords[i] = &pb.EvidenceRecord{
			EvidenceTxId: record.TxID,
			Evidence: &pb.EvidenceRequest{
				ConnectorId: record.ConnectorID,
				EventType:   string(record.EventType),
				ChannelId:   record.ChannelID,
				DataHash:    record.DataHash,
				Timestamp:   record.Timestamp.Format(time.RFC3339),
				Signature:   record.Signature,
				Metadata:    record.Metadata,
			},
			StoredTimestamp: record.Timestamp.Unix(),
		}
	}

	return &pb.QueryResponse{
		Logs:       pbRecords,
		TotalCount: int32(len(pbRecords)),
	}, nil
}

// VerifyEvidenceSignature 验证存证记录的数字签名
func (s *EvidenceServiceServer) VerifyEvidenceSignature(ctx context.Context, req *pb.VerifySignatureRequest) (*pb.VerifySignatureResponse, error) {
	// 验证请求者身份
	if err := security.VerifyConnectorID(ctx, req.RequesterId); err != nil {
		return &pb.VerifySignatureResponse{
			Valid:      false,
			Message:    fmt.Sprintf("身份验证失败: %v", err),
			VerifiedAt: time.Now().Unix(),
		}, nil
	}

	// 验证签名
	err := security.VerifyEvidenceSignature(req.ConnectorId, req.EventType, req.ChannelId, req.DataHash, req.Signature, req.Timestamp)

	response := &pb.VerifySignatureResponse{
		Valid:      err == nil,
		VerifiedAt: time.Now().Unix(),
	}

	if err != nil {
		response.Message = fmt.Sprintf("签名验证失败: %v", err)
	} else {
		response.Message = "签名验证成功，存证记录真实可信"
	}

	return response, nil
}


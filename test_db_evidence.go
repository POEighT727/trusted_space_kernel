package main

import (
	"fmt"
	"log"
	"time"

	"github.com/trusted-space/kernel/kernel/database"
	"github.com/trusted-space/kernel/kernel/evidence"
)

func main() {
	fmt.Println("=== 存证溯源模块 - MySQL数据库测试 ===")

	// MySQL配置（请根据实际情况修改）
	config := database.MySQLConfig{
		Host:     "localhost",
		Port:     3306,
		User:     "trusted_space",
		Password: "password",
		Database: "trusted_space",
	}

	// 初始化数据库管理器
	dbManager, err := database.NewDBManager(config)
	if err != nil {
		log.Printf("数据库连接失败（这是预期的，如果没有MySQL环境）: %v", err)
		fmt.Println("⚠️  跳过数据库测试，请确保MySQL服务已启动并配置正确")
		return
	}
	defer dbManager.Close()

	// 创建证据存储
	store := database.NewMySQLEvidenceStore(dbManager.GetDB())

	fmt.Println("✓ 数据库连接成功")

	// 测试存储证据记录
	record := &evidence.EvidenceRecord{
		TxID:        "test-tx-001",
		ConnectorID: "connector-test",
		EventType:   evidence.EventTypePermissionRequest,
		ChannelID:   "channel-test",
		DataHash:    "testhash123",
		Signature:   "testsignature",
		Timestamp:   time.Now(),
		Metadata: map[string]string{
			"test":      "true",
			"requester": "connector-test",
			"role":      "sender",
		},
	}

	// 计算哈希
	record.Hash = fmt.Sprintf("%x", record.Hash) // 简化哈希计算

	// 存储记录
	err = store.Store(record)
	if err != nil {
		log.Printf("存储证据记录失败: %v", err)
		return
	}

	fmt.Println("✓ 证据记录存储成功")

	// 测试查询
	records, err := store.GetByTxID("test-tx-001")
	if err != nil {
		log.Printf("查询证据记录失败: %v", err)
		return
	}

	if len(records) > 0 {
		fmt.Printf("✓ 查询成功，找到 %d 条记录\n", len(records))
		fmt.Printf("  事件类型: %s\n", records[0].EventType)
		fmt.Printf("  连接器ID: %s\n", records[0].ConnectorID)
		fmt.Printf("  频道ID: %s\n", records[0].ChannelID)
	}

	// 测试过滤器查询
	filter := evidence.EvidenceFilter{
		EventType: string(evidence.EventTypePermissionRequest),
		Limit:     10,
	}

	filteredRecords, err := store.Query(filter)
	if err != nil {
		log.Printf("过滤器查询失败: %v", err)
		return
	}

	fmt.Printf("✓ 过滤器查询成功，找到 %d 条权限请求记录\n", len(filteredRecords))

	// 统计数量
	count, err := store.Count(filter)
	if err != nil {
		log.Printf("统计失败: %v", err)
		return
	}

	fmt.Printf("✓ 统计成功，总共 %d 条权限请求记录\n", count)

	fmt.Println("=== 测试完成 ===")
	fmt.Println("\n📊 新增事件类型:")
	eventTypes := []evidence.EventType{
		evidence.EventTypePermissionRequest,
		evidence.EventTypePermissionGranted,
		evidence.EventTypePermissionDenied,
		evidence.EventTypePermissionRevoked,
		evidence.EventTypeSecurityViolation,
		evidence.EventTypeDataTampering,
		evidence.EventTypeEvidenceIntegrityFail,
		evidence.EventTypeSuspiciousActivity,
	}

	for _, et := range eventTypes {
		fmt.Printf("  - %s\n", et)
	}
}

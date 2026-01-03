# 存证溯源模块 - MySQL数据库支持

## 概述

存证溯源模块现已支持MySQL数据库化存储，大幅提升了查询性能和存储可靠性。同时增加了丰富的事件类型以覆盖更多安全场景。

## 新增功能

### 🗄️ MySQL数据库存储

#### 配置方式
在 `config/kernel.yaml` 中启用数据库：

```yaml
evidence:
  persistent: true
  log_file_path: "logs/audit.log"
  use_database: false  # 是否启用数据库存储

database:
  enabled: true        # 是否启用数据库
  host: "localhost"
  port: 3306
  user: "trusted_space"
  password: "password"
  database: "trusted_space"
```

#### 数据库表结构

**evidence_records** - 证据记录表
```sql
CREATE TABLE evidence_records (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    tx_id VARCHAR(36) NOT NULL COMMENT '事务ID',
    connector_id VARCHAR(100) NOT NULL COMMENT '连接器ID',
    event_type VARCHAR(50) NOT NULL COMMENT '事件类型',
    channel_id VARCHAR(36) NOT NULL COMMENT '频道ID',
    data_hash VARCHAR(128) DEFAULT '' COMMENT '数据哈希',
    signature TEXT COMMENT '数字签名',
    timestamp TIMESTAMP(6) NOT NULL COMMENT '时间戳',
    metadata JSON COMMENT '元数据',
    record_hash VARCHAR(128) NOT NULL COMMENT '记录哈希',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    INDEX idx_tx_id (tx_id),
    INDEX idx_connector_id (connector_id),
    INDEX idx_event_type (event_type),
    INDEX idx_channel_id (channel_id),
    INDEX idx_timestamp (timestamp),
    UNIQUE KEY uk_tx_event (tx_id, event_type)
);
```

**evidence_delivery** - 证据分发表
```sql
CREATE TABLE evidence_delivery (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    record_id BIGINT NOT NULL COMMENT '证据记录ID',
    connector_id VARCHAR(100) NOT NULL COMMENT '接收连接器ID',
    channel_id VARCHAR(36) NOT NULL COMMENT '证据频道ID',
    status ENUM('pending', 'delivered', 'failed') DEFAULT 'pending' COMMENT '分发状态',
    attempts INT DEFAULT 0 COMMENT '尝试次数',
    last_attempt TIMESTAMP NULL COMMENT '最后尝试时间',
    error_message TEXT COMMENT '错误信息',
    delivered_at TIMESTAMP NULL COMMENT '分发成功时间',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间'
);
```

**evidence_verification** - 证据验证表
```sql
CREATE TABLE evidence_verification (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    record_id BIGINT NOT NULL COMMENT '证据记录ID',
    connector_id VARCHAR(100) NOT NULL COMMENT '验证连接器ID',
    verification_type VARCHAR(50) NOT NULL COMMENT '验证类型',
    is_valid BOOLEAN NOT NULL COMMENT '是否有效',
    error_message TEXT COMMENT '验证错误信息',
    verified_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP COMMENT '验证时间'
);
```

### 🎯 新增事件类型

#### 数据传输事件
- `TRANSFER_START` - 数据传输开始
- `TRANSFER_END` - 数据传输结束
- `DATA_RECEIVED` - 数据接收
- `FILE_TRANSFER_START` - 文件传输开始
- `FILE_TRANSFER_END` - 文件传输结束

#### 频道管理事件
- `CHANNEL_CREATED` - 频道创建
- `CHANNEL_CLOSED` - 频道关闭
- `CHANNEL_ACTIVATED` - 频道激活
- `CHANNEL_SUSPENDED` - 频道暂停
- `CHANNEL_RESUMED` - 频道恢复

#### 权限管理事件
- `PERMISSION_REQUEST` - 权限申请
- `PERMISSION_GRANTED` - 权限批准
- `PERMISSION_DENIED` - 权限拒绝
- `PERMISSION_REVOKED` - 权限撤销
- `ROLE_CHANGED` - 角色变更

#### 安全事件
- `POLICY_VIOLATION` - 策略违反
- `SECURITY_VIOLATION` - 安全违规
- `DATA_TAMPERING` - 数据篡改
- `INTEGRITY_CHECK_FAIL` - 完整性检查失败
- `SUSPICIOUS_ACTIVITY` - 可疑活动
- `ACCESS_DENIED` - 访问拒绝

#### 身份认证事件
- `AUTH_SUCCESS` - 认证成功
- `AUTH_FAIL` - 认证失败
- `AUTH_ATTEMPT` - 认证尝试
- `AUTH_LOGOUT` - 认证登出
- `TOKEN_GENERATED` - 令牌生成
- `TOKEN_VALIDATED` - 令牌验证
- `TOKEN_EXPIRED` - 令牌过期

#### 连接器事件
- `CONNECTOR_REGISTERED` - 连接器注册
- `CONNECTOR_UNREGISTERED` - 连接器注销
- `CONNECTOR_STATUS_CHANGED` - 连接器状态变更
- `CONNECTOR_HEARTBEAT` - 连接器心跳
- `CONNECTOR_OFFLINE` - 连接器离线
- `CONNECTOR_ONLINE` - 连接器上线

#### 证据相关事件
- `EVIDENCE_GENERATED` - 证据生成
- `EVIDENCE_VERIFIED` - 证据验证
- `EVIDENCE_INTEGRITY_FAIL` - 证据完整性失败
- `EVIDENCE_DISTRIBUTED` - 证据分发
- `EVIDENCE_STORED` - 证据存储

#### 系统事件
- `SYSTEM_STARTUP` - 系统启动
- `SYSTEM_SHUTDOWN` - 系统关闭
- `CONFIG_CHANGED` - 配置变更
- `BACKUP_CREATED` - 备份创建
- `MAINTENANCE_START` - 维护开始
- `MAINTENANCE_END` - 维护结束

## API接口

### 证据存储接口
```go
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
    VerifyRecord(record *EvidenceRecord) error
    Close() error
}
```

### 证据过滤器
```go
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
```

## 使用示例

### 1. 启用数据库存储
```yaml
# config/kernel.yaml
database:
  enabled: true
  host: "localhost"
  port: 3306
  user: "trusted_space"
  password: "your_password"
  database: "trusted_space"
```

### 2. 查询证据记录
```go
// 创建过滤器
filter := evidence.EvidenceFilter{
    EventType: string(evidence.EventTypePermissionRequest),
    StartTime: &startTime,
    EndTime:   &endTime,
    Limit:     100,
}

// 查询记录
records, err := auditLog.Query(filter)
if err != nil {
    log.Printf("查询失败: %v", err)
    return
}

// 处理结果
for _, record := range records {
    fmt.Printf("事件: %s, 时间: %s, 连接器: %s\n",
        record.EventType, record.Timestamp, record.ConnectorID)
}
```

### 3. 统计分析
```go
// 统计特定类型事件数量
count, err := auditLog.Count(evidence.EvidenceFilter{
    EventType: string(evidence.EventTypeSecurityViolation),
})
if err != nil {
    log.Printf("统计失败: %v", err)
    return
}

fmt.Printf("发现 %d 起安全违规事件\n", count)
```

## 性能优势

### 数据库存储 vs 文件存储

| 特性 | 文件存储 | 数据库存储 |
|------|----------|------------|
| 查询性能 | O(n) | O(log n) + 索引 |
| 并发访问 | 文件锁竞争 | 数据库事务 |
| 数据完整性 | 哈希验证 | ACID事务 |
| 存储容量 | 受文件系统限制 | 数据库容量 |
| 备份恢复 | 文件拷贝 | 数据库工具 |
| 多条件查询 | 遍历过滤 | SQL查询 |
| 统计分析 | 内存计算 | SQL聚合函数 |

### 索引优化
- 事务ID索引：快速定位相关事件
- 连接器ID索引：跟踪特定连接器活动
- 事件类型索引：统计各类事件
- 频道ID索引：频道活动分析
- 时间戳索引：时间范围查询

## 安全特性

1. **数字签名**：所有证据记录都经过RSA数字签名
2. **哈希验证**：记录内容完整性校验
3. **不可篡改**：数据库级别的数据保护
4. **访问控制**：基于角色的证据访问权限
5. **审计追踪**：所有证据操作都有审计记录

## 部署建议

### 数据库配置
```sql
-- 创建数据库
CREATE DATABASE trusted_space CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 创建用户
CREATE USER 'trusted_space'@'%' IDENTIFIED BY 'password';
GRANT ALL PRIVILEGES ON trusted_space.* TO 'trusted_space'@'%';
FLUSH PRIVILEGES;
```

### 连接池配置
```go
db.SetMaxOpenConns(25)
db.SetMaxIdleConns(25)
db.SetConnMaxLifetime(5 * time.Minute)
```

### 监控指标
- 证据生成速率
- 存储延迟
- 查询响应时间
- 数据库连接状态
- 磁盘使用率

## 故障排查

### 常见问题

1. **数据库连接失败**
   - 检查MySQL服务状态
   - 验证连接参数
   - 检查网络连通性

2. **查询性能慢**
   - 检查索引是否创建
   - 分析查询执行计划
   - 考虑分页查询

3. **存储失败**
   - 检查磁盘空间
   - 验证数据库权限
   - 检查表结构完整性

### 日志分析
```bash
# 查看证据生成统计
grep "EVIDENCE SUBMIT" logs/audit.log | wc -l

# 分析事件类型分布
grep "EVIDENCE SUBMIT" logs/audit.log | awk -F', ' '{print $2}' | sort | uniq -c
```

## 未来扩展

1. **多数据库支持**：PostgreSQL、MongoDB等
2. **分布式存储**：分片和复制
3. **实时分析**：流处理和实时告警
4. **可视化界面**：Web控制台
5. **API接口**：RESTful查询接口
6. **数据导出**：多种格式导出
7. **备份策略**：自动备份和恢复

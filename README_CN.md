# 可信数据空间内核架构 - 详细设计文档

## 一、设计原则

本架构严格遵循《内核外延方案》的核心理念：

1. **内核标准化**：内核是标准化的"操作系统"，提供统一的接口规范
2. **外延灵活性**：连接器作为外延组件，可灵活适配各种业务场景
3. **互联互通**：通过标准 gRPC 接口实现跨组织、跨系统的互操作性
4. **安全底座**：基于 mTLS 的零信任安全架构

## 二、核心设计

### 2.1 三层架构

```
┌─────────────────────────────────────────────────┐
│          实体外延层 (Extension Layer)            │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐      │
│  │数据库连接器│  │算法连接器│  │应用连接器│      │
│  └─────┬────┘  └─────┬────┘  └─────┬────┘      │
└────────┼─────────────┼─────────────┼────────────┘
         │ mTLS        │ mTLS        │ mTLS
┌────────┼─────────────┼─────────────┼────────────┐
│        │  可信交互层 (gRPC/mTLS)    │            │
│        ↓             ↓             ↓            │
│  ┌────────────────────────────────────────┐    │
│  │         核心内核层 (Kernel Layer)       │    │
│  │  ┌──────────┐  ┌──────────┐           │    │
│  │  │安全认证  │  │管控模块  │           │    │
│  │  └──────────┘  └──────────┘           │    │
│  │  ┌──────────┐  ┌──────────┐           │    │
│  │  │流通调度  │  │存证模块  │           │    │
│  │  └──────────┘  └──────────┘           │    │
│  └────────────────────────────────────────┘    │
└─────────────────────────────────────────────────┘
```

### 2.2 身份验证与连接建立流程

```
连接器                     内核
   │                         │
   │   1. mTLS握手（携带证书）│
   ├────────────────────────>│
   │                         │ 验证证书CN
   │   2. 握手响应            │ 检查CA签名
   │<────────────────────────┤
   │                         │
   │   3. Handshake(ID,Type) │
   ├────────────────────────>│
   │                         │ 验证ID与证书匹配
   │   4. 会话令牌           │ 注册到Registry
   │<────────────────────────┤ 记录存证
   │                         │
   │   5. 心跳（15秒间隔）    │
   ├────────────────────────>│
   │                         │
```

### 2.3 数据流通流程

```
发送方连接器          内核            接收方连接器
     │                 │                   │
     │ CreateChannel   │                   │
     ├────────────────>│                   │
     │                 │ 权限检查          │
     │                 │ 创建频道          │
     │                 │ 记录存证          │
     │ ChannelID       │                   │
     │<────────────────┤                   │
     │                 │                   │
     │                 │   SubscribeData   │
     │                 │<──────────────────┤
     │                 │                   │
     │ StreamData      │                   │
     ├────────────────>│                   │
     │                 │ 数据包1           │
     │                 ├──────────────────>│
     │ 确认            │                   │
     │<────────────────┤                   │
     │                 │ 数据包2           │
     │                 ├──────────────────>│
     │                 │                   │
     │ CloseChannel    │                   │
     ├────────────────>│                   │
     │                 │ 关闭流            │
     │                 │ 记录存证          │
     │                 ├──────────────────>│
```

### 2.4 存证溯源机制

每条存证记录采用链式结构：

```
Record N:
{
  TxID: "uuid",
  EventType: "TRANSFER_START",
  DataHash: "sha256(...)",
  PrevHash: "hash of Record N-1",
  RecordHash: "sha256(this record)"
}
    │
    ↓ 链接
Record N+1:
{
  TxID: "uuid",
  EventType: "TRANSFER_END",
  PrevHash: "hash of Record N",  ← 指向前一条
  RecordHash: "sha256(this record)"
}
```

验证完整性：

```go
// 验证链式结构
for i, record := range records {
    if i > 0 {
        if record.PrevHash != records[i-1].RecordHash {
            return errors.New("chain broken")
        }
    }
    if calculateHash(record) != record.RecordHash {
        return errors.New("record tampered")
    }
}
```

## 三、关键实现细节

### 3.1 mTLS 双向认证

**证书结构**：

```
CA (自签名)
 ├── kernel.crt (CN=trusted-data-space-kernel)
 ├── connector-A.crt (CN=connector-A)
 ├── connector-B.crt (CN=connector-B)
 └── connector-C.crt (CN=connector-C)
```

**验证逻辑**：

1. TCP 层：TLS 握手验证证书有效性
2. gRPC 层：从证书提取 CN 作为 connector_id
3. 应用层：验证请求中的 ID 与证书 CN 匹配

### 3.2 权限策略引擎

**规则格式**：

```go
type PolicyRule struct {
    SenderID   string   // 发送方 ID
    ReceiverID string   // 接收方 ID（支持 "*" 通配符）
    DataTopics []string // 允许的数据主题（支持 "*"）
    Allowed    bool     // 是否允许
}
```

**匹配优先级**：

1. 精确匹配：`sender:receiver`
2. 通配符匹配：`sender:*`
3. 默认策略：`default_allow` 配置

### 3.3 频道生命周期管理

```
Created → Active → Closed → Cleanup
   │         │         │         │
   │         │         │         └─ 1小时后自动清理
   │         │         └─────────── CloseChannel()
   │         └─────────────────────── StreamData/Subscribe
   └───────────────────────────────── CreateChannel()
```

**自动清理**：
- 健康检查：每 30 秒检查连接器心跳
- 频道清理：每 10 分钟清理已关闭且 1 小时无活动的频道

## 四、扩展性设计

### 4.1 水平扩展

**内核集群部署**：

```
               负载均衡器
                   │
      ┌────────────┼────────────┐
      ↓            ↓            ↓
   Kernel-1    Kernel-2    Kernel-3
      │            │            │
      └────────────┴────────────┘
               共享存储
           (审计日志、配置)
```

**状态同步策略**：
- 无状态设计：连接器可连接任意内核实例
- 会话令牌：使用 JWT 签名的令牌，各节点独立验证
- 审计日志：集中存储到分布式数据库或区块链

### 4.2 集成区块链

将审计日志锚定到区块链：

```go
// 定期将审计日志哈希提交到区块链
func (al *AuditLog) AnchorToBlockchain(interval time.Duration) {
    ticker := time.NewTicker(interval)
    for range ticker.C {
        // 获取最近的记录
        latestRecord := al.records[len(al.records)-1]
        
        // 提交到区块链
        txHash := blockchain.SubmitProof(latestRecord.RecordHash)
        
        // 记录锚定信息
        al.SubmitEvidence(
            "kernel",
            "BLOCKCHAIN_ANCHOR",
            "",
            latestRecord.RecordHash,
            "",
            map[string]string{"tx_hash": txHash},
        )
    }
}
```

### 4.3 插件化扩展

**策略引擎插件**：

```go
type PolicyPlugin interface {
    Name() string
    CheckPermission(req *PermissionRequest) (bool, error)
}

// 注册自定义策略
policyEngine.RegisterPlugin(NewABACPlugin())
policyEngine.RegisterPlugin(NewRBACPlugin())
```

**存证插件**：

```go
type EvidenceBackend interface {
    Store(record *EvidenceRecord) error
    Query(query *Query) ([]*EvidenceRecord, error)
}

// 支持多种存储后端
auditLog.AddBackend(NewIPFSBackend())
auditLog.AddBackend(NewBlockchainBackend())
```

## 五、性能优化

### 5.1 数据传输优化

- **流式传输**：使用 gRPC 双向流，避免大数据包
- **缓冲队列**：每个频道 1000 个数据包的缓冲
- **并发订阅**：单个频道支持多个订阅者

### 5.2 存证性能优化

- **批量写入**：累积多条记录后批量写入磁盘
- **异步存证**：存证操作不阻塞数据传输
- **索引优化**：对 ChannelID、ConnectorID 建立索引

### 5.3 内存管理

- **连接池**：复用 gRPC 连接
- **频道回收**：自动关闭长时间不活跃的频道
- **日志滚动**：审计日志按日期或大小分片

## 六、安全加固

### 6.1 防御深度

1. **传输层**：TLS 1.3 加密
2. **认证层**：mTLS 双向认证
3. **授权层**：策略引擎细粒度控制
4. **审计层**：所有操作全程记录

### 6.2 攻击防护

**防重放攻击**：
```go
// 检查时间戳是否在合理范围内
if time.Now().Unix() - req.Timestamp > 300 {
    return errors.New("request expired")
}
```

**防 DDoS**：
```go
// 限制每个连接器的频道创建速率
rateLimiter := NewRateLimiter(10, time.Minute) // 每分钟最多10个
if !rateLimiter.Allow(connectorID) {
    return errors.New("rate limit exceeded")
}
```

### 6.3 数据隐私

- **端到端加密**：业务数据在连接器端加密，内核仅中转
- **数据指纹**：存证仅记录数据哈希，不存储原始数据
- **访问隔离**：连接器只能查询自己的存证记录

## 七、故障处理

### 7.1 连接器故障

- **自动重连**：连接器断开后自动重连
- **心跳超时**：超过 30 秒无心跳标记为离线
- **频道恢复**：连接器重连后可恢复订阅

### 7.2 内核故障

- **优雅关闭**：收到 SIGTERM 信号时完成当前传输
- **状态持久化**：审计日志持久化到磁盘
- **故障转移**：部署多个内核实例实现高可用

### 7.3 数据一致性

- **原子操作**：频道创建和权限检查在同一事务中
- **确认机制**：每个数据包都有确认应答
- **重传机制**：传输失败时支持重传

## 八、监控与运维

### 8.1 关键指标

- 连接器在线数量
- 活跃频道数量
- 数据传输速率
- 存证写入速率
- 认证失败次数
- 策略违规次数

### 8.2 日志规范

```
[时间] [级别] [模块] [连接器ID] [频道ID] 消息
[2024-01-01 10:00:00] [INFO] [Channel] [connector-A] [ch-123] Channel created
[2024-01-01 10:00:05] [ERROR] [Auth] [connector-X] [] Authentication failed
```

### 8.3 告警策略

- 错误率 > 5%：发送告警
- 连接器离线超过 5 分钟：通知管理员
- 审计日志写入失败：紧急告警

## 九、部署拓扑

### 9.1 单节点部署（测试环境）

```
┌─────────────────────┐
│   Kernel (50051)    │
└─────────────────────┘
         ↑ ↑ ↑
         │ │ │
    ┌────┘ │ └────┐
    │      │      │
Conn-A  Conn-B  Conn-C
```

### 9.2 集群部署（生产环境）

```
              ┌─────────────┐
              │ Load Balancer│
              └──────┬───────┘
                     │
        ┌────────────┼────────────┐
        ↓            ↓            ↓
   Kernel-1      Kernel-2     Kernel-3
        │            │            │
        └────────────┴────────────┘
                     │
            ┌────────┴────────┐
            ↓                 ↓
      PostgreSQL         Blockchain
      (审计日志)          (存证锚定)
```

## 十、总结

本架构实现了以下核心目标：

✅ **标准化接口**：通过 Protobuf 定义统一的 API  
✅ **安全保障**：基于 mTLS 的零信任架构  
✅ **可扩展性**：模块化设计支持灵活扩展  
✅ **可追溯性**：链式存证确保操作不可抵赖  
✅ **互操作性**：任何实现标准接口的连接器均可接入  

架构充分体现了"内核+外延"的设计理念，为可信数据空间的互联互通提供了坚实的技术基础。


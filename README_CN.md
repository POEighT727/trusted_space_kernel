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
     │                 │ 自动订阅          │
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

## 九、运行模式

### 9.1 交互模式（推荐，默认）

```bash
# 启动完整的内核管理控制台（推荐）
./bin/kernel -config config/kernel.yaml

# 端口分配：
# 50051: 主gRPC服务器（连接器连接）
# 50052: Bootstrap服务器（连接器证书注册）
# 50053: 内核间通信服务器（多内核模式）

# 特性：
# ✓ 完整的gRPC服务器（端口50051）
# ✓ 交互式管理界面
# ✓ 支持所有管理命令
# ✓ 多内核网络（默认启用）
# ✓ 实时监控和状态查询
```

**管理命令示例：**
```bash
[kernel-1] > status          # 查看内核状态
[kernel-1] > connectors      # 列出连接器
[kernel-1] > channels        # 列出频道
[kernel-1] > kernels         # 列出已知内核
[kernel-1] > connect-kernel kernel-2 192.168.1.100 50051
[kernel-1] > help            # 显示所有命令
```

### 9.2 守护进程模式

```bash
# 仅作为后台服务运行（生产环境）
./bin/kernel -config config/kernel.yaml -daemon

# 特性：
# ✓ 仅gRPC服务器运行
# ✓ 无交互界面
# ✓ 多内核网络（后台运行）
# ✓ 适合生产环境部署
# ✓ 可通过systemd等服务管理
```

## 十、独立部署包

### 10.1 内核部署包

内核支持独立打包部署：

```bash
# Linux/Mac 内核打包
make package-kernel-linux VERSION=1.0.0

# Windows 内核打包
make package-kernel-windows VERSION=1.0.0

# 所有平台内核打包
make package-kernel VERSION=1.0.0
```

生成的部署包包含：
- 内核可执行文件
- 配置模板
- 证书目录（运行时自动生成）
- 日志目录
- 频道配置目录
- 数据库目录
- 管理脚本（启动/停止/状态检查）

### 10.2 连接器部署包

```bash
# Linux/Mac 连接器打包
make package-connector-linux VERSION=1.0.0

# Windows 连接器打包
make package-connector-windows VERSION=1.0.0

# 所有平台连接器打包
make package-connector VERSION=1.0.0
```

### 10.3 一键打包所有组件

```bash
# 打包内核和连接器（所有平台）
make package-all VERSION=1.0.0

# 或使用统一脚本（推荐）
# Linux/Mac
./scripts/package_all.sh 1.0.0 linux-amd64 all

# Windows
.\scripts\package_all.ps1 -Version 1.0.0 -Platform windows-amd64 -Target all
```

#### 10.4 单独打包组件

```bash
# 只打包内核
make package-kernel VERSION=1.0.0
./scripts/package_all.sh 1.0.0 linux-amd64 kernel

# 只打包连接器
make package-connector VERSION=1.0.0
./scripts/package_all.sh 1.0.0 linux-amd64 connector
```

## 十一、多内核互联网络

### 11.1 概述

可信数据空间内核支持多内核组网模式，多个内核可以互相发现、互联互通，形成一个分布式的内核网络。这种模式适用于：
- 跨组织的数据空间互联
- 多数据中心部署
- 高可用容灾架构

### 11.2 架构设计

```
┌─────────────────────────────────────────────────────────────┐
│                    多内核互联网络                             │
│                                                             │
│    ┌──────────┐         ┌──────────┐         ┌──────────┐ │
│    │ kernel-1 │◄───────►│ kernel-2 │◄───────►│ kernel-3 │ │
│    │192.168.1.4:50053│   │192.168.202.136:50053│   │192.168.202.140:50053│ │
│    └────┬─────┘         └────┬─────┘         └────┬─────┘ │
│         │                    │                    │       │
│         │  ┌─────────────────┼──────────────────┐ │       │
│         └─►│   内核发现与同步  │◄─────────────────┘ │       │
│            │  (SyncKnownKernels)│                  │       │
│            └─────────────────┬──────────────────┘ │       │
│                              │                      │       │
└──────────────────────────────┼──────────────────────┼───────┘
                               │                      │
                    ┌──────────┴──────────┐          │
                    ↓                      ↓          │
              连接器-A                 连接器-B       │
```

### 11.3 端口配置

每个内核使用三个端口进行通信：

| 端口 | 用途 | 说明 |
|------|------|------|
| 50051 | 主服务端口 | 提供 IdentityService、ChannelService 等核心服务 |
| 50052 | 引导服务端口 | 用于证书注册（Bootstrap Server） |
| 50053 | 内核间通信端口 | 用于内核之间的互联（Kernel-to-Kernel Server） |

### 11.4 互联流程

#### 11.4.1 手动互联模式

```
内核-A                      内核-B
   │                          │
   │  1. connect-kernel <id> <addr> 50053
   ├─────────────────────────────────>
   │                          │
   │  2. 保存待审批请求
   │  ◄────────────────────────────────
   │                          │
   │                          │  3. approve-request <request-id>
   │                          ├─
   │                          │  4. 批准请求并建立双向连接
   │◄────────────────────────────────
   │                          │
   │  5. 广播已知内核列表
   ├─────────────────────────────────>
   │                          │
```

#### 11.4.2 详细步骤

**步骤1：发起互联请求**

在内核-A上执行：
```
connect-kernel kernel-B 192.168.202.136 50053
```

内核-A会：
1. 读取或创建对端CA证书
2. 发起TLS连接到内核-B的50053端口
3. 发送 `RegisterKernelRequest`（带 `interconnect_request: true` 元数据）
4. 如果内核-B返回 `interconnect_request_id`，保存为待审批请求

**步骤2：审批互联请求**

在内核-B上执行：
```
approve-request <request-id>
```

内核-B会：
1. 调用 `ApprovePendingRequest` 批准请求
2. 通过 `notifyRequesterApprove` 通知内核-A已批准
3. 建立到内核-A的持久连接
4. 调用 `BroadcastKnownKernels` 广播已知的内核列表

**步骤3：内核发现与同步**

当内核-A收到广播后：
1. 调用 `SyncKnownKernels` 同步内核列表
2. 将新内核添加到本地 `kernels` map
3. 尝试连接到新内核
4. 连接成功后，调用 `SyncKnownKernelsToKernel` 主动向新内核同步

### 11.5 内核发现机制

#### 11.5.1 同步协议

内核间使用 `SyncKnownKernels` RPC 进行信息同步：

```protobuf
// 同步已知内核请求
message SyncKnownKernelsRequest {
  string source_kernel_id = 1;   // 源内核ID
  repeated KernelInfo known_kernels = 2; // 已知内核列表
  string sync_type = 3;          // 同步类型: full, incremental
}

// 同步已知内核响应
message SyncKnownKernelsResponse {
  bool success = 1;
  string message = 2;
  repeated KernelInfo newly_known_kernels = 3; // 对方之前不知道的内核
}
```

#### 11.5.2 同步流程

当内核-X收到来自内核-Y的 `SyncKnownKernels` 时：

1. **处理收到的内核列表**
   - 跳过自己
   - 检查是否已在本地存在
   - 如果不存在，添加到本地 `kernels` map

2. **返回对方不知道的内核**
   - 遍历本地内核列表
   - 找出对方不知道的内核
   - 返回给请求方

3. **主动连接新内核**
   - 尝试连接到新发现的内核
   - 连接成功后立即同步信息

### 11.6 核心代码结构

```
kernel/server/
├── kernel_service.go          # 内核服务实现
│   ├── RegisterKernel()       # 处理内核注册
│   ├── SyncKnownKernels()     # 处理内核列表同步
│   └── DiscoverKernels()      # 发现可用内核
│
└── multi_kernel_manager.go    # 多内核管理器
    ├── ConnectToKernel()      # 连接到其他内核
    ├── ApprovePendingRequest() # 批准互联请求
    ├── BroadcastKnownKernels() # 广播已知内核列表
    ├── SyncKnownKernelsToKernel() # 向指定内核同步
    └── kernelHeartbeat()       # 内核心跳维护
```

### 11.7 交互命令

内核启动后，提供以下交互命令：

| 命令 | 说明 |
|------|------|
| `connect-kernel <id> <addr> <port>` | 连接到指定内核 |
| `approve-request <request-id>` | 批准互联请求 |
| `list-requests` | 列出待审批请求 |
| `ks` 或 `kernels` | 列出已知内核 |
| `disconnect-kernel <id>` | 断开与指定内核的连接 |
| `help` | 显示帮助信息 |
| `status` | 显示内核状态 |

### 11.8 使用示例

#### 场景：三个内核互联

假设有三个内核：
- kernel-1: 192.168.1.4
- kernel-2: 192.168.202.136
- kernel-3: 192.168.202.140

**在 kernel-1 上执行：**
```
# 连接到 kernel-2
connect-kernel kernel-2 192.168.202.136 50053

# 连接到 kernel-3
connect-kernel kernel-3 192.168.202.140 50053

# 查看已知内核
ks
```

**在 kernel-2 上执行：**
```
# 批准 kernel-1 的请求
approve-request <request-id-1>

# 查看已知内核
ks  # 应该看到 kernel-1 和 kernel-3
```

**在 kernel-3 上执行：**
```
# 批准 kernel-1 的请求
approve-request <request-id-2>

# 查看已知内核
ks  # 应该看到 kernel-1 和 kernel-2
```

### 11.9 证书管理

内核互联需要以下证书：

| 证书 | 用途 |
|------|------|
| `certs/ca.crt` | 自己的CA证书 |
| `certs/kernel.crt` | 内核证书 |
| `certs/kernel.key` | 内核私钥 |
| `certs/peer-<kernel-id>-ca.crt` | 对端内核的CA证书 |

当两个内核首次互联时：
1. 发起方发送自己的CA证书
2. 接收方保存对端CA证书并返回自己的CA证书
3. 双方保存对方的CA证书，后续通信使用

### 11.10 心跳机制

内核间通过心跳维护连接：

- **心跳间隔**：默认60秒（可在配置中修改）
- **心跳内容**：包含连接器数量、频道数量等统计信息
- **心跳响应**：更新最后心跳时间，返回其他内核状态更新

### 11.11 故障处理

#### 连接断开

当心跳检测到连接断开时：
1. 更新内核状态为 `inactive`
2. 清理相关连接资源
3. 保留内核信息以便重连

#### 重复注册

当收到重复的注册请求时：
1. 检查内核ID是否冲突
2. 如果冲突，返回错误
3. 如果是已注册内核，更新连接信息

### 11.12 配置说明

在 `config/kernel.yaml` 中配置多内核参数：

```yaml
kernel:
  kernel_id: "kernel-1"
  address: "192.168.1.4"
  port: 50051       # 主服务端口
  kernel_port: 50053 # 内核间通信端口

# 多内核配置
multi_kernel:
  enabled: true                    # 是否启用多内核模式
  heartbeat_interval: 60          # 心跳间隔（秒）
  connect_timeout: 10             # 连接超时（秒）
  max_retries: 3                  # 最大重试次数
```

## 十二、部署拓扑

### 12.1 单节点部署（测试环境）

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

### 11.2 集群部署（生产环境）

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

## 十二、总结

本架构实现了以下核心目标：

✅ **标准化接口**：通过 Protobuf 定义统一的 API  
✅ **安全保障**：基于 mTLS 的零信任架构  
✅ **可扩展性**：模块化设计支持灵活扩展  
✅ **可追溯性**：链式存证确保操作不可抵赖  
✅ **互操作性**：任何实现标准接口的连接器均可接入  

架构充分体现了"内核+外延"的设计理念，为可信数据空间的互联互通提供了坚实的技术基础。


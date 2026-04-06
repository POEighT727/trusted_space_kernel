# 可信数据空间内核 - 项目架构文档

> 本文档为 AI 助手提供项目全景视图，包含技术栈、目录结构、核心规范、接口调用方式等关键信息。
> 首次阅读请从「快速概览」开始，遇到具体问题时可查阅对应章节。

---

## 目录

1. [快速概览](#1-快速概览)
2. [技术栈](#2-技术栈)
3. [目录结构](#3-目录结构)
4. [核心概念](#4-核心概念)
5. [核心模块详解](#5-核心模块详解)
6. [gRPC 接口规范](#6-grpc-接口规范)
7. [编码规范](#7-编码规范)
8. [关键设计模式](#8-关键设计模式)
9. [特殊逻辑与注意事项](#9-特殊逻辑与注意事项)
10. [扩展开发指南](#10-扩展开发指南)
11. [故障排查](#11-故障排查)

---

## 1. 快速概览

### 1.1 项目定位

**可信数据空间内核 (Trusted Space Kernel)** 是一个标准化、轻量化的数据空间核心组件，采用**内核 + 外延 (Kernel + Connector)** 的设计理念。

```
┌──────────────────────────────────────────────────────────────┐
│                      可信数据空间                              │
│  ┌──────────┐        ┌──────────┐        ┌──────────┐       │
│  │  内核-1   │◄──────►│  内核-2   │◄──────►│  内核-3   │       │
│  │ (kernel) │  mTLS  │ (kernel) │  mTLS  │ (kernel) │       │
│  └────┬─────┘        └────┬─────┘        └────┬─────┘       │
│       │                    │                    │             │
│  ┌────┴────┐          ┌────┴────┐          ┌────┴────┐      │
│  │连接器A  │          │连接器B  │          │连接器C  │      │
│  │Connector│          │Connector│          │Connector│      │
│  └─────────┘          └─────────┘          └─────────┘      │
└──────────────────────────────────────────────────────────────┘
```

### 1.2 核心能力

| 能力 | 说明 |
|------|------|
| **互联互通** | 支持跨组织、跨系统的数据流通 |
| **安全底座** | mTLS 双向认证 + RSA 数字签名 + 哈希链 |
| **完整存证** | 60+ 事件类型，覆盖数据传输全生命周期 |
| **多内核网络** | P2P 形态的多内核分布式网络 |
| **多跳路由** | 支持通过中间内核转发数据 |

### 1.3 端口配置

| 端口 | 用途 | 协议 |
|------|------|------|
| **50051** | 主服务端口（连接器服务） | mTLS |
| **50052** | 引导服务端口（首次注册） | TLS（无需客户端证书） |
| **50053** | 内核间通信端口 | mTLS |

---

## 2. 技术栈

### 2.1 核心技术

| 类别 | 技术选型 | 版本要求 |
|------|----------|----------|
| 编程语言 | Go | 1.21+ (推荐 1.24+) |
| 通信框架 | gRPC | v1.77.0 |
| 接口定义 | Protocol Buffers | proto3 |
| 数据库 | MySQL | 5.7+ (可选) |
| 配置格式 | YAML | - |
| 依赖管理 | Go Modules | - |
| UUID生成 | google/uuid | v1.6.0 |

### 2.2 第三方库

```go
require (
    github.com/go-sql-driver/mysql v1.9.3      // MySQL 驱动
    github.com/google/uuid v1.6.0             // UUID 生成
    google.golang.org/grpc v1.77.0            // gRPC 框架
    google.golang.org/protobuf v1.36.10       // Protobuf 序列化
    gopkg.in/yaml.v3 v3.0.1                   // YAML 解析
)
```

### 2.3 安全技术

| 技术 | 用途 |
|------|------|
| TLS 1.3 | 传输层加密 |
| mTLS | 双向证书认证 |
| RSA-PSS | 数字签名 |
| SHA-256 | 哈希计算 |
| 哈希链 | 存证记录防篡改 |

---

## 3. 目录结构

```
trusted_space_kernel/
├── bin/                           # 编译后的可执行文件
│   ├── kernel.exe                 # 内核可执行文件
│   └── connector.exe              # 连接器可执行文件
│
├── certs/                         # 证书目录
│   ├── ca.crt / ca.key           # CA 根证书
│   ├── kernel.crt / kernel.key   # 内核证书
│   └── connector-*.crt/key       # 连接器证书
│
├── config/                        # 配置文件目录
│   ├── kernel.yaml               # 内核配置
│   ├── connector.yaml            # 连接器配置
│   └── connector-*.yaml          # 多连接器配置
│
├── kernel_configs/                # 多跳路由配置
│   └── multi-hop-route-*.json
│
├── logs/                          # 日志目录
│
├── connector/                     # 连接器模块（外延层）
│   ├── cmd/
│   │   └── main.go               # 连接器入口 (1676行)
│   ├── client/
│   │   ├── connector.go          # 连接器核心客户端 (2196行)
│   │   └── tls.go                # TLS 配置
│   └── database/
│       ├── store.go              # 本地存储
│       ├── hash_chain.go         # 业务哈希链
│       └── mysql.go              # 数据库支持
│
├── kernel/                        # 内核模块（核心层）
│   ├── cmd/
│   │   └── main.go               # 内核入口 (1239行)
│   ├── circulation/              # 流通调度模块
│   │   ├── channel_manager.go    # 频道管理器 (3184行)
│   │   └── channel_config.go     # 频道配置
│   ├── control/                  # 管控模块
│   │   ├── registry.go           # 身份注册表
│   │   └── policy.go            # 权限策略
│   ├── database/                 # 数据库模块
│   │   ├── mysql.go             # MySQL 连接
│   │   ├── evidence_store.go    # 证据存储
│   │   └── business_chain_store.go  # 业务哈希链存储
│   ├── evidence/                 # 存证模块
│   │   └── audit_log.go         # 审计日志
│   ├── security/                 # 安全模块
│   │   ├── ca.go                # CA 证书管理
│   │   ├── mtls.go              # mTLS 配置
│   │   └── signing.go          # 数字签名
│   └── server/                   # gRPC 服务实现
│       ├── channel_service.go    # 频道服务 (2950行)
│       ├── identity_service.go   # 身份服务
│       ├── evidence_service.go   # 存证服务
│       ├── kernel_service.go     # 内核间服务
│       ├── multi_kernel_manager.go    # 多内核管理 (1974行)
│       ├── multi_hop_config.go         # 多跳配置
│       └── business_chain_manager.go   # 业务哈希链管理
│
├── proto/kernel/v1/               # Protocol Buffers 定义
│   ├── kernel.proto              # 内核间通信
│   ├── channel.proto             # 频道服务
│   ├── identity.proto            # 身份服务
│   ├── evidence.proto            # 存证服务
│   └── business_chain.proto      # 业务哈希链
│
├── scripts/                       # 工具脚本
│   ├── gen_certs.sh / gen_certs.ps1
│   ├── quick_start.sh / quick_start.ps1
│   └── package_all.sh / package_all.ps1
│
├── docs/                          # 文档
│   ├── CORE.md                  # 核心模块说明
│   └── MULTI_KERNEL_NETWORK.md  # 多内核网络说明
│
├── go.mod                         # Go 模块定义
├── go.sum                         # 依赖校验
├── Makefile                       # 构建脚本
├── README.md                      # 英文文档
└── README_CN.md                   # 中文文档
```

---

## 4. 核心概念

### 4.1 内核 (Kernel)

内核是可信数据空间的**核心组件**，提供：
- 连接器身份管理
- 频道（Channel）管理
- 数据传输路由
- 存证记录
- 多内核互联

### 4.2 连接器 (Connector)

连接器是**外延组件**，作为数据空间与外部系统的接口：
- 与内核建立 mTLS 连接
- 创建/订阅频道
- 发送/接收数据
- 管理本地业务哈希链

### 4.3 频道 (Channel)

频道是**逻辑数据传输管道**：

```go
type Channel struct {
    ChannelID    string
    CreatorID    string
    SenderIDs    []string  // 发送方列表
    ReceiverIDs  []string  // 接收方列表
    Status       ChannelStatus  // proposed | active | closed
    Encrypted    bool
    DataTopic    string
}
```

**状态流转**：

```
proposed (提议) ──[所有参与方确认]──> active (活跃) ──[关闭]──> closed
```

### 4.4 跨内核 ID 格式

当参与者位于远端内核时，使用 `kernelID:connectorID` 格式：

| 格式 | 含义 |
|------|------|
| `connector-A` | 本地连接器 |
| `kernel-2:connector-U` | kernel-2 上的连接器 |

**判断逻辑**：
```go
if strings.Contains(id, ":") {
    // 跨内核参与者
    parts := strings.SplitN(id, ":", 2)
    kernelID := parts[0]
    connectorID := parts[1]
}
```

### 4.5 连接器状态

```go
const (
    ConnectorStatusActive   ConnectorStatus = "active"    // 活跃，可自动订阅
    ConnectorStatusInactive ConnectorStatus = "inactive"  // 非活跃，需手动订阅
    ConnectorStatusClosed   ConnectorStatus = "closed"    // 已关闭
)
```

**自动订阅规则**：只有 `active` 状态的连接器会在收到通知时自动订阅频道。

---

## 5. 核心模块详解

### 5.1 安全模块 (`kernel/security/`)

#### CA 证书管理 (`ca.go`)
- 加载/验证证书
- 动态签发连接器证书
- 支持 Bootstrap 机制

#### mTLS 配置 (`mtls.go`)
```go
// 服务端 TLS 配置（要求客户端证书）
tlsConfig := &tls.Config{
    ClientAuth:   tls.RequireAndVerifyClientCert,  // 强制 mTLS
    ClientCAs:    caCertPool,
    Certificates: []tls.Certificate{serverCert},
    MinVersion:   tls.VersionTLS13,
}
```

#### 数字签名 (`signing.go`)
```go
// RSA-PSS 签名
func SignData(data []byte, privateKey *rsa.PrivateKey) (string, error) {
    hash := sha256.Sum256(data)
    signature, err := rsa.SignPSS(rand.Reader, privateKey, crypto.SHA256, hash[:], nil)
    return base64.StdEncoding.EncodeToString(signature), err
}
```

### 5.2 管控模块 (`kernel/control/`)

#### 身份注册表 (`registry.go`)
```go
type Registry struct {
    mu         sync.RWMutex
    connectors map[string]*ConnectorInfo
}

// 心跳间隔：15秒
// 离线检测：30秒无心跳视为离线
func (r *Registry) IsOnline(connectorID string) bool {
    return time.Since(info.LastHeartbeat) < 30*time.Second
}
```

#### 权限策略 (`policy.go`)
- 基于规则的访问控制 (ACL)
- 支持精确匹配和通配符
- 可配置默认策略（允许/拒绝）

### 5.3 流通调度模块 (`kernel/circulation/`)

#### 频道管理器 (`channel_manager.go`)
```go
type ChannelManager struct {
    channels            map[string]*Channel
    forwardToKernel    func(kernelID string, packet *DataPacket, isFinal bool) error
    signForForward      func(dataHash, prevSignature string) (string, error)
    GetNextHopKernel    func(currentKernelID, targetKernelID string) (...) bool
    kernelID            string  // 当前内核ID
}
```

**关键能力**：
- 创建/关闭频道
- 订阅管理
- 数据缓冲（离线暂存）
- 跨内核转发
- 多跳路由

### 5.4 存证模块 (`kernel/evidence/`)

#### 审计日志 (`audit_log.go`)

**事件类型**（60+种）：

| 类别 | 事件 |
|------|------|
| 认证 | AUTH_SUCCESS, AUTH_FAILED, AUTH_TIMEOUT |
| 互联 | INTERCONNECT_REQUESTED, INTERCONNECT_APPROVED, INTERCONNECT_REJECTED |
| 频道 | CHANNEL_PROPOSED, CHANNEL_ACCEPTED, CHANNEL_CREATED, CHANNEL_CLOSED |
| 数据 | DATA_SEND, DATA_RECEIVE, ACK_RECEIVED |
| 权限 | PERMISSION_REQUESTED, PERMISSION_GRANTED, PERMISSION_REJECTED |

**哈希链结构**：
```go
type EvidenceRecord struct {
    EventID    string  // UUID
    EventType  EventType
    Timestamp  time.Time
    SourceID   string  // 事件来源
    TargetID   string  // 直接下一跳
    ChannelID  string
    DataHash   string  // SHA-256
    Signature  string  // RSA 签名
    Hash       string  // 记录内容哈希
    PrevHash   string  // 上一条记录哈希
}
```

### 5.5 多内核模块 (`kernel/server/`)

#### 多内核管理器 (`multi_kernel_manager.go`)
```go
type MultiKernelManager struct {
    config         *KernelConfig
    kernels        map[string]*KernelInfo
    pendingRequests map[string]*PendingInterconnectRequest
    multiHopConfigManager *MultiHopConfigManager
}

// 心跳间隔：60秒（可配置）
// 连接超时：10秒（可配置）
// 最大重试：3次
```

#### 多跳配置 (`multi_hop_config.go`)
```json
{
    "route_name": "route-kernel-1-to-kernel-3-via-kernel-2",
    "hops": [
        {"from_kernel": "kernel-1", "to_kernel": "kernel-2", "to_address": "192.168.1.100", "to_port": 50053},
        {"from_kernel": "kernel-2", "to_kernel": "kernel-3", "to_address": "192.168.1.101", "to_port": 50053}
    ]
}
```

### 5.6 业务哈希链

#### 内核侧 (`kernel/server/business_chain_manager.go`)
```go
// 记录数据哈希
func (m *BusinessChainManager) RecordDataHash(connectorID, channelID, dataHash, prevHash, prevSignature, signature string) error

// 记录 ACK
func (m *BusinessChainManager) RecordAck(connectorID, channelID, prevSignature, signature string) error
```

#### 连接器侧 (`connector/database/hash_chain.go`)
```go
// 构建并签名数据哈希
func (m *BusinessChainManager) BuildAndSignDataHash(data []byte, channelID, prevSignature string) (dataHash string, signature string, error)

// 接收并签名
func (m *BusinessChainManager) ReceiveAndSign(data []byte, channelID, sourceSignature string) (signature string, error)
```

**签名链计算公式**：
```
hashInput = data + prevHash
dataHash = SHA256(hashInput)
signature = HEX(dataHash + prevSignature)
```

---

## 6. gRPC 接口规范

### 6.1 身份服务 (IdentityService)

```protobuf
service IdentityService {
    rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
    rpc Heartbeat(HeartbeatRequest) returns (HeartbeatResponse);
    rpc DiscoverConnectors(DiscoverRequest) returns (DiscoverResponse);
    rpc DiscoverCrossKernelConnectors(CrossKernelDiscoverRequest) returns (CrossKernelDiscoverResponse);
    rpc GetConnectorInfo(GetConnectorInfoRequest) returns (GetConnectorInfoResponse);
    rpc SetConnectorStatus(SetConnectorStatusRequest) returns (SetConnectorStatusResponse);
    rpc RegisterConnector(RegisterConnectorRequest) returns (RegisterConnectorResponse);  // Bootstrap
}
```

**调用示例**：
```go
// 连接器注册（首次）
resp, err := identitySvc.RegisterConnector(ctx, &pb.RegisterConnectorRequest{
    ConnectorId:  "connector-A",
    EntityType:   "data_source",
    PublicKey:    publicKey,
})
```

### 6.2 频道服务 (ChannelService)

```protobuf
service ChannelService {
    // 数据传输
    rpc StreamData (stream DataPacket) returns (stream TransferStatus);  // 上传
    rpc SubscribeData (SubscribeRequest) returns (stream DataPacket);       // 下载
    
    // 频道管理
    rpc CreateChannel (CreateChannelRequest) returns (CreateChannelResponse);
    rpc CloseChannel (CloseChannelRequest) returns (CloseChannelResponse);
    rpc GetChannelInfo (GetChannelInfoRequest) returns (GetChannelInfoResponse);
    
    // 频道协商（两阶段）
    rpc ProposeChannel (ProposeChannelRequest) returns (ProposeChannelResponse);
    rpc AcceptChannelProposal (AcceptChannelProposalRequest) returns (AcceptChannelProposalResponse);
    rpc RejectChannelProposal (RejectChannelProposalRequest) returns (RejectChannelProposalResponse);
    
    // 订阅申请（频道外连接器）
    rpc RequestChannelSubscription (RequestChannelSubscriptionRequest) returns (RequestChannelSubscriptionResponse);
    rpc ApproveChannelSubscription (ApproveChannelSubscriptionRequest) returns (ApproveChannelSubscriptionResponse);
    
    // 权限变更
    rpc RequestPermissionChange (RequestPermissionChangeRequest) returns (RequestPermissionChangeResponse);
    rpc ApprovePermissionChange (ApprovePermissionChangeRequest) returns (ApprovePermissionChangeResponse);
    rpc GetPermissionRequests (GetPermissionRequestsRequest) returns (GetPermissionRequestsResponse);
    
    // ACK
    rpc SendAck (AckPacket) returns (SendAckResponse);
}
```

**调用示例**：

```go
// 1. 创建频道
resp, err := channelSvc.CreateChannel(ctx, &pb.CreateChannelRequest{
    CreatorId:   "connector-A",
    SenderIds:   []string{"connector-A"},
    ReceiverIds: []string{"connector-B"},
    DataTopic:   "business_data",
    Encrypted:   true,
})

// 2. 发送数据（流）
stream, err := channelSvc.StreamData(ctx)
stream.Send(&pb.DataPacket{
    ChannelId:       resp.ChannelId,
    Payload:         data,
    SequenceNumber:  1,
    IsFinal:         true,
})

// 3. 订阅数据（流）
stream, err := channelSvc.SubscribeData(ctx, &pb.SubscribeRequest{
    ConnectorId: "connector-B",
    ChannelId:   channelID,
})
for {
    packet, err := stream.Recv()
    // 处理数据
}
```

### 6.3 存证服务 (EvidenceService)

```protobuf
service EvidenceService {
    rpc SubmitEvidence (EvidenceRequest) returns (EvidenceResponse);
    rpc QueryEvidence (QueryRequest) returns (QueryResponse);
    rpc VerifyEvidenceSignature (VerifySignatureRequest) returns (VerifySignatureResponse);
}
```

**调用示例**：
```go
// 查询存证记录
resp, err := evidenceSvc.QueryEvidence(ctx, &pb.QueryRequest{
    ChannelId: channelID,
    Limit:     50,
})
```

### 6.4 内核服务 (KernelService)

```protobuf
service KernelService {
    rpc RegisterKernel (RegisterKernelRequest) returns (RegisterKernelResponse);
    rpc KernelHeartbeat (KernelHeartbeatRequest) returns (KernelHeartbeatResponse);
    rpc DiscoverKernels (DiscoverKernelsRequest) returns (DiscoverKernelsResponse);
    rpc SyncKnownKernels (SyncKnownKernelsRequest) returns (SyncKnownKernelsResponse);
    rpc CreateCrossKernelChannel (CreateCrossKernelChannelRequest) returns (CreateCrossKernelChannelResponse);
    rpc ForwardData (ForwardDataRequest) returns (ForwardDataResponse);
    rpc GetCrossKernelChannelInfo (GetCrossKernelChannelInfoRequest) returns (GetCrossKernelChannelInfoResponse);
    rpc SyncConnectorInfo (SyncConnectorInfoRequest) returns (SyncConnectorInfoResponse);
}
```

### 6.5 数据包格式 (DataPacket)

```protobuf
message DataPacket {
    string channel_id = 1;           // 频道ID
    int64  sequence_number = 2;       // 序列号
    bytes  payload = 3;               // 数据载荷
    string signature = 4;             // 当前节点 RSA 签名
    int64  timestamp = 5;             // 时间戳
    string sender_id = 6;             // 发送方ID
    repeated string target_ids = 7;   // 目标接收者（空=广播）
    string sender_kernel_id = 8;       // 发送方内核ID（跨内核）
    string flow_id = 9;               // 业务流程ID
    bool   is_final = 10;             // 流结束标志
    string data_hash = 11;            // 业务数据哈希
    bool   is_ack = 12;               // 是否为 ACK 包
    string ack_prev_signature = 13;   // ACK 中 prev_signature
    string original_signature = 14;   // 原始 connector 签名
    string prev_kernel_signature = 15; // 上一跳 kernel 签名
}
```

---

## 7. 编码规范

### 7.1 命名规范

| 类型 | 规范 | 示例 |
|------|------|------|
| 包名 | 小写字母 + 下划线 | `circulation`, `evidence` |
| 结构体 | PascalCase | `ChannelManager`, `ConnectorInfo` |
| 接口 | PascalCase + `er` | `Registry`, `PolicyEngine` |
| 常量 | PascalCase 或 全大写下划线 | `ChannelStatusActive`, `MAX_RETRIES` |
| 变量 | 驼峰命名 | `channelID`, `senderIDs` |
| Proto消息 | PascalCase | `CreateChannelRequest` |
| Proto字段 | 下划线命名 | `channel_id`, `sender_ids` |

### 7.2 类型定义

**枚举类型**：
```go
// Good
type ChannelStatus string
const (
    ChannelStatusProposed ChannelStatus = "proposed"
    ChannelStatusActive   ChannelStatus = "active"
)

// Bad
const (
    STATUS_PROPOSED = 1
    STATUS_ACTIVE = 2
)
```

**可选字段使用指针**：
```go
type Connector struct {
    exposed *bool  // nil=默认true, false=显式不公开
}
```

### 7.3 错误处理

```go
// 标准模式
if err != nil {
    return nil, fmt.Errorf("operation failed: %w", err)
}

// 日志记录（不阻断流程）
if err != nil {
    log.Printf("[WARN] operation failed: %v", err)
    return nil  // 或继续执行
}
```

### 7.4 并发安全

```go
type Manager struct {
    mu sync.RWMutex
    data map[string]*Item
}

// 读操作
func (m *Manager) Get(id string) (*Item, error) {
    m.mu.RLock()
    defer m.mu.RUnlock()
    return m.data[id], nil
}

// 写操作
func (m *Manager) Set(id string, item *Item) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.data[id] = item
    return nil
}
```

### 7.5 日志格式

```go
// 格式：[LEVEL] message
log.Printf("[INFO] Starting service on %s", addr)
log.Printf("[OK] Component initialized")
log.Printf("[WARN] Operation failed, using fallback: %v", err)
log.Printf("[ERROR] Critical failure: %v", err)

// 状态日志（用于初始化确认）
log.Println("[OK] Registry initialized")
```

---

## 8. 关键设计模式

### 8.1 回调函数注入

项目大量使用**回调注入**实现模块解耦：

```go
// ChannelManager 定义回调接口
type ChannelManager struct {
    forwardToKernel func(kernelID string, packet *DataPacket, isFinal bool) error
    signForForward  func(dataHash, prevSignature string) (string, error)
    GetNextHopKernel func(currentKernelID, targetKernelID string) (...) bool
}

// main.go 注入实现
channelManager.SetForwardToKernel(func(kernelID string, packet *circulation.DataPacket, isFinal bool) error {
    return multiKernelManager.ForwardData(kernelID, pbPacket, isFinal)
})

channelManager.SetSignForForward(func(dataHash, prevSignature string) (string, error) {
    return server.GenerateKernelSignature(dataHash, prevSignature)
})
```

**好处**：
- 避免循环依赖
- 延迟绑定实现
- 便于测试替换

### 8.2 通知管理器

```go
type NotificationManager struct {
    notifications map[string]chan *pb.ChannelNotification
}

func (nm *NotificationManager) Notify(receiverID string, notification *pb.ChannelNotification) error {
    isActive := nm.registry.IsActive(receiverID)
    if isActive {
        go nm.autoSubscribe(receiverID, notification.ChannelId)
    }
    // 发送通知到 channel
}
```

### 8.3 双层哈希链

```
┌─────────────────────────────────────────┐
│      审计日志哈希链 (kernel/evidence)     │
│  Record: Hash = SHA256(内容 + PrevHash)   │
│  用于：不可篡改的存证记录                  │
└─────────────────────────────────────────┘
                    +
┌─────────────────────────────────────────┐
│    业务数据哈希链 (connector/database)    │
│  dataHash = SHA256(data + prevHash)      │
│  signature = HEX(dataHash + prevSig)    │
│  用于：数据传输完整性证明                  │
└─────────────────────────────────────────┘
```

---

## 9. 特殊逻辑与注意事项

### 9.1 证书 Bootstrap 机制

首次启动连接器时，使用无证书连接获取证书：

```
连接器首次启动 ──> bootstrap端口(50052) ──> RegisterConnector
                <── 返回证书PEM ──
                使用证书连接主端口(50051)
```

**实现位置**：
- 内核：`kernel/cmd/main.go` 启动 bootstrap server (port + 1)
- 连接器：`connector/cmd/main.go` 检测证书是否存在

### 9.2 心跳机制

| 组件 | 间隔 | 超时检测 |
|------|------|----------|
| 连接器 → 内核 | 15秒 | 30秒离线 |
| 内核 → 内核 | 60秒 | 可配置 |

**状态恢复逻辑**：
```go
// 心跳更新时，如果之前离线则恢复为活跃
if info.Status == ConnectorStatusOffline {
    info.Status = ConnectorStatusActive
}
```

### 9.3 数据缓冲机制

当接收方未订阅时，数据暂存到缓冲区：

```go
type Channel struct {
    buffer        []*DataPacket  // 暂存的数据包
    maxBufferSize int           // 最大暂存数量 (默认10000)
}
```

### 9.4 ACK 签名链

ACK 包有特殊处理：`DataHash` 为空，通过 `PrevSignature` 和 `Signature` 形成签名链：

```go
// ACK 记录
record := &database.BusinessChainRecord{
    DataHash:      "",           // ACK 记录为空
    PrevSignature: prevSignature,
    Signature:     signature,
}
```

### 9.5 频道协商流程

```
提议者 ──ProposeChannel──> 状态: proposed
         <──ProposalID──
所有参与方 ──AcceptChannelProposal──> 
                              全部确认后
                              状态: active
```

**自动批准**：创建者自动批准自己的提议

### 9.6 跨内核 ID 解析

```go
func ParseCrossKernelID(id string) (kernelID, connectorID string, isCrossKernel bool) {
    if strings.Contains(id, ":") {
        parts := strings.SplitN(id, ":", 2)
        return parts[0], parts[1], true
    }
    return "", id, false
}
```

### 9.7 多跳路由建立

使用 `MultiHopSession` 和 `PendingHopInfo` 追踪：

```go
type PendingHopInfo struct {
    RouteName  string
    HopIndex   int
    TotalHops  int
    RequestID  string
    Status     string  // pending, approved, failed
}
```

---

## 10. 扩展开发指南

### 10.1 添加新事件类型

**步骤 1**：在 `kernel/evidence/audit_log.go` 添加常量：

```go
const (
    // ... 现有事件 ...
    EventTypeYourNewEvent EventType = "YOUR_NEW_EVENT"
)
```

**步骤 2**：在需要记录的地方调用：

```go
auditLog.SubmitBasicEvidenceWithMetadata(
    sourceID, EventTypeYourNewEvent, channelID, dataHash, 
    evidence.DirectionInternal, targetID, flowID, metadata,
)
```

### 10.2 添加新 gRPC 服务

**步骤 1**：在 `proto/kernel/v1/*.proto` 定义：

```protobuf
service YourService {
    rpc YourMethod (YourRequest) returns (YourResponse);
}

message YourRequest {
    string field = 1;
}

message YourResponse {
    bool success = 1;
    string message = 2;
}
```

**步骤 2**：生成 Go 代码：

```bash
protoc --go_out=. --go_opt=paths=source_relative \
    --go-grpc_out=. --go-grpc_opt=paths=source_relative \
    proto/kernel/v1/your.proto
```

**步骤 3**：在 `kernel/server/` 实现服务：

```go
type YourServiceServer struct {
    pb.UnimplementedYourServiceServer
    // 依赖注入
}

func (s *YourServiceServer) YourMethod(ctx context.Context, req *pb.YourRequest) (*pb.YourResponse, error) {
    // 实现逻辑
}
```

**步骤 4**：在 `kernel/cmd/main.go` 注册：

```go
yourSvc := server.NewYourServiceServer(/* dependencies */)
pb.RegisterYourServiceServer(grpcServer, yourSvc)
```

### 10.3 添加新频道状态

**步骤 1**：在 `kernel/circulation/channel_manager.go`：

```go
type NegotiationStatus int

const (
    NegotiationStatusProposed NegotiationStatus = 1
    NegotiationStatusAccepted NegotiationStatus = 2
    NegotiationStatusRejected NegotiationStatus = 3
    // 新增状态
    NegotiationStatusPaused   NegotiationStatus = 4
)
```

**步骤 2**：更新状态转换逻辑

### 10.4 添加新配置项

**步骤 1**：在 `config/kernel.yaml` 添加配置：

```yaml
your_feature:
  enabled: true
  option: "value"
```

**步骤 2**：在 `kernel/cmd/main.go` 的 Config 结构体中添加：

```go
type Config struct {
    // ... 现有配置 ...
    YourFeature struct {
        Enabled bool
        Option  string
    }
}
```

**步骤 3**：在 main() 中使用配置

---

## 11. 故障排查

### 11.1 连接失败

| 检查项 | 命令/方法 |
|--------|----------|
| 证书是否有效 | `openssl x509 -in certs/kernel.crt -text -noout` |
| 端口是否开放 | `telnet <ip> 50051` |
| CA 证书是否匹配 | 检查 `config/*.yaml` 中 `ca_cert_path` |

### 11.2 认证失败

1. 检查连接器证书 CN 是否与 connectorID 一致
2. 检查证书是否由正确 CA 签发
3. 查看内核日志中的 `[ERROR] Auth failed`

### 11.3 频道创建失败

1. 检查发送方/接收方 ID 是否正确
2. 检查连接器是否已注册
3. 查看内核日志中的 `Channel proposal` 相关日志

### 11.4 数据传输失败

1. 检查频道状态是否为 `active`
2. 检查发送方是否为频道参与者
3. 检查接收方是否已订阅

### 11.5 跨内核通信失败

1. 检查内核间端口 (50053) 是否可达
2. 检查多跳路由配置
3. 查看两端内核日志中的 `KernelService` 相关日志

---

## 附录 A：配置文件模板

### kernel.yaml
```yaml
kernel:
  id: "kernel-1"
  type: "primary"
  description: "Primary Trusted Space Kernel"

server:
  address: "0.0.0.0"
  port: 50051

multi_kernel:
  kernel_port: 50053
  heartbeat_interval: 60
  connect_timeout: 10
  max_retries: 3

security:
  ca_cert_path: "certs/ca.crt"
  ca_key_path: "certs/ca.key"
  server_cert_path: "certs/kernel.crt"
  server_key_path: "certs/kernel.key"

evidence:
  persistent: true
  log_file_path: "logs/audit.log"

database:
  enabled: false
  host: "localhost"
  port: 3306
  user: "root"
  password: "123456"
  database: "trusted_space"

policy:
  default_allow: true

channel:
  evidence:
    default_mode: "none"
    default_strategy: "all"
```

### connector.yaml
```yaml
connector:
  id: "connector-A"
  entity_type: "data_source"
  public_key: "mock_public_key"
  expose_to_others: true

kernel:
  address: "localhost"
  port: 50051

security:
  ca_cert_path: "certs/ca.crt"
  client_cert_path: "certs/connector-A.crt"
  client_key_path: "certs/connector-A.key"
  server_name: "trusted-data-space-kernel"

channel:
  config_dir: "./channels/connector-A"

database:
  enabled: false
```

---

## 附录 B：常见命令

### 内核命令
```
status              - 查看状态
connectors / cs     - 列出连接器
channels / ch       - 列出频道
kernels / ks        - 列出已知内核
connect-kernel <id> <addr> <port>  - 连接其他内核
approve-request <id>                - 批准互联请求
routes / rt        - 列出多跳路由
exit / quit        - 退出
```

### 连接器命令
```
list / ls           - 列出连接器
info <id>          - 查看连接器信息
create --sender A --receiver B --reason "..."  - 创建频道
accept <ch_id> <prop_id>     - 接受提议
sendto <ch_id>               - 发送数据
subscribe <ch_id>            - 订阅频道
channels / ch                - 查看参与的频道
query-evidence --channel <id> - 查询存证
status [active|inactive]     - 查看/设置状态
help                        - 帮助
```

---

## 附录 C：事件类型完整列表

### 认证事件
- AUTH_SUCCESS, AUTH_FAILED, AUTH_TIMEOUT, AUTH_ATTEMPT, AUTH_LOGOUT

### 连接器事件
- CONNECTOR_REGISTERED, CONNECTOR_UNREGISTERED, CONNECTOR_ONLINE, CONNECTOR_OFFLINE, CONNECTOR_HEARTBEAT, CONNECTOR_STATUS_CHANGED

### 频道事件
- CHANNEL_PROPOSED, CHANNEL_ACCEPTED, CHANNEL_REJECTED, CHANNEL_CREATED, CHANNEL_CLOSED, CHANNEL_SUBSCRIBED, CHANNEL_UNSUBSCRIBED

### 数据传输事件
- DATA_SEND, DATA_RECEIVE, TRANSFER_START, TRANSFER_END, ACK_RECEIVED

### 权限事件
- PERMISSION_REQUESTED, PERMISSION_GRANTED, PERMISSION_DENIED, PERMISSION_REVOKED, PERMISSION_CHANGE

### 互联事件
- INTERCONNECT_REQUESTED, INTERCONNECT_APPROVED, INTERCONNECT_REJECTED, INTERCONNECT_CLOSED

### Token 事件
- TOKEN_GENERATED, TOKEN_VALIDATED, TOKEN_EXPIRED

### 系统事件
- SYSTEM_STARTUP, SYSTEM_SHUTDOWN, CONFIG_CHANGED, POLICY_VIOLATION

---

*文档版本：v1.0*
*最后更新：2026-03-31*
*项目：可信数据空间内核 (Trusted Space Kernel)*

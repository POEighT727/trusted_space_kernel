# 可信数据空间内核 (Trusted Space Kernel)

[![Go Version](https://img.shields.io/badge/Go-1.21%2B-blue)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-green)](LICENSE)

## 项目概述

可信数据空间内核是一个**标准化、轻量化**的数据空间核心组件，采用**内核+外延**的设计理念。内核提供标准化接口和服务，外延组件（连接器）可灵活适配各种业务场景。

### 核心理念

- **内核标准化**：内核是标准化的"操作系统"，提供统一的接口规范
- **外延灵活性**：连接器作为外延组件，可灵活适配各种业务场景
- **互联互通**：通过标准 gRPC 接口实现跨组织、跨系统的互操作性
- **安全底座**：基于 mTLS 的零信任安全架构

---

## 系统架构

### 整体架构图

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           可信数据空间                                        │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│    组织-A                           组织-B                        组织-C   │
│  ┌─────────────┐               ┌─────────────┐               ┌───────────┐ │
│  │   内核      │◄─────────────►│   内核      │◄─────────────►│   内核    │ │
│  │ kernel-1    │    mTLS       │ kernel-2    │    mTLS       │ kernel-3  │ │
│  │ :50051      │               │ :50051      │               │ :50051    │ │
│  │ :50053      │               │ :50053      │               │ :50053    │ │
│  └──────┬──────┘               └──────┬──────┘               └─────┬─────┘ │
│         │                              │                              │       │
│    ┌────┴────┐                    ┌────┴────┐                    ┌────┴────┐  │
│    │连接器A1 │                    │连接器B1 │                    │连接器C1 │  │
│    │连接器A2 │                    │连接器B2 │                    │连接器C2 │  │
│    └─────────┘                    └─────────┘                    └─────────┘  │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 三层架构

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

---

## 核心模块

### 1. 安全认证模块 (Security)

采用**基于证书的双向认证（mTLS）机制**，构建零信任安全架构：

- **根证书 (CA)**：自签名的根证书，作为整个信任体系的锚点
- **服务端证书**：内核服务端持有的证书
- **客户端证书**：每个连接器持有的证书，CN 必须与连接器 ID 一致
- **动态证书注册**：支持连接器首次连接时动态申请证书（Bootstrap 服务）

### 2. 管控模块 (Control)

负责连接器的身份管理和数据传输的权限控制：

- **身份注册表**：维护连接器的完整生命周期信息
- **心跳机制**：客户端每15秒发送心跳，服务端30秒超时检测
- **权限策略引擎**：基于规则的访问控制，支持精确匹配和通配符

### 3. 流通调度模块 (Circulation)

采用**频道 (Channel)** 来管理数据传输：

- **频道**：逻辑上的数据传输管道，提供中转、缓冲和分发能力
- **发布-订阅模式**：发送方推送数据到频道，接收方订阅频道获取数据
- **协商机制**：支持两阶段频道创建（提议-确认）
- **跨内核频道**：支持跨内核的频道创建和数据转发

### 4. 存证溯源模块 (Evidence)

采用**时间戳排序的哈希链记录**方式：

- **60+ 事件类型**：覆盖数据传输、频道管理、权限管理、安全事件等
- **完整性保护**：RSA 数字签名 + 哈希链确保不可篡改
- **多后端支持**：文件存储 / MySQL 数据库 / 混合存储
- **区块链锚定**：支持将审计日志哈希提交到区块链

### 5. 多内核互联模块 (Multi-Kernel)

支持多个内核组成 P2P 形态的分布式网络：

- **内核发现**：通过 `SyncKnownKernels` 同步已知内核列表
- **直接连接**：内核间直接建立 gRPC 连接（50053 端口）
- **多跳路由**：支持配置多跳路由链路
- **心跳维护**：60 秒心跳间隔检测连接状态

---

## 项目结构

```
trusted_space_kernel/
├── bin/                        # 编译后的可执行文件
│   ├── kernel.exe              # 内核可执行文件
│   └── connector.exe           # 连接器可执行文件
├── certs/                      # 证书目录
│   ├── ca.crt                  # CA 根证书
│   ├── kernel.crt              # 内核证书
│   └── ...
├── channel_configs/            # 频道配置目录
├── channels/                   # 频道数据目录
│   ├── connector-A/
│   └── connector-B/
├── config/                     # 配置文件
│   ├── kernel.yaml            # 内核配置
│   ├── connector.yaml          # 连接器配置
│   └── ...
├── connector/                  # 连接器实现
│   ├── cmd/
│   │   └── main.go             # 连接器入口
│   └── client/
│       └── connector.go        # 连接器客户端
├── docs/                       # 文档
│   ├── CORE.md                # 核心模块说明
│   └── MULTI_KERNEL_NETWORK.md # 多内核网络说明
├── kernel/                     # 内核实现
│   ├── bin/
│   ├── circulation/            # 流通调度模块
│   │   ├── channel_manager.go  # 频道管理器
│   │   └── channel_config.go  # 频道配置
│   ├── cmd/
│   │   └── main.go            # 内核入口
│   ├── control/                # 管控模块
│   │   ├── policy.go           # 权限策略引擎
│   │   └── registry.go        # 身份注册表
│   ├── database/               # 数据库模块
│   │   ├── evidence_store.go   # 证据存储
│   │   └── mysql.go           # MySQL 支持
│   ├── evidence/              # 存证模块
│   │   └── audit_log.go       # 审计日志
│   ├── security/              # 安全模块
│   │   ├── ca.go              # CA 证书管理
│   │   ├── mtls.go            # mTLS 配置
│   │   └── signing.go         # 数字签名
│   └── server/                 # gRPC 服务
│       ├── channel_service.go  # 频道服务
│       ├── identity_service.go # 身份服务
│       ├── evidence_service.go # 存证服务
│       ├── kernel_service.go   # 内核服务
│       ├── multi_kernel_manager.go    # 多内核管理
│       └── multi_hop_config.go        # 多跳配置
├── kernel_configs/             # 多跳路由配置
│   ├── multi-hop-route-sample.json
│   └── multi-hop-route-complex.json
├── proto/                      # Protocol Buffers 定义
│   └── kernel/
│       └── v1/
│           ├── kernel.proto    # 内核间通信
│           ├── channel.proto   # 频道服务
│           ├── identity.proto  # 身份服务
│           └── evidence.proto  # 存证服务
├── scripts/                    # 脚本工具
│   ├── gen_certs.sh/ps1       # 证书生成
│   ├── quick_start.sh/ps1     # 快速启动
│   └── package_all.sh/ps1     # 打包工具
├── go.mod                      # Go 模块定义
├── go.sum                      # 依赖校验
└── Makefile                    # 构建脚本
```

---

## 快速开始

### 环境要求

- **Go**: 1.21 或更高版本
- **操作系统**: Linux / macOS / Windows

### 1. 生成证书

```bash
# Linux/Mac
./scripts/gen_certs.sh

# Windows
.\scripts\gen_certs.ps1
```

### 2. 启动内核

```bash
# 交互模式（推荐）
./bin/kernel.exe --config config/kernel.yaml

# 守护进程模式
./bin/kernel.exe --config config/kernel.yaml -daemon
```

### 3. 启动连接器

```bash
# 首次启动会自动注册并获取证书
./bin/connector.exe --config config/connector.yaml
```

---

## 使用指南

### 内核管理命令

```bash
# 查看状态
status

# 列出连接器
connectors 或 cs

# 列出频道
channels 或 ch

# 列出已知内核
kernels 或 ks

# 连接到其他内核
connect-kernel <kernel_id> <address> <port>

# 批准互联请求
approve-request <request_id>

# 列出待审批请求
pending-requests

# 断开内核连接
disconnect-kernel <kernel_id>

# 多跳路由命令
routes, rt                    # 列出所有路由
load-route <filename>         # 加载路由配置
connect-route <route_name>    # 连接指定路由
route-info <route_name>       # 查看路由详情

# 退出
exit 或 quit
```

### 连接器命令

```bash
# 列出连接器
list 或 ls

# 查看连接器信息
info <connector_id>

# 创建频道（提议）
create --config <config_file>
create --sender <sender_ids> --receiver <receiver_ids> --reason <reason>

# 接受频道提议
accept <channel_id> <proposal_id>

# 拒绝频道提议
reject <channel_id> <proposal_id> --reason <reason>

# 发送数据
sendto <channel_id> [file_path]

# 订阅频道
subscribe <channel_id>

# 查看已参与的频道
channels 或 ch

# 查询存证记录
query-evidence --channel <channel_id>
query-evidence --connector <connector_id>
query-evidence --flow <flow_id>

# 权限管理
request-permission <channel_id> <change_type> <target_id> <reason>
approve-permission <channel_id> <request_id>
reject-permission <channel_id> <request_id> <reason>
list-permissions <channel_id>

# 设置状态
status [active|inactive|closed]
```

---

## gRPC 服务接口

### 1. IdentityService (身份服务)

```protobuf
service IdentityService {
  rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
  rpc Heartbeat(HeartbeatRequest) returns (HeartbeatResponse);
  rpc DiscoverConnectors(DiscoverRequest) returns (DiscoverResponse);
  rpc DiscoverCrossKernelConnectors(CrossKernelDiscoverRequest) returns (CrossKernelDiscoverResponse);
  rpc GetConnectorInfo(GetConnectorInfoRequest) returns (GetConnectorInfoResponse);
  rpc SetConnectorStatus(SetConnectorStatusRequest) returns (SetConnectorStatusResponse);
  rpc RegisterConnector(RegisterConnectorRequest) returns (RegisterConnectorResponse);
}
```

### 2. ChannelService (频道服务)

```protobuf
service ChannelService {
  rpc CreateChannel(CreateChannelRequest) returns (CreateChannelResponse);
  rpc StreamData(stream DataPacket) returns (stream TransferStatus);
  rpc SubscribeData(SubscribeRequest) returns (stream DataPacket);
  rpc CloseChannel(CloseChannelRequest) returns (CloseChannelResponse);
  rpc GetChannelInfo(GetChannelInfoRequest) returns (GetChannelInfoResponse);
  
  // 协商相关
  rpc ProposeChannel(ProposeChannelRequest) returns (ProposeChannelResponse);
  rpc AcceptChannelProposal(AcceptChannelProposalRequest) returns (AcceptChannelProposalResponse);
  rpc RejectChannelProposal(RejectChannelProposalRequest) returns (RejectChannelProposalResponse);
  
  // 订阅申请
  rpc RequestChannelSubscription(RequestChannelSubscriptionRequest) returns (RequestChannelSubscriptionResponse);
  rpc ApproveChannelSubscription(ApproveChannelSubscriptionRequest) returns (ApproveChannelSubscriptionResponse);
  rpc RejectChannelSubscription(RejectChannelSubscriptionRequest) returns (RejectChannelSubscriptionResponse);
  
  // 权限变更
  rpc RequestPermissionChange(RequestPermissionChangeRequest) returns (RequestPermissionChangeResponse);
  rpc ApprovePermissionChange(ApprovePermissionChangeRequest) returns (ApprovePermissionChangeResponse);
  rpc RejectPermissionChange(RejectPermissionChangeRequest) returns (RejectPermissionChangeResponse);
}
```

### 3. EvidenceService (存证服务)

```protobuf
service EvidenceService {
  rpc SubmitEvidence(EvidenceRequest) returns (EvidenceResponse);
  rpc QueryEvidence(QueryRequest) returns (QueryResponse);
  rpc VerifyEvidenceSignature(VerifySignatureRequest) returns (VerifySignatureResponse);
}
```

### 4. KernelService (内核间服务)

```protobuf
service KernelService {
  rpc RegisterKernel(RegisterKernelRequest) returns (RegisterKernelResponse);
  rpc KernelHeartbeat(KernelHeartbeatRequest) returns (KernelHeartbeatResponse);
  rpc DiscoverKernels(DiscoverKernelsRequest) returns (DiscoverKernelsResponse);
  rpc SyncKnownKernels(SyncKnownKernelsRequest) returns (SyncKnownKernelsResponse);
  rpc CreateCrossKernelChannel(CreateCrossKernelChannelRequest) returns (CreateCrossKernelChannelResponse);
  rpc ForwardData(ForwardDataRequest) returns (ForwardDataResponse);
  rpc GetCrossKernelChannelInfo(GetCrossKernelChannelInfoRequest) returns (GetCrossKernelChannelInfoResponse);
  rpc SyncConnectorInfo(SyncConnectorInfoRequest) returns (SyncConnectorInfoResponse);
}
```

---

## 端口配置

每个内核使用三个端口进行通信：

| 端口 | 用途 | 说明 |
|------|------|------|
| 50051 | 主服务端口 | 提供 IdentityService、ChannelService、EvidenceService |
| 50052 | 引导服务端口 | 用于连接器首次注册时的证书申请 |
| 50053 | 内核间通信端口 | 用于内核之间的互联（P2P 通信） |

---

## 多内核互联

### 网络拓扑

```
┌─────────────────────────────────────────────────────────────┐
│                    多内核互联网络                             │
│                                                             │
│    ┌──────────┐         ┌──────────┐         ┌──────────┐ │
│    │ kernel-1 │◄───────►│ kernel-2 │◄───────►│ kernel-3 │ │
│    └────┬─────┘         └────┬─────┘         └────┬─────┘ │
│         │                    │                    │       │
│         │  ┌─────────────────┼──────────────────┐ │       │
│         └─►│   内核发现与同步  │◄─────────────────┘ │       │
│            │  (SyncKnownKernels)│                  │       │
│            └─────────────────┴──────────────────┘ │       │
└─────────────────────────────────────────────────────────────┘
```

### 互联流程

1. **发起互联**：在 kernel-1 上执行 `connect-kernel kernel-2 192.168.x.x 50053`
2. **审批请求**：在 kernel-2 上执行 `approve-request <request-id>`
3. **自动同步**：两个内核会自动同步已知的内核列表

---

## 数据流通流程

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

---

## 存证溯源机制

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
  PrevHash: "hash of Record N",  ← 指向前一条
  RecordHash: "sha256(this record)"
}
```

---

## 部署拓扑

### 单节点部署（测试环境）

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

### 集群部署（生产环境）

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

---

## 频道配置示例

### 简单频道配置

```json
{
  "channel_name": "数据交换频道",
  "creator_id": "connector-A",
  "sender_ids": ["connector-A"],
  "receiver_ids": ["connector-B"],
  "data_topic": "business_data",
  "encrypted": true
}
```

### 带存证的频道配置

```json
{
  "channel_name": "合规数据频道",
  "creator_id": "connector-A",
  "sender_ids": ["connector-A"],
  "receiver_ids": ["connector-B"],
  "data_topic": "compliant_data",
  "encrypted": true,
  "evidence_config": {
    "mode": "internal",
    "strategy": "all",
    "retention_days": 90,
    "compress_data": true
  }
}
```

### 跨内核频道配置

```json
{
  "creator_id": "connector-A",
  "sender_ids": ["kernel-A:connector-A"],
  "receiver_ids": ["kernel-B:connector-B"],
  "data_topic": "cross_kernel_data",
  "encrypted": true,
  "evidence_config": {
    "mode": "hybrid",
    "strategy": "important",
    "backup_enabled": true
  }
}
```

---

## 多跳路由配置示例

```json
{
  "route_name": "route-kernel-1-to-kernel-3-via-kernel-2",
  "name": "Kernel-1 到 Kernel-3 经过 Kernel-2",
  "description": "通过中间内核进行数据转发",
  "creator_id": "kernel-1",
  "enabled": true,
  "hops": [
    {
      "hop_id": 1,
      "from_kernel": "kernel-1",
      "to_kernel": "kernel-2",
      "to_address": "192.168.202.136",
      "to_port": 50053,
      "auto_connect": true
    },
    {
      "hop_id": 2,
      "from_kernel": "kernel-2",
      "to_kernel": "kernel-3",
      "to_address": "192.168.202.140",
      "to_port": 50053,
      "auto_connect": true
    }
  ]
}
```

---

## 性能特性

- **流式传输**：gRPC 双向流，支持大数据包
- **缓冲队列**：每个频道 1000 个数据包缓冲
- **并发订阅**：单个频道支持多个订阅者
- **批量写入**：存证操作批量持久化
- **连接池**：复用 gRPC 连接

---

## 安全特性

| 层级 | 机制 |
|------|------|
| 传输层 | TLS 1.3 加密 |
| 认证层 | mTLS 双向认证 |
| 授权层 | 策略引擎细粒度控制 |
| 审计层 | 全程存证记录 |

---

## 构建与打包

### 构建

```bash
# 构建内核
make build-kernel

# 构建连接器
make build-connector

# 构建所有
make build
```

### 打包

```bash
# Linux/Mac
./scripts/package_all.sh 1.0.0 linux-amd64 all

# Windows
.\scripts\package_all.ps1 -Version 1.0.0 -Platform windows-amd64 -Target all
```

---

## 故障处理

| 场景 | 处理方式 |
|------|----------|
| 连接断开 | 心跳检测，更新状态，保留信息以便重连 |
| 证书错误 | 检查证书路径、有效期、CA 签名 |
| 权限拒绝 | 检查 ACL 策略配置 |
| 存证失败 | 自动降级到文件存储 |

---

## 扩展性设计

### 插件化扩展

```go
// 策略引擎插件
type PolicyPlugin interface {
    Name() string
    CheckPermission(req *PermissionRequest) (bool, error)
}

// 存证插件
type EvidenceBackend interface {
    Store(record *EvidenceRecord) error
    Query(query *Query) ([]*EvidenceRecord, error)
}
```

---

## 许可证

MIT License - 详见 LICENSE 文件

---

## 贡献指南

欢迎提交 Issue 和 Pull Request！

---

## 联系方式

- 项目维护者：[您的名字]
- 邮箱：[your@email.com]

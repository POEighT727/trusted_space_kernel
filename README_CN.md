# 可信数据空间内核 (Trusted Space Kernel)

[![Go Version](https://img.shields.io/badge/Go-1.21%2B-blue)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-green)](LICENSE)

## 项目概述

可信数据空间内核是一个**标准化、轻量化**的数据空间核心组件，采用**内核+外延**的双组件设计理念。内核提供标准化接口和服务，外延组件（连接器）可灵活适配各种业务场景。

### 核心理念

- **内核标准化**：内核是标准化的"操作系统"，提供统一的接口规范
- **外延灵活性**：连接器作为外延组件，可灵活适配各种业务场景
- **互联互通**：通过标准 gRPC 接口和 P2P 直连协议实现跨组织、跨系统的互操作性
- **安全底座**：基于 mTLS 的零信任安全架构，支持 RSA-PSS 数字签名

---

## 系统架构

### 整体架构图

```
┌─────────────────────────────────────────────────────────────────────────────────────┐
│                              可信数据空间                                              │
├─────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                     │
│    组织-A                           组织-B                              组织-C       │
│  ┌─────────────┐               ┌─────────────┐                   ┌───────────┐     │
│  │   内核      │◄─────────────►│   内核      │◄─────────────────►│   内核    │     │
│  │ kernel-1    │    mTLS       │ kernel-2    │      mTLS          │ kernel-3  │     │
│  │ :50051      │               │ :50051      │                    │ :50051    │     │
│  │ :50052      │               │ :50052      │                    │ :50052    │     │
│  │ :50053      │               │ :50053      │                    │ :50053    │     │
│  │ :50055      │               │ :50055      │                    │ :50055    │     │
│  └──────┬──────┘               └──────┬──────┘                    └─────┬─────┘     │
│         │                              │                                │           │
│    ┌────┴────┐                    ┌────┴────┐                     ┌────┴────┐      │
│    │连接器A1 │                    │连接器B1 │                     │连接器C1 │      │
│    │连接器A2 │                    │连接器B2 │                     │连接器C2 │      │
│    └─────────┘                    └─────────┘                     └─────────┘      │
│                                                                                     │
│  ┌──────────────────────────────── P2P 运维直连 ─────────────────────────────────┐  │
│  │  运维方 ↔ 运维方 (TCP 直连, 自定义二进制协议, 同步连接器列表/转发临时消息)      │  │
│  └──────────────────────────────────────────────────────────────────────────────┘  │
│                                                                                     │
└─────────────────────────────────────────────────────────────────────────────────────┘
```

### 系统架构

```
┌─────────────────────────────────────────────────────────────────┐
│                        可信数据空间内核                           │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │                      通信协议层                             │  │
│  │            gRPC (mTLS) + 自定义 TCP (P2P)                 │  │
│  └─────────────────────────────────┬───────────────────────────┘  │
│                                    │                              │
│  ┌─────────────────────────────────┴───────────────────────────┐  │
│  │                      内核核心 (Core)                        │  │
│  │                                                         │  │
│  │  ┌─────────────────────────────────────────────────────┐ │  │
│  │  │  ┌─────────────┐  ┌─────────────┐  ┌───────────┐  │ │  │
│  │  │  │  安全认证    │  │  管控模块    │  │  流通调度  │  │ │  │
│  │  │  │  Security   │  │  Control    │  │Circulation│  │ │  │
│  │  │  │  ────────   │  │  ────────   │  │  ────────  │  │ │  │
│  │  │  │  • mTLS    │  │  • 注册表   │  │  • 频道    │  │ │  │
│  │  │  │  • CA签发   │  │  • 心跳    │  │  • 临时会话│  │ │  │
│  │  │  │  • RSA签名  │  │  • ACL     │  │  • P2P直连 │  │ │  │
│  │  │  └─────────────┘  └─────────────┘  └─────┬─────┘  │ │  │
│  │  │                                          │        │ │  │
│  │  │  ┌───────────────────────────────────────┴───────┐ │ │  │
│  │  │  │                  存证溯源                       │ │ │  │
│  │  │  │                  Evidence                      │ │ │  │
│  │  │  │      • 60+ 事件类型  • 哈希链  • 签名        │ │ │  │
│  │  │  └───────────────────────────────────────────────┘ │ │  │
│  │  └─────────────────────────────────────────────────────┘ │  │
│  │                                                         │  │
│  │  ┌─────────────────────────────────────────────────────┐ │  │
│  │  │                   多内核互联                         │ │  │
│  │  │                 Multi-Kernel                        │ │  │
│  │  │      • 内核发现  • 多跳路由  • 跨内核频道          │ │  │
│  │  └─────────────────────────────────────────────────────┘ │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                 │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │                     外部接入 (External)                    │  │
│  │    ┌─────────────┐  ┌─────────────┐  ┌─────────────┐     │  │
│  │    │  数据库      │  │  算法       │  │  应用       │     │  │
│  │    │  连接器      │  │  连接器      │  │  连接器      │     │  │
│  │    └─────────────┘  └─────────────┘  └─────────────┘     │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘

服务端口：
  • 50051 — 连接器服务 (gRPC/mTLS)
  • 50052 — 证书Bootstrap (gRPC/TLS单向)
  • 50053 — 内核互联 (gRPC/mTLS)
  • 50055 — P2P运维直连 (自定义TCP)
```

---

## 核心模块

### 1. 安全认证模块 (Security)

采用**基于证书的双向认证（mTLS）机制**，构建零信任安全架构：

- **根证书 (CA)**：自签名的根证书，作为整个信任体系的锚点
- **服务端证书**：内核服务端持有的证书
- **客户端证书**：每个连接器持有的证书，CN 必须与连接器 ID 一致
- **动态证书注册**：支持连接器首次连接时动态申请证书（Bootstrap 服务，50052 端口）
- **RSA-PSS 数字签名**：使用 RSA-PSS 算法对存证记录进行数字签名，确保不可否认性
- **SHA-256 哈希**：关键数据使用 SHA-256 算法生成哈希值

### 2. 管控模块 (Control)

负责连接器的身份管理和数据传输的权限控制：

- **身份注册表** (`registry.go`)：维护连接器的完整生命周期信息
- **心跳机制**：客户端每 15 秒发送心跳，服务端 30 秒超时检测离线连接器
- **权限策略引擎** (`policy.go`)：基于规则的访问控制，支持精确匹配和通配符模式
- **连接器状态管理**：支持 `active` / `inactive` / `closed` 三种状态

### 3. 流通调度模块 (Circulation)

采用**频道 (Channel)** 来管理数据传输，同时包含**临时会话 (TempChat)** 和 **P2P 运维方直连** 功能：

#### 3.1 频道管理 (Channel Management)

- **频道**：逻辑上的数据传输管道，提供中转、缓冲和分发能力
- **发布-订阅模式**：发送方推送数据到频道，接收方订阅频道获取数据
- **协商机制**：支持两阶段频道创建（提议-确认）
- **跨内核频道**：支持跨内核的频道创建和数据转发
- **多跳路由**：支持配置多跳路由链路进行数据转发
- **订阅审批**：发送方可审批接收方的订阅申请

#### 3.2 临时会话 (TempChat)

为解决连接器间无需建立正式频道的临时通信需求（如运维消息、调试命令），提供轻量级的临时会话能力：

- **会话注册**：连接器注册临时会话，包含连接器 ID 和会话密钥
- **心跳保活**：会话通过心跳机制保持活跃
- **消息收发**：支持实时双向消息传递
- **跨内核转发**：消息可跨内核转发，路由到目标连接器所在内核
- **远程连接器缓存**：跨内核通信时缓存远程连接器列表

**架构**：

```
Connector ↔ TempChatService (gRPC, port 50055) ↔ TempChatManager (内存)
                                    ↓
                         跨内核转发（gRPC ForwardTempMessage / P2P RelayMessage）
```

**内核侧** (`kernel/tempchat/`)：
- `manager.go` — 会话管理：注册/心跳/注销/消息路由（本地 vs 跨内核）/ 远程连接器缓存
- `service.go` — gRPC 服务端实现（8 个 RPC 方法）

**连接器侧** (`connector/tempchat/`)：
- `client.go` — 连接到内核 TempChat 服务，注册会话、心跳保活、消息收发

#### 3.3 P2P 运维方直连 (Operator Peer)

实现运维方之间的直接 TCP 连接，用于同步在线连接器列表和转发临时消息，避免依赖 gRPC 转发：

- **按需连接**：不预连接，需要时从 `KernelInfoProvider` 获取地址后建立
- **双工复用**：主动连接和被动接受的连接统一为 `PeerClient`，提供一致的发送接口
- **自动重连**：内置自动重连机制（`PeerClientWithReconnect`）
- **连接器同步**：通过 `SyncConnectors` 消息同步在线连接器列表
- **消息转发**：`RelayMessage` 消息用于转发连接器间的临时消息
- **集成 TempChat**：消息投递到 `TempChatManager.DeliverMessage()`，连接器同步缓存到 `SetRemoteConnectors()`

**自定义 TCP 协议**（二进制包头 + JSON 载荷）：

```
┌──────────────────────────────────────┐
│ Magic(4) + Ver(1) + Type(1)         │  ← 固定 6 字节
│ Flags(1) + Reserved(1)              │  ← 2 字节
│ Len(4)                              │  ← 4 字节
│ TraceID(16)                          │  ← 16 字节
├──────────────────────────────────────┤
│ JSON Payload (变长, ≤8MB)            │
└──────────────────────────────────────┘
```

**消息类型**：

| 类型 | 用途 |
|------|------|
| `Handshake` | 握手协商内核 ID |
| `SyncConnectors` | 同步在线连接器列表 |
| `RelayMessage` | 转发连接器间的临时消息 |
| `Heartbeat` | P2P 心跳保活 |
| `Disconnect` | 通知断开 |

**子模块** (`kernel/operator_peer/`)：

| 文件 | 功能 |
|------|------|
| `packet.go` | P2P 数据包编解码、协议常量、所有 Payload 结构体定义 |
| `server.go` | `P2PServer`（被动接收连接）+ `PeerConnection`（每个 TCP 连接管理） |
| `client.go` | `PeerClient`（主动连接）+ `PeerClientWithReconnect`（自动重连） |
| `manager.go` | `P2PManager`：整合 Server/Client，管理 peer 列表，按需连接，同步循环，消息路由回调 |

### 4. 存证溯源模块 (Evidence)

采用**时间戳排序的哈希链记录**方式：

- **60+ 事件类型**：覆盖数据传输、频道管理、权限管理、安全事件、互联事件等
- **完整性保护**：RSA-PSS 数字签名 + 哈希链确保不可篡改
- **多后端支持**：文件存储 / MySQL 数据库 / 混合存储
- **业务哈希链**：连接器可构建本地业务数据的哈希链并签名
- **自动降级**：存证操作失败时自动降级到文件存储

### 5. 多内核互联模块 (Multi-Kernel)

支持多个内核组成 P2P 形态的分布式网络：

- **内核发现**：通过 `SyncKnownKernels` 同步已知内核列表
- **直接连接**：内核间直接建立 gRPC 连接（50053 端口）
- **多跳路由**：支持配置多跳路由链路
- **心跳维护**：60 秒心跳间隔检测连接状态
- **跨内核频道**：支持创建跨多个内核的数据传输频道
- **连接器信息同步**：跨内核同步连接器在线状态和基本信息

---

## 事件类型详解

系统支持以下主要事件类型，用于记录数据流转的完整生命周期：

### 认证相关事件

| 事件类型 | 说明 |
|---------|------|
| AUTH_SUCCESS | 连接器认证成功 |
| AUTH_FAILED | 连接器认证失败 |
| AUTH_TIMEOUT | 认证超时 |

### 互联相关事件

| 事件类型 | 说明 |
|---------|------|
| INTERCONNECT_REQUESTED | 发起内核互联请求 |
| INTERCONNECT_APPROVED | 互联请求已批准 |
| INTERCONNECT_REJECTED | 互联请求被拒绝 |
| INTERCONNECT_CLOSED | 互联连接已关闭 |

### 频道管理事件

| 事件类型 | 说明 |
|---------|------|
| CHANNEL_PROPOSED | 频道提议创建 |
| CHANNEL_ACCEPTED | 频道提议已接受 |
| CHANNEL_REJECTED | 频道提议被拒绝 |
| CHANNEL_CREATED | 频道已正式创建 |
| CHANNEL_CLOSED | 频道已关闭 |
| CHANNEL_SUBSCRIBED | 订阅频道 |
| CHANNEL_UNSUBSCRIBED | 取消订阅 |

### 数据传输事件

| 事件类型 | 说明 |
|---------|------|
| DATA_SEND | 数据发送（connector→kernel 或 kernel→kernel） |
| DATA_RECEIVE | 数据接收（kernel→connector 或 kernel→kernel） |

### 权限管理事件

| 事件类型 | 说明 |
|---------|------|
| PERMISSION_REQUESTED | 权限变更请求 |
| PERMISSION_GRANTED | 权限已授予 |
| PERMISSION_REJECTED | 权限被拒绝 |
| PERMISSION_REVOKED | 权限已撤销 |

---

## 典型数据流转场景

### 场景一：单内核内数据传输

```
connector-A ──────► kernel-1 ──────► connector-B

产生存证记录：
1. DATA_SEND: connector-A → kernel-1
2. DATA_RECEIVE: kernel-1 → connector-B
```

### 场景二：跨内核数据传输（两跳）

```
connector-A ──► kernel-1 ──► kernel-2 ──► connector-U

产生存证记录：
1. DATA_SEND: connector-A → kernel-1         (本地发送)
2. DATA_SEND: kernel-1 → kernel-2           (跨内核转发)
3. DATA_RECEIVE: kernel-1 → kernel-2        (到达目标内核)
4. DATA_RECEIVE: kernel-2 → connector-U     (分发到接收者)
```

### 场景三：临时会话消息

```
connector-A ──► kernel-1 (TempChat) ──► kernel-2 (TempChat) ──► connector-U

说明：
1. 连接器注册 TempChat 会话
2. 发送方通过 gRPC SendMessage 发送临时消息
3. 若目标连接器在远程内核，通过 ForwardTempMessage 跨内核转发
4. 接收方通过 ReceiveMessage 流接收消息
```

---

## 项目结构

```
trusted_space_kernel/
├── bin/                              # 编译后的可执行文件
│   ├── kernel.exe                    # 内核可执行文件
│   └── connector.exe                  # 连接器可执行文件
├── certs/                            # 证书目录
│   ├── ca.crt / ca.key               # CA 根证书
│   ├── kernel.crt / kernel.key       # 内核证书
│   └── connector-{A,B,C,X}*.crt/.key # 各连接器证书
├── config/                           # 配置文件
│   ├── kernel.yaml                   # 内核主配置
│   ├── connector.yaml                # 连接器 A 配置
│   ├── connector-B.yaml              # 连接器 B 配置
│   ├── connector-C.yaml              # 连接器 C 配置
│   └── connector-X.yaml              # 连接器 X 配置
├── channels/                         # 频道数据目录
│   └── {connector-id}/               # 每个连接器一个子目录
│       ├── channel-{id}.json        # 频道元数据
│       └── data/                     # 数据文件
├── kernel_configs/                    # 多跳路由配置（JSON）
│   ├── multi-hop-route-sample.json
│   └── multi-hop-route-complex.json
├── connector/                         # 连接器实现
│   ├── cmd/
│   │   └── main.go                   # 连接器入口（2118 行）
│   ├── client/
│   │   ├── connector.go              # 连接器核心客户端（2196 行）
│   │   └── tls.go                    # TLS 配置加载
│   ├── database/
│   │   ├── store.go                  # 本地数据存储
│   │   ├── hash_chain.go             # 业务哈希链（RSA 签名）
│   │   └── mysql.go                   # MySQL 支持
│   ├── tempchat/
│   │   └── client.go                 # 临时通信客户端
│   └── tempchat/                     # 临时通信模块
├── kernel/                           # 内核实现
│   ├── cmd/
│   │   └── main.go                   # 内核入口（1505 行）
│   ├── circulation/                   # 流通调度模块
│   │   ├── channel_manager.go       # 频道管理器
│   │   └── channel_config.go         # 频道配置管理器
│   ├── control/                      # 管控模块
│   │   ├── registry.go               # 身份注册表
│   │   └── policy.go                  # 权限策略引擎
│   ├── database/                      # 数据库模块
│   │   ├── mysql.go                   # MySQL 连接管理
│   │   ├── evidence_store.go         # 证据记录存储
│   │   └── business_chain_store.go   # 业务哈希链存储
│   ├── evidence/                      # 存证模块
│   │   └── audit_log.go              # 审计日志（60+ 事件类型）
│   ├── security/                      # 安全模块
│   │   ├── ca.go                      # CA 证书管理
│   │   ├── mtls.go                   # mTLS 配置生成
│   │   └── signing.go                 # RSA-PSS 数字签名
│   ├── server/                        # gRPC 服务实现
│   │   ├── channel_service.go        # 频道服务（20+ 方法）
│   │   ├── identity_service.go        # 身份服务
│   │   ├── evidence_service.go        # 存证服务
│   │   ├── kernel_service.go           # 内核间通信服务
│   │   ├── multi_kernel_manager.go    # 多内核 P2P 网络管理器
│   │   ├── multi_hop_config.go        # 多跳路由配置管理器
│   │   └── business_chain_manager.go  # 业务哈希链服务
│   ├── tempchat/                      # 临时通信模块
│   │   ├── manager.go                 # 会话管理器
│   │   └── service.go                 # gRPC 服务端
│   └── operator_peer/                  # P2P 运维方直连
│       ├── packet.go                   # P2P 数据包编解码
│       ├── server.go                   # P2P 服务端
│       ├── client.go                   # P2P 客户端（支持自动重连）
│       └── manager.go                  # P2P 管理器
├── proto/                            # Protocol Buffers 定义
│   └── kernel/
│       └── v1/
│           ├── kernel.proto           # 内核间通信服务
│           ├── channel.proto          # 频道服务
│           ├── identity.proto         # 身份服务
│           ├── evidence.proto         # 存证服务
│           ├── business_chain.proto   # 业务哈希链服务
│           ├── tempchat.proto         # 临时会话服务
│           └── operator_peer.proto    # P2P 运维方直连协议
├── scripts/                           # 脚本工具
│   ├── gen_certs.sh / gen_certs.ps1  # 证书生成脚本
│   ├── package_all.sh / package_all.ps1 # 打包脚本
│   └── quick_start.sh / quick_start.ps1 # 快速启动脚本
├── docs/                              # 文档
│   └── CORE.md                       # 核心模块详细设计文档
├── go.mod                            # Go 模块定义
├── go.sum                            # 依赖校验
├── Makefile                          # 构建脚本
└── README.md / README_CN.md          # 英文/中文文档
```

---

## 端口配置

每个内核使用四个端口进行通信：

| 端口 | 用途 | 说明 |
|------|------|------|
| 50051 | 主服务端口 | 提供 IdentityService、ChannelService、EvidenceService、BusinessChainService |
| 50052 | 引导服务端口 | 用于连接器首次注册时的证书申请（无需 mTLS） |
| 50053 | 内核间通信端口 | 用于内核之间的互联（mTLS 加密的 gRPC） |
| 50055 | TempChat 服务端口 | 用于连接器临时会话通信（gRPC） |

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

### 4. KernelService (内核间通信服务)

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

### 5. BusinessChainService (业务哈希链服务)

```protobuf
service BusinessChainService {
  rpc SubmitHashChain(SubmitHashChainRequest) returns (SubmitHashChainResponse);
  rpc QueryHashChain(QueryHashChainRequest) returns (QueryHashChainResponse);
  rpc VerifyHashChain(VerifyHashChainRequest) returns (VerifyHashChainResponse);
}
```

### 6. TempChatService (临时会话服务)

```protobuf
service TempChatService {
  rpc RegisterSession(RegisterSessionRequest) returns (RegisterSessionResponse);
  rpc HeartbeatSession(HeartbeatSessionRequest) returns (HeartbeatSessionResponse);
  rpc UnregisterSession(UnregisterSessionRequest) returns (UnregisterSessionResponse);
  rpc ListOnlineConnectors(ListOnlineConnectorsRequest) returns (ListOnlineConnectorsResponse);
  rpc SendMessage(SendMessageRequest) returns (SendMessageResponse);
  rpc ReceiveMessage(ReceiveMessageRequest) returns (stream Message);
  rpc ForwardTempMessage(ForwardTempMessageRequest) returns (ForwardTempMessageResponse);
  rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse);
}
```

---

## 快速开始

### 环境要求

- **Go**: 1.21 或更高版本
- **数据库**: MySQL 5.7+ (可选，默认使用文件存储)
- **操作系统**: Linux / macOS / Windows

### 1. 生成证书

```bash
# Linux/Mac
./scripts/gen_certs.sh

# Windows
.\scripts\gen_certs.ps1
```

### 2. 配置数据库（可选）

如果使用 MySQL 存储存证记录，需要先创建数据库：

```sql
CREATE DATABASE IF NOT EXISTS trusted_space CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
```

然后修改配置文件 `config/kernel.yaml` 中的数据库配置。

### 3. 启动内核

```bash
# 交互模式（推荐）
./bin/kernel.exe --config config/kernel.yaml

# 守护进程模式
./bin/kernel.exe --config config/kernel.yaml -daemon
```

### 4. 启动连接器

```bash
# 首次启动会自动注册并获取证书
./bin/connector.exe --config config/connector.yaml
```

---

## 交互命令详解

### 内核管理命令

在启动内核的终端中，可以使用以下命令：

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
# 示例: connect-kernel kernel-2 192.168.202.136 50053

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

# P2P 运维方直连命令
connect-peer <kernel_id> <address> <port>  # 连接运维方
disconnect-peer <kernel_id>                # 断开运维方
peers 或 ps                               # 列出已连接的运维方
peer-info <kernel_id>                      # 查看运维方详情

# 临时会话命令
list-sessions                    # 列出当前会话
tempchat-connectors              # 列出已注册临时会话的连接器

# 退出
exit 或 quit
```

### 连接器命令

在启动连接器的终端中，可以使用以下命令：

```bash
# 查看已连接的连接器
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
# 示例: sendto <channel-id>
#       (输入数据，回车发送，输入 END 结束)

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

# 临时通信
tempchat list                  # 查看已注册的临时会话连接器
tempchat send <connector_id> <message>  # 发送临时消息
tempchat receive               # 接收临时消息

# 帮助
help
```

---

## 演示：跨内核数据传输

以下是一个完整的跨内核数据传输演示流程：

### 1. 环境准备

```
┌─────────────────────┐          ┌─────────────────────┐
│   kernel-1          │          │   kernel-2          │
│   192.168.31.155    │◄────────►│   192.168.202.136  │
│   (Windows)         │   mTLS   │   (Linux VM)       │
└─────────┬───────────┘          └─────────┬───────────┘
          │                              │
    connector-A                    connector-U
```

### 2. 启动服务

```bash
# 在 kernel-1 (Windows) 启动内核
.\bin\kernel.exe --config .\config\kernel.yaml

# 在 kernel-2 (Linux) 启动内核
./bin/kernel.exe --config config/kernel.yaml
```

### 3. 建立内核互联

```bash
# 在 kernel-1 上执行
connect-kernel kernel-2 192.168.202.136 50053

# 在 kernel-2 上执行
approve-request <request_id>
# 示例: approve-request ab29212c-35de-4a4f-9c65-09bae93c78d9
```

### 4. 连接器加入

```bash
# 在 kernel-1 上启动 connector-A
.\bin\connector.exe --config .\config\connector-A.yaml

# 在 kernel-2 上启动 connector-U
./bin/connector.exe --config config/connector-U.yaml
```

### 5. 创建跨内核频道

```bash
# 在 connector-A 上创建频道
create --sender connector-A --receiver kernel-2:connector-U --reason "测试数据传输"

# 在 connector-U 上接受频道
accept <channel_id> <proposal_id>
# 示例: accept 296bf273-e720-4930-b52a-b3867621897d 024cd002-5329-496c-9ca4-30701ca8847f
```

### 6. 发送数据

```bash
# 在 connector-A 上发送数据
sendto <channel_id>
# 输入数据内容
# 输入 END 结束发送
```

### 7. 查看存证记录

```bash
# 在内核上查询
query-evidence --channel <channel_id>
```

---

## 存证记录示例

当 connector-A 通过跨内核频道向 connector-U 发送 "hello" 数据时，会产生以下存证记录：

### kernel-1 存证记录

| 事件类型 | source_id | target_id | 说明 |
|---------|-----------|-----------|------|
| AUTH_SUCCESS | connector-A | kernel-1 | 连接器认证成功 |
| INTERCONNECT_REQUESTED | kernel-1 | kernel-2 | 发起内核互联 |
| CHANNEL_CREATED | kernel-1 | kernel-1 | 频道创建成功 |
| DATA_SEND | connector-A | kernel-1 | connector-A 发送数据到内核 |
| DATA_SEND | kernel-1 | kernel-2 | 内核转发到目标内核 |
| DATA_SEND | kernel-1 | kernel-2 | 转发完成确认（带 data_hash） |

### kernel-2 存证记录

| 事件类型 | source_id | target_id | 说明 |
|---------|-----------|-----------|------|
| AUTH_SUCCESS | connector-U | kernel-2 | 连接器认证成功 |
| INTERCONNECT_APPROVED | kernel-2 | kernel-1 | 互联请求已批准 |
| CHANNEL_CREATED | kernel-2 | kernel-2 | 频道创建成功 |
| DATA_RECEIVE | kernel-1 | kernel-2 | 接收来自 kernel-1 的数据 |
| DATA_RECEIVE | kernel-2 | connector-U | 分发给目标连接器 |

---

## 多内核互联

### 网络拓扑

```
┌─────────────────────────────────────────────────────────────────┐
│                    多内核互联网络                                   │
│                                                                 │
│    ┌──────────┐         ┌──────────┐         ┌──────────┐       │
│    │ kernel-1 │◄───────►│ kernel-2 │◄───────►│ kernel-3 │       │
│    └────┬─────┘         └────┬─────┘         └────┬─────┘       │
│         │                    │                    │             │
│         │  ┌─────────────────┼──────────────────┐ │             │
│         └─►│   内核发现与同步  │◄─────────────────┘ │             │
│            │  (SyncKnownKernels)│                  │             │
│            └─────────────────┴──────────────────┘ │             │
│                                                                 │
│  ┌────────────────── P2P 运维方直连 ──────────────────────────┐  │
│  │  运维方 ↔ 运维方 (自定义 TCP 协议, 同步连接器/转发临时消息)   │  │
│  └────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

### 互联流程

1. **发起互联**：在 kernel-1 上执行 `connect-kernel kernel-2 192.168.x.x 50053`
2. **审批请求**：在 kernel-2 上执行 `approve-request <request-id>`
3. **自动同步**：两个内核会自动同步已知的内核列表

---

## 数据流通流程

### 单内核流程

```
发送方连接器          内核            接收方连接器
     │                 │                   │
     │ CreateChannel   │                   │
     ├────────────────>│                   │
     │                 │ 权限检查           │
     │                 │ 创建频道           │
     │                 │ 记录存证           │
     │ ChannelID       │                   │
     │<────────────────┤                   │
     │                 │                   │
     │                 │ 自动订阅          │
     │                 │                   │
     │ StreamData      │                   │
     ├────────────────>│                   │
     │                 │ 数据包1            │
     │                 ├──────────────────>│
     │ 确认            │                   │
     │<────────────────┤                   │
     │                 │ 数据包2            │
     │                 ├──────────────────>│
     │                 │                   │
     │ CloseChannel    │                   │
     ├────────────────>│                   │
     │                 │ 关闭流             │
     │                 │ 记录存证           │
     │                 ├──────────────────>│
```

### 跨内核流程

```
connector-A        kernel-1            kernel-2         connector-U
    │                 │                    │                 │
    │ ProposeChannel  │                    │                 │
    ├────────────────>│                    │                 │
    │                 │ 创建跨内核频道       │                 │
    │                 ├───────────────────>│                 │
    │                 │                    │ 通知接收方       │
    │                 │                    ├────────────────>│
    │                 │                    │                 │
    │                 │              AcceptChannelProposal  │
    │                 │<────────────────────────────────────┤
    │                 │                    │                 │
    │                 │ ChannelCreated     │                 │
    │<────────────────┤<───────────────────│                 │
    │                 │                    │                 │
    │ StreamData      │                    │                 │
    ├────────────────>│                    │                 │
    │                 │ ForwardData        │                 │
    │                 ├───────────────────>│                 │
    │                 │                    │ PushData        │
    │                 │                    ├────────────────>│
    │                 │                    │                 │
```

### 临时会话流程

```
connector-A        kernel-1 (TempChat)      kernel-2 (TempChat)      connector-U
    │                      │                        │                      │
    │ RegisterSession      │                        │                      │
    ├─────────────────────>│                        │                      │
    │                      │                        │                      │
    │                      │ SyncConnectors         │                      │
    │                      ├────────────────────────>│                      │
    │                      │                        │ RegisterSession      │
    │                      │                        ├─────────────────────>│
    │                      │                        │                      │
    │ SendMessage          │                        │                      │
    ├─────────────────────>│                        │                      │
    │                      │ ForwardTempMessage      │                      │
    │                      ├────────────────────────>│                      │
    │                      │                        │ ReceiveMessage        │
    │                      │                        ├─────────────────────>│
    │                      │                        │                      │
```

---

## 存证溯源机制

每条存证记录采用链式结构：

```
Record N:
{
  EventID: "uuid",
  EventType: "DATA_SEND",
  SourceID: "connector-A",
  TargetID: "kernel-1",
  ChannelID: "channel-uuid",
  DataHash: "sha256(...)",
  Signature: "RSA-PSS signature",
  Hash: "sha256(this record)",
  PrevHash: "hash of Record N-1"
}
    │
    ↓ 链接
Record N+1:
{
  PrevHash: "hash of Record N",  ← 指向前一条
  Hash: "sha256(this record)"
}
```

### 存证字段说明

| 字段 | 说明 |
|------|------|
| event_id | 事件唯一标识 (UUID) |
| event_type | 事件类型 |
| timestamp | 精确到微秒的时间戳 |
| source_id | 事件来源（连接器或内核） |
| target_id | 目标 ID（直接下一跳） |
| channel_id | 关联的频道 ID |
| data_hash | 数据哈希（可选） |
| signature | 内核 RSA-PSS 数字签名 |
| hash | 记录内容哈希 |
| prev_hash | 上一条记录的哈希（哈希链） |
| metadata | 扩展元数据（JSON） |

---

## 业务哈希链

连接器可构建本地业务数据的哈希链，实现数据的不可否认性：

```
数据记录:
{
  DataID: "uuid",
  Data: "业务数据内容",
  Hash: "sha256(Data)",
  Timestamp: "2026-04-10T10:00:00Z",
  ConnectorID: "connector-A",
  Signature: "RSA-PSS signature on Hash"
}

链式结构:
Record 1 → Record 2 → Record 3 → ... → Record N
  ↓         ↓         ↓               ↓
PrevHash ← Hash ← Hash ← ... ← Hash ← PrevHash
```

**gRPC 接口** (`BusinessChainService`)：
- `SubmitHashChain`：提交业务哈希链记录
- `QueryHashChain`：查询业务哈希链
- `VerifyHashChain`：验证哈希链完整性

---

## 部署拓扑

### 单节点部署（测试环境）

```
┌──────────────────────────────────────┐
│          Kernel                       │
│  50051 (主服务)                       │
│  50052 (Bootstrap)                    │
│  50053 (内核间)                       │
│  50055 (TempChat)                     │
└──────────────────────────────────────┘
         ↑ ↑ ↑ ↑
         │ │ │ │
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
   (50051-53)    (50051-53)   (50051-53)
   (50055)       (50055)      (50055)
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

## P2P 运维方直连协议

### 协议格式

自定义 TCP 协议采用二进制包头 + JSON 载荷：

```
┌────────────────────────────────────────────────────────────────┐
│  包头 (28 字节)                                                  │
│  ┌────────┬──────┬────────┬────────┬──────────────────────────┐ │
│  │ Magic  │ Ver  │ Type  │ Flags │ Reserved (1 byte)         │ │
│  │ 4 字节 │1 字节│1 字节 │1 字节 │                           │ │
│  ├────────┴──────┴────────┴────────┴──────────────────────────┤ │
│  │ Len (4 字节, big-endian)    │ TraceID (16 字节)             │ │
│  └─────────────────────────────┴───────────────────────────────┘ │
├────────────────────────────────────────────────────────────────┤
│  载荷 (变长, ≤8MB)                                               │
│  JSON 格式的消息内容                                              │
└────────────────────────────────────────────────────────────────┘
```

### 消息类型

| Type | 名称 | 用途 |
|------|------|------|
| 0x01 | Handshake | 握手协商内核 ID |
| 0x02 | SyncConnectors | 同步在线连接器列表 |
| 0x03 | RelayMessage | 转发连接器间的临时消息 |
| 0x04 | Heartbeat | P2P 心跳保活 |
| 0x05 | Disconnect | 通知断开连接 |

### Handshake 消息

```json
{
  "kernel_id": "kernel-1",
  "version": "1.0.0"
}
```

### SyncConnectors 消息

```json
{
  "connectors": [
    {
      "connector_id": "connector-A",
      "kernel_id": "kernel-1",
      "status": "active",
      "last_seen": "2026-04-10T10:00:00Z"
    }
  ]
}
```

### RelayMessage 消息

```json
{
  "from_connector": "connector-A",
  "to_connector": "connector-B",
  "message": "临时消息内容",
  "timestamp": "2026-04-10T10:00:00Z",
  "message_id": "uuid"
}
```

---

## 性能特性

- **流式传输**：gRPC 双向流，支持大数据包分块传输
- **缓冲队列**：每个频道 1000 个数据包缓冲
- **并发订阅**：单个频道支持多个订阅者
- **批量写入**：存证操作批量持久化
- **连接池**：复用 gRPC 连接
- **自动重连**：P2P 客户端内置自动重连机制

---

## 安全特性

| 层级 | 机制 |
|------|------|
| 传输层 | TLS 1.3 加密 |
| 认证层 | mTLS 双向认证（连接器首次注册支持 Bootstrap 无证书接入） |
| 授权层 | 策略引擎细粒度控制（精确匹配 + 通配符） |
| 审计层 | 全程存证记录（60+ 事件类型） |
| 签名层 | RSA-PSS 数字签名 + SHA-256 哈希链 |

---

## 构建与打包

### 构建

```bash
# 生成 Protocol Buffer 代码
make proto

# 生成测试证书
make certs

# 构建内核
make kernel

# 构建连接器
make connector

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
| 跨内核转发失败 | 重试机制，保留原始数据 |
| P2P 连接断开 | 自动重连机制恢复连接 |
| 临时会话失效 | 心跳超时自动清理会话 |

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

- 项目维护者：可信数据空间团队
- 邮箱：trusted-space@example.com

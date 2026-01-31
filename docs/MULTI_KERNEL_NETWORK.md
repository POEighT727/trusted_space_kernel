# 多内核互联网络实现文档

## 一、概述

可信数据空间内核支持多内核组网模式，多个内核可以互相发现、互联互通，形成一个分布式的内核网络。

### 1.1 网络拓扑

```
┌─────────────────────────────────────────────────────────────────┐
│                      多内核互联网络示例                           │
├─────────────────────────────────────────────────────────────────┤
│    组织-A                    组织-B                    组织-C    │
│  ┌─────────┐             ┌─────────┐             ┌─────────┐  │
│  │kernel-1 │◄───────────►│kernel-2 │◄───────────►│kernel-3 │  │
│  │ :50051  │   mTLS      │ :50051  │   mTLS      │ :50051  │  │
│  │ :50053  │             │ :50053  │             │ :50053  │  │
│  └────┬────┘             └────┬────┘             └────┬────┘  │
│       │                       │                       │       │
│       └───────────────────────┼───────────────────────┘       │
│                               │                               │
│                        ┌──────┴──────┐                        │
│                        │   连接器     │                        │
│                        │ (Connector) │                        │
│                        └─────────────┘                        │
└─────────────────────────────────────────────────────────────────┘
```

### 1.2 核心设计原则

- **互联互通**：多个内核可以互相发现、建立连接
- **自动同步**：内核间自动同步已知的节点信息
- **安全可靠**：基于 mTLS 的双向认证
- **容错恢复**：支持连接断开后的重连机制

---

## 二、架构设计

### 2.1 核心组件

| 组件 | 职责 |
|------|------|
| **MultiKernelManager** | 管理所有已连接的内核、维护内核列表、处理互联请求 |
| **KernelServiceServer** | 提供 gRPC 服务、处理注册、同步、心跳等请求 |
| **KernelInfo** | 存储单个内核的元信息（地址、端口、状态、心跳等） |

### 2.2 数据结构

```
MultiKernelManager
├── kernels: map[string]*KernelInfo    # 已连接的内核
├── pendingRequests: map[string]*PendingInterconnectRequest  # 待审批请求
└── 配置信息（地址、端口、证书路径等）

KernelInfo
├── KernelID      内核唯一标识
├── Address       IP地址
├── Port          内核间通信端口
├── MainPort      主服务端口
├── Status        状态（active/inactive）
├── LastHeartbeat 最后心跳时间
└── Client        gRPC客户端（用于发送请求）
```

### 2.3 端口配置

每个内核使用三个端口：

| 端口 | 用途 | 说明 |
|------|------|------|
| 50051 | 主服务端口 | 提供 IdentityService、ChannelService 等 |
| 50052 | 引导服务端口 | 用于证书注册（Bootstrap Server） |
| 50053 | 内核间通信端口 | 用于内核之间的互联 |

---

## 三、gRPC 接口

### 3.1 服务定义

```protobuf
service KernelService {
  // 内核注册
  rpc RegisterKernel(RegisterKernelRequest) returns (RegisterKernelResponse);

  // 内核心跳
  rpc KernelHeartbeat(KernelHeartbeatRequest) returns (KernelHeartbeatResponse);

  // 内核发现
  rpc DiscoverKernels(DiscoverKernelsRequest) returns (DiscoverKernelsResponse);

  // 同步已知内核列表
  rpc SyncKnownKernels(SyncKnownKernelsRequest) returns (SyncKnownKernelsResponse);

  // ... 其他接口
}
```

### 3.2 核心消息

```protobuf
// 同步内核请求
message SyncKnownKernelsRequest {
  string source_kernel_id = 1;           // 源内核ID
  repeated KernelInfo known_kernels = 2; // 已知内核列表
  string sync_type = 3;                  // full/incremental
}

// 同步内核响应
message SyncKnownKernelsResponse {
  bool success = 1;
  repeated KernelInfo newly_known_kernels = 2; // 对方之前不知道的内核
}
```

---

## 四、互联流程

### 4.1 整体流程

```
┌──────────────────────────────────────────────────────────────────────┐
│                           互联流程                                     │
├──────────────────────────────────────────────────────────────────────┤
│                                                                       │
│  内核-A                              内核-B                          │
│    │                                    │                             │
│    │ 1. connect-kernel <id> <addr> 50053│                             │
│    ├───────────────────────────────────>│                             │
│    │                                    │                             │
│    │                                    │ 2. 创建 PendingInterconnect │
│    │◄───────────────────────────────────│                             │
│    │                                    │                             │
│    │                                    │ 3. approve-request <id>    │
│    │                                    ├─> 用户执行                  │
│    │                                    │                             │
│    │                                    │ 4. notifyRequesterApprove  │
│    │◄───────────────────────────────────│                             │
│    │                                    │                             │
│    │ 5. connectToKernelInternal         │                             │
│    ├───────────────────────────────────>│                             │
│    │                                    │                             │
│    │ 6. BroadcastKnownKernels          │                             │
│    ├───────────────────────────────────>│                             │
│    │                                    │                             │
│    │ 7. SyncKnownKernels               │                             │
│    │◄───────────────────────────────────│                             │
│    │                                    │                             │
│    │ 8. 添加新内核到本地                 │                             │
│    │ 9. 连接新内核                       │                             │
│    │ 10. SyncKnownKernelsToKernel      │                             │
│    ├───────────────────────────────────>│                             │
│    │                                    │                             │
│    └─────────────────────────────────────────────────────────────────┘
```

### 4.2 详细步骤

**步骤1：发起互联请求**

在内核-A上执行 `connect-kernel` 命令：
1. 读取或创建对端CA证书
2. 发起 TLS 连接到内核-B:50053
3. 发送 `RegisterKernelRequest`（带 `interconnect_request: true` 元数据）
4. 如果内核-B返回 `interconnect_request_id`，保存为待审批请求

**步骤2：审批互联请求**

在内核-B上执行 `approve-request` 命令：
1. 从待审批列表中获取请求信息
2. 通过 `notifyRequesterApprove` 通知内核-A已批准
3. 建立到内核-A的持久连接
4. 调用 `BroadcastKnownKernels` 广播已知的内核列表

**步骤3：内核同步**

内核-A收到广播后：
1. 调用 `SyncKnownKernels` 同步内核列表
2. 将新内核添加到本地 `kernels` map
3. 尝试连接到新内核
4. 连接成功后，调用 `SyncKnownKernelsToKernel` 主动向新内核同步

---

## 五、内核发现机制

### 5.1 同步协议

当内核-X收到来自内核-Y的 `SyncKnownKernels` 时：

```
内核-Y                     内核-X
   │                          │
   │ SyncKnownKernelsRequest  │
   │ (known_kernels: [A, B])  │
   ├─────────────────────────>│
   │                          │
   │                          │ 1. 遍历收到的内核列表
   │                          │ 2. 跳过自己
   │                          │ 3. 检查是否已在本地存在
   │                          │ 4. 不存在则添加到本地
   │                          │
   │ SyncKnownKernelsResponse │
   │ (newly_known_kernels: [C])│
   │◄─────────────────────────┤
   │                          │
   │                          │ 5. 尝试连接新内核C
```

### 5.2 双向同步

为了确保所有内核都能互相发现，采用双向同步机制：

1. **正向同步**：A 通知 B 关于 C 的信息
2. **反向同步**：B 收到后，立即向 C 同步自己的信息
3. **结果**：A、B、C 最终都会互相发现

---

## 六、心跳机制

### 6.1 心跳流程

```
内核-A                      内核-B
   │                          │
   │ KernelHeartbeatRequest   │
   │ (kernel_id, timestamp,   │
   │  stats: {connectors,     │
   │         channels})       │
   ├─────────────────────────>│
   │                          │
   │ KernelHeartbeatResponse  │
   │ (acknowledged, updates)  │
   │◄─────────────────────────┤
   │                          │
   │ 1. 更新 LastHeartbeat    │
   │ 2. 处理状态更新          │
```

### 6.2 心跳内容

- **请求**：内核ID、时间戳、统计信息（连接器数量、频道数量）
- **响应**：确认标志、服务器时间戳、其他内核状态更新

### 6.3 作用

- 检测连接是否存活
- 更新对端的心跳时间
- 同步其他内核的状态变更

---

## 七、证书管理

### 7.1 证书结构

```
certs/
├── ca.crt                    # CA证书（根证书）
├── kernel.crt               # 内核证书
├── kernel.key               # 内核私钥
├── peer-kernel-1-ca.crt     # 对端内核的CA证书
├── peer-kernel-2-ca.crt
└── peer-kernel-3-ca.crt
```

### 7.2 证书交换流程

1. **发起方**发送自己的CA证书
2. **接收方**保存对端CA证书
3. **接收方**返回自己的CA证书
4. **发起方**保存对方CA证书
5. 后续通信使用双方CA进行双向认证

---

## 八、交互命令

内核启动后，支持以下交互命令：

| 命令 | 说明 |
|------|------|
| `connect-kernel <id> <addr> <port>` | 连接到指定内核 |
| `approve-request <request-id>` | 批准互联请求 |
| `list-requests` | 列出待审批请求 |
| `ks` 或 `kernels` | 列出已知内核 |
| `disconnect-kernel <id>` | 断开与指定内核的连接 |
| `help` | 显示帮助信息 |
| `status` | 显示内核状态 |

---

## 九、使用示例

### 场景：三个内核互联

假设有三个内核：
- kernel-1: 192.168.1.4
- kernel-2: 192.168.202.136
- kernel-3: 192.168.202.140

**步骤1：启动三个内核**

```bash
# 在三台机器上分别启动
./bin/kernel.exe --config config/kernel.yaml
```

**步骤2：kernel-1 连接 kernel-2 和 kernel-3**

```
[kernel-1] > connect-kernel kernel-2 192.168.202.136 50053
[kernel-1] > connect-kernel kernel-3 192.168.202.140 50053
```

**步骤3：kernel-2 和 kernel-3 审批请求**

```
# 在 kernel-2 上
[kernel-2] > approve-request <request-id-1>

# 在 kernel-3 上
[kernel-3] > approve-request <request-id-2>
```

**步骤4：验证互联**

```
# 在任意内核上执行
[kernel-1] > ks
=== Known Kernels ===
kernel-2  192.168.202.136:50051  active
kernel-3  192.168.202.140:50051  active

[kernel-2] > ks
=== Known Kernels ===
kernel-1  192.168.1.4:50051  active
kernel-3  192.168.202.140:50051  active

[kernel-3] > ks
=== Known Kernels ===
kernel-1  192.168.1.4:50051  active
kernel-2  192.168.202.136:50051  active
```

---

## 十、配置说明

### 10.1 配置文件

```yaml:config/kernel.yaml
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

---

## 十一、故障处理

| 场景 | 处理方式 |
|------|----------|
| **连接断开** | 心跳检测到后更新状态为 inactive，保留信息以便重连 |
| **重复注册** | 检查内核ID，冲突则返回错误，否则更新连接信息 |
| **连接超时** | 重试机制（默认最大3次），记录错误日志 |
| **证书错误** | 检查证书路径、有效期、CA签名是否匹配 |

---

## 十二、总结

多内核互联功能的核心能力：

| 能力 | 描述 |
|------|------|
| **互联互通** | 多个内核可以互相发现、建立连接 |
| **自动同步** | 内核间自动同步已知的节点信息 |
| **安全可靠** | 基于 mTLS 的双向认证机制 |
| **容错恢复** | 支持连接断开后的重连机制 |
| **状态监控** | 心跳机制实时监控连接状态 |

该功能为可信数据空间的跨组织互联互通提供了坚实的技术基础。

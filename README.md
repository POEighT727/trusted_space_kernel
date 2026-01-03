# 可信数据空间内核 (Trusted Data Space Kernel)

基于"内核+外延"设计原则的可信数据空间内核架构实现。

## 📋 架构概览

本项目实现了一个**标准化、轻量级的可信数据空间内核**，包含以下核心组件：

### 核心内核层 (Kernel Layer)

1. **安全认证模块 (Security Hub)**
   - 内部 CA：证书签发与撤销
   - mTLS 终结：基于 TLS 1.3 的双向认证

2. **管控模块 (Control Plane)**
   - 身份注册表：维护连接器信息
   - 权限策略引擎：基于策略验证请求合法性

3. **流通调度模块 (Circulation Plane)**
   - 频道管理：管理数据传输通道
   - 数据中转：提供加密数据流中转

4. **存证模块 (Evidence Plane)**
   - 溯源日志：记录所有关键事件的哈希
   - 链式存储：确保审计日志不可篡改

### 实体外延层 (Extension Layer)

- **连接器 (Connector)**：作为实体进入数据空间的网关，通过标准 gRPC 接口与内核交互

## 🚀 快速开始

### 前置要求

- Go 1.21+
- Protocol Buffers compiler (protoc)
- OpenSSL（用于生成测试证书）
- Make（可选，用于简化构建）

### 安装步骤

#### 1. 克隆并安装依赖

```bash
git clone <repository-url>
cd trusted-data-space-kernel

# 安装 Go 依赖
go mod download

# 安装 protoc 插件
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

#### 2. 生成 Protobuf 代码

```bash
make proto
```

或手动执行：

```bash
mkdir -p api/v1
protoc --go_out=. --go_opt=paths=source_relative \
    --go-grpc_out=. --go-grpc_opt=paths=source_relative \
    proto/kernel/v1/*.proto
```

#### 3. 生成测试证书

**Linux/Mac:**

```bash
chmod +x scripts/gen_certs.sh
./scripts/gen_certs.sh
```

**Windows (PowerShell):**

```powershell
.\scripts\gen_certs.ps1
```

#### 4. 创建日志目录

```bash
mkdir -p logs
```

#### 5. 编译内核和连接器

```bash
make all
```

或手动编译：

```bash
go build -o bin/kernel ./kernel/cmd
go build -o bin/connector ./connector/cmd
```

### 运行示例

#### 终端 1：启动内核

```bash
./bin/kernel -config config/kernel.yaml
```

输出应包含：

```
✓ Registry initialized
✓ Policy engine initialized
✓ Channel manager initialized
✓ Audit log initialized
✓ mTLS configured
✓ gRPC services registered
🚀 Trusted Data Space Kernel started on 0.0.0.0:50051
```

#### 终端 2：启动接收方连接器（Connector B）

```bash
./bin/connector -config config/connector-B.yaml -mode receiver -channel <channel-id>
```

**注意**：第一次运行时，你需要先启动发送方以获取 channel ID。

#### 终端 3：启动发送方连接器（Connector A）

```bash
./bin/connector -config config/connector.yaml -mode sender -receiver connector-B
```

发送方会：
1. 创建到 connector-B 的频道
2. 发送测试数据
3. 提交存证记录
4. 关闭频道

#### 完整演示流程

1. 启动内核（终端 1）
2. 启动发送方并记录显示的 `channel-id`（终端 3）
3. 使用该 `channel-id` 启动接收方（终端 2）
4. 观察数据流转和存证记录

## 📡 接口定义

### Identity Service（身份与准入服务）

```protobuf
service IdentityService {
  rpc Handshake (HandshakeRequest) returns (HandshakeResponse);
  rpc Heartbeat (HeartbeatRequest) returns (HeartbeatResponse);
}
```

### Channel Service（流通与频道服务）

```protobuf
service ChannelService {
  rpc CreateChannel (CreateChannelRequest) returns (CreateChannelResponse);
  rpc StreamData (stream DataPacket) returns (stream TransferStatus);
  rpc SubscribeData (SubscribeRequest) returns (stream DataPacket);
  rpc CloseChannel (CloseChannelRequest) returns (CloseChannelResponse);
}
```

### Evidence Service（存证溯源服务）

```protobuf
service EvidenceService {
  rpc SubmitEvidence (EvidenceRequest) returns (EvidenceResponse);
  rpc QueryEvidence (QueryRequest) returns (QueryResponse);
}
```

完整的 Protocol Buffers 定义见 `proto/kernel/v1/` 目录。

## 🔐 安全机制

### mTLS 双向认证

- 所有连接器必须持有由内部 CA 签发的有效 X.509 证书
- 使用 TLS 1.3 进行加密通信
- 连接器 ID 必须与证书 CN（Common Name）字段匹配

### 权限策略引擎

支持细粒度的访问控制：

```go
// 示例：允许 connector-A 向 connector-B 发送特定主题的数据
policyEngine.AddRule(&PolicyRule{
    SenderID:   "connector-A",
    ReceiverID: "connector-B",
    DataTopics: []string{"data-type-1", "data-type-2"},
    Allowed:    true,
})
```

### 存证溯源

所有关键操作自动记录：
- `CHANNEL_CREATED`：频道创建
- `TRANSFER_START`：传输开始
- `TRANSFER_END`：传输结束
- `AUTH_SUCCESS` / `AUTH_FAIL`：认证结果
- `POLICY_VIOLATION`：策略违规

存证记录采用链式结构，每条记录包含前一条记录的哈希，确保不可篡改。

## 📂 项目结构

```
.
├── proto/                    # Protocol Buffer 定义
│   └── kernel/v1/
│       ├── identity.proto
│       ├── channel.proto
│       └── evidence.proto
├── kernel/                   # 内核实现
│   ├── cmd/                  # 内核主程序
│   ├── server/               # gRPC 服务实现
│   ├── security/             # 安全认证模块
│   ├── control/              # 管控模块
│   ├── circulation/          # 流通调度模块
│   └── evidence/             # 存证模块
├── connector/                # 连接器实现
│   ├── cmd/                  # 连接器主程序
│   └── client/               # 连接器客户端库
├── config/                   # 配置文件
├── scripts/                  # 工具脚本
├── certs/                    # 证书目录（生成后）
├── logs/                     # 日志目录
├── Makefile
├── go.mod
└── README.md
```

## 🔧 配置说明

### 内核配置 (`config/kernel.yaml`)

```yaml
server:
  address: "0.0.0.0"
  port: 50051

security:
  ca_cert_path: "certs/ca.crt"
  server_cert_path: "certs/kernel.crt"
  server_key_path: "certs/kernel.key"

evidence:
  persistent: true
  log_file_path: "logs/audit.log"

policy:
  default_allow: true  # 默认策略
```

### 连接器配置 (`config/connector.yaml`)

```yaml
connector:
  id: "connector-A"
  entity_type: "data_source"
  public_key: "mock_public_key_abc123"

kernel:
  address: "localhost"
  port: 50051

security:
  ca_cert_path: "certs/ca.crt"
  client_cert_path: "certs/connector-A.crt"
  client_key_path: "certs/connector-A.key"
  server_name: "trusted-data-space-kernel"
```

## 🧪 开发指南

### 添加新的连接器

1. 复制并修改配置文件：

```bash
cp config/connector.yaml config/connector-C.yaml
```

2. 修改 `connector_id` 为 `connector-C`

3. 生成证书（已在 `gen_certs.sh` 中包含）

4. 启动连接器：

```bash
./bin/connector -config config/connector-C.yaml -mode sender -receiver connector-A
```

### 自定义权限策略

在 `kernel/control/policy.go` 的 `LoadDefaultRules()` 中添加规则：

```go
pe.AddRule(&PolicyRule{
    SenderID:   "connector-C",
    ReceiverID: "connector-A",
    DataTopics: []string{"sensitive-data"},
    Allowed:    true,
})
```

### 查询存证记录

使用连接器的 `QueryEvidence` 方法：

```go
records, err := connector.QueryEvidence(channelID, 100)
for _, record := range records {
    log.Printf("Event: %s, Hash: %s", 
        record.Evidence.EventType, 
        record.Evidence.DataHash)
}
```

## 📊 性能特性

- **并发处理**：支持多个连接器同时连接
- **流式传输**：基于 gRPC 双向流的高效数据传输
- **缓冲队列**：每个频道 1000 个数据包的缓冲
- **自动清理**：定期清理不活跃的频道和离线连接器

## 🛡️ 生产部署建议

1. **证书管理**
   - 使用正式的 PKI 基础设施
   - 定期轮换证书
   - 实现证书吊销列表（CRL）

2. **日志存储**
   - 将审计日志持久化到分布式存储
   - 考虑集成区块链进行存证

3. **高可用性**
   - 部署多个内核实例
   - 使用负载均衡器
   - 实现故障转移机制

4. **监控告警**
   - 集成 Prometheus/Grafana
   - 监控连接器在线状态
   - 告警异常认证尝试

5. **安全加固**
   - 启用防火墙规则
   - 实施 IP 白名单
   - 定期安全审计

## 📝 许可证

本项目为示例实现，仅供学习和研究使用。

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！

## 📧 联系方式

如有问题或建议，请通过以下方式联系：

- Issue Tracker: [GitHub Issues]
- Email: [your-email]

---

**注意**：本项目生成的证书仅用于测试目的，请勿在生产环境中使用。


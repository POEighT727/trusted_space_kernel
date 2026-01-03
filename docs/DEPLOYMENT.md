# 跨主机部署指南

本文档说明如何将可信数据空间内核和连接器部署在不同的主机上。

## 部署架构

```
主机A (192.168.1.100)         主机B (192.168.1.101)         主机C (192.168.1.102)
┌──────────────────┐          ┌──────────────────┐          ┌──────────────────┐
│   内核服务器      │          │   连接器-B        │          │   连接器-C        │
│  (Kernel)        │◄─────────┤                  │          │                  │
│  Port: 50051     │          │                  │          │                  │
└──────────────────┘          └──────────────────┘          └──────────────────┘
         ▲                              ▲                              ▲
         │                              │                              │
         └──────────────────────────────┴──────────────────────────────┘
                             所有连接器连接到同一个内核
```

## 前置要求

1. **网络连通性**
   - 确保所有主机之间网络互通
   - 内核服务器需要开放端口 50051（或配置的端口）
   - 检查防火墙规则，允许 gRPC 流量

2. **证书配置**
   - 所有连接器需要共享相同的 CA 证书
   - 每个连接器需要自己的客户端证书
   - 服务器证书的 CN 或 SAN 需要匹配 `server_name` 配置

## 部署步骤

### 1. 在内核服务器上部署内核

#### 1.1 配置内核服务器

编辑 `config/kernel.yaml`：

```yaml
server:
  # 监听所有网络接口，允许跨主机连接
  address: "0.0.0.0"
  port: 50051
```

#### 1.2 启动内核服务器

```bash
# 在内核服务器主机上
cd /path/to/kernel
go run kernel/cmd/main.go -config config/kernel.yaml
```

#### 1.3 验证内核运行

内核应该显示：
```
✓ 内核服务器启动成功
✓ 监听地址: 0.0.0.0:50051
```

### 2. 在连接器主机上部署连接器

#### 2.1 获取连接器发布包

**方式1: 使用独立发布包（推荐）**

连接器可以独立部署，无需源代码和开发环境。请参考 [连接器独立部署指南](CONNECTOR_DEPLOYMENT.md) 获取详细说明。

**方式2: 从源代码编译**

如果你有源代码访问权限，可以自行编译：

```bash
# 在连接器主机或编译服务器上
go build -o connector ./connector/cmd
```

#### 2.2 准备证书文件

**推荐方式：首次运行自动注册（无需手动准备证书）**

连接器首次运行时会自动连接到内核并注册获取证书，无需手动准备证书文件。

**备选方式：手动准备证书**

如果需要手动准备证书文件，确保连接器主机上有：
- `certs/ca.crt` - CA 证书（与内核服务器共享）
- `certs/connector-X.crt` - 连接器客户端证书
- `certs/connector-X.key` - 连接器私钥

**证书分发方式：**
- 方式1：手动复制证书文件到连接器主机
- 方式2：使用首次注册功能自动获取证书（推荐）

#### 2.3 配置连接器

编辑连接器配置文件（例如 `config/connector-B.yaml`）：

```yaml
connector:
  id: "connector-B"
  entity_type: "algorithm_node"
  public_key: "mock_public_key_xyz789"

kernel:
  # 使用内核服务器的IP地址或域名
  address: "192.168.1.100"  # 替换为实际的内核服务器地址
  port: 50051

security:
  ca_cert_path: "certs/ca.crt"
  client_cert_path: "certs/connector-B.crt"
  client_key_path: "certs/connector-B.key"
  # 服务器名称必须与服务器证书的CN或SAN匹配
  # 如果使用IP地址，可能需要调整证书配置
  server_name: "trusted-data-space-kernel"
```

**重要配置说明：**

- `kernel.address`: 
  - 本地部署：使用 `"localhost"` 或 `"127.0.0.1"`
  - 跨主机部署：使用内核服务器的实际IP地址或域名，例如 `"192.168.1.100"` 或 `"kernel.example.com"`

- `security.server_name`:
  - 必须与服务器证书的 Common Name (CN) 或 Subject Alternative Name (SAN) 匹配
  - 如果使用IP地址连接，证书的SAN需要包含该IP地址
  - 如果使用域名连接，证书的CN或SAN需要包含该域名

#### 2.4 启动连接器（首次运行自动注册）

```bash
# 在连接器主机上
cd /path/to/connector
go run connector/cmd/main.go -config config/connector-B.yaml
```

**首次运行流程：**

连接器首次运行时会自动：
1. 检测到证书文件不存在
2. 连接到内核的引导端口（主端口+1，如 50052）
3. 发送注册请求，包含连接器配置信息
4. 内核验证并签发证书
5. 连接器保存证书到 `certs/` 目录
6. 使用证书连接到内核主端口（50051）
7. 完成握手，开始正常工作

**首次运行输出示例：**
```
未找到证书文件，开始首次注册...
正在连接到引导服务器 192.168.1.100:50052...
✓ 证书已保存
正在连接到内核 192.168.1.100:50051...
✓ 连接成功！连接器ID: connector-B
```

#### 2.5 验证连接

连接器应该显示：
```
正在连接到内核 192.168.1.100:50051...
✓ 连接成功！连接器ID: connector-B
```

### 3. 网络配置检查

#### 3.1 防火墙规则

**Linux (iptables):**
```bash
# 允许入站连接到内核服务器
sudo iptables -A INPUT -p tcp --dport 50051 -j ACCEPT
```

**Linux (firewalld):**
```bash
sudo firewall-cmd --permanent --add-port=50051/tcp
sudo firewall-cmd --reload
```

**Windows (PowerShell):**
```powershell
New-NetFirewallRule -DisplayName "Trusted Data Space Kernel" -Direction Inbound -LocalPort 50051 -Protocol TCP -Action Allow
```

#### 3.2 网络连通性测试

在连接器主机上测试到内核服务器的连接：

```bash
# 测试TCP连接
telnet 192.168.1.100 50051

# 或使用nc (netcat)
nc -zv 192.168.1.100 50051
```

### 4. TLS 证书配置（跨主机）

#### 4.1 使用IP地址连接

如果使用IP地址连接（例如 `192.168.1.100`），需要确保：

1. **服务器证书包含IP地址SAN：**
   ```bash
   # 生成证书时添加IP地址到SAN
   openssl req -new -x509 -key kernel.key -out kernel.crt \
     -subj "/CN=trusted-data-space-kernel" \
     -addext "subjectAltName=IP:192.168.1.100"
   ```

2. **连接器配置：**
   ```yaml
   kernel:
     address: "192.168.1.100"
   
   security:
     server_name: "192.168.1.100"  # 或使用证书的CN
   ```

#### 4.2 使用域名连接

如果使用域名连接（例如 `kernel.example.com`），需要确保：

1. **DNS解析：**
   - 确保域名可以解析到内核服务器的IP地址
   - 或在 `/etc/hosts` 中添加映射：
     ```
     192.168.1.100 kernel.example.com
     ```

2. **服务器证书包含域名：**
   ```bash
   openssl req -new -x509 -key kernel.key -out kernel.crt \
     -subj "/CN=kernel.example.com" \
     -addext "subjectAltName=DNS:kernel.example.com"
   ```

3. **连接器配置：**
   ```yaml
   kernel:
     address: "kernel.example.com"
   
   security:
     server_name: "kernel.example.com"
   ```

## 故障排查

### 问题1: 连接超时

**症状：**
```
failed to connect to kernel: context deadline exceeded
```

**解决方案：**
1. 检查网络连通性：`ping 192.168.1.100`
2. 检查防火墙规则是否允许端口 50051
3. 检查内核服务器是否正在运行
4. 检查内核服务器是否监听在 `0.0.0.0` 而不是 `127.0.0.1`

### 问题2: TLS 证书验证失败

**症状：**
```
x509: certificate is valid for ... not ...
```

**解决方案：**
1. 确保 `server_name` 配置与服务器证书的CN或SAN匹配
2. 如果使用IP地址，确保证书包含该IP地址的SAN
3. 检查CA证书是否正确

### 问题3: 连接被拒绝

**症状：**
```
connection refused
```

**解决方案：**
1. 检查内核服务器是否正在运行
2. 检查内核服务器监听的地址和端口
3. 检查防火墙是否阻止了连接

### 问题4: 证书文件不存在

**症状：**
```
failed to read CA cert: open certs/ca.crt: no such file or directory
```

**解决方案：**
1. 确保证书文件路径正确
2. 从内核服务器复制证书文件到连接器主机
3. 或使用首次注册功能获取证书

## 性能优化建议

1. **网络延迟：**
   - 连接超时已设置为30秒，适应跨主机网络延迟
   - 如果网络延迟较大，可以考虑增加超时时间

2. **连接池：**
   - gRPC 连接会自动复用，无需手动管理连接池

3. **证书缓存：**
   - 证书文件会在启动时加载，后续不会重新读取
   - 如果证书更新，需要重启连接器

## 安全建议

1. **使用域名而非IP地址：**
   - 域名更易于管理和更新
   - 证书验证更可靠

2. **定期更新证书：**
   - 建议定期轮换证书
   - 确保证书有效期足够长

3. **网络安全：**
   - 使用VPN或专用网络连接
   - 限制内核服务器的访问来源

4. **防火墙规则：**
   - 仅允许必要的IP地址访问内核服务器
   - 使用白名单而非开放所有来源

## 示例场景

### 场景1: 三台主机部署

- **主机A (192.168.1.100)**: 内核服务器
- **主机B (192.168.1.101)**: 连接器-B
- **主机C (192.168.1.102)**: 连接器-C

**配置示例：**

主机B的 `config/connector-B.yaml`:
```yaml
kernel:
  address: "192.168.1.100"
  port: 50051
```

主机C的 `config/connector-C.yaml`:
```yaml
kernel:
  address: "192.168.1.100"
  port: 50051
```

### 场景2: 混合部署（本地+远程）

- **主机A**: 内核服务器 + 连接器-A（本地连接）
- **主机B**: 连接器-B（远程连接）

主机A的连接器-A配置：
```yaml
kernel:
  address: "localhost"  # 本地连接
  port: 50051
```

主机B的连接器-B配置：
```yaml
kernel:
  address: "192.168.1.100"  # 远程连接
  port: 50051
```

## 连接器独立部署

连接器支持独立部署，无需完整的源代码和开发环境。详细说明请参考：

📖 **[连接器独立部署指南](CONNECTOR_DEPLOYMENT.md)**

该指南包含：
- 如何获取连接器发布包
- 独立部署步骤
- 首次运行自动注册流程
- 多连接器部署
- 故障排查
- 生产环境部署建议

## 总结

跨主机部署的关键点：

1. ✅ 内核服务器监听 `0.0.0.0` 以接受远程连接
2. ✅ 连接器配置使用内核服务器的实际IP或域名
3. ✅ 确保证书配置正确，`server_name` 与证书匹配
4. ✅ 配置防火墙允许端口 50051
5. ✅ 确保所有主机网络互通
6. ✅ 连接器支持独立部署，首次运行自动注册获取证书

完成以上配置后，连接器就可以成功连接到远程内核服务器了。

**推荐部署方式：**
- 使用连接器独立发布包（无需源代码）
- 首次运行自动注册获取证书（无需手动准备证书）
- 参考 [连接器独立部署指南](CONNECTOR_DEPLOYMENT.md) 获取详细说明


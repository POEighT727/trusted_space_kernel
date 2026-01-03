# 连接器独立部署指南

本文档说明如何独立部署连接器，无需完整的源代码和开发环境。

## 概述

连接器是一个独立的客户端程序，可以部署在任何能够连接到内核服务器的机器上。连接器主机上**不需要**：

- ❌ 源代码
- ❌ Go 开发环境
- ❌ protoc 编译器
- ❌ 内核代码

连接器主机上**只需要**：

- ✅ 连接器可执行文件
- ✅ 配置文件
- ✅ 证书文件（首次运行后自动获取）

## 获取连接器发布包

### 方式1: 从发布版本下载

从项目发布页面下载对应平台的连接器发布包：

- Linux: `connector-{version}-linux-amd64.tar.gz`
- Windows: `connector-{version}-windows-amd64.zip`

### 方式2: 自行打包

如果你有源代码访问权限，可以自行打包：

**Linux/Mac:**
```bash
# 打包 Linux 版本
bash scripts/package_connector.sh 1.0.0 linux-amd64

# 打包 Windows 版本（需要交叉编译）
bash scripts/package_connector.sh 1.0.0 windows-amd64
```

**Windows PowerShell:**
```powershell
# 打包 Windows 版本
.\scripts\package_connector.ps1 -Version 1.0.0 -Platform windows-amd64
```

打包完成后，发布包位于 `dist/` 目录。

## 部署步骤

### 步骤 1: 解压发布包

**Linux/Mac:**
```bash
tar -xzf connector-1.0.0-linux-amd64.tar.gz
cd connector-1.0.0-linux-amd64
```

**Windows:**
```powershell
# 使用解压工具解压
Expand-Archive connector-1.0.0-windows-amd64.zip
cd connector-1.0.0-windows-amd64
```

### 步骤 2: 配置连接器

1. **复制配置模板：**

   **Linux/Mac:**
   ```bash
   cp config/connector-template.yaml config/connector.yaml
   ```

   **Windows:**
   ```powershell
   Copy-Item config\connector-template.yaml config\connector.yaml
   ```

2. **编辑配置文件：**

   打开 `config/connector.yaml`，修改以下内容：

   ```yaml
   connector:
     id: "connector-B"              # 修改为你的连接器ID（唯一标识）
     entity_type: "algorithm_node"   # 修改为你的实体类型
     public_key: "your-public-key"   # 修改为你的公钥

   kernel:
     address: "192.168.1.100"        # 修改为内核服务器的IP地址或域名
     port: 50051                      # 内核服务器端口（通常为50051）

   security:
     ca_cert_path: "certs/ca.crt"
     client_cert_path: "certs/connector-B.crt"  # 修改为你的连接器ID
     client_key_path: "certs/connector-B.key"    # 修改为你的连接器ID
     server_name: "trusted-data-space-kernel"    # 必须与服务器证书匹配
   ```

   **重要配置说明：**

   - `connector.id`: 连接器的唯一标识符，必须在内核中唯一
   - `kernel.address`: 内核服务器的地址，可以是IP或域名
   - `security.server_name`: 必须与内核服务器证书的CN或SAN匹配

### 步骤 3: 首次运行并注册

首次运行连接器时，会自动连接到内核并注册获取证书。

**Linux/Mac:**
```bash
./connector -config config/connector.yaml
```

**Windows:**
```powershell
.\connector.exe -config config\connector.yaml
```

首次运行流程：

1. 连接器检测到证书文件不存在
2. 自动连接到内核的引导端口（主端口+1，如 50052）
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

### 步骤 4: 验证连接

连接成功后，你会看到：
- ✓ 连接成功提示
- 连接器ID显示
- 交互式命令提示符

### 步骤 5: 使用连接器

连接器启动后，可以使用以下命令：

```
> create channel-1 data-topic connector-C
> sendto channel-1 Hello, World!
> receive channel-1
> channels
> status
> exit
```

详细命令说明请参考连接器 README 或主项目文档。

## 目录结构

部署后的目录结构：

```
connector-{version}/
├── connector              # Linux/Mac 可执行文件
├── connector.exe          # Windows 可执行文件
├── config/
│   ├── connector-template.yaml  # 配置模板
│   └── connector.yaml            # 实际配置文件（需要创建）
├── certs/                 # 证书目录（首次运行后自动生成）
│   ├── ca.crt            # CA 证书
│   ├── connector-X.crt   # 连接器证书
│   └── connector-X.key   # 连接器私钥
├── received/             # 接收文件目录（自动创建）
├── README.md             # 快速开始指南
└── DEPLOY.md             # 部署说明
```

## 配置多个连接器

如果需要在同一台主机上运行多个连接器：

1. **为每个连接器创建独立目录：**
   ```bash
   connector-B/
   connector-C/
   connector-D/
   ```

2. **每个目录包含独立的：**
   - 可执行文件（可以共享）
   - 配置文件（`config/connector.yaml`）
   - 证书目录（`certs/`）

3. **分别运行：**
   ```bash
   cd connector-B && ./connector -config config/connector.yaml
   cd connector-C && ./connector -config config/connector.yaml
   ```

## 更新连接器

### 备份配置和证书

**Linux/Mac:**
```bash
mkdir -p backup
cp -r config backup/
cp -r certs backup/
```

**Windows:**
```powershell
New-Item -ItemType Directory -Path backup
Copy-Item -Recurse config backup\config
Copy-Item -Recurse certs backup\certs
```

### 安装新版本

1. 解压新版本连接器
2. 恢复配置和证书：
   ```bash
   # Linux/Mac
   cp backup/config/* config/
   cp backup/certs/* certs/
   
   # Windows
   Copy-Item backup\config\* config\
   Copy-Item backup\certs\* certs\
   ```
3. 运行新版本连接器

## 故障排查

### 问题1: 首次注册失败

**症状：**
```
注册失败: connection refused
```

**可能原因：**
- 内核服务器未运行
- 内核服务器未启动引导服务
- 网络不通或防火墙阻止

**解决方案：**
1. 检查内核服务器是否运行
2. 检查内核服务器日志，确认引导服务已启动
3. 测试网络连通性：`ping <kernel-address>`
4. 检查防火墙规则

### 问题2: 证书验证失败

**症状：**
```
x509: certificate is valid for ... not ...
```

**可能原因：**
- `server_name` 配置与服务器证书不匹配
- 使用IP地址连接但证书不包含该IP

**解决方案：**
1. 检查 `server_name` 配置
2. 如果使用IP地址，确保证书包含该IP的SAN
3. 或使用域名连接，并配置DNS解析

### 问题3: 连接器ID已存在

**症状：**
```
注册失败: connector X already registered
```

**可能原因：**
- 该连接器ID已被其他连接器使用

**解决方案：**
1. 修改配置文件中的 `connector.id`
2. 或联系内核管理员撤销旧注册

### 问题4: 证书文件权限问题（Linux/Mac）

**症状：**
```
failed to read cert: permission denied
```

**解决方案：**
```bash
chmod 600 certs/*.key
chmod 644 certs/*.crt
```

## 安全最佳实践

1. **证书安全：**
   - 证书文件包含敏感信息，设置适当的文件权限
   - Linux/Mac: `chmod 600 certs/*.key`
   - 不要将证书文件提交到版本控制系统

2. **网络安全：**
   - 使用VPN或专用网络连接内核服务器
   - 配置防火墙，仅允许必要的IP访问

3. **配置安全：**
   - 不要在生产环境使用默认配置
   - 定期更新连接器版本
   - 使用强密码保护私钥（如果支持）

4. **运行安全：**
   - 使用非特权用户运行连接器
   - 限制连接器目录的访问权限

## 生产环境部署建议

### 1. 使用系统服务（Linux）

创建 systemd 服务文件 `/etc/systemd/system/connector.service`：

```ini
[Unit]
Description=Trusted Data Space Connector
After=network.target

[Service]
Type=simple
User=connector
WorkingDirectory=/opt/connector
ExecStart=/opt/connector/connector -config /opt/connector/config/connector.yaml
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
```

启动服务：
```bash
sudo systemctl enable connector
sudo systemctl start connector
sudo systemctl status connector
```

### 2. 使用 Windows 服务

可以使用 NSSM (Non-Sucking Service Manager) 将连接器注册为 Windows 服务。

### 3. 日志管理

连接器输出到标准输出，建议重定向到日志文件：

```bash
./connector -config config/connector.yaml >> logs/connector.log 2>&1
```

或使用日志轮转工具（如 logrotate）管理日志文件。

## 总结

连接器独立部署的关键点：

1. ✅ 只需要可执行文件、配置文件和证书
2. ✅ 首次运行自动注册获取证书
3. ✅ 支持跨主机部署
4. ✅ 配置简单，易于管理
5. ✅ 支持多实例运行

完成以上步骤后，连接器就可以成功连接到内核服务器并开始工作了。


# 连接器独立打包脚本 (Windows PowerShell)
# 用于创建可独立部署的连接器发布包

param(
    [string]$Version = "1.0.0",
    [string]$Platform = "windows-amd64"
)

$ErrorActionPreference = "Stop"

$OutputDir = "dist"
$PackageName = "connector-$Version-$Platform"

Write-Host "📦 开始打包连接器 $Version for $Platform..." -ForegroundColor Cyan

# 清理旧的构建
Write-Host "清理旧的构建文件..." -ForegroundColor Yellow
if (Test-Path $OutputDir) {
    Remove-Item -Recurse -Force $OutputDir
}
New-Item -ItemType Directory -Path "$OutputDir\$PackageName" -Force | Out-Null

# 编译连接器
Write-Host "编译连接器..." -ForegroundColor Yellow
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -o "$OutputDir\$PackageName\connector.exe" ./connector/cmd

# 创建配置模板目录
Write-Host "创建配置模板..." -ForegroundColor Yellow
New-Item -ItemType Directory -Path "$OutputDir\$PackageName\config" -Force | Out-Null
Copy-Item "config\connector.yaml" "$OutputDir\$PackageName\config\connector-template.yaml"

# 创建证书目录结构（空目录，证书通过首次注册获取）
New-Item -ItemType Directory -Path "$OutputDir\$PackageName\certs" -Force | Out-Null
"# 证书目录`n# 首次运行连接器时会自动注册并获取证书" | Out-File -FilePath "$OutputDir\$PackageName\certs\.gitkeep" -Encoding UTF8

# 创建接收文件目录
New-Item -ItemType Directory -Path "$OutputDir\$PackageName\received" -Force | Out-Null

# 创建 README
$ReadmeContent = @"
# 连接器独立部署包

## 快速开始

### 1. 配置连接器

编辑 `config\connector-template.yaml`，修改以下配置：

```yaml
connector:
  id: "your-connector-id"        # 修改为你的连接器ID
  entity_type: "data_source"     # 修改为你的实体类型
  public_key: "your-public-key" # 修改为你的公钥

kernel:
  address: "192.168.1.100"       # 修改为内核服务器地址
  port: 50051

security:
  ca_cert_path: "certs\ca.crt"
  client_cert_path: "certs\connector-X.crt"
  client_key_path: "certs\connector-X.key"
  server_name: "trusted-data-space-kernel"
```

将 `config\connector-template.yaml` 复制为 `config\connector.yaml`：

```powershell
Copy-Item config\connector-template.yaml config\connector.yaml
# 然后编辑 config\connector.yaml
```

### 2. 首次运行（自动注册）

首次运行时会自动连接到内核并注册获取证书：

```powershell
.\connector.exe -config config\connector.yaml
```

首次运行成功后，证书会自动保存到 `certs\` 目录。

### 3. 后续运行

证书获取后，后续运行直接使用已保存的证书：

```powershell
.\connector.exe -config config\connector.yaml
```

## 目录结构

```
connector-{version}/
├── connector.exe      # 连接器可执行文件
├── config/
│   └── connector-template.yaml  # 配置模板
├── certs/             # 证书目录（首次运行后自动生成）
├── received/          # 接收文件目录
└── README.md          # 本文件
```

## 命令说明

连接器支持以下命令：

- `create <channel-id> <data-topic> <receiver-id1,receiver-id2,...>` - 创建频道
- `sendto <channel-id> <message>` - 发送数据到频道
- `sendto <channel-id> <file-path>` - 发送文件到频道
- `receive <channel-id>` - 接收频道数据
- `subscribe <channel-id>` - 订阅频道
- `channels` - 查看当前参与的频道
- `status` - 查看连接器状态
- `status <active|inactive|closed>` - 设置连接器状态
- `discover` - 发现其他连接器
- `info <connector-id>` - 查看连接器信息
- `exit` 或 `quit` - 退出

## 故障排查

### 连接失败

1. 检查内核服务器地址和端口是否正确
2. 检查网络连通性：`ping <kernel-address>`
3. 检查防火墙是否允许端口 50051

### 证书问题

1. 删除 `certs\` 目录下的证书文件
2. 重新运行连接器进行首次注册

### 更多帮助

请参考完整部署文档：`docs\DEPLOYMENT.md`
"@

$ReadmeContent | Out-File -FilePath "$OutputDir\$PackageName\README.md" -Encoding UTF8

# 创建部署说明
$DeployContent = @"
# 连接器部署说明

## 系统要求

- Windows x64
- 网络连接到内核服务器
- 防火墙允许连接到内核服务器端口（默认 50051）

## 部署步骤

### 步骤 1: 解压发布包

使用解压工具解压 `connector-{version}-windows-amd64.zip`

### 步骤 2: 配置连接器

1. 复制配置模板：
   ```powershell
   Copy-Item config\connector-template.yaml config\connector.yaml
   ```

2. 编辑 `config\connector.yaml`，设置：
   - 连接器ID
   - 实体类型
   - 公钥
   - 内核服务器地址

### 步骤 3: 首次运行并注册

```powershell
.\connector.exe -config config\connector.yaml
```

首次运行会自动：
- 连接到内核服务器
- 注册连接器
- 获取并保存证书

### 步骤 4: 验证连接

连接成功后，你会看到：
```
✓ 连接成功！连接器ID: your-connector-id
```

### 步骤 5: 使用连接器

连接器启动后，你可以使用交互式命令：
- 创建频道
- 发送/接收数据
- 发送/接收文件
- 查看频道信息
- 等等

## 安全注意事项

1. **证书安全**：
   - 证书文件包含敏感信息，请妥善保管
   - 不要将证书文件提交到版本控制系统

2. **网络安全**：
   - 使用VPN或专用网络连接
   - 配置防火墙规则，限制访问来源

3. **配置安全**：
   - 不要在生产环境中使用默认配置
   - 定期更新连接器版本

## 更新连接器

1. 备份当前配置和证书：
   ```powershell
   Copy-Item -Recurse config backup\config
   Copy-Item -Recurse certs backup\certs
   ```

2. 解压新版本连接器

3. 恢复配置和证书：
   ```powershell
   Copy-Item backup\config\* config\
   Copy-Item backup\certs\* certs\
   ```

4. 运行新版本连接器

## 卸载

直接删除连接器目录即可。证书和配置可以保留以备将来使用。
"@

$DeployContent | Out-File -FilePath "$OutputDir\$PackageName\DEPLOY.md" -Encoding UTF8

# 打包
Write-Host "打包发布包..." -ForegroundColor Yellow
Set-Location $OutputDir

if (Get-Command Compress-Archive -ErrorAction SilentlyContinue) {
    Compress-Archive -Path $PackageName -DestinationPath "$PackageName.zip" -Force
    Write-Host "✓ 打包完成: $OutputDir\$PackageName.zip" -ForegroundColor Green
} else {
    Write-Host "⚠ Compress-Archive 命令不可用，请手动打包 $PackageName 目录" -ForegroundColor Yellow
}

Set-Location ..

Write-Host ""
Write-Host "✅ 连接器打包完成！" -ForegroundColor Green
Write-Host ""
Write-Host "发布包位置:" -ForegroundColor Cyan
Write-Host "  $OutputDir\$PackageName.zip" -ForegroundColor White
Write-Host ""
Write-Host "发布包内容:" -ForegroundColor Cyan
Write-Host "  - 连接器可执行文件" -ForegroundColor White
Write-Host "  - 配置模板" -ForegroundColor White
Write-Host "  - 部署文档" -ForegroundColor White
Write-Host "  - README" -ForegroundColor White
Write-Host ""


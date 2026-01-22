# 统一打包脚本 (Windows PowerShell)
# 可信数据空间内核和连接器打包工具

param(
    [string]$Version = "1.0.0",
    [string]$Platform = "windows-amd64",
    [string]$Target = "all"  # kernel, connector, all
)

$ErrorActionPreference = "Stop"

$OutputDir = "dist"
$KernelDir = "$OutputDir\kernel-$Version-$Platform"
$ConnectorDir = "$OutputDir\connector-$Version-$Platform"

Write-Host "📦 开始打包可信数据空间组件 $Version for $Platform..." -ForegroundColor Cyan
Write-Host "   目标组件: $Target" -ForegroundColor White

# 清理函数
function Cleanup-Build {
    param([string]$Component, [string]$BuildDir)

    Write-Host "清理${Component}旧的构建文件..." -ForegroundColor Yellow
    if (Test-Path $BuildDir) {
        Remove-Item -Recurse -Force $BuildDir
    }
    New-Item -ItemType Directory -Path $BuildDir -Force | Out-Null
}

# 打包内核函数
function Package-Kernel {
    Write-Host "🔧 正在打包内核..." -ForegroundColor Yellow

    Cleanup-Build "内核" $KernelDir

    # 编译内核
    Write-Host "   编译内核..." -ForegroundColor Yellow
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    go build -o "$KernelDir\kernel.exe" ./kernel/cmd

    # 创建配置模板目录
    Write-Host "   创建配置模板..." -ForegroundColor Yellow
    New-Item -ItemType Directory -Path "$KernelDir\config" -Force | Out-Null
    Copy-Item "config\kernel.yaml" "$KernelDir\config\kernel-template.yaml"

    # 创建证书目录结构
    New-Item -ItemType Directory -Path "$KernelDir\certs" -Force | Out-Null
    "# 证书目录`n# 内核首次运行时会自动生成CA证书和服务器证书`n# 连接器注册时会自动获取客户端证书" | Out-File -FilePath "$KernelDir\certs\.gitkeep" -Encoding UTF8

    # 创建日志目录
    New-Item -ItemType Directory -Path "$KernelDir\logs" -Force | Out-Null
    "# 日志目录`n# 内核运行时会自动创建审计日志文件" | Out-File -FilePath "$KernelDir\logs\.gitkeep" -Encoding UTF8

    # 创建频道配置文件目录
    New-Item -ItemType Directory -Path "$KernelDir\channel_configs" -Force | Out-Null
    "# 频道配置文件目录`n# 存放频道特定的配置文件（JSON格式）" | Out-File -FilePath "$KernelDir\channel_configs\.gitkeep" -Encoding UTF8

    # 创建数据库目录
    New-Item -ItemType Directory -Path "$KernelDir\data" -Force | Out-Null
    "# 数据库目录`n# 如果使用SQLite数据库，数据文件会存储在这里" | Out-File -FilePath "$KernelDir\data\.gitkeep" -Encoding UTF8

    # 创建启动脚本
    $StartScript = @"
# 内核启动脚本 (PowerShell)

# 检查配置文件
if (!(Test-Path "config\kernel.yaml")) {
    Write-Host "错误：找不到配置文件 config\kernel.yaml" -ForegroundColor Red
    Write-Host "请复制 config\kernel-template.yaml 为 config\kernel.yaml 并修改配置" -ForegroundColor Yellow
    exit 1
}

# 检查是否为首次运行（检查服务器证书）
if (!(Test-Path "certs\kernel.crt")) {
    Write-Host "检测到首次运行，将自动生成CA证书和服务器证书..." -ForegroundColor Yellow
}

# 启动内核
Write-Host "启动可信数据空间内核..." -ForegroundColor Green
if (Test-Path ".\kernel.exe") {
    .\kernel.exe -config config\kernel.yaml
} else {
    Write-Host "错误：找不到内核可执行文件" -ForegroundColor Red
    exit 1
}
"@

    $StartScript | Out-File -FilePath "$KernelDir\start.ps1" -Encoding UTF8

    # 创建停止脚本
    $StopScript = @"
# 内核停止脚本 (PowerShell)

Write-Host "停止可信数据空间内核..." -ForegroundColor Yellow

# 查找内核进程
\$kernelProcesses = Get-Process | Where-Object { \$_.ProcessName -eq "kernel" -and \$_.CommandLine -like "*kernel.yaml*" }

if (\$kernelProcesses) {
    foreach (\$process in \$kernelProcesses) {
        Write-Host "正在停止内核进程 (PID: \$(\$process.Id))..." -ForegroundColor Yellow
        Stop-Process -Id \$process.Id -Force
    }
    Write-Host "内核已停止" -ForegroundColor Green
} else {
    Write-Host "未找到运行中的内核进程" -ForegroundColor Yellow
}
"@

    $StopScript | Out-File -FilePath "$KernelDir\stop.ps1" -Encoding UTF8

    # 创建状态检查脚本
    $StatusScript = @"
# 内核状态检查脚本 (PowerShell)

Write-Host "=== 可信数据空间内核状态 ===" -ForegroundColor Cyan

# 检查进程
\$kernelProcesses = Get-Process | Where-Object { \$_.ProcessName -eq "kernel" -and \$_.CommandLine -like "*kernel.yaml*" }
if (\$kernelProcesses) {
    Write-Host "✅ 内核运行中 (PID: \$(\$kernelProcesses[0].Id))" -ForegroundColor Green
} else {
    Write-Host "❌ 内核未运行" -ForegroundColor Red
}

# 检查端口
function Test-Port {
    param([int]`$port, [string]`$name)
    try {
        `$connection = New-Object System.Net.Sockets.TcpClient("localhost", `$port)
        `$connection.Close()
        Write-Host "✅ `$name 端口 `$port 正在监听" -ForegroundColor Green
    } catch {
        Write-Host "❌ `$name 端口 `$port 未监听" -ForegroundColor Red
    }
}

Test-Port 50051 "主服务"
Test-Port 50052 "引导服务"
Test-Port 50053 "内核间通信"

# 检查证书
if (Test-Path "certs\ca.crt") {
    Write-Host "✅ CA证书存在" -ForegroundColor Green
} else {
    Write-Host "⚠️  CA证书不存在（首次运行时自动生成）" -ForegroundColor Yellow
}

if (Test-Path "certs\kernel.crt") {
    Write-Host "✅ 服务器证书存在" -ForegroundColor Green
} else {
    Write-Host "⚠️  服务器证书不存在（首次运行时自动生成）" -ForegroundColor Yellow
}

# 检查配置文件
if (Test-Path "config\kernel.yaml") {
    Write-Host "✅ 配置文件存在" -ForegroundColor Green
} else {
    Write-Host "❌ 配置文件不存在" -ForegroundColor Red
}

Write-Host "=== 状态检查完成 ===" -ForegroundColor Cyan
"@

    $StatusScript | Out-File -FilePath "$KernelDir\status.ps1" -Encoding UTF8

    # 创建证书生成脚本
    $CertScript = @"
# 证书生成脚本 (PowerShell) - 为内核预生成证书

Write-Host "🔐 开始生成内核证书..." -ForegroundColor Cyan

# 检查openssl是否可用
if (!(Get-Command openssl -ErrorAction SilentlyContinue)) {
    Write-Host "❌ 错误：需要安装openssl" -ForegroundColor Red
    Write-Host "   请从 https://slproweb.com/products/Win32OpenSSL.html 下载并安装" -ForegroundColor Yellow
    exit 1
}

# 检查配置文件
if (!(Test-Path "config\kernel.yaml")) {
    Write-Host "错误：找不到配置文件 config\kernel.yaml" -ForegroundColor Red
    Write-Host "请先创建配置文件" -ForegroundColor Yellow
    exit 1
}

# 读取配置（简单的文本解析）
function Get-ConfigValue {
    param([string]`$key, [string]`$file)
    `$line = Get-Content `$file | Where-Object { `$_ -match "^`${key}:" }
    if (`$line) {
        (`$line -split ':', 2)[1].Trim().Trim('"')
    } else {
        ""
    }
}

`$kernelId = Get-ConfigValue "id" "config\kernel.yaml"
if ([string]::IsNullOrEmpty(`$kernelId)) {
    `$kernelId = "kernel-1"
}

`$address = Get-ConfigValue "address" "config\kernel.yaml"
if ([string]::IsNullOrEmpty(`$address)) {
    `$address = "0.0.0.0"
}

Write-Host "   内核ID: `$kernelId" -ForegroundColor White
Write-Host "   服务器地址: `$address" -ForegroundColor White

# 创建证书目录
New-Item -ItemType Directory -Path "certs" -Force | Out-Null

# 生成CA证书（如果不存在）
if (!(Test-Path "certs\ca.crt")) {
    Write-Host "   生成CA根证书..." -ForegroundColor Yellow

    # 生成CA私钥
    & openssl genrsa -out certs\ca.key 4096 2>`$null

    # 生成CA证书
    & openssl req -new -x509 -days 3650 -key certs\ca.key -sha256 -out certs\ca.crt `
        -subj "/C=CN/ST=State/L=City/O=Trusted Data Space/CN=Trusted Data Space CA" 2>`$null

    Write-Host "   ✅ CA证书生成完成" -ForegroundColor Green
} else {
    Write-Host "   ✅ CA证书已存在" -ForegroundColor Green
}

# 生成服务器私钥（如果不存在）
if (!(Test-Path "certs\kernel.key")) {
    Write-Host "   生成服务器私钥..." -ForegroundColor Yellow
    & openssl genrsa -out certs\kernel.key 2048 2>`$null
    Write-Host "   ✅ 服务器私钥生成完成" -ForegroundColor Green
} else {
    Write-Host "   ✅ 服务器私钥已存在" -ForegroundColor Green
}

# 生成服务器证书（如果不存在）
if (!(Test-Path "certs\kernel.crt")) {
    Write-Host "   生成服务器证书..." -ForegroundColor Yellow

    # 创建OpenSSL配置文件
    `$configContent = @"
[req]
distinguished_name = req_distinguished_name
req_extensions = v3_req
prompt = no

[req_distinguished_name]
C = CN
ST = State
L = City
O = Trusted Data Space
CN = `$kernelId

[v3_req]
keyUsage = keyEncipherment, dataEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = `$kernelId
DNS.2 = localhost
IP.1 = 127.0.0.1
IP.2 = `$address
"@

    `$configContent | Out-File -FilePath "certs\kernel.cnf" -Encoding ASCII

    # 生成证书请求
    & openssl req -new -key certs\kernel.key -out certs\kernel.csr -config certs\kernel.cnf 2>`$null

    # 使用CA签发证书
    & openssl x509 -req -in certs\kernel.csr -CA certs\ca.crt -CAkey certs\ca.key `
        -CAcreateserial -out certs\kernel.crt -days 365 -sha256 `
        -extensions v3_req -extfile certs\kernel.cnf 2>`$null

    # 清理临时文件
    Remove-Item "certs\kernel.cnf", "certs\kernel.csr" -ErrorAction SilentlyContinue
    if (Test-Path "certs\ca.srl") { Remove-Item "certs\ca.srl" }

    Write-Host "   ✅ 服务器证书生成完成" -ForegroundColor Green
} else {
    Write-Host "   ✅ 服务器证书已存在" -ForegroundColor Green
}

Write-Host ""
Write-Host "🎉 证书生成完成！" -ForegroundColor Green
Write-Host "   CA证书: certs\ca.crt" -ForegroundColor White
Write-Host "   CA私钥: certs\ca.key" -ForegroundColor White
Write-Host "   服务器证书: certs\kernel.crt" -ForegroundColor White
Write-Host "   服务器私钥: certs\kernel.key" -ForegroundColor White
Write-Host ""
Write-Host "现在可以启动内核了：" -ForegroundColor Cyan
Write-Host "   .\start.ps1" -ForegroundColor White
"@

    $CertScript | Out-File -FilePath "$KernelDir\generate_certs.ps1" -Encoding UTF8

    Write-Host "✅ 内核打包完成 -> $KernelDir" -ForegroundColor Green
}

# 打包连接器函数
function Package-Connector {
    Write-Host "🔧 正在打包连接器..." -ForegroundColor Yellow

    Cleanup-Build "连接器" $ConnectorDir

    # 编译连接器
    Write-Host "   编译连接器..." -ForegroundColor Yellow
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    go build -o "$ConnectorDir\connector.exe" ./connector/cmd

    # 创建配置模板目录
    Write-Host "   创建配置模板..." -ForegroundColor Yellow
    New-Item -ItemType Directory -Path "$ConnectorDir\config" -Force | Out-Null
    Copy-Item "config\connector.yaml" "$ConnectorDir\config\connector-template.yaml"

    # 创建证书目录结构（空目录，证书通过首次注册获取）
    New-Item -ItemType Directory -Path "$ConnectorDir\certs" -Force | Out-Null
    "# 证书目录`n# 首次运行连接器时会自动注册并获取证书" | Out-File -FilePath "$ConnectorDir\certs\.gitkeep" -Encoding UTF8

    # 创建接收文件目录
    New-Item -ItemType Directory -Path "$ConnectorDir\received" -Force | Out-Null

    # 创建存证目录结构和初始文件
    New-Item -ItemType Directory -Path "$ConnectorDir\evidence" -Force | Out-Null
    "# 存证数据目录`n# 连接器运行时会自动创建和更新 evidence.log 文件" | Out-File -FilePath "$ConnectorDir\evidence\.gitkeep" -Encoding UTF8
    # 创建空的evidence.log文件
    New-Item -ItemType File -Path "$ConnectorDir\evidence\evidence.log" -Force | Out-Null

    Write-Host "✅ 连接器打包完成 -> $ConnectorDir" -ForegroundColor Green
}

# 生成统一的README
function Generate-Readme {
    param([string]$TargetDir, [string]$ComponentName)

    $ReadmeContent = @"
# 可信数据空间${ComponentName}独立部署包

## 快速开始

### 1. 配置${ComponentName}

编辑 `config/*-template.yaml`，修改以下配置：

```yaml
# 基本配置
id: "your-${ComponentName,,}-id"        # 修改为你的${ComponentName}ID
address: "192.168.1.100"       # 修改为服务器地址
port: 50051                      # 服务器端口

# 安全配置
ca_cert_path: "certs\ca.crt"
client_cert_path: "certs\${ComponentName,,}-X.crt"
client_key_path: "certs\${ComponentName,,}-X.key"
server_name: "trusted-data-space-kernel"
```

将模板文件复制为配置文件：

```powershell
Copy-Item config\*-template.yaml config\*.yaml
# 然后编辑配置文件
```

### 2. 首次运行（自动注册）

首次运行时会自动连接到内核并注册获取证书：

```powershell
# Windows
.\${ComponentName,,}.exe -config config\*.yaml
```

### 3. 后续运行

证书获取后，后续运行直接使用已保存的证书：

```powershell
.\${ComponentName,,}.exe -config config\*.yaml
```

## 目录结构

```
${ComponentName,,}-{version}-{platform}\
├── ${ComponentName,,}.exe          # 可执行文件（Windows）
├── config\
│   └── *-template.yaml    # 配置模板
"@

if ($ComponentName -eq "内核") {
    $ReadmeContent += @"
├── certs\              # 证书目录（可预生成或首次运行自动生成）
├── logs\               # 日志目录
├── channel_configs\    # 频道配置文件目录
├── data\               # 数据库目录（SQLite时使用）
├── generate_certs.ps1  # 证书生成脚本（可选）
├── start.ps1           # 启动脚本
├── stop.ps1            # 停止脚本
├── status.ps1          # 状态检查脚本
"@
} else {
    $ReadmeContent += @"
├── certs\              # 证书目录（首次运行后自动生成）
├── received\           # 接收文件目录
├── evidence\           # 存证数据目录
"@
}

$ReadmeContent += @"
└── README.md           # 本文件
```

## 证书管理
"@

if ($ComponentName -eq "内核") {
    $ReadmeContent += @"

### 自动证书生成（推荐）

内核支持首次运行时自动生成证书，无需手动操作。

### 手动证书生成（可选）

如果需要在部署前预生成证书：

```powershell
# 1. 配置内核
Copy-Item config\kernel-template.yaml config\kernel.yaml
# 编辑 config\kernel.yaml

# 2. 生成证书
.\generate_certs.ps1

# 3. 启动内核
.\start.ps1
```

证书生成脚本会：
- 生成CA根证书和私钥
- 生成服务器证书和私钥
- 基于配置文件中的内核ID和地址配置证书
"@
}

## 技术支持

如遇到问题，请查看相关日志文件或项目文档。
"@

    $ReadmeContent | Out-File -FilePath "$TargetDir\README.md" -Encoding UTF8
}

# 主逻辑
switch ($Target) {
    "kernel" {
        Package-Kernel
        Generate-Readme $KernelDir "内核"
    }
    "connector" {
        Package-Connector
        Generate-Readme $ConnectorDir "连接器"
    }
    "all" {
        Package-Kernel
        Package-Connector
        Generate-Readme $KernelDir "内核"
        Generate-Readme $ConnectorDir "连接器"
    }
    default {
        Write-Host "❌ 无效的目标: $Target" -ForegroundColor Red
        Write-Host "   使用方法: .\package_all.ps1 -Version <version> -Platform <platform> -Target <kernel|connector|all>" -ForegroundColor Yellow
        Write-Host "   示例: .\package_all.ps1 -Version 1.0.0 -Platform windows-amd64 -Target all" -ForegroundColor Yellow
        exit 1
    }
}

# 创建ZIP包
Write-Host "📦 创建压缩包..." -ForegroundColor Yellow

if ($Target -eq "kernel" -or $Target -eq "all") {
    Compress-Archive -Path "$KernelDir\*" -DestinationPath "$OutputDir\kernel-$Version-$Platform.zip" -Force
    Write-Host "   ✅ 内核压缩包: $OutputDir\kernel-$Version-$Platform.zip" -ForegroundColor Green
}

if ($Target -eq "connector" -or $Target -eq "all") {
    Compress-Archive -Path "$ConnectorDir\*" -DestinationPath "$OutputDir\connector-$Version-$Platform.zip" -Force
    Write-Host "   ✅ 连接器压缩包: $OutputDir\connector-$Version-$Platform.zip" -ForegroundColor Green
}

Write-Host ""
Write-Host "🎉 打包完成！" -ForegroundColor Green
Write-Host ""
Write-Host "📋 部署说明：" -ForegroundColor Cyan

if ($Target -eq "kernel" -or $Target -eq "all") {
    Write-Host "   内核部署：" -ForegroundColor White
    Write-Host "   1. Expand-Archive kernel-$Version-$Platform.zip ." -ForegroundColor White
    Write-Host "   2. cd kernel-$Version-$Platform" -ForegroundColor White
    Write-Host "   3. Copy-Item config\kernel-template.yaml config\kernel.yaml" -ForegroundColor White
    Write-Host "   4. # 编辑 config\kernel.yaml" -ForegroundColor White
    Write-Host "   5. (可选) .\generate_certs.ps1  # 预生成证书" -ForegroundColor White
    Write-Host "   6. .\start.ps1" -ForegroundColor White
}

if ($Target -eq "connector" -or $Target -eq "all") {
    Write-Host ""
    Write-Host "   连接器部署：" -ForegroundColor White
    Write-Host "   1. Expand-Archive connector-$Version-$Platform.zip ." -ForegroundColor White
    Write-Host "   2. cd connector-$Version-$Platform" -ForegroundColor White
    Write-Host "   3. Copy-Item config\connector-template.yaml config\connector.yaml" -ForegroundColor White
    Write-Host "   4. # 编辑 config\connector.yaml" -ForegroundColor White
    Write-Host "   5. .\connector.exe -config config\connector.yaml" -ForegroundColor White
}

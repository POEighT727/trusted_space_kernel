# PowerShell 证书生成脚本（Windows）
# 用于为可信数据空间内核和连接器生成测试证书

$ErrorActionPreference = "Stop"

$CERTS_DIR = "certs"
$VALIDITY_DAYS = 365

Write-Host "🔐 Generating certificates for Trusted Data Space..." -ForegroundColor Green

# 创建证书目录
if (-not (Test-Path $CERTS_DIR)) {
    New-Item -ItemType Directory -Path $CERTS_DIR | Out-Null
}

# 检查 OpenSSL 是否可用
try {
    $null = Get-Command openssl -ErrorAction Stop
} catch {
    Write-Host "[FAILED] OpenSSL not found. Please install OpenSSL first." -ForegroundColor Red
    Write-Host "   Download from: https://slproweb.com/products/Win32OpenSSL.html" -ForegroundColor Yellow
    exit 1
}

# 1. 生成 CA 根证书
Write-Host "Step 1: Generating CA root certificate..."
openssl genrsa -out "$CERTS_DIR/ca.key" 4096

openssl req -new -x509 -days ($VALIDITY_DAYS * 10) -key "$CERTS_DIR/ca.key" `
  -out "$CERTS_DIR/ca.crt" `
  -subj "/C=CN/ST=Beijing/L=Beijing/O=Trusted Data Space/CN=Trusted Data Space Internal CA"

Write-Host "✓ CA certificate created" -ForegroundColor Green

# 2. 生成内核服务端证书
Write-Host "Step 2: Generating kernel server certificate..."

# 自动检测本地IP地址
$localIPs = @()
try {
    $netAdapters = Get-NetIPAddress | Where-Object {
        $_.AddressFamily -eq "IPv4" -and
        $_.IPAddress -ne "127.0.0.1" -and
        $_.PrefixOrigin -ne "WellKnown"
    }
    foreach ($adapter in $netAdapters) {
        if ($adapter.IPAddress -notmatch "^169\.254\.") {  # 排除APIPA地址
            $localIPs += $adapter.IPAddress
        }
    }
} catch {
    Write-Host "⚠️  Warning: Could not automatically detect local IP addresses. Using localhost only." -ForegroundColor Yellow
}

# 构建Subject Alternative Name
$sanEntries = @("DNS:trusted-data-space-kernel", "DNS:localhost", "IP:127.0.0.1")
foreach ($ip in $localIPs) {
    $sanEntries += "IP:$ip"
}
$sanString = $sanEntries -join ","

Write-Host "📋 Using Subject Alternative Names: $sanString" -ForegroundColor Cyan

openssl genrsa -out "$CERTS_DIR/kernel.key" 2048

openssl req -new -key "$CERTS_DIR/kernel.key" `
  -out "$CERTS_DIR/kernel.csr" `
  -subj "/C=CN/ST=Beijing/L=Beijing/O=Trusted Data Space/CN=trusted-data-space-kernel"

# 创建扩展配置
@"
subjectAltName = $sanString
extendedKeyUsage = serverAuth,clientAuth
"@ | Out-File -FilePath "$CERTS_DIR/kernel.ext" -Encoding ASCII

openssl x509 -req -days $VALIDITY_DAYS `
  -in "$CERTS_DIR/kernel.csr" `
  -CA "$CERTS_DIR/ca.crt" `
  -CAkey "$CERTS_DIR/ca.key" `
  -CAcreateserial `
  -out "$CERTS_DIR/kernel.crt" `
  -extfile "$CERTS_DIR/kernel.ext"

Remove-Item "$CERTS_DIR/kernel.csr", "$CERTS_DIR/kernel.ext"
Write-Host "✓ Kernel server certificate created" -ForegroundColor Green

# 清理临时文件
if (Test-Path "$CERTS_DIR/ca.srl") {
    Remove-Item "$CERTS_DIR/ca.srl"
}

Write-Host ""
Write-Host "[OK] All certificates generated successfully!" -ForegroundColor Green
Write-Host ""
Write-Host "📁 Certificate files:" -ForegroundColor Cyan
Write-Host "   CA:          $CERTS_DIR/ca.crt"
Write-Host "   Kernel:      $CERTS_DIR/kernel.crt, $CERTS_DIR/kernel.key"
Write-Host ""
Write-Host "⚠️  These are TEST certificates. Do NOT use in production!" -ForegroundColor Yellow


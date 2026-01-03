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
    Write-Host "❌ OpenSSL not found. Please install OpenSSL first." -ForegroundColor Red
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
openssl genrsa -out "$CERTS_DIR/kernel.key" 2048

openssl req -new -key "$CERTS_DIR/kernel.key" `
  -out "$CERTS_DIR/kernel.csr" `
  -subj "/C=CN/ST=Beijing/L=Beijing/O=Trusted Data Space/CN=trusted-data-space-kernel"

# 创建扩展配置
@"
subjectAltName = DNS:trusted-data-space-kernel,DNS:localhost,IP:127.0.0.1
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

# 3. 生成连接器证书
function Generate-ConnectorCert {
    param (
        [string]$ConnectorID,
        [int]$Step
    )
    
    Write-Host "Step 3.$Step`: Generating certificate for $ConnectorID..."
    
    openssl genrsa -out "$CERTS_DIR/$ConnectorID.key" 2048
    
    openssl req -new -key "$CERTS_DIR/$ConnectorID.key" `
      -out "$CERTS_DIR/$ConnectorID.csr" `
      -subj "/C=CN/ST=Beijing/L=Beijing/O=Trusted Data Space/CN=$ConnectorID"
    
    @"
extendedKeyUsage = clientAuth,serverAuth
"@ | Out-File -FilePath "$CERTS_DIR/$ConnectorID.ext" -Encoding ASCII
    
    openssl x509 -req -days $VALIDITY_DAYS `
      -in "$CERTS_DIR/$ConnectorID.csr" `
      -CA "$CERTS_DIR/ca.crt" `
      -CAkey "$CERTS_DIR/ca.key" `
      -CAcreateserial `
      -out "$CERTS_DIR/$ConnectorID.crt" `
      -extfile "$CERTS_DIR/$ConnectorID.ext"
    
    Remove-Item "$CERTS_DIR/$ConnectorID.csr", "$CERTS_DIR/$ConnectorID.ext"
    Write-Host "✓ Certificate for $ConnectorID created" -ForegroundColor Green
}

# 为连接器 A、B、C 生成证书
Generate-ConnectorCert -ConnectorID "connector-A" -Step 1
Generate-ConnectorCert -ConnectorID "connector-B" -Step 2
Generate-ConnectorCert -ConnectorID "connector-C" -Step 3

# 清理临时文件
if (Test-Path "$CERTS_DIR/ca.srl") {
    Remove-Item "$CERTS_DIR/ca.srl"
}

Write-Host ""
Write-Host "✅ All certificates generated successfully!" -ForegroundColor Green
Write-Host ""
Write-Host "📁 Certificate files:" -ForegroundColor Cyan
Write-Host "   CA:          $CERTS_DIR/ca.crt"
Write-Host "   Kernel:      $CERTS_DIR/kernel.crt, $CERTS_DIR/kernel.key"
Write-Host "   Connector-A: $CERTS_DIR/connector-A.crt, $CERTS_DIR/connector-A.key"
Write-Host "   Connector-B: $CERTS_DIR/connector-B.crt, $CERTS_DIR/connector-B.key"
Write-Host "   Connector-C: $CERTS_DIR/connector-C.crt, $CERTS_DIR/connector-C.key"
Write-Host ""
Write-Host "⚠️  These are TEST certificates. Do NOT use in production!" -ForegroundColor Yellow


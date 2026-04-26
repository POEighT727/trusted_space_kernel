# PowerShell 证书生成脚本（Windows）
# 用于为可信数据空间内核和连接器生成测试证书

$ErrorActionPreference = "Stop"

$CERTS_DIR = "certs"
$VALIDITY_DAYS = 365

Write-Host "Generating certificates for Trusted Data Space..." -ForegroundColor Green

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

# 1. 生成外部根 CA（自签名）
Write-Host "Step 1: Generating external root CA (self-signed)..." -ForegroundColor Cyan
openssl genrsa -out "$CERTS_DIR/root_ca.key" 4096

openssl req -new -x509 -days ($VALIDITY_DAYS * 20) -key "$CERTS_DIR/root_ca.key" `
  -out "$CERTS_DIR/root_ca.crt" `
  -subj "/C=CN/ST=Beijing/L=Beijing/O=Trusted Data Space/CN=Trusted Data Space Root CA"

Write-Host "  [OK] Root CA created: $CERTS_DIR/root_ca.crt" -ForegroundColor Green

# 2. 生成内核中间 CA（由根 CA 签发）
Write-Host "Step 2: Generating kernel intermediate CA (signed by root CA)..." -ForegroundColor Cyan
openssl genrsa -out "$CERTS_DIR/ca.key" 4096

openssl req -new -key "$CERTS_DIR/ca.key" `
  -out "$CERTS_DIR/ca.csr" `
  -subj "/C=CN/ST=Beijing/L=Beijing/O=Trusted Data Space/CN=Trusted Data Space Kernel CA"

# 创建中间 CA 扩展配置
@"
basicConstraints = CA:TRUE, pathlen:1
keyUsage = cRLSign, keyCertSign, digitalSignature
extendedKeyUsage = serverAuth, clientAuth
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid:always
"@ | Out-File -FilePath "$CERTS_DIR/ca.ext" -Encoding ASCII

openssl x509 -req -days ($VALIDITY_DAYS * 10) `
  -in "$CERTS_DIR/ca.csr" `
  -CA "$CERTS_DIR/root_ca.crt" `
  -CAkey "$CERTS_DIR/root_ca.key" `
  -CAcreateserial `
  -out "$CERTS_DIR/ca.crt" `
  -extfile "$CERTS_DIR/ca.ext"

Remove-Item "$CERTS_DIR/ca.csr", "$CERTS_DIR/ca.ext"
Write-Host "  [OK] Kernel intermediate CA created: $CERTS_DIR/ca.crt" -ForegroundColor Green

# 清理根 CA 的 serial 文件
if (Test-Path "$CERTS_DIR/root_ca.srl") {
    Remove-Item "$CERTS_DIR/root_ca.srl"
}

# 3. 生成内核服务端证书（由内核中间 CA 签发）
Write-Host "Step 3: Generating kernel server certificate..." -ForegroundColor Cyan

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
    Write-Host "  [WARN] Could not auto-detect local IPs, using localhost only." -ForegroundColor Yellow
}

# 构建Subject Alternative Name
$sanEntries = @("DNS:trusted-data-space-kernel", "DNS:localhost", "IP:127.0.0.1")
foreach ($ip in $localIPs) {
    $sanEntries += "IP:$ip"
}
$sanString = $sanEntries -join ","

Write-Host "  SAN: $sanString" -ForegroundColor Cyan

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
Write-Host "  [OK] Kernel server certificate created" -ForegroundColor Green

# 清理中间 CA 的 serial 文件
if (Test-Path "$CERTS_DIR/ca.srl") {
    Remove-Item "$CERTS_DIR/ca.srl"
}

Write-Host ""
Write-Host "[OK] All certificates generated successfully!" -ForegroundColor Green
Write-Host ""
Write-Host "Certificate files:" -ForegroundColor Cyan
Write-Host "  Root CA (external):      $CERTS_DIR/root_ca.crt, $CERTS_DIR/root_ca.key"
Write-Host "  Kernel CA (intermediate): $CERTS_DIR/ca.crt, $CERTS_DIR/ca.key"
Write-Host "  Kernel server:           $CERTS_DIR/kernel.crt, $CERTS_DIR/kernel.key"
Write-Host ""
Write-Host "Verify certificate chain:" -ForegroundColor Cyan
Write-Host "  openssl verify -CAfile root_ca.crt -untrusted ca.crt kernel.crt" -ForegroundColor White
Write-Host ""
Write-Host "[WARN] These are TEST certificates. Do NOT use in production!" -ForegroundColor Yellow
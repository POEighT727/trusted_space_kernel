#!/bin/bash

# 证书生成脚本
# 用于为可信数据空间内核和连接器生成测试证书

set -e

CERTS_DIR="certs"
VALIDITY_DAYS=365

echo "Generating certificates for Trusted Data Space..."

# 创建证书目录
mkdir -p "$CERTS_DIR"

# 1. 生成外部根 CA（自签名）
echo "Step 1: Generating external root CA (self-signed)..."
openssl genrsa -out "$CERTS_DIR/root_ca.key" 4096

openssl req -new -x509 -days $((VALIDITY_DAYS * 20)) -key "$CERTS_DIR/root_ca.key" \
  -out "$CERTS_DIR/root_ca.crt" \
  -subj "/C=CN/ST=Beijing/L=Beijing/O=Trusted Data Space/CN=Trusted Data Space Root CA"

echo "  [OK] Root CA created: $CERTS_DIR/root_ca.crt"

# 2. 生成内核中间 CA（由根 CA 签发）
echo "Step 2: Generating kernel intermediate CA (signed by root CA)..."
openssl genrsa -out "$CERTS_DIR/ca.key" 4096

openssl req -new -key "$CERTS_DIR/ca.key" \
  -out "$CERTS_DIR/ca.csr" \
  -subj "/C=CN/ST=Beijing/L=Beijing/O=Trusted Data Space/CN=Trusted Data Space Kernel CA"

# 创建中间 CA 扩展配置
cat > "$CERTS_DIR/ca.ext" << EOF
basicConstraints = CA:TRUE, pathlen:1
keyUsage = cRLSign, keyCertSign, digitalSignature
extendedKeyUsage = serverAuth, clientAuth
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid:always
EOF

openssl x509 -req -days $((VALIDITY_DAYS * 10)) \
  -in "$CERTS_DIR/ca.csr" \
  -CA "$CERTS_DIR/root_ca.crt" \
  -CAkey "$CERTS_DIR/root_ca.key" \
  -CAcreateserial \
  -out "$CERTS_DIR/ca.crt" \
  -extfile "$CERTS_DIR/ca.ext"

rm "$CERTS_DIR/ca.csr" "$CERTS_DIR/ca.ext"
echo "  [OK] Kernel intermediate CA created: $CERTS_DIR/ca.crt"

# 清理根 CA 的 serial 文件
rm -f "$CERTS_DIR/root_ca.srl"

# 3. 生成内核服务端证书（由内核中间 CA 签发）
echo "Step 3: Generating kernel server certificate..."

# 自动检测本地IP地址
local_ips=""
if command -v hostname >/dev/null 2>&1; then
    hostname_ips=$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | grep -v '^127\.' | head -5)
    if [ -n "$hostname_ips" ]; then
        local_ips="$hostname_ips"
    fi
fi

if [ -z "$local_ips" ] && command -v ip >/dev/null 2>&1; then
    default_ip=$(ip route get 1 2>/dev/null | head -1 | grep -oE 'src [0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | cut -d' ' -f2)
    if [ -n "$default_ip" ] && [ "$default_ip" != "127.0.0.1" ]; then
        local_ips="$default_ip"
    fi
fi

# 构建Subject Alternative Name
san_entries="DNS:trusted-data-space-kernel,DNS:localhost,IP:127.0.0.1"
if [ -n "$local_ips" ]; then
    for ip in $local_ips; do
        if [[ $ip =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] && [ "$ip" != "127.0.0.1" ]; then
            san_entries="$san_entries,IP:$ip"
        fi
    done
fi

echo "  SAN: $san_entries"

openssl genrsa -out "$CERTS_DIR/kernel.key" 2048

openssl req -new -key "$CERTS_DIR/kernel.key" \
  -out "$CERTS_DIR/kernel.csr" \
  -subj "/C=CN/ST=Beijing/L=Beijing/O=Trusted Data Space/CN=trusted-data-space-kernel"

# 创建扩展配置
cat > "$CERTS_DIR/kernel.ext" << EOF
subjectAltName = $san_entries
extendedKeyUsage = serverAuth,clientAuth
EOF

openssl x509 -req -days $VALIDITY_DAYS \
  -in "$CERTS_DIR/kernel.csr" \
  -CA "$CERTS_DIR/ca.crt" \
  -CAkey "$CERTS_DIR/ca.key" \
  -CAcreateserial \
  -out "$CERTS_DIR/kernel.crt" \
  -extfile "$CERTS_DIR/kernel.ext"

rm "$CERTS_DIR/kernel.csr" "$CERTS_DIR/kernel.ext"

# 清理中间 CA 的 serial 文件
rm -f "$CERTS_DIR/ca.srl"

echo "  [OK] Kernel server certificate created"

echo ""
echo "[OK] All certificates generated successfully!"
echo ""
echo "Certificate files:"
echo "  Root CA (external):      $CERTS_DIR/root_ca.crt, $CERTS_DIR/root_ca.key"
echo "  Kernel CA (intermediate): $CERTS_DIR/ca.crt, $CERTS_DIR/ca.key"
echo "  Kernel server:           $CERTS_DIR/kernel.crt, $CERTS_DIR/kernel.key"
echo ""
echo "Verify certificate chain:"
echo "  openssl verify -CAfile root_ca.crt -untrusted ca.crt kernel.crt"
echo ""
echo "[WARN] These are TEST certificates. Do NOT use in production!"

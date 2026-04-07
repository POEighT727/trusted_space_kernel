#!/bin/bash

# 证书生成脚本
# 用于为可信数据空间内核和连接器生成测试证书

set -e

CERTS_DIR="certs"
VALIDITY_DAYS=365

echo "🔐 Generating certificates for Trusted Data Space..."

# 创建证书目录
mkdir -p "$CERTS_DIR"

# 1. 生成 CA 根证书
echo "Step 1: Generating CA root certificate..."
openssl genrsa -out "$CERTS_DIR/ca.key" 4096

openssl req -new -x509 -days $((VALIDITY_DAYS * 10)) -key "$CERTS_DIR/ca.key" \
  -out "$CERTS_DIR/ca.crt" \
  -subj "/C=CN/ST=Beijing/L=Beijing/O=Trusted Data Space/CN=Trusted Data Space Internal CA"

echo "✓ CA certificate created"

# 2. 生成内核服务端证书
echo "Step 2: Generating kernel server certificate..."

# 自动检测本地IP地址
local_ips=""
if command -v hostname >/dev/null 2>&1; then
    # 尝试使用 hostname -I 获取所有IP地址
    hostname_ips=$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | grep -v '^127\.' | head -5)
    if [ -n "$hostname_ips" ]; then
        local_ips="$hostname_ips"
    fi
fi

# 如果hostname失败，尝试使用ip route
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

echo "📋 Using Subject Alternative Names: $san_entries"

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
echo "✓ Kernel server certificate created"


# 清理临时文件
rm -f "$CERTS_DIR/ca.srl"

echo ""
echo "[OK] All certificates generated successfully!"
echo ""
echo "📁 Certificate files:"
echo "   CA:          $CERTS_DIR/ca.crt"
echo "   Kernel:      $CERTS_DIR/kernel.crt, $CERTS_DIR/kernel.key"
echo ""
echo "⚠️  These are TEST certificates. Do NOT use in production!"


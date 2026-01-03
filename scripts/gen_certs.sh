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
openssl genrsa -out "$CERTS_DIR/kernel.key" 2048

openssl req -new -key "$CERTS_DIR/kernel.key" \
  -out "$CERTS_DIR/kernel.csr" \
  -subj "/C=CN/ST=Beijing/L=Beijing/O=Trusted Data Space/CN=trusted-data-space-kernel"

# 创建扩展配置
cat > "$CERTS_DIR/kernel.ext" << EOF
subjectAltName = DNS:trusted-data-space-kernel,DNS:localhost,IP:127.0.0.1
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

# 3. 生成连接器证书
generate_connector_cert() {
  CONNECTOR_ID=$1
  echo "Step 3.$2: Generating certificate for $CONNECTOR_ID..."
  
  openssl genrsa -out "$CERTS_DIR/$CONNECTOR_ID.key" 2048
  
  openssl req -new -key "$CERTS_DIR/$CONNECTOR_ID.key" \
    -out "$CERTS_DIR/$CONNECTOR_ID.csr" \
    -subj "/C=CN/ST=Beijing/L=Beijing/O=Trusted Data Space/CN=$CONNECTOR_ID"
  
  cat > "$CERTS_DIR/$CONNECTOR_ID.ext" << EOF
extendedKeyUsage = clientAuth,serverAuth
EOF
  
  openssl x509 -req -days $VALIDITY_DAYS \
    -in "$CERTS_DIR/$CONNECTOR_ID.csr" \
    -CA "$CERTS_DIR/ca.crt" \
    -CAkey "$CERTS_DIR/ca.key" \
    -CAcreateserial \
    -out "$CERTS_DIR/$CONNECTOR_ID.crt" \
    -extfile "$CERTS_DIR/$CONNECTOR_ID.ext"
  
  rm "$CERTS_DIR/$CONNECTOR_ID.csr" "$CERTS_DIR/$CONNECTOR_ID.ext"
  echo "✓ Certificate for $CONNECTOR_ID created"
}

# 为连接器 A 和 B 生成证书
generate_connector_cert "connector-A" 1
generate_connector_cert "connector-B" 2
generate_connector_cert "connector-C" 3

# 清理临时文件
rm -f "$CERTS_DIR/ca.srl"

echo ""
echo "✅ All certificates generated successfully!"
echo ""
echo "📁 Certificate files:"
echo "   CA:          $CERTS_DIR/ca.crt"
echo "   Kernel:      $CERTS_DIR/kernel.crt, $CERTS_DIR/kernel.key"
echo "   Connector-A: $CERTS_DIR/connector-A.crt, $CERTS_DIR/connector-A.key"
echo "   Connector-B: $CERTS_DIR/connector-B.crt, $CERTS_DIR/connector-B.key"
echo "   Connector-C: $CERTS_DIR/connector-C.crt, $CERTS_DIR/connector-C.key"
echo ""
echo "⚠️  These are TEST certificates. Do NOT use in production!"


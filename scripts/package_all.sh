#!/bin/bash

# 统一打包脚本 - 可信数据空间内核和连接器
# 用于创建可独立部署的内核和连接器发布包

set -e

# 参数解析
VERSION=${1:-"1.0.0"}
PLATFORM=${2:-"linux-amd64"}
TARGET=${3:-"all"}  # kernel, connector, all

OUTPUT_DIR="dist"
KERNEL_DIR="${OUTPUT_DIR}/kernel-${VERSION}-${PLATFORM}"
CONNECTOR_DIR="${OUTPUT_DIR}/connector-${VERSION}-${PLATFORM}"

echo "📦 开始打包可信数据空间组件 ${VERSION} for ${PLATFORM}..."
echo "   目标组件: ${TARGET}"

# 清理函数
cleanup_build() {
    local component=$1
    local build_dir=$2

    echo "清理${component}旧的构建文件..."
    rm -rf "${build_dir}"
    mkdir -p "${build_dir}"
}

# 打包内核函数
package_kernel() {
    echo "🔧 正在打包内核..."

    cleanup_build "内核" "${KERNEL_DIR}"

    # 编译内核
    echo "   编译内核..."
    if [ "$PLATFORM" = "windows-amd64" ]; then
        GOOS=windows GOARCH=amd64 go build -o "${KERNEL_DIR}/kernel.exe" ./kernel/cmd
    else
        GOOS=linux GOARCH=amd64 go build -o "${KERNEL_DIR}/kernel" ./kernel/cmd
    fi

    # 创建配置模板目录
    echo "   创建配置模板..."
    mkdir -p "${KERNEL_DIR}/config"
    cp config/kernel.yaml "${KERNEL_DIR}/config/kernel-template.yaml"

    # 创建证书目录结构
    mkdir -p "${KERNEL_DIR}/certs"
    echo "# 证书目录" > "${KERNEL_DIR}/certs/.gitkeep"
    echo "# 内核首次运行时会自动生成CA证书和服务器证书" >> "${KERNEL_DIR}/certs/.gitkeep"
    echo "# 连接器注册时会自动获取客户端证书" >> "${KERNEL_DIR}/certs/.gitkeep"

    # 创建日志目录
    mkdir -p "${KERNEL_DIR}/logs"
    echo "# 日志目录" > "${KERNEL_DIR}/logs/.gitkeep"
    echo "# 内核运行时会自动创建审计日志文件" >> "${KERNEL_DIR}/logs/.gitkeep"

    # 创建频道配置文件目录
    mkdir -p "${KERNEL_DIR}/channel_configs"
    echo "# 频道配置文件目录" > "${KERNEL_DIR}/channel_configs/.gitkeep"
    echo "# 存放频道特定的配置文件（JSON格式）" >> "${KERNEL_DIR}/channel_configs/.gitkeep"

    # 创建数据库目录
    mkdir -p "${KERNEL_DIR}/data"
    echo "# 数据库目录" > "${KERNEL_DIR}/data/.gitkeep"
    echo "# 如果使用SQLite数据库，数据文件会存储在这里" >> "${KERNEL_DIR}/data/.gitkeep"

    # 创建启动脚本
    cat > "${KERNEL_DIR}/start.sh" << 'EOF'
#!/bin/bash
# 内核启动脚本

# 检查配置文件
if [ ! -f "config/kernel.yaml" ]; then
    echo "错误：找不到配置文件 config/kernel.yaml"
    echo "请复制 config/kernel-template.yaml 为 config/kernel.yaml 并修改配置"
    exit 1
fi

# 检查是否为首次运行（检查服务器证书）
if [ ! -f "certs/kernel.crt" ]; then
    echo "检测到首次运行，将自动生成CA证书和服务器证书..."
fi

# 启动内核
echo "启动可信数据空间内核..."
if [ -f "./kernel" ]; then
    ./kernel -config config/kernel.yaml
elif [ -f "./kernel.exe" ]; then
    ./kernel.exe -config config/kernel.yaml
else
    echo "错误：找不到内核可执行文件"
    exit 1
fi
EOF

    chmod +x "${KERNEL_DIR}/start.sh"

    # 创建停止脚本
    cat > "${KERNEL_DIR}/stop.sh" << 'EOF'
#!/bin/bash
# 内核停止脚本

echo "停止可信数据空间内核..."

# 查找内核进程
KERNEL_PID=$(pgrep -f "kernel.*-config.*kernel.yaml" | head -1)

if [ -n "$KERNEL_PID" ]; then
    echo "正在停止内核进程 (PID: $KERNEL_PID)..."
    kill $KERNEL_PID

    # 等待进程结束
    for i in {1..10}; do
        if ! kill -0 $KERNEL_PID 2>/dev/null; then
            echo "内核已停止"
            exit 0
        fi
        sleep 1
    done

    # 强制终止
    echo "强制终止内核进程..."
    kill -9 $KERNEL_PID
    echo "内核已强制停止"
else
    echo "未找到运行中的内核进程"
fi
EOF

    chmod +x "${KERNEL_DIR}/stop.sh"

    # 创建状态检查脚本
    cat > "${KERNEL_DIR}/status.sh" << 'EOF'
#!/bin/bash
# 内核状态检查脚本

echo "=== 可信数据空间内核状态 ==="

# 检查进程
KERNEL_PID=$(pgrep -f "kernel.*-config.*kernel.yaml" | head -1)
if [ -n "$KERNEL_PID" ]; then
    echo "[OK] 内核运行中 (PID: $KERNEL_PID)"
else
    echo "[FAILED] 内核未运行"
fi

# 检查端口
check_port() {
    local port=$1
    local name=$2
    if netstat -tln 2>/dev/null | grep -q ":$port "; then
        echo "[OK] $name 端口 $port 正在监听"
    else
        echo "[FAILED] $name 端口 $port 未监听"
    fi
}

check_port 50051 "主服务"
check_port 50052 "引导服务"
check_port 50053 "内核间通信"

# 检查证书
if [ -f "certs/ca.crt" ]; then
    echo "[OK] CA证书存在"
else
    echo "⚠️  CA证书不存在（首次运行时自动生成）"
fi

if [ -f "certs/kernel.crt" ]; then
    echo "[OK] 服务器证书存在"
else
    echo "⚠️  服务器证书不存在（首次运行时自动生成）"
fi

# 检查配置文件
if [ -f "config/kernel.yaml" ]; then
    echo "[OK] 配置文件存在"
else
    echo "[FAILED] 配置文件不存在"
fi

echo "=== 状态检查完成 ==="
EOF

    chmod +x "${KERNEL_DIR}/status.sh"

    # 创建证书生成脚本
    cat > "${KERNEL_DIR}/generate_certs.sh" << 'CERT_EOF'
#!/bin/bash
# 证书生成脚本 - 为内核预生成证书

set -e

echo "🔐 开始生成内核证书..."

# 检查openssl是否可用
if ! command -v openssl &> /dev/null; then
    echo "[FAILED] 错误：需要安装openssl"
    echo "   Ubuntu/Debian: sudo apt-get install openssl"
    echo "   CentOS/RHEL: sudo yum install openssl"
    echo "   macOS: brew install openssl"
    exit 1
fi

# 检查配置文件
if [ ! -f "config/kernel.yaml" ]; then
    echo "错误：找不到配置文件 config/kernel.yaml"
    echo "请先创建配置文件"
    exit 1
fi

# 读取配置（简单的YAML解析）
get_config_value() {
    local key=$1
    local file=$2
    grep "^${key}:" "$file" | sed 's/.*: *//' | tr -d '"' || echo ""
}

KERNEL_ID=$(get_config_value "id" config/kernel.yaml)
if [ -z "$KERNEL_ID" ]; then
    KERNEL_ID="kernel-1"
fi

ADDRESS=$(get_config_value "address" config/kernel.yaml)
if [ -z "$ADDRESS" ]; then
    ADDRESS="0.0.0.0"
fi

echo "   内核ID: $KERNEL_ID"
echo "   服务器地址: $ADDRESS"

# 创建证书目录
mkdir -p certs

# 生成CA证书（如果不存在）
if [ ! -f "certs/ca.crt" ]; then
    echo "   生成CA根证书..."
    openssl genrsa -out certs/ca.key 4096 2>/dev/null
    openssl req -new -x509 -days 3650 -key certs/ca.key -sha256 -out certs/ca.crt \
        -subj "/C=CN/ST=State/L=City/O=Trusted Data Space/CN=Trusted Data Space CA" 2>/dev/null
    echo "   [OK] CA证书生成完成"
else
    echo "   [OK] CA证书已存在"
fi

# 生成服务器私钥（如果不存在）
if [ ! -f "certs/kernel.key" ]; then
    echo "   生成服务器私钥..."
    openssl genrsa -out certs/kernel.key 2048 2>/dev/null
    echo "   [OK] 服务器私钥生成完成"
else
    echo "   [OK] 服务器私钥已存在"
fi

# 生成服务器证书请求
if [ ! -f "certs/kernel.crt" ]; then
    echo "   生成服务器证书..."

    # 生成OpenSSL配置文件
    cat > certs/kernel.cnf << KERNEL_EOF
[req]
distinguished_name = req_distinguished_name
req_extensions = v3_req
prompt = no

[req_distinguished_name]
C = CN
ST = State
L = City
O = Trusted Data Space
CN = $KERNEL_ID

[v3_req]
keyUsage = keyEncipherment, dataEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = $KERNEL_ID
DNS.2 = localhost
IP.1 = 127.0.0.1
IP.2 = $ADDRESS
KERNEL_EOF

    # 生成证书请求
    openssl req -new -key certs/kernel.key -out certs/kernel.csr -config certs/kernel.cnf 2>/dev/null

    # 使用CA签发证书
    openssl x509 -req -in certs/kernel.csr -CA certs/ca.crt -CAkey certs/ca.key \
        -CAcreateserial -out certs/kernel.crt -days 365 -sha256 \
        -extensions v3_req -extfile certs/kernel.cnf 2>/dev/null

    # 清理临时文件
    rm -f certs/kernel.cnf certs/kernel.csr certs/ca.srl

    echo "   [OK] 服务器证书生成完成"
else
    echo "   [OK] 服务器证书已存在"
fi

# 设置证书权限
chmod 644 certs/ca.crt certs/kernel.crt 2>/dev/null || true
chmod 600 certs/ca.key certs/kernel.key 2>/dev/null || true

echo ""
echo "🎉 证书生成完成！"
echo "   CA证书: certs/ca.crt"
echo "   CA私钥: certs/ca.key"
echo "   服务器证书: certs/kernel.crt"
echo "   服务器私钥: certs/kernel.key"
echo ""
echo "现在可以启动内核了："
echo "   ./start.sh"
CERT_EOF

    chmod +x "${KERNEL_DIR}/generate_certs.sh"

    # 设置执行权限
    chmod +x "${KERNEL_DIR}/kernel" 2>/dev/null || true
    chmod +x "${KERNEL_DIR}/kernel.exe" 2>/dev/null || true

    echo "[OK] 内核打包完成 -> ${KERNEL_DIR}"
}

# 打包连接器函数
package_connector() {
    echo "🔧 正在打包连接器..."

    cleanup_build "连接器" "${CONNECTOR_DIR}"

    # 编译连接器
    echo "   编译连接器..."
    if [ "$PLATFORM" = "windows-amd64" ]; then
        GOOS=windows GOARCH=amd64 go build -o "${CONNECTOR_DIR}/connector.exe" ./connector/cmd
    else
        GOOS=linux GOARCH=amd64 go build -o "${CONNECTOR_DIR}/connector" ./connector/cmd
    fi

    # 创建配置模板目录
    echo "   创建配置模板..."
    mkdir -p "${CONNECTOR_DIR}/config"
    cp config/connector.yaml "${CONNECTOR_DIR}/config/connector-template.yaml"

    # 创建证书目录结构（空目录，证书通过首次注册获取）
    mkdir -p "${CONNECTOR_DIR}/certs"
    echo "# 证书目录" > "${CONNECTOR_DIR}/certs/.gitkeep"
    echo "# 首次运行连接器时会自动注册并获取证书" >> "${CONNECTOR_DIR}/certs/.gitkeep"

    # 创建接收文件目录
    mkdir -p "${CONNECTOR_DIR}/received"

    # 创建存证目录结构和初始文件
    mkdir -p "${CONNECTOR_DIR}/evidence"
    echo "# 存证数据目录" > "${CONNECTOR_DIR}/evidence/.gitkeep"
    echo "# 连接器运行时会自动创建和更新 evidence.log 文件" >> "${CONNECTOR_DIR}/evidence/.gitkeep"
    # 创建空的evidence.log文件
    touch "${CONNECTOR_DIR}/evidence/evidence.log"

    echo "[OK] 连接器打包完成 -> ${CONNECTOR_DIR}"
}

# 生成统一的README
generate_readme() {
    local target_dir=$1
    local component_name=$2

    cat > "${target_dir}/README.md" << EOF
# 可信数据空间${component_name}独立部署包

## 快速开始

### 1. 配置${component_name}

编辑 'config/*-template.yaml'，修改以下配置：

```yaml
# 基本配置
id: "your-${component_name,,}-id"        # 修改为你的${component_name}ID
address: "192.168.1.100"       # 修改为服务器地址
port: 50051                      # 服务器端口

# 安全配置
ca_cert_path: "certs/ca.crt"
client_cert_path: "certs/${component_name,,}-X.crt"
client_key_path: "certs/${component_name,,}-X.key"
server_name: "trusted-data-space-kernel"
\`\`\`

将模板文件复制为配置文件：

\`\`\`bash
cp config/*-template.yaml config/*.yaml
# 然后编辑配置文件
\`\`\`

### 2. 首次运行（自动注册）

首次运行时会自动连接到内核并注册获取证书：

\`\`\`bash
# Linux/Mac
./${component_name,,} -config config/*.yaml

# Windows
${component_name,,}.exe -config config/*.yaml
\`\`\`

### 3. 后续运行

证书获取后，后续运行直接使用已保存的证书：

\`\`\`bash
./${component_name,,} -config config/*.yaml
\`\`\`

## 目录结构

\`\`\`
${component_name,,}-{version}-{platform}/
├── ${component_name,,}              # 可执行文件（Linux/Mac）
├── ${component_name,,}.exe          # 可执行文件（Windows）
├── config/
│   └── *-template.yaml    # 配置模板
├── certs/               # 证书目录（可预生成或首次运行自动生成）
EOF

if [ "$component_name" = "内核" ]; then
    cat >> "${target_dir}/README.md" << EOF
├── logs/               # 日志目录
├── channel_configs/    # 频道配置文件目录
├── data/               # 数据库目录（SQLite时使用）
├── generate_certs.sh   # 证书生成脚本（可选）
├── start.sh            # 启动脚本
├── stop.sh             # 停止脚本
├── status.sh           # 状态检查脚本
EOF
else
    cat >> "${target_dir}/README.md" << EOF
├── received/           # 接收文件目录
├── evidence/           # 存证数据目录
EOF
fi

cat >> "${target_dir}/README.md" << EOF
└── README.md           # 本文件
```

## 证书管理
EOF

if [ "$component_name" = "内核" ]; then
    cat >> "${target_dir}/README.md" << EOF

### 自动证书生成（推荐）

内核支持首次运行时自动生成证书，无需手动操作。

### 手动证书生成（可选）

如果需要在部署前预生成证书：

```bash
# 1. 配置内核
cp config/kernel-template.yaml config/kernel.yaml
# 编辑 config/kernel.yaml

# 2. 生成证书
./generate_certs.sh

# 3. 启动内核
./start.sh
```

证书生成脚本会：
- 生成CA根证书和私钥
- 生成服务器证书和私钥
- 基于配置文件中的内核ID和地址配置证书
EOF
fi

## 技术支持

如遇到问题，请查看相关日志文件或项目文档。
EOF
}

# 主逻辑
case "$TARGET" in
    "kernel")
        package_kernel
        generate_readme "${KERNEL_DIR}" "内核"
        ;;
    "connector")
        package_connector
        generate_readme "${CONNECTOR_DIR}" "连接器"
        ;;
    "all")
        package_kernel
        package_connector
        generate_readme "${KERNEL_DIR}" "内核"
        generate_readme "${CONNECTOR_DIR}" "连接器"
        ;;
    *)
        echo "[FAILED] 无效的目标: $TARGET"
        echo "   使用方法: $0 <version> <platform> <kernel|connector|all>"
        echo "   示例: $0 1.0.0 linux-amd64 all"
        exit 1
        ;;
esac

# 创建压缩包
echo "📦 创建压缩包..."

if [ "$TARGET" = "kernel" ] || [ "$TARGET" = "all" ]; then
    cd "${OUTPUT_DIR}"
    tar -czf "kernel-${VERSION}-${PLATFORM}.tar.gz" "kernel-${VERSION}-${PLATFORM}"
    cd ..
    echo "   [OK] 内核压缩包: ${OUTPUT_DIR}/kernel-${VERSION}-${PLATFORM}.tar.gz"
fi

if [ "$TARGET" = "connector" ] || [ "$TARGET" = "all" ]; then
    cd "${OUTPUT_DIR}"
    tar -czf "connector-${VERSION}-${PLATFORM}.tar.gz" "connector-${VERSION}-${PLATFORM}"
    cd ..
    echo "   [OK] 连接器压缩包: ${OUTPUT_DIR}/connector-${VERSION}-${PLATFORM}.tar.gz"
fi

echo ""
echo "🎉 打包完成！"
echo ""
echo "📋 部署说明："
if [ "$TARGET" = "kernel" ] || [ "$TARGET" = "all" ]; then
    echo "   内核部署："
    echo "   1. tar -xzf kernel-${VERSION}-${PLATFORM}.tar.gz"
    echo "   2. cd kernel-${VERSION}-${PLATFORM}"
    echo "   3. cp config/kernel-template.yaml config/kernel.yaml"
    echo "   4. 编辑 config/kernel.yaml"
    echo "   5. (可选) ./generate_certs.sh  # 预生成证书"
    echo "   6. ./start.sh"
fi

if [ "$TARGET" = "connector" ] || [ "$TARGET" = "all" ]; then
    echo ""
    echo "   连接器部署："
    echo "   1. tar -xzf connector-${VERSION}-${PLATFORM}.tar.gz"
    echo "   2. cd connector-${VERSION}-${PLATFORM}"
    echo "   3. cp config/connector-template.yaml config/connector.yaml"
    echo "   4. 编辑 config/connector.yaml"
    echo "   5. ./connector -config config/connector.yaml"
fi

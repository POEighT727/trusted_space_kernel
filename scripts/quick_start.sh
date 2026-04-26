#!/bin/bash

# 快速启动脚本 - 一键启动完整的演示环境

set -e

echo "🚀 Trusted Data Space Kernel - Quick Start"
echo "=========================================="
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 检查依赖
check_dependencies() {
    echo "Checking dependencies..."
    
    if ! command -v go &> /dev/null; then
        echo -e "${RED}✗ Go is not installed${NC}"
        exit 1
    fi
    echo -e "${GREEN}✓ Go found${NC}"
    
    if ! command -v protoc &> /dev/null; then
        echo -e "${YELLOW}⚠ protoc not found, attempting to continue...${NC}"
    else
        echo -e "${GREEN}✓ protoc found${NC}"
    fi
    
    if ! command -v openssl &> /dev/null; then
        echo -e "${RED}✗ OpenSSL is not installed${NC}"
        exit 1
    fi
    echo -e "${GREEN}✓ OpenSSL found${NC}"
    
    echo ""
}

# 安装依赖
install_deps() {
    echo "Installing Go dependencies..."
    go mod download
    echo -e "${GREEN}✓ Dependencies installed${NC}"
    echo ""
}

# 生成 Protobuf 代码
generate_proto() {
    if [ -d "api/v1" ]; then
        echo "Protobuf code already generated, skipping..."
    else
        echo "Generating Protobuf code..."
        mkdir -p api/v1
        protoc --go_out=. --go_opt=paths=source_relative \
            --go-grpc_out=. --go-grpc_opt=paths=source_relative \
            proto/kernel/v1/*.proto 2>/dev/null || {
            echo -e "${YELLOW}⚠ Protobuf generation skipped (protoc not available)${NC}"
            echo "  Please run 'make proto' after installing protoc"
        }
        echo -e "${GREEN}✓ Protobuf code generated${NC}"
    fi
    echo ""
}

# 生成证书
generate_certs() {
    if [ -f "certs/root_ca.crt" ]; then
        echo "Certificates already exist, skipping..."
    else
        echo "Generating test certificates..."
        chmod +x scripts/gen_certs.sh
        ./scripts/gen_certs.sh
    fi
    echo ""
}

# 创建目录
create_dirs() {
    echo "Creating directories..."
    mkdir -p logs
    mkdir -p bin
    echo -e "${GREEN}✓ Directories created${NC}"
    echo ""
}

# 编译
build() {
    echo "Building kernel and connector..."
    go build -o bin/kernel ./kernel/cmd
    go build -o bin/connector ./connector/cmd
    echo -e "${GREEN}✓ Build complete${NC}"
    echo ""
}

# 启动演示
start_demo() {
    echo "=========================================="
    echo "Starting demonstration..."
    echo "=========================================="
    echo ""
    echo "This will start:"
    echo "  1. Kernel server"
    echo "  2. Sender connector (connector-A)"
    echo "  3. Receiver connector (connector-B)"
    echo ""
    echo -e "${YELLOW}Press Ctrl+C to stop all services${NC}"
    echo ""
    sleep 2
    
    # 启动内核（后台）
    echo "Starting kernel..."
    ./bin/kernel -config config/kernel.yaml > logs/kernel.log 2>&1 &
    KERNEL_PID=$!
    echo -e "${GREEN}✓ Kernel started (PID: $KERNEL_PID)${NC}"
    sleep 2
    
    # 创建临时脚本用于演示
    cat > /tmp/demo_sender.sh << 'EOF'
#!/bin/bash
sleep 3
echo ""
echo "================================================"
echo "Sender (Connector-A) - Starting data transfer"
echo "================================================"
./bin/connector -config config/connector.yaml -mode sender -receiver connector-B
EOF
    chmod +x /tmp/demo_sender.sh
    
    cat > /tmp/demo_receiver.sh << 'EOF'
#!/bin/bash
sleep 1
echo ""
echo "================================================"
echo "Receiver (Connector-B) - Waiting for data"
echo "================================================"
# 从发送方日志中提取 channel ID（简化演示，实际应通过其他方式通知）
echo "Note: In production, channel ID should be communicated through proper channels"
echo "For this demo, receiver will attempt to connect after sender creates channel"
sleep 5
# 这里简化处理，实际需要从某处获取 channel ID
echo "Receiver ready (use the channel ID from sender)"
EOF
    chmod +x /tmp/demo_receiver.sh
    
    # 启动发送方
    /tmp/demo_sender.sh &
    SENDER_PID=$!
    
    # 等待完成
    wait $SENDER_PID
    
    echo ""
    echo "=========================================="
    echo "Demo completed!"
    echo "=========================================="
    echo ""
    echo "Check logs:"
    echo "  Kernel:  logs/kernel.log"
    echo "  Audit:   logs/audit.log"
    echo ""
    
    # 停止内核
    echo "Stopping kernel..."
    kill $KERNEL_PID 2>/dev/null || true
    sleep 1
    
    # 清理
    rm -f /tmp/demo_sender.sh /tmp/demo_receiver.sh
    
    echo -e "${GREEN}✓ All services stopped${NC}"
}

# 主流程
main() {
    check_dependencies
    install_deps
    generate_proto
    generate_certs
    create_dirs
    build
    
    echo ""
    echo "=========================================="
    echo "Setup complete! 🎉"
    echo "=========================================="
    echo ""
    echo "You can now:"
    echo ""
    echo "1. Start kernel manually:"
    echo "   ./bin/kernel -config config/kernel.yaml"
    echo ""
    echo "2. Start connector as sender:"
    echo "   ./bin/connector -config config/connector.yaml -mode sender -receiver connector-B"
    echo ""
    echo "3. Start connector as receiver:"
    echo "   ./bin/connector -config config/connector-B.yaml -mode receiver -channel <channel-id>"
    echo ""
    echo "Or run automated demo:"
    read -p "Run automated demo now? (y/n) " -n 1 -r
    echo ""
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        start_demo
    fi
}

# 运行
main


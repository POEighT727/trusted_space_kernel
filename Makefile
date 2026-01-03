.PHONY: proto certs kernel connector clean

# 生成 protobuf 代码
proto:
	@echo "Generating protobuf code..."
	@mkdir -p api/v1
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/kernel/v1/*.proto

# 生成测试证书
certs:
	@echo "Generating certificates..."
	@bash scripts/gen_certs.sh

# 编译内核服务
kernel:
	@echo "Building kernel service..."
	@go build -o bin/kernel ./kernel/cmd

# 编译连接器
connector:
	@echo "Building connector..."
	@go build -o bin/connector ./connector/cmd

# 运行内核
run-kernel: kernel
	@echo "Starting kernel service..."
	@./bin/kernel -config config/kernel.yaml

# 运行连接器
run-connector: connector
	@echo "Starting connector..."
	@./bin/connector -config config/connector.yaml

# 清理
clean:
	@echo "Cleaning..."
	@rm -rf bin/ certs/ api/
	@go clean

# 安装依赖
deps:
	@echo "Installing dependencies..."
	@go mod download
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	@go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# 运行测试
test:
	@echo "Running tests..."
	@go test -v ./...

# 打包连接器（Linux/Mac）
package-connector-linux:
	@bash scripts/package_connector.sh $(VERSION) linux-amd64

# 打包连接器（Windows）
package-connector-windows:
	@powershell -ExecutionPolicy Bypass -File scripts/package_connector.ps1 -Version $(VERSION) -Platform windows-amd64

# 打包连接器（所有平台）
package-connector: package-connector-linux package-connector-windows
	@echo "✓ 所有平台打包完成"

all: deps proto certs kernel connector


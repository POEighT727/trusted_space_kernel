.PHONY: proto certs kernel connector clean

# 生成 protobuf 代码（输出到 proto/kernel/v1/，与 .proto 文件同目录）
proto:
	@echo "Generating protobuf code..."
	@cd proto/kernel/v1 && protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		*.proto

# 生成测试证书
certs:
	@echo "Generating certificates..."
	@bash scripts/gen_certs.sh

# 编译内核服务
kernel:
	@echo "Building kernel service..."
	@go build -o bin/kernel.exe ./kernel/cmd

# 编译连接器
connector:
	@echo "Building connector..."
	@go build -o bin/connector.exe ./connector/cmd

# 清理
clean:
	@echo "Cleaning..."
	@rm -rf bin/ certs/ 
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
	@echo "✓ 所有平台连接器打包完成"

# 打包内核（Linux/Mac）
package-kernel-linux:
	@bash scripts/package_kernel.sh $(VERSION) linux-amd64

# 打包内核（Windows）
package-kernel-windows:
	@powershell -ExecutionPolicy Bypass -File scripts/package_kernel.ps1 -Version $(VERSION) -Platform windows-amd64

# 打包内核（所有平台）
package-kernel: package-kernel-linux package-kernel-windows
	@echo "✓ 所有平台内核打包完成"

# 打包所有组件（内核+连接器，所有平台）
package-all: package-all-linux package-all-windows
	@echo "✓ 所有组件和平台打包完成"

# 打包所有组件（Linux/Mac）
package-all-linux:
	@bash scripts/package_all.sh $(VERSION) linux-amd64 all

# 打包所有组件（Windows）
package-all-windows:
	@powershell -ExecutionPolicy Bypass -File scripts/package_all.ps1 -Version $(VERSION) -Platform windows-amd64 -Target all

all: deps proto certs kernel connector


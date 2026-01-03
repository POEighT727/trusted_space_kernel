#!/bin/bash

# 连接器独立打包脚本
# 用于创建可独立部署的连接器发布包

set -e

VERSION=${1:-"1.0.0"}
PLATFORM=${2:-"linux-amd64"}
OUTPUT_DIR="dist"
PACKAGE_NAME="connector-${VERSION}-${PLATFORM}"

echo "📦 开始打包连接器 ${VERSION} for ${PLATFORM}..."

# 清理旧的构建
echo "清理旧的构建文件..."
rm -rf "${OUTPUT_DIR}"
mkdir -p "${OUTPUT_DIR}/${PACKAGE_NAME}"

# 编译连接器
echo "编译连接器..."
if [ "$PLATFORM" = "windows-amd64" ]; then
    GOOS=windows GOARCH=amd64 go build -o "${OUTPUT_DIR}/${PACKAGE_NAME}/connector.exe" ./connector/cmd
else
    GOOS=linux GOARCH=amd64 go build -o "${OUTPUT_DIR}/${PACKAGE_NAME}/connector" ./connector/cmd
fi

# 创建配置模板目录
echo "创建配置模板..."
mkdir -p "${OUTPUT_DIR}/${PACKAGE_NAME}/config"
cp config/connector.yaml "${OUTPUT_DIR}/${PACKAGE_NAME}/config/connector-template.yaml"

# 创建证书目录结构（空目录，证书通过首次注册获取）
mkdir -p "${OUTPUT_DIR}/${PACKAGE_NAME}/certs"
echo "# 证书目录" > "${OUTPUT_DIR}/${PACKAGE_NAME}/certs/.gitkeep"
echo "# 首次运行连接器时会自动注册并获取证书" >> "${OUTPUT_DIR}/${PACKAGE_NAME}/certs/.gitkeep"

# 创建接收文件目录
mkdir -p "${OUTPUT_DIR}/${PACKAGE_NAME}/received"

# 创建 README
cat > "${OUTPUT_DIR}/${PACKAGE_NAME}/README.md" << 'EOF'
# 连接器独立部署包

## 快速开始

### 1. 配置连接器

编辑 `config/connector-template.yaml`，修改以下配置：

```yaml
connector:
  id: "your-connector-id"        # 修改为你的连接器ID
  entity_type: "data_source"     # 修改为你的实体类型
  public_key: "your-public-key" # 修改为你的公钥

kernel:
  address: "192.168.1.100"       # 修改为内核服务器地址
  port: 50051

security:
  ca_cert_path: "certs/ca.crt"
  client_cert_path: "certs/connector-X.crt"
  client_key_path: "certs/connector-X.key"
  server_name: "trusted-data-space-kernel"
```

将 `config/connector-template.yaml` 复制为 `config/connector.yaml`：

```bash
cp config/connector-template.yaml config/connector.yaml
# 然后编辑 config/connector.yaml
```

### 2. 首次运行（自动注册）

首次运行时会自动连接到内核并注册获取证书：

```bash
# Linux/Mac
./connector -config config/connector.yaml

# Windows
connector.exe -config config/connector.yaml
```

首次运行成功后，证书会自动保存到 `certs/` 目录。

### 3. 后续运行

证书获取后，后续运行直接使用已保存的证书：

```bash
./connector -config config/connector.yaml
```

## 目录结构

```
connector-{version}/
├── connector          # 连接器可执行文件（Linux/Mac）
├── connector.exe      # 连接器可执行文件（Windows）
├── config/
│   └── connector-template.yaml  # 配置模板
├── certs/             # 证书目录（首次运行后自动生成）
├── received/          # 接收文件目录
└── README.md          # 本文件
```

## 命令说明

连接器支持以下命令：

- `create <channel-id> <data-topic> <receiver-id1,receiver-id2,...>` - 创建频道
- `sendto <channel-id> <message>` - 发送数据到频道
- `sendto <channel-id> <file-path>` - 发送文件到频道
- `receive <channel-id>` - 接收频道数据
- `subscribe <channel-id>` - 订阅频道
- `channels` - 查看当前参与的频道
- `status` - 查看连接器状态
- `status <active|inactive|closed>` - 设置连接器状态
- `discover` - 发现其他连接器
- `info <connector-id>` - 查看连接器信息
- `exit` 或 `quit` - 退出

## 故障排查

### 连接失败

1. 检查内核服务器地址和端口是否正确
2. 检查网络连通性：`ping <kernel-address>`
3. 检查防火墙是否允许端口 50051

### 证书问题

1. 删除 `certs/` 目录下的证书文件
2. 重新运行连接器进行首次注册

### 更多帮助

请参考完整部署文档：`docs/DEPLOYMENT.md`
EOF

# 创建部署说明
cat > "${OUTPUT_DIR}/${PACKAGE_NAME}/DEPLOY.md" << 'EOF'
# 连接器部署说明

## 系统要求

- Linux x86_64 或 Windows x64
- 网络连接到内核服务器
- 防火墙允许连接到内核服务器端口（默认 50051）

## 部署步骤

### 步骤 1: 解压发布包

```bash
# Linux/Mac
tar -xzf connector-{version}-linux-amd64.tar.gz

# Windows
# 使用解压工具解压 connector-{version}-windows-amd64.zip
```

### 步骤 2: 配置连接器

1. 复制配置模板：
   ```bash
   cp config/connector-template.yaml config/connector.yaml
   ```

2. 编辑 `config/connector.yaml`，设置：
   - 连接器ID
   - 实体类型
   - 公钥
   - 内核服务器地址

### 步骤 3: 首次运行并注册

```bash
# Linux/Mac
./connector -config config/connector.yaml

# Windows
connector.exe -config config/connector.yaml
```

首次运行会自动：
- 连接到内核服务器
- 注册连接器
- 获取并保存证书

### 步骤 4: 验证连接

连接成功后，你会看到：
```
✓ 连接成功！连接器ID: your-connector-id
```

### 步骤 5: 使用连接器

连接器启动后，你可以使用交互式命令：
- 创建频道
- 发送/接收数据
- 发送/接收文件
- 查看频道信息
- 等等

## 安全注意事项

1. **证书安全**：
   - 证书文件包含敏感信息，请妥善保管
   - 不要将证书文件提交到版本控制系统
   - 建议设置适当的文件权限（Linux: 600）

2. **网络安全**：
   - 使用VPN或专用网络连接
   - 配置防火墙规则，限制访问来源

3. **配置安全**：
   - 不要在生产环境中使用默认配置
   - 定期更新连接器版本

## 更新连接器

1. 备份当前配置和证书：
   ```bash
   cp -r config certs backup/
   ```

2. 解压新版本连接器

3. 恢复配置和证书：
   ```bash
   cp backup/config/* config/
   cp backup/certs/* certs/
   ```

4. 运行新版本连接器

## 卸载

直接删除连接器目录即可。证书和配置可以保留以备将来使用。
EOF

# 打包
echo "打包发布包..."
cd "${OUTPUT_DIR}"

if [ "$PLATFORM" = "windows-amd64" ]; then
    # Windows 使用 zip
    if command -v zip &> /dev/null; then
        zip -r "${PACKAGE_NAME}.zip" "${PACKAGE_NAME}"
        echo "✓ 打包完成: ${OUTPUT_DIR}/${PACKAGE_NAME}.zip"
    else
        echo "⚠ zip 命令未找到，请手动打包 ${PACKAGE_NAME} 目录"
    fi
else
    # Linux/Mac 使用 tar.gz
    tar -czf "${PACKAGE_NAME}.tar.gz" "${PACKAGE_NAME}"
    echo "✓ 打包完成: ${OUTPUT_DIR}/${PACKAGE_NAME}.tar.gz"
fi

cd ..

echo ""
echo "✅ 连接器打包完成！"
echo ""
echo "发布包位置:"
if [ "$PLATFORM" = "windows-amd64" ]; then
    echo "  ${OUTPUT_DIR}/${PACKAGE_NAME}.zip"
else
    echo "  ${OUTPUT_DIR}/${PACKAGE_NAME}.tar.gz"
fi
echo ""
echo "发布包内容:"
echo "  - 连接器可执行文件"
echo "  - 配置模板"
echo "  - 部署文档"
echo "  - README"
echo ""


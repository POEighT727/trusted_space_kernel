# 频道配置文件使用指南

## 概述

频道配置文件用于定义数据传输频道的所有属性，包括参与者、存证配置等。每个连接器都有自己专门的配置文件目录，便于管理和组织。

## 配置文件模板

### 1. 最简单配置（无存证）

文件：`channel-simple-config.json`

```json
{
  "channel_id": "channel-A-to-B-simple",
  "name": "简单数据传输频道",
  "description": "connector-A 向 connector-B 的简单数据传输频道",
  "creator_id": "connector-A",
  "approver_id": "connector-A",
  "sender_ids": ["connector-A"],
  "receiver_ids": ["connector-B"],
  "data_topic": "simple.data.transfer",
  "encrypted": false,
  "created_at": "2024-01-15T14:00:00Z",
  "updated_at": "2024-01-15T14:00:00Z",
  "version": 1
}
```

### 2. 带存证配置

文件：`channel-with-evidence-config.json`

```json
{
  "channel_id": "channel-A-to-B-with-evidence",
  "name": "带存证的数据传输频道",
  "description": "connector-A 向 connector-B 的数据传输频道（包含存证）",
  "creator_id": "connector-A",
  "approver_id": "connector-A",
  "sender_ids": ["connector-A"],
  "receiver_ids": ["connector-B"],
  "data_topic": "data.transfer.evidence",
  "encrypted": true,
  "evidence_config": {
    "mode": "internal",
    "strategy": "all",
    "backup_enabled": false,
    "retention_days": 30,
    "compress_data": true,
    "custom_settings": {}
  },
  "created_at": "2024-01-15T14:30:00Z",
  "updated_at": "2024-01-15T14:30:00Z",
  "version": 1
}
```

## 目录结构

每个连接器启动时会自动创建自己的频道配置目录：

```
项目根目录/
├── channels/
│   ├── connector-A/          # connector-A 的配置文件目录
│   │   ├── channel-simple-config.json
│   │   └── channel-with-evidence-config.json
│   └── connector-B/          # connector-B 的配置文件目录
│       └── channel-from-B-to-A-config.json
```

### 配置目录设置

在连接器配置文件中可以指定频道配置目录：

```yaml
# config/connector-A.yaml
channel:
  config_dir: "./channels/connector-A"  # 自定义目录
  # 如果不设置，将自动使用: ./channels/{connector_id}
```

## 使用方法

### 方式1：通过连接器交互式命令行

在连接器启动后，使用交互式命令行：

```bash
# 从配置文件创建频道（推荐）- 在连接器自己的目录中查找
[connector-A] > create --config channel-simple-config.json

# 或指定完整路径
[connector-A] > create --config ./channels/connector-A/channel-simple-config.json

# 或手动指定参数创建频道
[connector-A] > create --sender connector-A --receiver connector-B --reason "data transfer"
```

### 方式2：通过程序API

```go
// 在连接器客户端代码中从配置文件创建
config, err := connector.CreateChannelFromConfig("./channel-simple-config.json")
if err != nil {
    log.Fatalf("创建频道失败: %v", err)
}
fmt.Printf("频道创建成功: %s\n", config.ChannelID)

// 或使用传统API创建
channelID, proposalID, err := connector.ProposeChannel(
    []string{"connector-A"}, // 发送方
    []string{"connector-B"}, // 接收方
    "simple.data.transfer",   // 数据主题
    "",                       // 批准者（空表示使用创建者）
    "data transfer")          // 理由
```

### 方式3：内核API（内部使用）

```go
// 在内核代码中从配置文件创建
channel, err := channelManager.CreateChannelFromConfig("./channel-simple-config.json")

// 或使用传统API创建
channel, err := channelManager.CreateChannel(
    "connector-A", "connector-A",
    []string{"connector-A"}, []string{"connector-B"},
    "simple.data.transfer", false, nil,
    "./channel-simple-config.json")
```

### 方式3：保存现有频道配置

```go
// 方式3：为现有频道保存配置
err := channelManager.SaveChannelConfig("channel-A-to-B-simple", "频道名称", "频道描述")
```

## 配置字段说明

### 必需字段
- `channel_id`: 频道唯一标识
- `creator_id`: 创建者ID
- `sender_ids`: 发送方ID列表（至少一个）
- `receiver_ids`: 接收方ID列表（至少一个）
- `data_topic`: 数据主题
- `created_at`: 创建时间（RFC3339格式）

### 可选字段
- `name`: 频道名称
- `description`: 频道描述
- `approver_id`: 权限批准者ID（默认等于creator_id）
- `encrypted`: 是否加密传输（默认false）
- `evidence_config`: 存证配置

### 自动管理字段
- `updated_at`: 更新时间（自动更新）
- `version`: 配置版本（自动递增）

## 存证配置选项

### mode（存证方式）
- `"none"`: 不进行存证
- `"internal"`: 使用内核内置存证
- `"external"`: 使用外部存证连接器
- `"hybrid"`: 同时使用内置和外部存证

### strategy（存证策略）
- `"all"`: 存证所有消息
- `"data"`: 只存证数据消息
- `"control"`: 只存证控制消息
- `"important"`: 只存证重要消息

## 连接器命令行帮助

在连接器交互模式下，输入 `help` 可以查看所有可用命令，包括：

```
create-from-config <config_file_path> - 从配置文件创建频道
  示例: create-from-config ./channel-simple-config.json
```

## 注意事项

1. **配置文件格式**：必须是有效的JSON格式，使用UTF-8编码
2. **时间戳格式**：使用RFC3339格式（如："2024-01-15T14:00:00Z"）
3. **Connector ID**：必须与实际注册的连接器ID匹配
4. **文件路径**：支持绝对路径和相对路径
5. **权限要求**：只有配置文件中指定的creator才能执行创建操作
6. **协商流程**：创建后仍需要所有参与方确认才能激活频道
7. **版本控制**：建议为配置文件添加版本控制

## 自定义配置

您可以根据需要修改配置文件中的任何字段：

- 更改参与者列表（sender_ids, receiver_ids）
- 修改数据主题（data_topic）
- 调整加密设置（encrypted）
- 配置存证策略（evidence_config）
- 添加自定义存证设置（custom_settings）

只需确保JSON格式正确，系统就能正确解析和使用配置。

## 故障排除

1. **"配置文件不存在"**：检查文件路径是否正确
2. **"不是创建者"**：确保当前连接器是配置文件中指定的creator_id
3. **"频道协商失败"**：检查所有参与方是否都确认了提议
4. **"JSON格式错误"**：验证JSON语法，使用JSON验证工具检查

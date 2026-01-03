# 频道订阅申请工作流

## 概述

系统支持两种不同的权限管理模式：

1. **频道内权限管理**：频道参与者使用permission命令管理频道权限
2. **频道外订阅申请**：频道外连接器使用subscribe命令申请加入频道

## 工作流程

### 📋 场景1：频道外连接器申请订阅

```bash
# 连接器C（频道外）申请订阅频道
[connector-C]> subscribe channel-123 --role receiver --reason "需要接收数据"

# 系统响应
📝 申请加入频道 channel-123 作为 receiver...
   理由: 需要接收数据
✓ 订阅申请已发送，等待审批...
   审批通过后您将自动获得订阅权限
```

### 👥 场景2：频道参与者审批订阅申请

```bash
# 频道参与者（连接器A）查看待处理请求
[connector-A]> list-permissions channel-123

# 批准订阅申请
[connector-A]> approve-permission channel-123 sub-req-456
✓ 订阅申请已批准: sub-req-456
  申请者现在可以订阅该频道

# 或者拒绝订阅申请
[connector-A]> reject-permission channel-123 sub-req-456 "不符合加入条件"
✓ 订阅申请已拒绝: sub-req-456
  拒绝理由: 不符合加入条件
```

### 🔧 场景3：频道内参与者权限管理

```bash
# 频道参与者申请添加新成员（权限变更）
[connector-A]> request-permission channel-123 add_receiver connector-X "添加新接收者"

# 其他参与者审批
[connector-B]> approve-permission channel-123 perm-req-789
✓ 权限变更已批准: perm-req-789
```

## API差异

### 订阅申请API（频道外使用）

| 方法 | 用途 | 调用者限制 |
|------|------|------------|
| `RequestChannelSubscription` | 申请订阅频道 | 频道外连接器 |
| `ApproveChannelSubscription` | 批准订阅申请 | 频道参与者 |
| `RejectChannelSubscription` | 拒绝订阅申请 | 频道参与者 |

### 权限变更API（频道内使用）

| 方法 | 用途 | 调用者限制 |
|------|------|------------|
| `RequestPermissionChange` | 申请权限变更 | 频道参与者 |
| `ApprovePermissionChange` | 批准权限变更 | 频道参与者 |
| `RejectPermissionChange` | 拒绝权限变更 | 频道参与者 |

## 命令行接口

### 订阅申请命令

```bash
# 申请订阅频道（频道外连接器）
subscribe <channel_id> --role <sender|receiver> [--reason <reason>]

# 示例
subscribe channel-123 --role receiver --reason "业务需要"
```

### 权限审批命令

```bash
# 批准请求（订阅申请或权限变更）
approve-permission <channel_id> <request_id>

# 拒绝请求（订阅申请或权限变更）
reject-permission <channel_id> <request_id> <reason>

# 查看请求列表
list-permissions <channel_id>
```

## 安全考虑

1. **身份验证**：所有操作都需要有效的连接器身份
2. **权限控制**：
   - 订阅申请：任何连接器都可以申请
   - 权限审批：只有频道参与者可以审批
3. **审计记录**：所有操作都会记录完整审计日志
4. **状态一致性**：确保频道状态在所有操作后保持一致

## 错误处理

### 常见错误

| 错误信息 | 原因 | 解决方案 |
|----------|------|----------|
| `requester is not a channel participant` | 频道外连接器尝试权限变更 | 使用 `subscribe` 而非 permission 命令 |
| `approver is not a channel participant` | 非频道成员尝试审批 | 确保审批者是频道参与者 |
| `subscriber is already a channel participant` | 已加入的连接器重复申请 | 检查当前频道成员列表 |
| `invalid role: must be 'sender' or 'receiver'` | 角色参数错误 | 使用正确的角色值 |

## 实现细节

### 数据结构

```go
// 订阅申请请求
type RequestChannelSubscriptionRequest struct {
    SubscriberId string // 申请者ID
    ChannelId    string // 频道ID
    Role         string // 申请角色
    Reason       string // 申请理由
}

// 权限变更请求（频道内使用）
type RequestPermissionChangeRequest struct {
    RequesterId string // 请求者ID（必须是频道参与者）
    ChannelId   string // 频道ID
    ChangeType  string // 变更类型: add_sender, remove_sender, etc.
    TargetId    string // 目标连接器ID
    Reason      string // 变更理由
}
```

### 状态机

```
订阅申请流程：
未申请 → 申请中 → 已批准（成为频道参与者）
                → 已拒绝（保持频道外状态）

权限变更流程：
频道参与者 → 申请变更 → 已批准（权限更新）
                    → 已拒绝（权限不变）
```

## 测试用例

### 成功场景

1. **订阅申请成功**：
   - 连接器C申请订阅channel-123
   - 连接器A（频道参与者）批准申请
   - 连接器C成为channel-123的接收者

2. **权限变更成功**：
   - 连接器A申请添加连接器X为发送者
   - 连接器B批准申请
   - 连接器X成为channel-123的发送者

### 失败场景

1. **重复申请**：已提交申请的连接器再次申请
2. **权限不足**：频道外连接器尝试权限变更
3. **无效参数**：错误的角色或变更类型
4. **状态冲突**：频道非活跃状态下的操作

## 扩展性

该设计支持未来扩展：

1. **多级审批**：支持多个审批者的审批流程
2. **条件审批**：基于策略的自动审批
3. **审批委托**：允许委托其他连接器进行审批
4. **审批历史**：完整的审批操作历史记录

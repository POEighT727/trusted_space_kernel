-- =============================================================================
-- 迁移脚本: 删除 business_data_chain 表的 status 字段
-- 说明: 移除 status 字段，ACK 记录通过 data_hash 为空来区分
-- 执行: 在 connector 数据库中执行
-- =============================================================================

-- 注意: 执行前请确保已备份数据！

-- 删除旧表
DROP TABLE IF EXISTS business_data_chain;

-- 创建新表（无 status 字段，增加 prev_signature 字段）
CREATE TABLE IF NOT EXISTS business_data_chain (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    channel_id VARCHAR(36) NOT NULL COMMENT '频道ID',
    data_hash VARCHAR(128) NOT NULL DEFAULT '' COMMENT '当前业务数据包的哈希（ACK记录为空）',
    prev_data_hash VARCHAR(128) NOT NULL DEFAULT '' COMMENT '前一个数据包的哈希',
    prev_signature TEXT NOT NULL COMMENT '上一跳的RSA签名（ACK记录中为上一跳ACK签名）',
    signature TEXT NOT NULL COMMENT '当前节点对data_hash的RSA签名',
    timestamp DATETIME(6) NOT NULL COMMENT '时间戳',
    INDEX idx_channel_id (channel_id),
    INDEX idx_data_hash (data_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='业务数据哈希链表';

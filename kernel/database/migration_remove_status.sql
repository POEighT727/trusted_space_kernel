-- =============================================================================
-- 迁移脚本: 删除 business_data_chain 表的 status 字段
-- 说明: 移除 status 字段，ACK 记录通过 data_hash 为空来区分
-- 执行: 分别在 kernel 和 connector 数据库中执行
-- =============================================================================

-- =====================================================
-- Kernel 数据库迁移
-- =====================================================

-- 方案 1: 如果表中有历史数据需要保留，采用 ALTER TABLE
-- 注意: 会丢失 status='ack' 的记录中的 original_data_hash 信息

-- ALTER TABLE business_data_chain
--     DROP COLUMN status,
--     ADD COLUMN prev_signature TEXT NOT NULL COMMENT '上一跳的RSA签名' AFTER prev_data_hash,
--     ADD INDEX idx_data_hash (data_hash);

-- 方案 2: 如果表为空或可以重建，执行 DROP + CREATE
-- 以下脚本采用方案 2（因为项目初期数据可丢失）

-- 注意: 执行前请确保已备份数据！

-- 删除旧表
DROP TABLE IF EXISTS business_data_chain;

-- 创建新表（无 status 字段）
CREATE TABLE IF NOT EXISTS business_data_chain (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    connector_id VARCHAR(100) NOT NULL COMMENT '连接器ID',
    channel_id VARCHAR(36) NOT NULL COMMENT '频道ID',
    data_hash VARCHAR(128) NOT NULL DEFAULT '' COMMENT '业务数据包的哈希（ACK记录为空）',
    prev_data_hash VARCHAR(128) NOT NULL DEFAULT '' COMMENT '前一个数据包的哈希',
    prev_signature TEXT NOT NULL COMMENT '上一跳（kernel/connector）的RSA签名',
    signature TEXT NOT NULL COMMENT '当前节点对data_hash的RSA签名',
    timestamp DATETIME(6) NOT NULL COMMENT '时间戳',
    INDEX idx_connector_channel (connector_id, channel_id),
    INDEX idx_channel_id (channel_id),
    INDEX idx_connector_id (connector_id),
    INDEX idx_data_hash (data_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='业务数据哈希链记录表';

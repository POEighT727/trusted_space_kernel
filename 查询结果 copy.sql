-- phpMyAdmin SQL Dump
-- version 5.2.1
-- https://www.phpmyadmin.net/
--
-- 主机： localhost
-- 生成日期： 2026-03-18 02:21:12
-- 服务器版本： 8.0.35
-- PHP 版本： 8.0.30

SET SQL_MODE = "NO_AUTO_VALUE_ON_ZERO";
START TRANSACTION;
SET time_zone = "+00:00";


/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;
/*!40101 SET @OLD_CHARACTER_SET_RESULTS=@@CHARACTER_SET_RESULTS */;
/*!40101 SET @OLD_COLLATION_CONNECTION=@@COLLATION_CONNECTION */;
/*!40101 SET NAMES utf8mb4 */;

--
-- 数据库： `trusted_space`
--

-- --------------------------------------------------------

--
-- 表的结构 `evidence_records`
--

CREATE TABLE `evidence_records` (
  `id` bigint NOT NULL,
  `event_id` varchar(36) NOT NULL COMMENT '事件实例ID',
  `event_type` varchar(50) NOT NULL COMMENT '事件类型',
  `timestamp` timestamp(6) NOT NULL COMMENT '时间戳',
  `source_id` varchar(100) NOT NULL COMMENT '事件来源ID（连接器或内核）',
  `target_id` varchar(100) DEFAULT '' COMMENT '目标ID（直接下一跳：内核或连接器）',
  `channel_id` varchar(36) DEFAULT '' COMMENT '频道ID',
  `flow_id` varchar(36) DEFAULT '' COMMENT '业务流程ID',
  `data_hash` varchar(128) DEFAULT '' COMMENT '数据哈希',
  `tx_id` varchar(36) DEFAULT '' COMMENT '业务流程ID（用于跨内核关联）',
  `signature` text COMMENT '签名（流模式下仅在流结束时生成）',
  `hash` varchar(128) NOT NULL COMMENT '记录内容哈希',
  `prev_hash` varchar(128) DEFAULT '' COMMENT '上一条记录的哈希（哈希链）',
  `metadata` json DEFAULT NULL COMMENT '元数据'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='内核存证记录表';

--
-- 转存表中的数据 `evidence_records`
--

INSERT INTO `evidence_records` (`id`, `event_id`, `event_type`, `timestamp`, `source_id`, `target_id`, `channel_id`, `flow_id`, `data_hash`, `tx_id`, `signature`, `hash`, `prev_hash`, `metadata`) VALUES
(1, '264a8e91-4a06-4040-b944-8d857e124e11', 'AUTH_SUCCESS', '2026-03-18 02:14:03.711361', 'connector-U', '', '', '', '', '', '', '62cbaa2acfec3fa4806742b0b38b2f0c5b45985528b7dd02877e448ff278cad5', '', '{\"data_category\": \"control\"}'),
(2, '9493ce93-3928-47e6-b801-2c8a4dfb5ff4', 'INTERCONNECT_APPROVED', '2026-03-18 02:18:18.608877', 'kernel-2', 'kernel-1', '', '', '1214faf7-b649-41ac-a21f-83618b72bc72', '', '', 'af63083448bae85031a85aa72c233766a8b6a4bd81b01ac2ad94f2b011ec615b', '62cbaa2acfec3fa4806742b0b38b2f0c5b45985528b7dd02877e448ff278cad5', '{\"data_category\": \"control\"}');

--
-- 转储表的索引
--

--
-- 表的索引 `evidence_records`
--
ALTER TABLE `evidence_records`
  ADD PRIMARY KEY (`id`),
  ADD KEY `idx_event_id` (`event_id`),
  ADD KEY `idx_event_type` (`event_type`),
  ADD KEY `idx_source_id` (`source_id`),
  ADD KEY `idx_target_id` (`target_id`),
  ADD KEY `idx_channel_id` (`channel_id`),
  ADD KEY `idx_flow_id` (`flow_id`),
  ADD KEY `idx_tx_id` (`tx_id`),
  ADD KEY `idx_timestamp` (`timestamp`);

--
-- 在导出的表使用AUTO_INCREMENT
--

--
-- 使用表AUTO_INCREMENT `evidence_records`
--
ALTER TABLE `evidence_records`
  MODIFY `id` bigint NOT NULL AUTO_INCREMENT, AUTO_INCREMENT=3;
COMMIT;

/*!40101 SET CHARACTER_SET_CLIENT=@OLD_CHARACTER_SET_CLIENT */;
/*!40101 SET CHARACTER_SET_RESULTS=@OLD_CHARACTER_SET_RESULTS */;
/*!40101 SET COLLATION_CONNECTION=@OLD_COLLATION_CONNECTION */;
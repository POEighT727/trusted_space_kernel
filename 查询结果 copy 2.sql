-- phpMyAdmin SQL Dump
-- version 5.2.1
-- https://www.phpmyadmin.net/
--
-- Host: localhost
-- Generation Time: Mar 18, 2026 at 02:20 AM
-- Server version: 8.0.35
-- PHP Version: 8.0.30

SET SQL_MODE = "NO_AUTO_VALUE_ON_ZERO";
START TRANSACTION;
SET time_zone = "+00:00";


/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;
/*!40101 SET @OLD_CHARACTER_SET_RESULTS=@@CHARACTER_SET_RESULTS */;
/*!40101 SET @OLD_COLLATION_CONNECTION=@@COLLATION_CONNECTION */;
/*!40101 SET NAMES utf8mb4 */;

--
-- Database: `trusted_space`
--

-- --------------------------------------------------------

--
-- Table structure for table `evidence_records`
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
-- Dumping data for table `evidence_records`
--

INSERT INTO `evidence_records` (`id`, `event_id`, `event_type`, `timestamp`, `source_id`, `target_id`, `channel_id`, `flow_id`, `data_hash`, `tx_id`, `signature`, `hash`, `prev_hash`, `metadata`) VALUES
(1, 'b5204913-2514-4bd0-8a02-a38608ed25cd', 'AUTH_SUCCESS', '2026-03-18 02:16:56.490529', 'connector-X', '', '', '', '', '', '', '5651383771eb913c47755e35c6308f445fc36e73fd26fca905da4a882b2d398f', '', '{\"data_category\": \"control\"}'),
(2, '99b0ab3e-ace4-48d7-8fc4-e39cf2710320', 'INTERCONNECT_APPROVED', '2026-03-18 02:18:29.620264', 'kernel-3', 'kernel-1', '', '', '08b1c23a-f78c-4007-90c5-799055f89183', '', '', '90ade54b151b4552e001682c2bd8ffe58e60c4e726b5c484a5e1db3ac3459b12', '5651383771eb913c47755e35c6308f445fc36e73fd26fca905da4a882b2d398f', '{\"data_category\": \"control\"}'),
(3, 'd7bcec08-deb8-4d15-8d3e-881708a69d35', 'CHANNEL_CREATED', '2026-03-18 02:19:06.807619', 'kernel-3', 'kernel-3', 'e32fce1f-6c84-4cfc-8aff-b8b513f08985', '', 'bb08796e-761e-4744-a812-f7a64c63b728', '', '', 'f3bd2ad298d5cc1ace6be65d01f1d70019d4015b0d50088224d4217ab9701792', '90ade54b151b4552e001682c2bd8ffe58e60c4e726b5c484a5e1db3ac3459b12', '{\"data_category\": \"control\"}');

--
-- Indexes for dumped tables
--

--
-- Indexes for table `evidence_records`
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
-- AUTO_INCREMENT for dumped tables
--

--
-- AUTO_INCREMENT for table `evidence_records`
--
ALTER TABLE `evidence_records`
  MODIFY `id` bigint NOT NULL AUTO_INCREMENT, AUTO_INCREMENT=4;
COMMIT;

/*!40101 SET CHARACTER_SET_CLIENT=@OLD_CHARACTER_SET_CLIENT */;
/*!40101 SET CHARACTER_SET_RESULTS=@OLD_CHARACTER_SET_RESULTS */;
/*!40101 SET COLLATION_CONNECTION=@OLD_COLLATION_CONNECTION */;
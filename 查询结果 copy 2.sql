-- phpMyAdmin SQL Dump
-- version 5.2.1
-- https://www.phpmyadmin.net/
--
-- Host: localhost
-- Generation Time: Mar 14, 2026 at 12:13 PM
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
  `data_hash` varchar(128) DEFAULT '' COMMENT '数据哈希',
  `tx_id` varchar(36) DEFAULT '' COMMENT '业务流程ID（用于跨内核关联）',
  `signature` text COMMENT '内核数字签名',
  `hash` varchar(128) NOT NULL COMMENT '记录内容哈希',
  `prev_hash` varchar(128) DEFAULT '' COMMENT '上一条记录的哈希（哈希链）',
  `metadata` json DEFAULT NULL COMMENT '元数据'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='内核存证记录表';

--
-- Dumping data for table `evidence_records`
--

INSERT INTO `evidence_records` (`id`, `event_id`, `event_type`, `timestamp`, `source_id`, `target_id`, `channel_id`, `data_hash`, `tx_id`, `signature`, `hash`, `prev_hash`, `metadata`) VALUES
(1, '2467394d-50de-43c6-97f7-8d292d228cee', 'AUTH_SUCCESS', '2026-03-14 12:06:27.024253', 'connector-X', '', '', '', '', 'mN4VlyOwXvSqYut+4XHeyS4sZnO2w0mf1Ev9PQ9zJ6IUyDVmIovZXf501doMv+Y1f3MXYadhanMk7ZqJyvuDnRn3eN1Kf6QhdU/A3bf296SNSHTJOr7WAVryTtfZ/nBzUrvLKHEKHBKV7rsPPuJfIGnGXtulF2l9S1riANZgVinrX+iIqIuC3n6FuvB5YOCl9vyiNSoDVuVULWV27WKWEJwo/SzCeepfEKhIVZ0cT/qTMtx4pBTk4rGwLx3n9GiPR9j7fe2VWD8xRQDPPm4JH/Xz4csOfzZg82JSZHuJnVEw7cnGtY3q4I4vAykr6VztrQ5DrxUcXu9DSkureTvCzA==', '1948eb00ef127b42c602149ede6e8b86f914a3efdcb5827db70245e8e9ae78fa', '', '{\"data_category\": \"control\"}'),
(2, '22176c65-f3f4-4bcd-9009-a0005f419fe4', 'INTERCONNECT_APPROVED', '2026-03-14 12:07:22.928062', 'kernel-3', 'kernel-1', '', '8a8eacdd-b5c0-4c05-812a-8b257086e2d4', '', 'DEvl8HHazhFzteUw9DfSHxAqRiqXueLWMYBYswEPVWoufQfjv+iozBfcrLkgPEt8ycEJQdbNgh5lfHI53m7b/C7p4P4LkhrpU0X6WrrunIM+y8cEt3y1sdelYZF7GA9CpoTiKUed3e8YTmgucl7xWnLg7WRX23N7dzX4PyiiExvpWHp+3zFFz9KtH4UYSHEKjdDNnhinekmj46qW8sFJfwEI7nziWJ65BPNjPRciKr/bzzeTW46/WNa/HmMvhMQFIRrsmdtARQkKqE/2FKCez+drCBBUvDvRl/bSjL3QJzNt3MT8Te6H0XGQmFw4/RjqLRKZDVF5OEmOwgGJxBMDpA==', '7b9a8f2f70c55bb51829cff6b35e6fae76cf42abb376cb91a115e383434948eb', '1948eb00ef127b42c602149ede6e8b86f914a3efdcb5827db70245e8e9ae78fa', '{\"data_category\": \"control\"}'),
(3, 'a6d29383-b3ef-4413-8278-c6cec79d0264', 'CHANNEL_CREATED', '2026-03-14 12:07:37.235455', 'kernel-3', 'kernel-3', '80b50e55-cb6c-41dd-a9d6-3e2075282160', 'f9d2764b-8a86-4732-8f89-e4d765e0b925', '', 'RH8C0wxWeM6ZmXw9KXxbMbCo1b1hpZzJs/QeKLoTX73jPd79uSIMyuPRI+qSygZs7k7qd/6ZhOYjlZEOTLqJVlabL0/sCTJlIR1JYk1O4fgIG/AxnNsjpF8ouaulB3edWe7PWbg0Vebncp4/LZSPMhG+5V7IAGNTBjzJscSe2Pa7Soq5X/Dk50ZQgC3NXj7n/Y7sHn9tSchse6hTR+YsRxIaFroqJvy39JVXoxY50/ccjkD7/reBQgiD/PKsQZ/9YClPBTj/9gkJ8AolDMjhfSu2t+d8GABwpH2bt06wYfu6o8hVEJbnZL462GCxag1vWwO6oajkXydkI7NVk8owIg==', '05ef05262c56317cd01fb44270d3d02b31bf384c81a9f04e9dec12a39d1f3b22', '7b9a8f2f70c55bb51829cff6b35e6fae76cf42abb376cb91a115e383434948eb', '{\"data_category\": \"control\"}');

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
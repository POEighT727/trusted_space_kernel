-- phpMyAdmin SQL Dump
-- version 5.2.1
-- https://www.phpmyadmin.net/
--
-- 主机： localhost
-- 生成日期： 2026-03-14 12:13:08
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
  `data_hash` varchar(128) DEFAULT '' COMMENT '数据哈希',
  `tx_id` varchar(36) DEFAULT '' COMMENT '业务流程ID（用于跨内核关联）',
  `signature` text COMMENT '内核数字签名',
  `hash` varchar(128) NOT NULL COMMENT '记录内容哈希',
  `prev_hash` varchar(128) DEFAULT '' COMMENT '上一条记录的哈希（哈希链）',
  `metadata` json DEFAULT NULL COMMENT '元数据'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='内核存证记录表';

--
-- 转存表中的数据 `evidence_records`
--

INSERT INTO `evidence_records` (`id`, `event_id`, `event_type`, `timestamp`, `source_id`, `target_id`, `channel_id`, `data_hash`, `tx_id`, `signature`, `hash`, `prev_hash`, `metadata`) VALUES
(1, 'f2aac163-708d-477e-9798-66179f4785ba', 'AUTH_SUCCESS', '2026-03-14 12:05:14.157046', 'connector-U', '', '', '', '', 'Cj26HQ1W5p56Fn1ai48wLyN8BRhNxwM+QNTcLIxsQd0zpU1NaG1HaV1NXJJ3729zT8EEKNgcoOju/sgACk/qaFA2fA/6HDchEB1AWJ+pS3mhzIftcMr3HBdSoaiTuKIIafaJsK4CTNIIriQK6a+orKP0BvroZqHw2TfQE9hQdN0riixPJtz8RAZm4GLnhf/U562Jf0wN/JWNL5b6J3pIhdW0PFulV8oWmSB/5ZAb63LOxvsKEq3eUvnz4KBXBUDWqn+/NQ2z7ZkkTrROe9VHOaMjTlXXJXQU9Cje+jYukCFwltDvAWnY+akBFij6/OK2yluqNXNWRTClaksCvGXtAA==', 'e085e3859fa2eee549ef7e2c498815a63894d3a813ca0f50e63beebe52260cef', '', '{\"data_category\": \"control\"}'),
(2, '5b09f146-cdde-4fd7-a831-a9bb951be85c', 'INTERCONNECT_APPROVED', '2026-03-14 12:07:10.761640', 'kernel-2', 'kernel-1', '', '4ec97f00-0fd3-4b20-8af3-97ad6def94d9', '', 'JXCEER2J3ThqndTuYE3StS+i3eM7TrgQgtTPqRJWaDg31nKcAtjzg5tFVsGFrOcdHx1W9B1Tdlvkc2baKG25n49upDVaPixw2JMKzVnxFJIOV7UOMsDYVjqDGbUNlD2nqkrcj2h+nKK9AgDtFc1D0DGEAP4eeUNchKlciafhddYGr7qGwNpn6DV0szYcSlMNQ/0/OY12nyD92WU8lijnNS03RR3G6nh+KGNpwyXOJfAFBD3j5S6O8H4obHWlG5MG3termuEjOMwrKIqyI6Jf74grChIv683371yjf8RMzUPFOERHlv8ZR6D/xGjVmnuaRYwdbdAZ9zfnzLNs/xC1iQ==', 'c65bc20b4d0b25253f90a550e7aaea1462275e6a9bcb4690b203cf6aad99cc67', 'e085e3859fa2eee549ef7e2c498815a63894d3a813ca0f50e63beebe52260cef', '{\"data_category\": \"control\"}'),
(3, '88e6793d-e67a-4931-95d0-5e2e46149e5c', 'CHANNEL_CREATED', '2026-03-14 12:07:37.261951', 'kernel-2', 'kernel-2', '80b50e55-cb6c-41dd-a9d6-3e2075282160', 'f9d2764b-8a86-4732-8f89-e4d765e0b925', '', 'QU66glLF7RMTjS/nDFNGos0NMC+PQ+f6dzhngbojwwAVWEQapDsred7eptf8sgNIP8Twq8hPm+E8nEkoOPdQtndlgCwyvawaqz3aljS0KeekwXW8jQAx5yEpeRCPau+cB82lKcp3vCc8LgQz3WybK3+rERJ70DzlIP7VWBZnnmklt3AnbOE7ZLKVtHrma0wIQL4Dnd5KywwS80W2kLka4j9bTjQSeA83xpBbKfbvMsVk7mgPqp2H5pVJwJYDVx0xz2TGI338GnU7l4qhz3ozmsNr4TersGuTBPDm0UpSUiY2DMlCCTMTvr1lAfrML7Ydt2NOVrVKw6ygGjuOTCraPg==', 'ca070175c50e76a2156729d4d2148fa2cb0f80fb9a7764c9fc1abd1263a87ac5', 'c65bc20b4d0b25253f90a550e7aaea1462275e6a9bcb4690b203cf6aad99cc67', '{\"data_category\": \"control\"}');

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
  ADD KEY `idx_tx_id` (`tx_id`),
  ADD KEY `idx_timestamp` (`timestamp`);

--
-- 在导出的表使用AUTO_INCREMENT
--

--
-- 使用表AUTO_INCREMENT `evidence_records`
--
ALTER TABLE `evidence_records`
  MODIFY `id` bigint NOT NULL AUTO_INCREMENT, AUTO_INCREMENT=4;
COMMIT;

/*!40101 SET CHARACTER_SET_CLIENT=@OLD_CHARACTER_SET_CLIENT */;
/*!40101 SET CHARACTER_SET_RESULTS=@OLD_CHARACTER_SET_RESULTS */;
/*!40101 SET COLLATION_CONNECTION=@OLD_COLLATION_CONNECTION */;
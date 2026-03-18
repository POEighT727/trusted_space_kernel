# Host: localhost  (Version: 5.7.26)
# Date: 2026-03-18 10:21:30
# Generator: MySQL-Front 5.3  (Build 4.234)

/*!40101 SET NAMES utf8 */;

#
# Structure for table "evidence_records"
#

DROP TABLE IF EXISTS `evidence_records`;
CREATE TABLE `evidence_records` (
  `id` bigint(20) NOT NULL AUTO_INCREMENT,
  `event_id` varchar(36) NOT NULL COMMENT '事件实例ID',
  `event_type` varchar(50) NOT NULL COMMENT '事件类型',
  `timestamp` timestamp(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6) COMMENT '时间戳',
  `source_id` varchar(100) NOT NULL COMMENT '事件来源ID（连接器或内核）',
  `target_id` varchar(100) DEFAULT '' COMMENT '目标ID（直接下一跳：内核或连接器）',
  `channel_id` varchar(36) DEFAULT '' COMMENT '频道ID',
  `flow_id` varchar(36) DEFAULT '' COMMENT '业务流程ID',
  `data_hash` varchar(128) DEFAULT '' COMMENT '数据哈希',
  `tx_id` varchar(36) DEFAULT '' COMMENT '业务流程ID（用于跨内核关联）',
  `signature` text COMMENT '签名（流模式下仅在流结束时生成）',
  `hash` varchar(128) NOT NULL COMMENT '记录内容哈希',
  `prev_hash` varchar(128) DEFAULT '' COMMENT '上一条记录的哈希（哈希链）',
  `metadata` json DEFAULT NULL COMMENT '元数据',
  PRIMARY KEY (`id`),
  KEY `idx_event_id` (`event_id`),
  KEY `idx_event_type` (`event_type`),
  KEY `idx_source_id` (`source_id`),
  KEY `idx_target_id` (`target_id`),
  KEY `idx_channel_id` (`channel_id`),
  KEY `idx_flow_id` (`flow_id`),
  KEY `idx_tx_id` (`tx_id`),
  KEY `idx_timestamp` (`timestamp`)
) ENGINE=InnoDB AUTO_INCREMENT=7 DEFAULT CHARSET=utf8mb4 COMMENT='内核存证记录表';

#
# Data for table "evidence_records"
#

INSERT INTO `evidence_records` VALUES (1,'cc23265b-32fc-4155-9856-8c59171be363','AUTH_SUCCESS','2026-03-18 10:11:33.656918','connector-A','','','','','','','beddf13c0ba2d9e6b387e6b64a990353d70773f6d17c441f1b7edea83fb9a237','',X'7B22646174615F63617465676F7279223A2022636F6E74726F6C227D'),(2,'ed7add0a-77f3-4004-a621-ee4d38b9452f','INTERCONNECT_REQUESTED','2026-03-18 10:18:05.627136','kernel-1','kernel-2','','','','','','f5ffd41b52bc9b6aa87e2b156eef33f8c1d7aa81b866b54adee212eb35b18a93','beddf13c0ba2d9e6b387e6b64a990353d70773f6d17c441f1b7edea83fb9a237',X'7B22646174615F63617465676F7279223A2022636F6E74726F6C227D'),(3,'cd22f63a-cfef-4249-aa63-b3da41c71497','INTERCONNECT_REQUESTED','2026-03-18 10:18:05.669417','kernel-1','kernel-3','','','','','','86fc24dbb965e748906619aad99a5eda9f9547c757cb2f5e87e9de24f94caffd','f5ffd41b52bc9b6aa87e2b156eef33f8c1d7aa81b866b54adee212eb35b18a93',X'7B22646174615F63617465676F7279223A2022636F6E74726F6C227D'),(4,'f1c03180-de53-440b-b47b-f9a8890162f3','CHANNEL_CREATED','2026-03-18 10:19:07.955086','kernel-1','kernel-1','e32fce1f-6c84-4cfc-8aff-b8b513f08985','','bb08796e-761e-4744-a812-f7a64c63b728','','','ef96b2efa7fd9a805987f6fd5ba7d7c450523b4cdef111253c9dc57733d50cb3','86fc24dbb965e748906619aad99a5eda9f9547c757cb2f5e87e9de24f94caffd',X'7B22646174615F63617465676F7279223A2022636F6E74726F6C227D'),(5,'9081b321-cee1-4c5c-8ae4-fc6a34b19762','DATA_SEND','2026-03-18 10:19:17.846429','connector-A','kernel-1','e32fce1f-6c84-4cfc-8aff-b8b513f08985','','20861cec1ee5d87e2b632a061f96340d31d3e644328c4196e843b4bd3bd54da2','c7644c12-4edb-4600-a614-211c4852b85d','XQXVOLCIVToXwYZFX5ZszCAYLBNt2r+wlRRgo00FiB8GCmgkzyOF7HgQ+cwQJLtdlmvR4kYqrVM2RfyHoeA65AZgpCaAyBhGXgHamaRtRPe6g2wi3FWw79oH0BIAGuCrC5DRpCOhfOuvhBTrEVfvEvgTrRKF9N4dWD+tUD9ns+38WdtzYyRBx1NMnQj2w5D8WDBgOWIhnhX7+iNdK2HxoHDQZT0oljVQGxrMsZ7GJAbrf8oriUmZvEIMM8llsKtmW1Dx7gAYYSWENfhN6tA3iq6MpbXEqg+0oR1dF4uP3Fg5i/EnOtU28S7IKIt6Mu+ysMU7WsEpEyBVZmO3LQ2GuA==','a1884c596315eca79b308f1d563c9ac29ef803e714211ddb0ce58bcd400bc406','ef96b2efa7fd9a805987f6fd5ba7d7c450523b4cdef111253c9dc57733d50cb3',X'7B22646174615F63617465676F7279223A2022627573696E657373227D'),(6,'e57d7ec8-0c0e-40ec-9d9d-ba7d831c1bef','DATA_SEND','2026-03-18 10:19:17.862906','kernel-1','kernel-2','e32fce1f-6c84-4cfc-8aff-b8b513f08985','c7644c12-4edb-4600-a614-211c4852b85d','20861cec1ee5d87e2b632a061f96340d31d3e644328c4196e843b4bd3bd54da2','c7644c12-4edb-4600-a614-211c4852b85d','','8bc32976f972bf7c88262815ef99c33bd400b8fcab4af07a7fe3235f61a1735d','a1884c596315eca79b308f1d563c9ac29ef803e714211ddb0ce58bcd400bc406',X'7B22646174615F63617465676F7279223A2022627573696E657373227D');

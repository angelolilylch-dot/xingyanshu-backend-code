ALTER TABLE `evidence_record`
  ADD COLUMN `chain_block_height` VARCHAR(128) NULL DEFAULT NULL COMMENT '链区块高度；由可信存证服务返回' AFTER `chain_transaction_hash`;

CREATE TABLE IF NOT EXISTS `evidence_certificate` (
  `id` BIGINT UNSIGNED NOT NULL COMMENT '主键；内部主键',
  `status` VARCHAR(40) NOT NULL COMMENT '状态；PENDING_REQUEST/PENDING/READY/FAILED',
  `status_reason_code` VARCHAR(64) NULL DEFAULT NULL COMMENT '失败原因码',
  `status_reason` VARCHAR(500) NULL DEFAULT NULL COMMENT '失败说明；脱敏展示',
  `version` INT UNSIGNED NOT NULL DEFAULT 1 COMMENT '乐观锁版本',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `evidence_id` BIGINT UNSIGNED NOT NULL COMMENT '关联存证记录',
  `provider` VARCHAR(64) NOT NULL COMMENT '证书服务商',
  `provider_status` VARCHAR(64) NULL DEFAULT NULL COMMENT '服务商原始状态',
  `certificate_url` VARCHAR(2048) NULL DEFAULT NULL COMMENT '链上证书地址；仅后端按权限输出',
  `request_retry_count` INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '生成请求重试次数',
  `query_retry_count` INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '查询重试次数',
  `requested_at` DATETIME(3) NULL DEFAULT NULL COMMENT '最近发起生成时间',
  `last_queried_at` DATETIME(3) NULL DEFAULT NULL COMMENT '最近查询时间',
  `next_run_at` DATETIME(3) NOT NULL COMMENT '下次任务时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_evidence_certificate_evidence_id` (`evidence_id`),
  KEY `idx_evidence_certificate_status_next_run_at` (`status`, `next_run_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='可信存证链上证书任务与受控地址';

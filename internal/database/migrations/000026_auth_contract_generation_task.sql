CREATE TABLE IF NOT EXISTS `auth_contract_generation_task` (
  `id` BIGINT UNSIGNED NOT NULL COMMENT '主键',
  `status` VARCHAR(40) NOT NULL COMMENT 'PENDING/PROCESSING/GENERATED/FAILED',
  `version` INT UNSIGNED NOT NULL DEFAULT 1 COMMENT '乐观锁版本',
  `authorization_id` BIGINT UNSIGNED NOT NULL COMMENT '有效授权；一授权一合同生成任务',
  `auth_order_id` BIGINT UNSIGNED NOT NULL COMMENT '订单；合同正文仅读取其购买快照',
  `creator_user_id` BIGINT UNSIGNED NOT NULL COMMENT '合同私有文件归属用户',
  `retry_count` INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '已尝试次数',
  `next_run_at` DATETIME(3) NOT NULL COMMENT '下次可执行时间',
  `last_error` VARCHAR(500) NULL DEFAULT NULL COMMENT '最近一次失败摘要',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_auth_contract_generation_task_authorization_id` (`authorization_id`),
  KEY `idx_auth_contract_generation_task_status_next_run_at` (`status`, `next_run_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='授权合同后置生成任务；支付成功不依赖 PDF 或存证成功';

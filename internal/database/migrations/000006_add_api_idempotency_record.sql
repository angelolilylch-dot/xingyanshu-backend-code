-- 接口幂等记录：订单、支付、退款、履约等可重复提交动作的首次处理结果。
CREATE TABLE IF NOT EXISTS `api_idempotency_record` (
  `id` BIGINT UNSIGNED NOT NULL COMMENT '主键；内部幂等记录 ID',
  `subject_type` VARCHAR(20) NOT NULL COMMENT '调用主体类型；ADMIN / CREATOR / CONSUMER / SYSTEM',
  `subject_id` BIGINT UNSIGNED NOT NULL COMMENT '调用主体 ID；逻辑关联账号或服务主体',
  `request_method` VARCHAR(10) NOT NULL COMMENT 'HTTP 请求方法',
  `request_path` VARCHAR(255) NOT NULL COMMENT '标准化接口路径；不含查询参数',
  `idempotency_key` VARCHAR(128) NOT NULL COMMENT '客户端幂等键；同主体同接口唯一',
  `request_hash` CHAR(64) NOT NULL COMMENT '请求体 SHA256；用于识别同键不同请求',
  `status` VARCHAR(20) NOT NULL COMMENT '处理状态；PROCESSING / COMPLETED',
  `response_status` SMALLINT UNSIGNED NULL DEFAULT NULL COMMENT '首次处理的 HTTP 响应状态码',
  `response_code` VARCHAR(100) NULL DEFAULT NULL COMMENT '首次处理的业务响应码',
  `response_message` VARCHAR(500) NULL DEFAULT NULL COMMENT '首次处理的响应提示',
  `response_data` JSON NULL COMMENT '首次处理的业务 data 快照；不保存令牌和敏感原文',
  `completed_at` DATETIME(3) NOT NULL COMMENT '首次处理完成时间',
  `expires_at` DATETIME(3) NOT NULL COMMENT '幂等记录过期时间',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_api_idempotency_scope` (`subject_type`, `subject_id`, `request_method`, `request_path`, `idempotency_key`),
  KEY `idx_api_idempotency_expires_at` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='接口幂等记录：首次结果快照与重复请求复用';

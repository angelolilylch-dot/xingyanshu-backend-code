CREATE TABLE IF NOT EXISTS `auth_session` (
  `id` BIGINT UNSIGNED NOT NULL COMMENT '主键；内部会话 ID',
  `subject_type` VARCHAR(20) NOT NULL COMMENT '主体类型；ADMIN / CONSUMER',
  `subject_id` BIGINT UNSIGNED NOT NULL COMMENT '主体 ID；逻辑关联 sys_user.id 或 consumer_account.id',
  `client_type` VARCHAR(20) NOT NULL COMMENT '客户端类型；ADMIN_WEB / CREATOR_WEB / CONSUMER_H5 / SYSTEM',
  `refresh_token_hash` VARCHAR(255) NOT NULL COMMENT '刷新令牌哈希；不保存明文',
  `token_version` INT UNSIGNED NOT NULL DEFAULT 1 COMMENT '会话令牌版本；撤销或轮换时失效旧令牌',
  `status` VARCHAR(20) NOT NULL COMMENT '会话状态；ACTIVE / REVOKED / EXPIRED',
  `expires_at` DATETIME(3) NOT NULL COMMENT '会话到期时间',
  `last_used_at` DATETIME(3) NULL DEFAULT NULL COMMENT '最近使用时间',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '更新时间',
  PRIMARY KEY (`id`),
  KEY `idx_auth_session_subject_status` (`subject_type`, `subject_id`, `status`),
  KEY `idx_auth_session_expires_at` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='认证会话：刷新令牌轮换与撤销记录';

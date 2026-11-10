-- API-FUL-02 persistent venue QR with authenticated eligibility.
CREATE TABLE IF NOT EXISTS `activity_checkin_qrcode` (
  `id` BIGINT UNSIGNED NOT NULL COMMENT '主键；内部主键',
  `status` VARCHAR(40) NOT NULL COMMENT 'ACTIVE、PAUSED、REVOKED',
  `status_reason_code` VARCHAR(64) NULL DEFAULT NULL,
  `status_reason` VARCHAR(500) NULL DEFAULT NULL,
  `version` INT UNSIGNED NOT NULL DEFAULT 1,
  `created_by` BIGINT UNSIGNED NULL DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_by` BIGINT UNSIGNED NULL DEFAULT NULL,
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `status_changed_at` DATETIME(3) NULL DEFAULT NULL,
  `status_changed_by_type` VARCHAR(32) NULL DEFAULT NULL,
  `status_changed_by_id` VARCHAR(64) NULL DEFAULT NULL,
  `qrcode_no` VARCHAR(64) NOT NULL COMMENT '活动现场二维码编号',
  `activity_id` BIGINT UNSIGNED NOT NULL COMMENT '活动',
  `entry_code` VARCHAR(80) NOT NULL COMMENT '不可猜测的 H5 入口码；二维码公开展示',
  `scan_count` BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '成功确认核销次数',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_activity_checkin_qrcode_no` (`qrcode_no`),
  UNIQUE KEY `uk_activity_checkin_qrcode_entry_code` (`entry_code`),
  KEY `idx_activity_checkin_qrcode_activity_status` (`activity_id`, `status`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='活动现场核销二维码：活动固定展示码';

ALTER TABLE `verification_record`
  MODIFY COLUMN `operator_id` BIGINT UNSIGNED NULL COMMENT '核销主体；消费者自助核销时为消费者ID',
  ADD COLUMN `operator_type` VARCHAR(32) NULL DEFAULT NULL COMMENT 'STAFF、CONSUMER_SELF_SCAN 等' AFTER `operator_id`,
  ADD COLUMN `checkin_qrcode_id` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '活动现场二维码' AFTER `operator_type`,
  ADD COLUMN `location_json` JSON NULL COMMENT '消费者授权上报的位置审计信息；可为空' AFTER `verification_point`,
  ADD KEY `idx_verification_record_checkin_qrcode_id` (`checkin_qrcode_id`);

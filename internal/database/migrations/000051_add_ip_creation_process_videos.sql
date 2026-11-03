ALTER TABLE `ip_asset`
  ADD COLUMN `creation_process_video_exemption_reason` VARCHAR(500) NULL DEFAULT NULL COMMENT '创作过程视频豁免原因；非原始创作无视频时必填' AFTER `purchase_notice`;

CREATE TABLE IF NOT EXISTS `ip_creation_process_video` (
  `id` BIGINT UNSIGNED NOT NULL COMMENT '主键',
  `status` VARCHAR(40) NOT NULL COMMENT 'ACTIVE',
  `version` INT UNSIGNED NOT NULL DEFAULT 1 COMMENT '乐观锁版本',
  `created_by` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '创建人',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
  `updated_by` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '最后更新人',
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '最后更新时间',
  `ip_id` BIGINT UNSIGNED NOT NULL COMMENT 'IP资产',
  `file_id` BIGINT UNSIGNED NOT NULL COMMENT '私有MP4文件',
  `file_hash` CHAR(64) NOT NULL COMMENT '提交时文件SHA-256',
  `description` VARCHAR(500) NOT NULL COMMENT '视频内容说明',
  `recorded_at` DATE NULL DEFAULT NULL COMMENT '拍摄日期',
  `sort_no` INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '展示顺序',
  `submission_locked_at` DATETIME(3) NULL DEFAULT NULL COMMENT '提交审核或DCI代办时锁定',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_ip_creation_process_video_ip_file` (`ip_id`,`file_id`),
  KEY `idx_ip_creation_process_video_ip_sort` (`ip_id`,`sort_no`,`id`),
  KEY `idx_ip_creation_process_video_submission_locked` (`submission_locked_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='IP创作过程视频：私有审核证据，不作为授权素材包交付';

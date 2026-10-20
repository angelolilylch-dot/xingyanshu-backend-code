-- 个人数字形象 IP 库：独立导出任务。清单导出与原始归档包均不得复用普通文件下载记录。

CREATE TABLE IF NOT EXISTS `avatar_export_job` (
  `id` BIGINT UNSIGNED NOT NULL,
  `status` VARCHAR(40) NOT NULL COMMENT 'PENDING/PROCESSING/SUCCEEDED/FAILED/EXPIRED/REVOKED',
  `status_reason_code` VARCHAR(64) NULL DEFAULT NULL,
  `status_reason` VARCHAR(500) NULL DEFAULT NULL,
  `version` INT UNSIGNED NOT NULL DEFAULT 1,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `job_no` VARCHAR(64) NOT NULL,
  `export_type` VARCHAR(40) NOT NULL COMMENT 'PROGRESS_LIST/ARCHIVE_PACKAGE',
  `requester_id` BIGINT UNSIGNED NOT NULL,
  `requester_org_id` BIGINT UNSIGNED NOT NULL,
  `requester_role` VARCHAR(64) NOT NULL,
  `city_org_id` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '城市运营仅允许自身城市',
  `purpose_code` VARCHAR(64) NOT NULL COMMENT '归档包用途必填',
  `filter_snapshot_json` JSON NOT NULL,
  `item_count` INT UNSIGNED NOT NULL DEFAULT 0,
  `result_key_cipher` VARBINARY(2048) NULL DEFAULT NULL COMMENT '结果对象键密文',
  `result_content_hash` CHAR(64) NULL DEFAULT NULL,
  `result_file_name` VARCHAR(255) NULL DEFAULT NULL,
  `result_file_size` BIGINT UNSIGNED NULL DEFAULT NULL,
  `encryption_key_reference` VARCHAR(255) NULL DEFAULT NULL COMMENT '归档包密钥托管引用',
  `download_expires_at` DATETIME(3) NULL DEFAULT NULL,
  `download_limit` TINYINT UNSIGNED NOT NULL DEFAULT 1,
  `download_count` TINYINT UNSIGNED NOT NULL DEFAULT 0,
  `completed_at` DATETIME(3) NULL DEFAULT NULL,
  `expired_at` DATETIME(3) NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_avatar_export_job_no` (`job_no`),
  KEY `idx_avatar_export_job_requester_created` (`requester_id`,`created_at`),
  KEY `idx_avatar_export_job_city_status` (`city_org_id`,`status`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='数字形象导出任务';

CREATE TABLE IF NOT EXISTS `avatar_export_item` (
  `id` BIGINT UNSIGNED NOT NULL,
  `export_job_id` BIGINT UNSIGNED NOT NULL,
  `profile_id` BIGINT UNSIGNED NOT NULL,
  `included_asset_count` SMALLINT UNSIGNED NOT NULL DEFAULT 0,
  `status` VARCHAR(40) NOT NULL COMMENT 'PENDING/INCLUDED/SKIPPED',
  `skip_reason_code` VARCHAR(64) NULL DEFAULT NULL COMMENT '已删除/已超期/删除处理中等',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_avatar_export_item_job_profile` (`export_job_id`,`profile_id`),
  KEY `idx_avatar_export_item_profile` (`profile_id`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='数字形象导出任务档案明细';

INSERT INTO `sys_permission` (`id`,`status`,`version`,`permission_code`,`permission_name`,`permission_type`,`resource_code`)
SELECT 90350001,'ACTIVE',1,'AVATAR_PROGRESS_EXPORT','数字形象进度导出','ACTION','AVATAR'
WHERE NOT EXISTS (SELECT 1 FROM `sys_permission` WHERE `permission_code`='AVATAR_PROGRESS_EXPORT');
INSERT INTO `sys_permission` (`id`,`status`,`version`,`permission_code`,`permission_name`,`permission_type`,`resource_code`)
SELECT 90350002,'ACTIVE',1,'AVATAR_ARCHIVE_EXPORT','数字形象受控归档导出','ACTION','AVATAR'
WHERE NOT EXISTS (SELECT 1 FROM `sys_permission` WHERE `permission_code`='AVATAR_ARCHIVE_EXPORT');
INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.id,p.id FROM `sys_role` r JOIN `sys_permission` p ON p.permission_code='AVATAR_PROGRESS_EXPORT'
WHERE r.role_code='PLATFORM_ADMIN'
AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.role_id=r.id AND rp.permission_id=p.id);
INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.id,p.id FROM `sys_role` r JOIN `sys_permission` p ON p.permission_code='AVATAR_PROGRESS_EXPORT'
WHERE r.role_code='CITY_OPERATOR'
AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.role_id=r.id AND rp.permission_id=p.id);
INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.id,p.id FROM `sys_role` r JOIN `sys_permission` p ON p.permission_code='AVATAR_ARCHIVE_EXPORT'
WHERE r.role_code='PLATFORM_ADMIN'
AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.role_id=r.id AND rp.permission_id=p.id);

-- 实名主体名称用于生成私有委托书草稿；不对普通查询接口回传。
ALTER TABLE `creator_identity_verification`
  ADD COLUMN `legal_name` VARCHAR(128) NULL DEFAULT NULL COMMENT '实名认证主体名称；仅本人授权文件生成使用' AFTER `subject_type`;

-- 企业或工作室实名通过后自动生成的委托书草稿；与用户签署后上传留存的委托书分离。
CREATE TABLE IF NOT EXISTS `creator_authorization_letter_draft` (
  `id` BIGINT UNSIGNED NOT NULL,
  `status` VARCHAR(40) NOT NULL,
  `version` INT UNSIGNED NOT NULL DEFAULT 1,
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `created_by` BIGINT UNSIGNED NULL DEFAULT NULL,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `updated_by` BIGINT UNSIGNED NULL DEFAULT NULL,
  `creator_id` BIGINT UNSIGNED NOT NULL,
  `identity_verification_id` BIGINT UNSIGNED NOT NULL,
  `file_id` BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_creator_authorization_letter_draft_creator_id` (`creator_id`),
  UNIQUE KEY `uk_creator_authorization_letter_draft_file_id` (`file_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='创作者主体授权委托书自动生成草稿；仅本人可下载，不等同签署留存文件';

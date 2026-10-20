ALTER TABLE `work_version`
  ADD COLUMN `completed_at` DATETIME(3) NULL DEFAULT NULL COMMENT '作品完成时间' AFTER `creation_description`,
  ADD COLUMN `public_copyright_notice` TEXT NULL COMMENT '公开版权声明' AFTER `completed_at`,
  ADD COLUMN `ai_assisted` TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否使用AI辅助' AFTER `public_copyright_notice`,
  ADD COLUMN `ai_assistance_description` TEXT NULL COMMENT 'AI辅助说明' AFTER `ai_assisted`,
  ADD COLUMN `co_creator_description` TEXT NULL COMMENT '共同创作说明；首期不处理权利拆分' AFTER `ai_assistance_description`,
  ADD COLUMN `new_expression_ratio` TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '创作者自评新增独创表达比例，平台确权准入须不少于51' AFTER `co_creator_description`,
  ADD COLUMN `new_expression_dimensions_json` JSON NULL COMMENT '新增表达维度JSON数组' AFTER `new_expression_ratio`,
  ADD COLUMN `new_expression_description` TEXT NULL COMMENT '新增独创表达说明' AFTER `new_expression_dimensions_json`,
  ADD COLUMN `original_ip_usage_description` TEXT NULL COMMENT '原始IP使用说明' AFTER `new_expression_description`;

CREATE TABLE IF NOT EXISTS `work_material_usage` (
  `id` BIGINT UNSIGNED NOT NULL,
  `work_version_id` BIGINT UNSIGNED NOT NULL COMMENT '作品版本',
  `material_pack_id` BIGINT UNSIGNED NOT NULL COMMENT '已冻结素材包',
  `material_item_id` BIGINT UNSIGNED NOT NULL COMMENT '已冻结素材项',
  `file_id` BIGINT UNSIGNED NOT NULL COMMENT '素材文件',
  `file_hash` CHAR(64) NOT NULL COMMENT '提交时素材文件Hash',
  `created_by` BIGINT UNSIGNED NULL DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_work_material_usage_version_item` (`work_version_id`,`material_item_id`),
  KEY `idx_work_material_usage_pack` (`material_pack_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='二创作品所用的冻结IP素材引用';

ALTER TABLE `work_rights_confirmation`
  ADD COLUMN `work_version_id` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '完成确权的作品版本' AFTER `work_id`,
  ADD COLUMN `confirmation_snapshot_hash` CHAR(64) NULL DEFAULT NULL COMMENT '确权快照Hash' AFTER `file_hash`,
  ADD COLUMN `new_expression_ratio` TINYINT UNSIGNED NULL DEFAULT NULL COMMENT '平台审核认定新增独创表达比例' AFTER `confirmation_snapshot_hash`,
  ADD COLUMN `evidence_id` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '存证记录' AFTER `evidence_status`,
  ADD COLUMN `evidence_receipt_no` VARCHAR(256) NULL DEFAULT NULL COMMENT '存证回执号' AFTER `evidence_id`,
  ADD COLUMN `evidence_transaction_hash` VARCHAR(256) NULL DEFAULT NULL COMMENT '存证交易哈希' AFTER `evidence_receipt_no`,
  ADD COLUMN `evidenced_at` DATETIME(3) NULL DEFAULT NULL COMMENT '存证成功时间' AFTER `evidence_transaction_hash`,
  ADD UNIQUE KEY `uk_work_rights_confirmation_work_version_id` (`work_version_id`);

ALTER TABLE `ip_material_pack`
  ADD COLUMN `manifest_hash` CHAR(64) NULL DEFAULT NULL COMMENT '规范化素材包清单SHA-256' AFTER `scope_description`,
  ADD COLUMN `submission_locked_at` DATETIME(3) NULL DEFAULT NULL COMMENT 'DCI申报或审核提交时锁定时间' AFTER `manifest_hash`,
  ADD COLUMN `submission_manifest_hash` CHAR(64) NULL DEFAULT NULL COMMENT '锁定申报包清单SHA-256' AFTER `submission_locked_at`,
  ADD COLUMN `evidence_id` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '素材包清单存证记录' AFTER `submission_manifest_hash`,
  ADD COLUMN `evidence_status` VARCHAR(40) NOT NULL DEFAULT 'NOT_SUBMITTED' COMMENT 'NOT_SUBMITTED/PENDING_EVIDENCE/EVIDENCED/EVIDENCE_FAILED' AFTER `evidence_id`,
  ADD COLUMN `evidence_receipt_no` VARCHAR(256) NULL DEFAULT NULL AFTER `evidence_status`,
  ADD COLUMN `evidence_transaction_hash` VARCHAR(256) NULL DEFAULT NULL AFTER `evidence_receipt_no`,
  ADD COLUMN `frozen_at` DATETIME(3) NULL DEFAULT NULL COMMENT '确权成功后的冻结时间' AFTER `evidence_transaction_hash`,
  ADD KEY `idx_ip_material_pack_ip_status` (`ip_id`,`status`);

CREATE TABLE IF NOT EXISTS `ip_material_pack_item` (
  `id` BIGINT UNSIGNED NOT NULL,
  `material_pack_id` BIGINT UNSIGNED NOT NULL,
  `file_id` BIGINT UNSIGNED NOT NULL,
  `file_hash` CHAR(64) NOT NULL,
  `material_role` VARCHAR(40) NOT NULL,
  `sort_no` INT UNSIGNED NOT NULL DEFAULT 0,
  `deliverable` TINYINT(1) NOT NULL DEFAULT 1,
  `scope_description` TEXT NULL,
  `created_by` BIGINT UNSIGNED NULL DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_ip_material_pack_item_file` (`material_pack_id`,`file_id`),
  KEY `idx_ip_material_pack_item_pack_sort` (`material_pack_id`,`sort_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='IP入库素材包明细；冻结后禁止修改';

ALTER TABLE `ip_dci_assistance_request`
  ADD COLUMN `submission_manifest_hash` CHAR(64) NULL DEFAULT NULL COMMENT '本次代办锁定的素材包清单Hash' AFTER `request_note`,
  ADD COLUMN `submission_locked_at` DATETIME(3) NULL DEFAULT NULL COMMENT '代办申报包锁定时间' AFTER `submission_manifest_hash`;

ALTER TABLE `auth_order`
  ADD COLUMN `material_pack_manifest_hash` CHAR(64) NULL DEFAULT NULL COMMENT '购买时冻结素材包清单Hash快照' AFTER `purchase_snapshot`;

ALTER TABLE `material_download_log`
  MODIFY COLUMN `contract_id` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '授权合同；合同尚未生成时为空',
  ADD COLUMN `material_item_id` BIGINT UNSIGNED NULL DEFAULT NULL AFTER `material_pack_id`,
  ADD COLUMN `manifest_hash` CHAR(64) NULL DEFAULT NULL AFTER `file_id`,
  ADD COLUMN `failure_code` VARCHAR(64) NULL DEFAULT NULL AFTER `download_result`;

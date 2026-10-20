ALTER TABLE `product_version`
  ADD COLUMN `brand` VARCHAR(200) NULL DEFAULT NULL COMMENT '品牌，非必填' AFTER `product_type`,
  ADD COLUMN `manufacturer` VARCHAR(200) NULL DEFAULT NULL COMMENT '生产方，非必填' AFTER `brand`,
  ADD COLUMN `barcode` VARCHAR(128) NULL DEFAULT NULL COMMENT '条码，非必填' AFTER `manufacturer`,
  ADD COLUMN `tax_rate` DECIMAL(8,4) NULL DEFAULT NULL COMMENT '税率，非必填' AFTER `barcode`;

CREATE TABLE IF NOT EXISTS `product_review_record` (
  `id` BIGINT UNSIGNED NOT NULL,
  `product_version_id` BIGINT UNSIGNED NOT NULL,
  `review_node` VARCHAR(40) NOT NULL COMMENT 'COPYRIGHT_INITIAL/COMPLIANCE_REVIEW',
  `review_result` VARCHAR(20) NOT NULL COMMENT 'PASS/RETURN/REJECT',
  `review_comment` TEXT NULL,
  `supplement_items` TEXT NULL,
  `reviewer_id` BIGINT UNSIGNED NOT NULL,
  `created_by` BIGINT UNSIGNED NULL DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_product_review_record_version_node` (`product_version_id`,`review_node`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='商品版权初审与合规复核记录';

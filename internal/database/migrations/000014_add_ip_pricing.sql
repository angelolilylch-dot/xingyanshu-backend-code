ALTER TABLE `ip_asset`
  ADD COLUMN `suggested_transaction_price_amount` DECIMAL(18,2) NOT NULL DEFAULT 0.00 COMMENT '建议交易价；确权通过后锁定并用于商城交易' AFTER `purchase_notice`;

ALTER TABLE `ip_evaluation_report`
  ADD COLUMN `evaluation_suggested_price_amount` DECIMAL(18,2) NULL DEFAULT NULL COMMENT '评估建议价格；非必填，仅作定价参考' AFTER `file_id`;

ALTER TABLE `auth_plan_version`
  DROP COLUMN `price_amount`;

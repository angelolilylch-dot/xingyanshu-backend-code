ALTER TABLE `consumer_account`
  ADD COLUMN `agreement_version` VARCHAR(64) NOT NULL DEFAULT '' COMMENT '首次登录同意的用户协议与隐私政策版本' AFTER `mobile_masked`,
  ADD COLUMN `agreement_accepted_at` DATETIME(3) NULL DEFAULT NULL COMMENT '最近一次同意协议时间' AFTER `agreement_version`;

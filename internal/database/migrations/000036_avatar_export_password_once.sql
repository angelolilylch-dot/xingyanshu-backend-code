-- 受控归档包：解压密码只允许展示一次；未保存时只能作废后重新生成。
ALTER TABLE `avatar_export_job`
  ADD COLUMN `password_cipher` VARBINARY(2048) NULL DEFAULT NULL COMMENT '归档包一次性解压密码密文' AFTER `encryption_key_reference`,
  ADD COLUMN `password_revealed_at` DATETIME(3) NULL DEFAULT NULL COMMENT '首次也是唯一一次展示时间' AFTER `password_cipher`,
  ADD COLUMN `password_revealed_by` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '查看密码的平台管理员' AFTER `password_revealed_at`,
  ADD COLUMN `invalidated_at` DATETIME(3) NULL DEFAULT NULL COMMENT '包/密码作废时间' AFTER `expired_at`,
  ADD COLUMN `invalidated_reason` VARCHAR(500) NULL DEFAULT NULL COMMENT '作废原因' AFTER `invalidated_at`;

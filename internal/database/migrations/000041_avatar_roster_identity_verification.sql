ALTER TABLE `avatar_roster_member`
  ADD COLUMN `id_number_cipher` VARBINARY(1024) NULL DEFAULT NULL COMMENT '身份证号密文，仅实名服务核验通过后保存' AFTER `name_hash`,
  ADD COLUMN `id_number_hash` CHAR(64) NULL DEFAULT NULL COMMENT '身份证号 SHA-256 检索指纹' AFTER `id_number_cipher`,
  ADD COLUMN `id_number_last4` CHAR(4) NULL DEFAULT NULL COMMENT '身份证号末四位，仅后台脱敏展示' AFTER `id_number_hash`,
  ADD COLUMN `identity_provider_code` VARCHAR(40) NULL DEFAULT NULL COMMENT '导入实名服务商编码' AFTER `id_number_last4`,
  ADD COLUMN `identity_provider_request_no` VARCHAR(128) NULL DEFAULT NULL COMMENT '实名服务请求号' AFTER `identity_provider_code`,
  ADD COLUMN `identity_response_digest` CHAR(64) NULL DEFAULT NULL COMMENT '实名服务响应摘要，不保存原始响应' AFTER `identity_provider_request_no`,
  ADD COLUMN `identity_verified_at` DATETIME(3) NULL DEFAULT NULL COMMENT '导入实名核验通过时间' AFTER `identity_response_digest`,
  ADD UNIQUE KEY `uk_avatar_roster_member_city_id_hash` (`city_org_id`, `id_number_hash`),
  ADD KEY `idx_avatar_roster_member_identity_verified` (`identity_verified_at`);

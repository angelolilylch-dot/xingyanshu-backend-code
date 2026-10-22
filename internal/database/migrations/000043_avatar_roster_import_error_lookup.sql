-- 让扫码用户可获得与本人导入行关联的可操作错误，不暴露其他人员资料。
ALTER TABLE `avatar_roster_import_error`
  ADD COLUMN `name_hash` CHAR(64) NULL COMMENT '导入姓名哈希，用于本人绑定失败诊断' AFTER `error_message`,
  ADD COLUMN `mobile_hash` CHAR(64) NULL COMMENT '导入手机号哈希，用于本人绑定失败诊断' AFTER `name_hash`,
  ADD COLUMN `id_number_hash` CHAR(64) NULL COMMENT '导入身份证号哈希，仅审计去重使用' AFTER `mobile_hash`,
  ADD KEY `idx_avatar_roster_import_error_lookup` (`name_hash`, `mobile_hash`, `created_at`);

-- quota_total 为空表示普通授权不限名额；历史默认值 0 迁移为 NULL。
UPDATE `auth_plan_version` SET `quota_total` = NULL WHERE `quota_total` = 0;
ALTER TABLE `auth_plan_version`
  MODIFY COLUMN `quota_total` INT UNSIGNED NULL DEFAULT NULL COMMENT '授权名额；NULL表示不限额，独家买断必须为1';

-- 后台账号数据范围：一期每个账号仅一个角色，范围直接归属账号。
ALTER TABLE `sys_user`
  ADD COLUMN `data_scope` VARCHAR(20) NOT NULL DEFAULT 'SELF' COMMENT '数据范围；ALL 全平台 / CITY 本城市 / ORG 本组织 / SELF 仅本人' AFTER `org_id`;

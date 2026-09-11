ALTER TABLE `finance_distribution_plan_item`
  ADD COLUMN `is_required` TINYINT(1) NOT NULL DEFAULT 1 COMMENT '是否必选参与方；城市运营填写IP最终比例时必须覆盖必选方' AFTER `participant_type`;

ALTER TABLE `auth_plan_version`
  ADD COLUMN `distribution_template_id` BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '分账结构模板；逻辑关联finance_distribution_plan.id' AFTER `plan_id`,
  ADD KEY `idx_auth_plan_version_distribution_template_id` (`distribution_template_id`);

-- 为已部署系统补齐方案维护与审核权限；不使用物理外键，权限关系由应用层维护。
INSERT INTO `sys_permission` (`id`, `status`, `version`, `permission_code`, `permission_name`, `permission_type`, `resource_code`)
SELECT COALESCE(MAX(`id`), 10200) + 1, 'ACTIVE', 1, 'AUTH_PLAN_MANAGE', '授权与分账方案管理', 'ACTION', 'B05'
FROM `sys_permission`
WHERE NOT EXISTS (SELECT 1 FROM `sys_permission` p WHERE p.`permission_code` = 'AUTH_PLAN_MANAGE');

INSERT INTO `sys_permission` (`id`, `status`, `version`, `permission_code`, `permission_name`, `permission_type`, `resource_code`)
SELECT COALESCE(MAX(`id`), 10200) + 1, 'ACTIVE', 1, 'AUTH_PLAN_REVIEW', '授权方案合规审核', 'ACTION', 'B05'
FROM `sys_permission`
WHERE NOT EXISTS (SELECT 1 FROM `sys_permission` p WHERE p.`permission_code` = 'AUTH_PLAN_REVIEW');

INSERT INTO `sys_role_permission` (`id`, `role_id`, `permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`), 10300) + 1, r.`id`, p.`id`
FROM `sys_role` r JOIN `sys_permission` p ON p.`permission_code` = 'AUTH_PLAN_MANAGE'
WHERE r.`role_code` = 'PLATFORM_ADMIN'
  AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.`role_id` = r.`id` AND rp.`permission_id` = p.`id`);

INSERT INTO `sys_role_permission` (`id`, `role_id`, `permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`), 10300) + 1, r.`id`, p.`id`
FROM `sys_role` r JOIN `sys_permission` p ON p.`permission_code` = 'AUTH_PLAN_REVIEW'
WHERE r.`role_code` = 'COPYRIGHT_COMPLIANCE'
  AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.`role_id` = r.`id` AND rp.`permission_id` = p.`id`);

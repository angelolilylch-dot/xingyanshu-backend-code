-- 城市运营者名单导入、长期二维码和建档通知。
-- 行级错误独立落表，避免无导入人数上限时 error_summary_json 膨胀。

CREATE TABLE IF NOT EXISTS `avatar_roster_import_error` (
  `id` BIGINT UNSIGNED NOT NULL,
  `roster_batch_id` BIGINT UNSIGNED NOT NULL,
  `row_no` INT UNSIGNED NOT NULL,
  `error_code` VARCHAR(64) NOT NULL,
  `error_message` VARCHAR(500) NOT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_avatar_roster_import_error_batch_row` (`roster_batch_id`,`row_no`),
  KEY `idx_avatar_roster_import_error_batch` (`roster_batch_id`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='数字形象名单导入行级错误';

INSERT INTO `sys_permission` (`id`,`status`,`version`,`permission_code`,`permission_name`,`permission_type`,`resource_code`)
SELECT 90350003,'ACTIVE',1,'AVATAR_ROSTER_MANAGE','数字形象名单管理','ACTION','AVATAR'
WHERE NOT EXISTS (SELECT 1 FROM `sys_permission` WHERE `permission_code`='AVATAR_ROSTER_MANAGE');
INSERT INTO `sys_permission` (`id`,`status`,`version`,`permission_code`,`permission_name`,`permission_type`,`resource_code`)
SELECT 90350004,'ACTIVE',1,'AVATAR_REGISTRATION_ENTRY_MANAGE','数字形象建档入口管理','ACTION','AVATAR'
WHERE NOT EXISTS (SELECT 1 FROM `sys_permission` WHERE `permission_code`='AVATAR_REGISTRATION_ENTRY_MANAGE');
INSERT INTO `sys_permission` (`id`,`status`,`version`,`permission_code`,`permission_name`,`permission_type`,`resource_code`)
SELECT 90350005,'ACTIVE',1,'AVATAR_INVITATION_NOTIFY','数字形象建档通知','ACTION','AVATAR'
WHERE NOT EXISTS (SELECT 1 FROM `sys_permission` WHERE `permission_code`='AVATAR_INVITATION_NOTIFY');
INSERT INTO `sys_permission` (`id`,`status`,`version`,`permission_code`,`permission_name`,`permission_type`,`resource_code`)
SELECT 90350006,'ACTIVE',1,'AVATAR_PROGRESS_VIEW','数字形象建档进度查看','ACTION','AVATAR'
WHERE NOT EXISTS (SELECT 1 FROM `sys_permission` WHERE `permission_code`='AVATAR_PROGRESS_VIEW');

INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.id,p.id FROM `sys_role` r JOIN `sys_permission` p ON p.permission_code='AVATAR_ROSTER_MANAGE'
WHERE r.role_code='CITY_OPERATOR' AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.role_id=r.id AND rp.permission_id=p.id);
INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.id,p.id FROM `sys_role` r JOIN `sys_permission` p ON p.permission_code='AVATAR_REGISTRATION_ENTRY_MANAGE'
WHERE r.role_code='CITY_OPERATOR' AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.role_id=r.id AND rp.permission_id=p.id);
INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.id,p.id FROM `sys_role` r JOIN `sys_permission` p ON p.permission_code='AVATAR_INVITATION_NOTIFY'
WHERE r.role_code='CITY_OPERATOR' AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.role_id=r.id AND rp.permission_id=p.id);
INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.id,p.id FROM `sys_role` r JOIN `sys_permission` p ON p.permission_code='AVATAR_PROGRESS_VIEW'
WHERE r.role_code='CITY_OPERATOR' AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.role_id=r.id AND rp.permission_id=p.id);

INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.id,p.id FROM `sys_role` r JOIN `sys_permission` p ON p.permission_code='AVATAR_ROSTER_MANAGE'
WHERE r.role_code='PLATFORM_ADMIN' AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.role_id=r.id AND rp.permission_id=p.id);
INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.id,p.id FROM `sys_role` r JOIN `sys_permission` p ON p.permission_code='AVATAR_REGISTRATION_ENTRY_MANAGE'
WHERE r.role_code='PLATFORM_ADMIN' AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.role_id=r.id AND rp.permission_id=p.id);
INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.id,p.id FROM `sys_role` r JOIN `sys_permission` p ON p.permission_code='AVATAR_INVITATION_NOTIFY'
WHERE r.role_code='PLATFORM_ADMIN' AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.role_id=r.id AND rp.permission_id=p.id);
INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.id,p.id FROM `sys_role` r JOIN `sys_permission` p ON p.permission_code='AVATAR_PROGRESS_VIEW'
WHERE r.role_code='PLATFORM_ADMIN' AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.role_id=r.id AND rp.permission_id=p.id);

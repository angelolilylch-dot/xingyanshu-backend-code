-- 个人数字形象管理是城市运营者与平台管理员共用的一级菜单。
INSERT INTO `sys_permission` (`id`,`status`,`version`,`permission_code`,`permission_name`,`permission_type`,`resource_code`)
SELECT 90350007,'ACTIVE',1,'MENU_AVATAR_MANAGEMENT','个人数字形象管理','MENU','AVATAR'
WHERE NOT EXISTS (SELECT 1 FROM `sys_permission` WHERE `permission_code`='MENU_AVATAR_MANAGEMENT');

INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.id,p.id
FROM `sys_role` r JOIN `sys_permission` p ON p.permission_code='MENU_AVATAR_MANAGEMENT'
WHERE r.role_code IN ('CITY_OPERATOR','PLATFORM_ADMIN')
  AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.role_id=r.id AND rp.permission_id=p.id);

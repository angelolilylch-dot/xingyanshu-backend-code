-- AVATAR-OPS-02: platform-only manual quality review permissions.
INSERT INTO `sys_permission` (`id`,`permission_code`,`permission_name`,`permission_type`,`status`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_permission`),0)+1,'AVATAR_QUALITY_REVIEW','数字形象人工质检','API','ACTIVE'
WHERE NOT EXISTS (SELECT 1 FROM `sys_permission` WHERE `permission_code`='AVATAR_QUALITY_REVIEW');

INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.id,p.id
FROM `sys_role` r JOIN `sys_permission` p ON p.permission_code='AVATAR_QUALITY_REVIEW'
WHERE r.role_code='PLATFORM_ADMIN'
  AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.role_id=r.id AND rp.permission_id=p.id);

-- 创作者端菜单与已完成的 C01/C02 对齐；C05 在实现前不应显示为可进入菜单。
DELETE rp
FROM `sys_role_permission` rp
JOIN `sys_permission` p ON p.id = rp.permission_id
WHERE p.permission_code = 'MENU_CREATOR_CONTENT';

DELETE FROM `sys_permission`
WHERE permission_code = 'MENU_CREATOR_CONTENT';

INSERT INTO sys_permission (id, status, version, permission_code, permission_name, permission_type, resource_code)
SELECT COALESCE(MAX(id), 10200) + 1, 'ACTIVE', 1,
       'MENU_CREATOR_WORKSPACE', '创作者工作台', 'MENU', 'C01'
FROM sys_permission
WHERE NOT EXISTS (SELECT 1 FROM sys_permission WHERE permission_code = 'MENU_CREATOR_WORKSPACE');

INSERT INTO sys_permission (id, status, version, permission_code, permission_name, permission_type, resource_code)
SELECT COALESCE(MAX(id), 10200) + 1, 'ACTIVE', 1,
       'MENU_CREATOR_ONBOARDING', '注册/实名认证', 'MENU', 'C01'
FROM sys_permission
WHERE NOT EXISTS (SELECT 1 FROM sys_permission WHERE permission_code = 'MENU_CREATOR_ONBOARDING');

INSERT INTO sys_permission (id, status, version, permission_code, permission_name, permission_type, resource_code)
SELECT COALESCE(MAX(id), 10200) + 1, 'ACTIVE', 1,
       'MENU_CREATOR_IP_MALL', 'IP 商城', 'MENU', 'C02'
FROM sys_permission
WHERE NOT EXISTS (SELECT 1 FROM sys_permission WHERE permission_code = 'MENU_CREATOR_IP_MALL');

INSERT INTO sys_role_permission (id, role_id, permission_id)
SELECT (SELECT COALESCE(MAX(id), 10300) + 1 FROM sys_role_permission), r.id, p.id
FROM sys_role r
JOIN sys_permission p ON p.permission_code = 'MENU_CREATOR_WORKSPACE'
WHERE r.role_code = 'CREATOR'
  AND NOT EXISTS (SELECT 1 FROM sys_role_permission x WHERE x.role_id = r.id AND x.permission_id = p.id);

INSERT INTO sys_role_permission (id, role_id, permission_id)
SELECT (SELECT COALESCE(MAX(id), 10300) + 1 FROM sys_role_permission), r.id, p.id
FROM sys_role r
JOIN sys_permission p ON p.permission_code = 'MENU_CREATOR_ONBOARDING'
WHERE r.role_code = 'CREATOR'
  AND NOT EXISTS (SELECT 1 FROM sys_role_permission x WHERE x.role_id = r.id AND x.permission_id = p.id);

INSERT INTO sys_role_permission (id, role_id, permission_id)
SELECT (SELECT COALESCE(MAX(id), 10300) + 1 FROM sys_role_permission), r.id, p.id
FROM sys_role r
JOIN sys_permission p ON p.permission_code = 'MENU_CREATOR_IP_MALL'
WHERE r.role_code = 'CREATOR'
  AND NOT EXISTS (SELECT 1 FROM sys_role_permission x WHERE x.role_id = r.id AND x.permission_id = p.id);

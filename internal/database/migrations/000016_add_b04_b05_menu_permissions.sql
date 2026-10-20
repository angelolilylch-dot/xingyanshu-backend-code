-- B04/B05 页面菜单权限：供 /auth/me 返回平台管理员可见菜单。
-- 业务接口原有 AUTH_PLAN_MANAGE ACTION 权限继续保留，用于接口鉴权。
INSERT INTO sys_permission
    (id, status, version, permission_code, permission_name, permission_type, resource_code)
SELECT COALESCE(MAX(id), 10200) + 1, 'ACTIVE', 1,
       'MENU_AUTH_PLANS', '授权方案管理', 'MENU', 'B04'
FROM sys_permission
WHERE NOT EXISTS (
    SELECT 1 FROM sys_permission WHERE permission_code = 'MENU_AUTH_PLANS'
);

INSERT INTO sys_permission
    (id, status, version, permission_code, permission_name, permission_type, resource_code)
SELECT COALESCE(MAX(id), 10200) + 1, 'ACTIVE', 1,
       'MENU_DISTRIBUTION_TEMPLATES', '分账结构模板', 'MENU', 'B05'
FROM sys_permission
WHERE NOT EXISTS (
    SELECT 1 FROM sys_permission WHERE permission_code = 'MENU_DISTRIBUTION_TEMPLATES'
);

INSERT INTO sys_role_permission (id, role_id, permission_id)
SELECT (SELECT COALESCE(MAX(id), 10300) + 1 FROM sys_role_permission), r.id, p.id
FROM sys_role r
JOIN sys_permission p ON p.permission_code = 'MENU_AUTH_PLANS'
WHERE r.role_code = 'PLATFORM_ADMIN'
  AND NOT EXISTS (
      SELECT 1 FROM sys_role_permission x
      WHERE x.role_id = r.id AND x.permission_id = p.id
  );

INSERT INTO sys_role_permission (id, role_id, permission_id)
SELECT (SELECT COALESCE(MAX(id), 10300) + 1 FROM sys_role_permission), r.id, p.id
FROM sys_role r
JOIN sys_permission p ON p.permission_code = 'MENU_DISTRIBUTION_TEMPLATES'
WHERE r.role_code = 'PLATFORM_ADMIN'
  AND NOT EXISTS (
      SELECT 1 FROM sys_role_permission x
      WHERE x.role_id = r.id AND x.permission_id = p.id
  );

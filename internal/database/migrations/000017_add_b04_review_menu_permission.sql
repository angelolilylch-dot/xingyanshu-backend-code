-- 版权合规人员的 B04 授权方案审核菜单权限。
INSERT INTO sys_permission
    (id, status, version, permission_code, permission_name, permission_type, resource_code)
SELECT (SELECT COALESCE(MAX(id), 10200) + 1 FROM sys_permission), 'ACTIVE', 1,
       'MENU_AUTH_PLAN_REVIEW', '授权方案合规审核', 'MENU', 'B04'
WHERE NOT EXISTS (
    SELECT 1 FROM sys_permission WHERE permission_code = 'MENU_AUTH_PLAN_REVIEW'
);

INSERT INTO sys_role_permission (id, role_id, permission_id)
SELECT (SELECT COALESCE(MAX(id), 10300) + 1 FROM sys_role_permission), r.id, p.id
FROM sys_role r
JOIN sys_permission p ON p.permission_code = 'MENU_AUTH_PLAN_REVIEW'
WHERE r.role_code = 'COPYRIGHT_COMPLIANCE'
  AND NOT EXISTS (
      SELECT 1 FROM sys_role_permission x
      WHERE x.role_id = r.id AND x.permission_id = p.id
  );

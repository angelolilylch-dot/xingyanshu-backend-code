-- 菜单权限统一使用 MENU_* 编码，避免同一资源在 /auth/me 中出现重复菜单。
DELETE rp
FROM `sys_role_permission` rp
JOIN `sys_permission` p ON p.id = rp.permission_id
WHERE p.permission_code IN ('CREATOR_CONTENT_MANAGE', 'FINANCE_SETTLEMENT_MANAGE', 'FULFILLMENT_MANAGE');

DELETE FROM `sys_permission`
WHERE permission_code IN ('CREATOR_CONTENT_MANAGE', 'FINANCE_SETTLEMENT_MANAGE', 'FULFILLMENT_MANAGE');

-- 经营看板仅集团管理层可见，清除早期种子中平台管理员的历史关联。
DELETE rp
FROM `sys_role_permission` rp
JOIN `sys_role` r ON r.id = rp.role_id
JOIN `sys_permission` p ON p.id = rp.permission_id
WHERE r.role_code = 'PLATFORM_ADMIN' AND p.permission_code = 'DASHBOARD_VIEW_ALL';

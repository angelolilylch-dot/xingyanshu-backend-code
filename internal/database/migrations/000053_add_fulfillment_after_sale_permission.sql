-- API-AFS-01 / API-AFS-02: reserve the separate menu and action permission
-- for fulfilment after-sale handling. The action is granted now, while AFS-02
-- routes will enforce it when review and return-receipt operations are added.
INSERT INTO `sys_permission` (`id`,`status`,`version`,`permission_code`,`permission_name`,`permission_type`,`resource_code`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_permission`), 900053000) + 1,
       'ACTIVE', 1, 'MENU_FULFILLMENT_AFTER_SALE', '售后处理', 'MENU', 'D03'
WHERE NOT EXISTS (SELECT 1 FROM `sys_permission` p WHERE p.`permission_code`='MENU_FULFILLMENT_AFTER_SALE');

INSERT INTO `sys_permission` (`id`,`status`,`version`,`permission_code`,`permission_name`,`permission_type`,`resource_code`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_permission`), 900053000) + 1,
       'ACTIVE', 1, 'FULFILLMENT_AFTER_SALE_MANAGE', '商品售后处理', 'ACTION', 'D03'
WHERE NOT EXISTS (SELECT 1 FROM `sys_permission` p WHERE p.`permission_code`='FULFILLMENT_AFTER_SALE_MANAGE');

INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.`id`,p.`id`
FROM `sys_role` r JOIN `sys_permission` p ON p.`permission_code`='MENU_FULFILLMENT_AFTER_SALE'
WHERE r.`role_code`='FULFILLMENT_CUSTOMER_SERVICE'
  AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.`role_id`=r.`id` AND rp.`permission_id`=p.`id`);

INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.`id`,p.`id`
FROM `sys_role` r JOIN `sys_permission` p ON p.`permission_code`='FULFILLMENT_AFTER_SALE_MANAGE'
WHERE r.`role_code`='FULFILLMENT_CUSTOMER_SERVICE'
  AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.`role_id`=r.`id` AND rp.`permission_id`=p.`id`);

-- API-FUL-01: grant the explicit shipment/logistics action to fulfilment staff.
INSERT INTO `sys_permission` (`id`,`status`,`version`,`permission_code`,`permission_name`,`permission_type`,`resource_code`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_permission`), 900047000) + 1,
       'ACTIVE', 1, 'FULFILLMENT_SHIPMENT_MANAGE', '商品发货与物流处理', 'ACTION', 'D01'
WHERE NOT EXISTS (SELECT 1 FROM `sys_permission` p WHERE p.`permission_code`='FULFILLMENT_SHIPMENT_MANAGE');

INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.`id`,p.`id`
FROM `sys_role` r JOIN `sys_permission` p ON p.`permission_code`='FULFILLMENT_SHIPMENT_MANAGE'
WHERE r.`role_code`='FULFILLMENT_CUSTOMER_SERVICE'
  AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.`role_id`=r.`id` AND rp.`permission_id`=p.`id`);

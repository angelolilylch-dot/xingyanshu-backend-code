-- API-AFS-02: finance owns monetary refunds and fulfilment owns case review and return receipt.
INSERT INTO `sys_permission` (`id`,`status`,`version`,`permission_code`,`permission_name`,`permission_type`,`resource_code`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_permission`), 900055000) + 1,
       'ACTIVE', 1, 'FINANCE_REFUND_MANAGE', '售后退款处理', 'ACTION', 'E01'
WHERE NOT EXISTS (SELECT 1 FROM `sys_permission` p WHERE p.`permission_code`='FINANCE_REFUND_MANAGE');

INSERT INTO `sys_role_permission` (`id`,`role_id`,`permission_id`)
SELECT COALESCE((SELECT MAX(`id`) FROM `sys_role_permission`),0)+1,r.`id`,p.`id`
FROM `sys_role` r JOIN `sys_permission` p ON p.`permission_code`='FINANCE_REFUND_MANAGE'
WHERE r.`role_code`='FINANCE'
  AND NOT EXISTS (SELECT 1 FROM `sys_role_permission` rp WHERE rp.`role_id`=r.`id` AND rp.`permission_id`=p.`id`);

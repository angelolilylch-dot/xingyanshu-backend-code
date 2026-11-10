-- API-FND-DICT-01: provide the four confirmed activity ticket types on every
-- upgraded database, and hide the previous bootstrap-only alternatives.
UPDATE `sys_dict_item`
SET `enabled` = 0,
    `status` = 'SUSPENDED',
    `status_reason_code` = 'DEFAULT_TICKET_TYPE_RETIRED',
    `status_reason` = '已由平台默认活动票种替换',
    `version` = `version` + 1,
    `updated_at` = CURRENT_TIMESTAMP(3)
WHERE `dict_type` = 'ACTIVITY_TICKET_TYPE'
  AND `dict_code` NOT IN ('GENERAL', 'VIP', 'STUDENT', 'FREE')
  AND (`enabled` = 1 OR `status` <> 'SUSPENDED');

INSERT INTO `sys_dict_item`
(`id`, `status`, `version`, `dict_type`, `dict_code`, `dict_name`, `sort_no`, `enabled`)
VALUES
  (900046001, 'ACTIVE', 1, 'ACTIVITY_TICKET_TYPE', 'GENERAL', '普通票', 1, 1),
  (900046002, 'ACTIVE', 1, 'ACTIVITY_TICKET_TYPE', 'VIP', 'VIP票', 2, 1),
  (900046003, 'ACTIVE', 1, 'ACTIVITY_TICKET_TYPE', 'STUDENT', '学生票', 3, 1),
  (900046004, 'ACTIVE', 1, 'ACTIVITY_TICKET_TYPE', 'FREE', '免费票', 4, 1)
ON DUPLICATE KEY UPDATE
  `dict_name` = VALUES(`dict_name`),
  `sort_no` = VALUES(`sort_no`);

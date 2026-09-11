ALTER TABLE `finance_distribution_plan`
  ADD COLUMN `is_bound` TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否已被IP使用；使用后禁止修改参与方结构或停用' AFTER `current_version_no`;

ALTER TABLE `finance_distribution_plan_item`
  MODIFY COLUMN `ratio` DECIMAL(9,6) NULL DEFAULT NULL COMMENT '模板不设置比例；IP最终比例写入ip_distribution_item';

CREATE TABLE IF NOT EXISTS `ip_distribution_item` (
  `id` BIGINT UNSIGNED NOT NULL COMMENT '主键；内部主键',
  `ip_id` BIGINT UNSIGNED NOT NULL COMMENT 'IP资产；逻辑关联ip_asset.id',
  `distribution_template_id` BIGINT UNSIGNED NOT NULL COMMENT '分账结构模板；逻辑关联finance_distribution_plan.id',
  `template_version_no` INT UNSIGNED NOT NULL DEFAULT 1 COMMENT '模板版本',
  `participant_type` VARCHAR(40) NOT NULL COMMENT '参与方类型',
  `ratio` DECIMAL(9,6) NOT NULL COMMENT 'IP最终确认比例；合计必须为1',
  `confirmed_by` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '最终确认人；版权合规',
  `confirmed_at` DATETIME(3) NULL DEFAULT NULL COMMENT '最终确认时间',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_ip_distribution_item_ip_participant` (`ip_id`, `participant_type`),
  KEY `idx_ip_distribution_item_template` (`distribution_template_id`, `template_version_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='IP最终分账比例：城市运营填写、版权合规确认后冻结';

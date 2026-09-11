CREATE TABLE IF NOT EXISTS `ip_draft_auth_selection` (
  `id` BIGINT UNSIGNED NOT NULL COMMENT '主键；内部主键',
  `ip_id` BIGINT UNSIGNED NOT NULL COMMENT 'IP资产；逻辑关联ip_asset.id',
  `auth_plan_version_id` BIGINT UNSIGNED NOT NULL COMMENT '城市运营初选授权方案版本；逻辑关联auth_plan_version.id',
  `distribution_template_id` BIGINT UNSIGNED NOT NULL COMMENT '城市运营初选分账结构模板；逻辑关联finance_distribution_plan.id',
  `distribution_template_version_no` INT UNSIGNED NOT NULL DEFAULT 1 COMMENT '模板版本号',
  `created_by` BIGINT UNSIGNED NOT NULL COMMENT '创建人；城市运营用户ID',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间',
  `updated_by` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '更新人',
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_ip_draft_auth_selection_ip_id` (`ip_id`),
  KEY `idx_ip_draft_auth_selection_auth_plan_version_id` (`auth_plan_version_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='IP草稿方案初选：城市运营初选方案和模板，确权时转为正式授权绑定';

-- 分账结构模板为独立可复用主数据；实际模板及比例在 IP 入库/确权时绑定并冻结。
-- 保留历史方案原有值用于数据兼容，但新方案不再写入、接口不再返回该字段。
ALTER TABLE `auth_plan_version`
  MODIFY COLUMN `distribution_template_id` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '历史兼容字段；实际分账模板在IP确权绑定中确定';

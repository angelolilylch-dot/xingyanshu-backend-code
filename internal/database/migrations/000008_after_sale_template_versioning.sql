-- 固定售后模板采用追加版本模式，已发布商品/活动继续引用原模板 ID，禁止原地改写。
ALTER TABLE `after_sale_rule_template`
  DROP INDEX `uk_after_sale_rule_template_template_no`,
  ADD UNIQUE KEY `uk_after_sale_rule_template_template_no_version_no` (`template_no`, `version_no`);

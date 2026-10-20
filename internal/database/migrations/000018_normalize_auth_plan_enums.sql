-- 统一授权方案枚举：旧码仅在本迁移中兼容，后续接口只接受标准码。
UPDATE `auth_plan_version`
SET `term_type` = CASE `term_type`
  WHEN 'MONTHS' THEN 'MONTH'
  WHEN 'YEARS' THEN 'YEAR'
  ELSE `term_type`
END,
`region_mode` = CASE `region_mode`
  WHEN 'REGIONS' THEN 'DESIGNATED'
  ELSE `region_mode`
END,
`content_type` = CASE `content_type`
  WHEN 'PRODUCT' THEN 'PRODUCT_ONLY'
  WHEN 'ACTIVITY' THEN 'ACTIVITY_ONLY'
  WHEN 'PRODUCT_ACTIVITY' THEN 'PRODUCT_AND_ACTIVITY'
  ELSE `content_type`
END;

UPDATE `auth_plan_version`
SET `scenes_json` = REPLACE(`scenes_json`, '"PRODUCT"', '"PRODUCT_SALE"')
WHERE JSON_CONTAINS(`scenes_json`, JSON_QUOTE('PRODUCT'));

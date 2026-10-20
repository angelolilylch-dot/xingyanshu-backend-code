-- 统一授权分账参与方编码，后续接口仅接受 RIGHT_HOLDER、CREATOR、CITY_OPERATOR、PLATFORM。
DELETE legacy
FROM `finance_distribution_plan_item` legacy
JOIN `finance_distribution_plan_item` canonical
  ON canonical.`distribution_plan_id` = legacy.`distribution_plan_id`
 AND canonical.`version_no` = legacy.`version_no`
 AND canonical.`participant_type` = 'RIGHT_HOLDER'
WHERE legacy.`participant_type` = 'IP_HOLDER';

UPDATE `finance_distribution_plan_item`
SET `participant_type` = 'RIGHT_HOLDER'
WHERE `participant_type` = 'IP_HOLDER';

DELETE legacy
FROM `ip_distribution_item` legacy
JOIN `ip_distribution_item` canonical
  ON canonical.`ip_id` = legacy.`ip_id`
 AND canonical.`participant_type` = 'RIGHT_HOLDER'
WHERE legacy.`participant_type` = 'IP_HOLDER';

UPDATE `ip_distribution_item`
SET `participant_type` = 'RIGHT_HOLDER'
WHERE `participant_type` = 'IP_HOLDER';

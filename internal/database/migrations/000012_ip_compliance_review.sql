-- API-IP-02：DCI 代办由版权/合规人员处理，确权审核记录按节点查询。
ALTER TABLE `ip_dci_assistance_request`
  MODIFY COLUMN `handled_by` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '处理人；版权/合规用户ID';

CREATE INDEX `idx_ip_review_record_ip_id_reviewer_created_at`
  ON `ip_review_record` (`ip_id`, `reviewer_id`, `created_at`);

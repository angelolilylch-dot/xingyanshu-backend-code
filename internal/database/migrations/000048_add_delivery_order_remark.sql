-- API-FUL-01: persist the optional shipment remark returned by the API.
ALTER TABLE `delivery_order`
  ADD COLUMN `remark` VARCHAR(500) NULL DEFAULT NULL COMMENT '发货备注' AFTER `proof_file_id`;

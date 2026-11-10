-- AFS-01: 商品售后按订单明细和申请数量处理，并保存进入售后前的业务状态以便撤回/驳回恢复。
ALTER TABLE `after_sale_ticket`
  ADD COLUMN `product_order_item_id` BIGINT UNSIGNED NULL COMMENT '商品订单明细；仅商品售后使用' AFTER `biz_order_id`,
  ADD COLUMN `apply_quantity` INT UNSIGNED NOT NULL DEFAULT 1 COMMENT '申请售后数量；商品可部分数量，活动等于预约数量' AFTER `after_sale_type`,
  ADD COLUMN `original_biz_status` VARCHAR(40) NOT NULL DEFAULT '' COMMENT '申请前订单或预约状态；撤回/驳回时恢复' AFTER `apply_amount`,
  ADD KEY `idx_after_sale_ticket_product_order_item_status` (`product_order_item_id`, `status`);

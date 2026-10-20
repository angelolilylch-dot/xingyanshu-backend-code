-- 链服务成功回执除服务商回执编号外，还需保存可供业务端展示的链上交易/存证哈希。
ALTER TABLE `evidence_record`
  ADD COLUMN `chain_transaction_hash` VARCHAR(256) NULL DEFAULT NULL COMMENT '链上交易/存证哈希；SUCCESS 必填' AFTER `receipt_no`,
  ADD UNIQUE KEY `uk_evidence_record_chain_transaction_hash` (`chain_transaction_hash`);

-- PROCESSING 状态尚未产生首次响应，因此完成时间在首次写入时允许为空。
ALTER TABLE `api_idempotency_record`
  MODIFY COLUMN `completed_at` DATETIME(3) NULL DEFAULT NULL COMMENT '首次处理完成时间；处理中为空';

ALTER TABLE `avatar_identity_verification`
  ADD COLUMN `profile_id` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '关联数字形象档案' AFTER `account_id`,
  ADD COLUMN `collection_session_id` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '触发核验的采集会话' AFTER `profile_id`,
  ADD COLUMN `asset_id` BIGINT UNSIGNED NULL DEFAULT NULL COMMENT '用于核验的 FACE_01 素材' AFTER `collection_session_id`,
  ADD COLUMN `provider_result_code` VARCHAR(128) NULL DEFAULT NULL COMMENT '服务商业务结果码' AFTER `provider_request_no`,
  ADD COLUMN `face_similarity` DECIMAL(5,2) NULL DEFAULT NULL COMMENT '照片与权威库证件照相似度 0-100' AFTER `provider_result_code`,
  ADD KEY `idx_avatar_identity_verification_face_pending` (`verification_type`, `status`, `created_at`),
  ADD KEY `idx_avatar_identity_verification_collection` (`collection_session_id`);

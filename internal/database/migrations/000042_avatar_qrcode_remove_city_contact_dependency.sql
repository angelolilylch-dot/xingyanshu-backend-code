-- 二维码仅定位城市，建档咨询电话统一来自 avatar.fallback_support_phone 配置。
-- 历史城市联系人记录保留但不再作为二维码创建或 H5 展示的前置条件。
ALTER TABLE `avatar_registration_qrcode`
  MODIFY COLUMN `city_registration_config_id` BIGINT UNSIGNED NULL COMMENT '历史城市入口配置；二维码不再依赖城市联系人';

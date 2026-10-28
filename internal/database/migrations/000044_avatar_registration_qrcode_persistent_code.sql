-- 保存可逆加密的城市入口短码，使后台可再次展示建档链接并生成二维码图片。
-- code_hash 继续用于扫码校验；短码不会明文写入数据库。
ALTER TABLE `avatar_registration_qrcode`
  ADD COLUMN `code_cipher` VARBINARY(1024) NULL COMMENT '二维码短码密文；仅后台授权查看和下载使用' AFTER `code_hash`;

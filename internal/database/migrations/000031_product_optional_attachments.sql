ALTER TABLE `product_version`
  ADD COLUMN `attachment_files_json` JSON NULL COMMENT '可选附件：商品视频、说明书、质检报告、生产许可证等文件ID及类型' AFTER `detail_files_json`;

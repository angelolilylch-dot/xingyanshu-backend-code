ALTER TABLE `sys_user`
  ADD COLUMN `must_change_password` TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否首次登录或管理员重置后必须修改密码' AFTER `password_hash`;

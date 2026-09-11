-- 组织编码是跨账号分配和城市数据范围识别的稳定业务键，必须保持唯一。
ALTER TABLE `sys_org`
  ADD UNIQUE KEY `uk_sys_org_org_code` (`org_code`);

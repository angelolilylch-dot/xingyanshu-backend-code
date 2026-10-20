-- A rejected archive remains auditable but must not block a fresh city roster enrollment.
ALTER TABLE `avatar_roster_member`
  DROP INDEX `uk_avatar_roster_member_city_mobile`,
  ADD UNIQUE KEY `uk_avatar_roster_member_city_mobile_status` (`city_org_id`, `mobile_hash`, `status`);

-- API-H5-02: address book supports an optional postal code.  The initial schema is immutable.
ALTER TABLE `consumer_address`
  ADD COLUMN `postal_code` VARCHAR(32) NULL DEFAULT NULL COMMENT '邮编；可选' AFTER `detail_address_cipher`;

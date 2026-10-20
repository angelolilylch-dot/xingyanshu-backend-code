package database

import (
	"context"
	"testing"
	"time"

	"xingyanshu-backend/internal/config"
)

func TestHongKongTimezoneIsAvailableInStandaloneBinary(t *testing.T) {
	location, err := time.LoadLocation("Asia/Hong_Kong")
	if err != nil {
		t.Fatalf("embedded Asia/Hong_Kong timezone must be available: %v", err)
	}
	if _, offset := time.Date(2026, time.January, 1, 0, 0, 0, 0, location).Zone(); offset != 8*60*60 {
		t.Fatalf("unexpected Asia/Hong_Kong UTC offset: %d", offset)
	}
}

func TestOpenMySQLConfigBuildsSupportedConnection(t *testing.T) {
	cfg := config.DatabaseConfig{
		Driver: "mysql", Host: "127.0.0.1", Port: 3306, Name: "xingyanshu_test", Username: "root", Password: "root",
	}
	db, err := OpenMySQLConfig(cfg)
	if err != nil {
		t.Fatalf("open database config: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql database: %v", err)
	}
	defer sqlDB.Close()
}

func TestAvatarIPLibraryMigrationWithMySQL(t *testing.T) {
	cfg := config.DatabaseConfig{
		Driver: "mysql", Host: "127.0.0.1", Port: 3306, Name: "xingyanshu_test", Username: "root", Password: "root",
	}
	db, err := OpenMySQLConfig(cfg)
	if err != nil {
		t.Fatalf("open database config: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql database: %v", err)
	}
	defer sqlDB.Close()

	ctx := context.Background()
	for _, version := range []string{
		"000034_avatar_ip_library",
		"000035_avatar_export_jobs",
		"000036_avatar_export_password_once",
		"000037_avatar_city_operations",
		"000038_avatar_quality_review_operations",
		"000039_avatar_rejected_reenrollment",
		"000040_add_avatar_management_menu",
	} {
		var migrationCount int64
		if err := db.WithContext(ctx).Raw(
			"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version,
		).Scan(&migrationCount).Error; err != nil {
			t.Fatalf("read migration %s history: %v", version, err)
		}
		if migrationCount != 1 {
			t.Fatalf("migration %s history count = %d, want 1", version, migrationCount)
		}
	}

	var tableCount int64
	if err := db.WithContext(ctx).Raw(`
SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE() AND table_name LIKE 'avatar\\_%' ESCAPE '\\'`,
	).Scan(&tableCount).Error; err != nil {
		t.Fatalf("count avatar tables: %v", err)
	}
	if tableCount != 21 {
		t.Fatalf("avatar table count = %d, want 21", tableCount)
	}

	for _, check := range []struct {
		table  string
		column string
	}{
		{table: "avatar_roster_member", column: "gender"},
		{table: "avatar_h5_sms_code_log", column: "code_hash"},
		{table: "avatar_identity_verification", column: "verified_gender"},
		{table: "avatar_profile", column: "retention_until"},
		{table: "avatar_asset", column: "storage_key_cipher"},
		{table: "avatar_access_audit", column: "purpose_code"},
		{table: "avatar_export_job", column: "encryption_key_reference"},
		{table: "avatar_export_job", column: "password_cipher"},
		{table: "avatar_export_job", column: "password_revealed_at"},
		{table: "avatar_export_job", column: "invalidated_at"},
		{table: "avatar_export_item", column: "skip_reason_code"},
		{table: "avatar_roster_import_error", column: "error_message"},
	} {
		var count int64
		if err := db.WithContext(ctx).Raw(`
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, check.table, check.column,
		).Scan(&count).Error; err != nil {
			t.Fatalf("check %s.%s: %v", check.table, check.column, err)
		}
		if count != 1 {
			t.Fatalf("missing %s.%s", check.table, check.column)
		}
	}
}

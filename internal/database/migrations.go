package database

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type MigrationResult struct {
	AppliedVersions []string
	SkippedVersions []string
}

type schemaMigration struct {
	// Version 是迁移文件名去掉 .sql 后的版本标识。
	Version   string    `gorm:"primaryKey;size:64"`
	Checksum  string    `gorm:"size:64;not null"`
	AppliedAt time.Time `gorm:"not null"`
}

// TableName 指定迁移执行记录使用的数据库表名。
func (schemaMigration) TableName() string {
	return "schema_migrations"
}

// ApplyMigrations 按文件名顺序执行未登记的嵌入式 SQL 迁移，并写入执行记录。
func ApplyMigrations(ctx context.Context, db *gorm.DB) (MigrationResult, error) {
	// 已登记的迁移只校验文件内容，不会重复执行。
	if err := db.WithContext(ctx).Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
  version VARCHAR(64) NOT NULL COMMENT '迁移版本',
  checksum CHAR(64) NOT NULL COMMENT '迁移文件SHA256',
  applied_at DATETIME(3) NOT NULL COMMENT '执行时间',
  PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='数据库迁移执行记录'`).Error; err != nil {
		return MigrationResult{}, fmt.Errorf("create migration history table: %w", err)
	}

	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return MigrationResult{}, fmt.Errorf("read embedded migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	result := MigrationResult{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		version := strings.TrimSuffix(entry.Name(), ".sql")
		content, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return result, fmt.Errorf("read migration %s: %w", version, err)
		}
		checksum := checksum(content)

		var history schemaMigration
		err = db.WithContext(ctx).Where("version = ?", version).First(&history).Error
		switch {
		case err == nil:
			// 已执行迁移的校验和必须保持一致，防止同一版本在不同环境执行不同 SQL。
			if history.Checksum != checksum {
				return result, fmt.Errorf("migration %s checksum changed after execution", version)
			}
			result.SkippedVersions = append(result.SkippedVersions, version)
			continue
		case err != nil && err != gorm.ErrRecordNotFound:
			return result, fmt.Errorf("read migration history for %s: %w", version, err)
		}

		// MySQL DDL 会隐式提交；失败时不登记版本，修复后可再次执行。
		if err := executeSQL(ctx, db, string(content)); err != nil {
			return result, fmt.Errorf("apply migration %s: %w", version, err)
		}
		if err := db.WithContext(ctx).Create(&schemaMigration{
			Version: version, Checksum: checksum, AppliedAt: time.Now(),
		}).Error; err != nil {
			return result, fmt.Errorf("record migration %s: %w", version, err)
		}
		result.AppliedVersions = append(result.AppliedVersions, version)
	}

	return result, nil
}

// TableCount 查询指定数据库当前包含的表数量，用于迁移完成后的连通性校验。
func TableCount(ctx context.Context, db *gorm.DB, databaseName string) (int64, error) {
	var count int64
	if err := db.WithContext(ctx).Raw(
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ?", databaseName,
	).Scan(&count).Error; err != nil {
		return 0, fmt.Errorf("count tables: %w", err)
	}
	return count, nil
}

// executeSQL 将普通 DDL 文件按分号拆分并依次执行。
func executeSQL(ctx context.Context, db *gorm.DB, content string) error {
	// 当前迁移不包含存储过程或触发器，因此分号可作为语句边界。
	for index, statement := range strings.Split(content, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			return fmt.Errorf("statement %d: %w", index+1, err)
		}
	}
	return nil
}

// checksum 计算迁移文件内容的 SHA256，用于检测历史迁移是否被修改。
func checksum(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

package database

import (
	"net"
	"strconv"
	"time"
	_ "time/tzdata"

	driver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/platform/logx"
)

// OpenMySQL 根据已构造的 DSN 创建 GORM 数据库连接。
func OpenMySQL(dsn string) (*gorm.DB, error) {
	return gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logx.GormLogger{}})
}

// OpenMySQLConfig 将项目数据库配置转换为 DSN 后创建连接。
func OpenMySQLConfig(cfg config.DatabaseConfig) (*gorm.DB, error) {
	dsnConfig := driver.NewConfig()
	dsnConfig.User = cfg.Username
	dsnConfig.Passwd = cfg.Password
	dsnConfig.Net = "tcp"
	dsnConfig.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dsnConfig.DBName = cfg.Name
	dsnConfig.ParseTime = true
	// 内置 IANA 时区库，确保独立部署的 Windows EXE 也能解析
	// Asia/Hong_Kong，不依赖目标机器上的 Go 安装或时区文件。
	// 统一按项目时区解析和写入时间，避免部署机器时区差异。
	dsnConfig.Loc = time.FixedZone("Asia/Hong_Kong", 8*60*60)
	// 固定使用 utf8mb4，确保中文内容可完整保存。
	dsnConfig.Params = map[string]string{
		"charset":   "utf8mb4",
		"collation": "utf8mb4_unicode_ci",
	}

	return OpenMySQL(dsnConfig.FormatDSN())
}

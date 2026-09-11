package database

import (
	"testing"

	"xingyanshu-backend/internal/config"
)

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

package files

import (
	"context"
	"fmt"
	"mime/multipart"
	"os"
	"path/filepath"
	"testing"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/filestore"
)

// TestFileServiceWithCOS 使用真实测试 COS 验证上传、文件元数据和短期下载地址。
// 该测试只在显式开启时运行，测试对象统一落在 xingyanshu/test/autotest 前缀。
func TestFileServiceWithCOS(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_COS_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_COS_TESTS=1 to run real COS integration test")
	}
	configPath := os.Getenv("XINGYANSHU_COS_TEST_CONFIG")
	if configPath == "" {
		configPath = filepath.Join("..", "..", "..", "config", "cos-test.yaml")
	}
	samplePath := os.Getenv("XINGYANSHU_COS_TEST_FILE")
	if samplePath == "" {
		t.Fatal("XINGYANSHU_COS_TEST_FILE is required for real COS integration test")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.OpenMySQLConfig(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	storage, err := filestore.New(cfg.File.Storage)
	if err != nil {
		t.Fatal(err)
	}
	policy := config.NewFilePolicyProvider(cfg.File.Policy)
	service := NewService(db, storage, policy, cfg.File)

	source, err := os.Open(samplePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		t.Fatal(err)
	}
	fileName := filepath.Base(samplePath)
	response, code, err := service.Upload(context.Background(), uploadOperator{ID: 10401, RoleCode: "PLATFORM_ADMIN", ClientIP: "127.0.0.1"}, &multipart.FileHeader{Filename: fileName, Size: info.Size()}, source)
	if err != nil || code != "" {
		t.Fatalf("upload to COS: response=%#v code=%s err=%v", response, code, err)
	}
	fileID, ok := response["id"].(string)
	if !ok || fileID == "" || response["fileSize"] != info.Size() {
		t.Fatalf("unexpected upload response: %#v", response)
	}
	var id uint64
	if _, err := fmt.Sscan(fileID, &id); err != nil || id == 0 {
		t.Fatalf("invalid file ID %q: %v", fileID, err)
	}
	download, code, err := service.CreateDownloadURL(context.Background(), identity.CurrentAdmin{UserID: 10401, RoleCodes: []string{"PLATFORM_ADMIN"}, PermissionCodes: map[string]struct{}{adminConfigPermission: {}}}, "127.0.0.1", id)
	if err != nil || code != "" || download["downloadURL"] == "" {
		t.Fatalf("sign COS download URL: response=%#v code=%s err=%v", download, code, err)
	}
}

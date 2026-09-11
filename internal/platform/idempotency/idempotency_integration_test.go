package idempotency

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/platform/idgen"
)

// TestExecuteWithMySQL 使用本地测试库验证首次执行、重复返回和同键冲突。
// 仅在显式设置 XINGYANSHU_RUN_DB_TESTS=1 时运行，普通单元测试不依赖本机 MySQL。
func TestExecuteWithMySQL(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1 to run MySQL integration test")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "..", "..", "..", "config", "test.yaml")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.OpenMySQLConfig(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}

	subjectID := idgen.Next()
	scope := Scope{SubjectType: "TEST", SubjectID: subjectID, Method: "POST", Path: "/test/idempotency"}
	t.Cleanup(func() {
		if err := db.Where("subject_type = ? AND subject_id = ?", scope.SubjectType, scope.SubjectID).Delete(&record{}).Error; err != nil {
			t.Errorf("cleanup idempotency record: %v", err)
		}
	})

	var calls atomic.Int32
	handler := func(tx *gorm.DB) (Result, error) {
		calls.Add(1)
		return Result{HTTPStatus: 201, Code: "OK", Message: "已创建", Data: map[string]any{"businessId": "demo-1"}}, nil
	}
	first, replayed, err := Execute(context.Background(), db, scope, "idempotency-demo-0001", []byte(`{"name":"same"}`), handler)
	if err != nil || replayed || first.HTTPStatus != 201 {
		t.Fatalf("first execution: result=%+v replayed=%v err=%v", first, replayed, err)
	}
	second, replayed, err := Execute(context.Background(), db, scope, "idempotency-demo-0001", []byte(`{"name":"same"}`), handler)
	if err != nil || !replayed || second.HTTPStatus != 201 || calls.Load() != 1 {
		t.Fatalf("replay execution: result=%+v replayed=%v calls=%d err=%v", second, replayed, calls.Load(), err)
	}
	if _, _, err := Execute(context.Background(), db, scope, "idempotency-demo-0001", []byte(`{"name":"different"}`), handler); err != ErrConflict {
		t.Fatalf("different request should conflict, err=%v", err)
	}
}

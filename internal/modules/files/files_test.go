package files

import (
	"context"
	"mime/multipart"
	"testing"

	"xingyanshu-backend/internal/config"
)

// TestUploadRejectsSizeAndType 先在进入存储层前拦截超限文件与白名单外格式。
func TestUploadRejectsSizeAndType(t *testing.T) {
	policy := config.NewFilePolicyProvider(config.DefaultFileConfig().Policy)
	service := &Service{policy: policy}
	operator := uploadOperator{ID: 1}
	_, code, err := service.Upload(context.Background(), operator, &multipart.FileHeader{Filename: "too-large.jpg", Size: 11 * 1024 * 1024}, nil)
	if err != nil || code != "FILE_SIZE_EXCEEDED" {
		t.Fatalf("large image code=%s err=%v", code, err)
	}
	_, code, err = service.Upload(context.Background(), operator, &multipart.FileHeader{Filename: "not-allowed.zip", Size: 1024}, nil)
	if err != nil || code != "FILE_TYPE_NOT_ALLOWED" {
		t.Fatalf("zip code=%s err=%v", code, err)
	}
}

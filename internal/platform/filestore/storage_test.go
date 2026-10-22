package filestore

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"xingyanshu-backend/internal/config"
)

func TestMockStoragePutGetAndUploadURL(t *testing.T) {
	store, err := New(config.FileStorageConfig{Provider: "mock", LocalRoot: t.TempDir(), MockUploadBaseURL: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	uploader, ok := store.(MockHTTPStorage)
	if !ok {
		t.Fatal("mock storage does not expose HTTP upload capability")
	}
	key := "avatar/private/test/FACE_01"
	if err = uploader.PutMockUpload(context.Background(), key, "image/jpeg", strings.NewReader("image-content")); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Close()
	bytes, err := io.ReadAll(value)
	if err != nil || string(bytes) != "image-content" {
		t.Fatalf("read mock object bytes=%q err=%v", bytes, err)
	}
	signer := store.(interface {
		SignUploadURL(context.Context, string, string, time.Duration) (string, error)
	})
	url, err := signer.SignUploadURL(context.Background(), key, "image/jpeg", 0)
	if err != nil || !strings.Contains(url, "/api/v1/mock-storage/upload?key=avatar%2Fprivate%2Ftest%2FFACE_01") {
		t.Fatalf("unexpected upload URL=%q err=%v", url, err)
	}
}

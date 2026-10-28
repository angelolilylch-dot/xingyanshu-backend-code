package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/platform/filestore"
)

type noMockStorage struct{}

func (noMockStorage) Put(context.Context, string, string, io.Reader) error { return nil }
func (noMockStorage) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (noMockStorage) SignDownloadURL(context.Context, string, time.Duration) (string, error) {
	return "", errors.New("not implemented")
}

func TestAvatarMockStorageRouteDoesNotReplaceGlobalStorage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	avatarStore, err := filestore.New(config.FileStorageConfig{
		Provider: "mock", LocalRoot: root, MockUploadBaseURL: "http://127.0.0.1:8080",
	})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	registerMockStorageRoutes(router.Group("/api/v1"), noMockStorage{}, avatarStore)

	request := httptest.NewRequest(http.MethodPut, "/api/v1/mock-storage/upload?key=avatar/private/1/FACE_01", strings.NewReader("image"))
	request.Header.Set("Content-Type", "image/jpeg")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("avatar mock upload status=%d body=%s", response.Code, response.Body.String())
	}
	if content, err := os.ReadFile(filepath.Join(root, "avatar", "private", "1", "FACE_01")); err != nil || string(content) != "image" {
		t.Fatalf("avatar mock object not written: content=%q err=%v", content, err)
	}

	nonAvatar := httptest.NewRequest(http.MethodPut, "/api/v1/mock-storage/upload?key=files/test.txt", strings.NewReader("file"))
	nonAvatarResponse := httptest.NewRecorder()
	router.ServeHTTP(nonAvatarResponse, nonAvatar)
	if nonAvatarResponse.Code != http.StatusBadRequest {
		t.Fatalf("global COS path must not be served by avatar mock, got %d", nonAvatarResponse.Code)
	}
}

// Package filestore 抽象文件对象存储，测试环境使用本地 Mock，生产环境可切换腾讯云 COS。
package filestore

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	cos "github.com/tencentyun/cos-go-sdk-v5"

	"xingyanshu-backend/internal/config"
)

// Storage 只暴露文件服务需要的上传与临时读取能力，不让业务模块接触 COS 存储键。
type Storage interface {
	Put(ctx context.Context, key, contentType string, body io.Reader) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	SignDownloadURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// New 根据静态 COS 连接配置创建存储适配器。Bucket 或密钥变更必须重启服务。
func New(cfg config.FileStorageConfig) (Storage, error) {
	switch cfg.Provider {
	case "cos":
		return newCOSStorage(cfg.COS)
	case "mock":
		return newMockStorage(cfg)
	default:
		return nil, fmt.Errorf("unsupported file storage provider: %s", cfg.Provider)
	}
}

// MockHTTPStorage is available only in test configuration. It lets a browser
// PUT test files through this backend, avoiding any dependency on COS CORS.
type MockHTTPStorage interface {
	Storage
	PutMockUpload(context.Context, string, string, io.Reader) error
}

type mockStorage struct {
	root          string
	uploadBaseURL string
}

func newMockStorage(cfg config.FileStorageConfig) (Storage, error) {
	root, err := filepath.Abs(cfg.LocalRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve mock storage root: %w", err)
	}
	if err = os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("create mock storage root: %w", err)
	}
	return &mockStorage{root: root, uploadBaseURL: strings.TrimRight(cfg.MockUploadBaseURL, "/")}, nil
}

func (s *mockStorage) pathFor(key string) (string, error) {
	clean := strings.TrimPrefix(strings.ReplaceAll(key, "\\", "/"), "/")
	if clean == "" || clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("invalid mock storage key")
	}
	path := filepath.Join(s.root, filepath.FromSlash(clean))
	rel, err := filepath.Rel(s.root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid mock storage key")
	}
	return path, nil
}

func (s *mockStorage) PutMockUpload(ctx context.Context, key, contentType string, body io.Reader) error {
	return s.Put(ctx, key, contentType, body)
}

func (s *mockStorage) Put(_ context.Context, key, _ string, body io.Reader) error {
	path, err := s.pathFor(key)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, body)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (s *mockStorage) Get(_ context.Context, key string) (io.ReadCloser, error) {
	path, err := s.pathFor(key)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (s *mockStorage) SignDownloadURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return s.uploadBaseURL + "/api/v1/mock-storage/download?key=" + url.QueryEscape(key), nil
}

func (s *mockStorage) SignUploadURL(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	return s.uploadBaseURL + "/api/v1/mock-storage/upload?key=" + url.QueryEscape(key), nil
}

func (s *mockStorage) Delete(_ context.Context, key string) error {
	path, err := s.pathFor(key)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

type cosStorage struct {
	client    *cos.Client
	secretID  string
	secretKey string
}

func newCOSStorage(cfg config.COSFileConfig) (Storage, error) {
	bucketURL, err := url.Parse(cfg.BucketURL)
	if err != nil || bucketURL.Scheme != "https" || bucketURL.Host == "" {
		return nil, fmt.Errorf("invalid COS bucket_url")
	}
	client := cos.NewClient(&cos.BaseURL{BucketURL: bucketURL}, &http.Client{Transport: &cos.AuthorizationTransport{SecretID: cfg.SecretID, SecretKey: cfg.SecretKey}})
	return &cosStorage{client: client, secretID: cfg.SecretID, secretKey: cfg.SecretKey}, nil
}

func (s *cosStorage) Put(ctx context.Context, key, contentType string, body io.Reader) error {
	response, err := s.client.Object.Put(ctx, key, body, &cos.ObjectPutOptions{ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{ContentType: contentType}})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return nil
}

// Get 仅供服务端聚合已有私有文件，例如生成代办资料包；不向 HTTP 调用方暴露对象存储键。
func (s *cosStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	response, err := s.client.Object.Get(ctx, key, nil)
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

func (s *cosStorage) SignDownloadURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	value, err := s.client.Object.GetPresignedURL(ctx, http.MethodGet, key, s.secretID, s.secretKey, ttl, nil)
	if err != nil {
		return "", err
	}
	return value.String(), nil
}

func (s *cosStorage) SignUploadURL(ctx context.Context, key, contentType string, ttl time.Duration) (string, error) {
	header := http.Header{"Content-Type": []string{contentType}}
	value, err := s.client.Object.GetPresignedURL(ctx, http.MethodPut, key, s.secretID, s.secretKey, ttl, &cos.PresignedURLOptions{Header: &header})
	if err != nil {
		return "", err
	}
	return value.String(), nil
}

// Delete is intentionally optional to keep legacy storage test doubles compatible.
func (s *cosStorage) Delete(ctx context.Context, key string) error {
	response, err := s.client.Object.Delete(ctx, key)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	return err
}

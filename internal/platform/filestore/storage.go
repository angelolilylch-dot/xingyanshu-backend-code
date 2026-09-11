// Package filestore 抽象文件对象存储，测试环境使用本地 Mock，生产环境可切换腾讯云 COS。
package filestore

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	cos "github.com/tencentyun/cos-go-sdk-v5"

	"xingyanshu-backend/internal/config"
)

// Storage 只暴露文件服务需要的上传与临时读取能力，不让业务模块接触 COS 存储键。
type Storage interface {
	Put(ctx context.Context, key, contentType string, body io.Reader) error
	SignDownloadURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// New 根据静态 COS 连接配置创建存储适配器。Bucket 或密钥变更必须重启服务。
func New(cfg config.FileStorageConfig) (Storage, error) {
	if cfg.Provider != "cos" {
		return nil, fmt.Errorf("file storage provider must be cos")
	}
	return newCOSStorage(cfg.COS)
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

func (s *cosStorage) SignDownloadURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	value, err := s.client.Object.GetPresignedURL(ctx, http.MethodGet, key, s.secretID, s.secretKey, ttl, nil)
	if err != nil {
		return "", err
	}
	return value.String(), nil
}

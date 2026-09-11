// Package files 提供统一文件上传、文件元数据查询和经权限校验后的短期下载地址。
package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/filestore"
	"xingyanshu-backend/internal/platform/idgen"
)

const adminConfigPermission = "ADMIN_CONFIG_MANAGE"

// Service 管理文件元数据与权限；业务对象关联由后续 IP、作品、订单等模块在其事务内写入。
type Service struct {
	db        *gorm.DB
	storage   filestore.Storage
	policy    *config.FilePolicyProvider
	ttl       time.Duration
	keyPrefix string
}

func NewService(db *gorm.DB, storage filestore.Storage, policy *config.FilePolicyProvider, fileCfg config.FileConfig) *Service {
	return &Service{db: db, storage: storage, policy: policy, ttl: fileCfg.SignedURLTTL, keyPrefix: strings.Trim(fileCfg.Storage.KeyPrefix, "/")}
}

// MaxUploadBytes 返回当前策略允许的最大单文件大小，Handler 用它限制 multipart 请求体。
func (s *Service) MaxUploadBytes() int64 {
	policy := s.policy.Current()
	max := policy.Images.MaxSizeMB
	if policy.Documents.MaxSizeMB > max {
		max = policy.Documents.MaxSizeMB
	}
	if policy.Media.MaxSizeMB > max {
		max = policy.Media.MaxSizeMB
	}
	return max * 1024 * 1024
}

type fileObject struct {
	ID            uint64    `gorm:"column:id;primaryKey"`
	Status        string    `gorm:"column:status"`
	Version       uint32    `gorm:"column:version"`
	CreatedBy     uint64    `gorm:"column:created_by"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
	FileNo        string    `gorm:"column:file_no"`
	StorageKey    string    `gorm:"column:storage_key"`
	OriginalName  string    `gorm:"column:original_name"`
	MimeType      string    `gorm:"column:mime_type"`
	FileSize      int64     `gorm:"column:file_size"`
	FileHash      string    `gorm:"column:file_hash"`
	SecurityLevel string    `gorm:"column:security_level"`
	UploadStatus  string    `gorm:"column:upload_status"`
}

func (fileObject) TableName() string { return "file_object" }

type fileRelation struct {
	ID         uint64 `gorm:"column:id;primaryKey"`
	CreatedBy  uint64 `gorm:"column:created_by"`
	FileID     uint64 `gorm:"column:file_id"`
	ObjectType string `gorm:"column:object_type"`
	ObjectID   uint64 `gorm:"column:object_id"`
	FileRole   string `gorm:"column:file_role"`
	SortNo     int    `gorm:"column:sort_no"`
}

func (fileRelation) TableName() string { return "file_relation" }

type uploadOperator struct {
	ID       uint64
	RoleCode string
	ClientIP string
}

// Upload 校验文件名称、类型与大小后写入存储，并持久化文件元数据和审计日志。
func (s *Service) Upload(ctx context.Context, operator uploadOperator, header *multipart.FileHeader, source multipart.File) (map[string]any, string, error) {
	name := filepath.Base(strings.TrimSpace(header.Filename))
	if name == "." || name == "" || len(name) > 512 {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	category, maxBytes, ok := classify(s.policy.Current(), extension)
	if !ok {
		return nil, "FILE_TYPE_NOT_ALLOWED", nil
	}
	if header.Size <= 0 {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	if header.Size > maxBytes {
		return nil, "FILE_SIZE_EXCEEDED", nil
	}
	detected, err := mimetype.DetectReader(source)
	if err != nil {
		return nil, "FILE_CONTENT_INVALID", nil
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	if !validMIME(category, detected.String()) {
		return nil, "FILE_CONTENT_INVALID", nil
	}

	id := idgen.Next()
	now := time.Now()
	key := fmt.Sprintf("%s/%s/%04d/%02d/%d.%s", s.keyPrefix, category, now.Year(), now.Month(), id, extension)
	item := fileObject{ID: id, Status: "UPLOADING", Version: 1, CreatedBy: operator.ID, FileNo: fmt.Sprintf("FILE-%d", id), StorageKey: key, OriginalName: name, MimeType: detected.String(), SecurityLevel: "PRIVATE", UploadStatus: "UPLOADING"}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&item).Error; err != nil {
			return err
		}
		if err := audit.WriteStatusHistory(tx, "FILE", id, "", "UPLOADING", "开始上传文件", toAuditOperator(operator)); err != nil {
			return err
		}
		return audit.WriteLog(tx, "FILE_UPLOAD_STARTED", "FILE", id, nil, fileResponse(item), "SUCCESS", toAuditOperator(operator))
	}); err != nil {
		return mapDatabaseError(err)
	}

	hash := sha256.New()
	counted := &countingReader{reader: io.TeeReader(source, hash)}
	err = s.storage.Put(ctx, key, detected.String(), counted)
	if err != nil || counted.size != header.Size {
		_ = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			failure := map[string]any{"status": "FAILED", "upload_status": "FAILED", "status_reason_code": "STORAGE_UPLOAD_FAILED", "updated_by": operator.ID, "version": 2}
			if updateErr := tx.Model(&fileObject{}).Where("id = ? AND version = ?", id, 1).Updates(failure).Error; updateErr != nil {
				return updateErr
			}
			if historyErr := audit.WriteStatusHistory(tx, "FILE", id, "UPLOADING", "FAILED", "对象存储上传失败", toAuditOperator(operator)); historyErr != nil {
				return historyErr
			}
			return audit.WriteLog(tx, "FILE_UPLOAD_FAILED", "FILE", id, fileResponse(item), map[string]any{"fileId": fmt.Sprint(id), "uploadStatus": "FAILED"}, "FAILED", toAuditOperator(operator))
		})
		return nil, "FILE_UPLOAD_FAILED", err
	}
	item.Status, item.UploadStatus, item.FileSize, item.FileHash = "ACTIVE", "SUCCESS", counted.size, hex.EncodeToString(hash.Sum(nil))
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&fileObject{}).Where("id = ? AND version = ?", id, 1).Updates(map[string]any{"status": item.Status, "upload_status": item.UploadStatus, "file_size": item.FileSize, "file_hash": item.FileHash, "updated_by": operator.ID, "version": 2}).Error; err != nil {
			return err
		}
		item.Version = 2
		if err := audit.WriteStatusHistory(tx, "FILE", id, "UPLOADING", "ACTIVE", "文件上传成功", toAuditOperator(operator)); err != nil {
			return err
		}
		return audit.WriteLog(tx, "FILE_UPLOADED", "FILE", id, nil, fileResponse(item), "SUCCESS", toAuditOperator(operator))
	}); err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return fileResponse(item), "", nil
}

// UploadForCreator 复用统一文件策略和 COS 上传链路，为创作者的委托书等私有资料提供上传能力。
func (s *Service) UploadForCreator(ctx context.Context, creatorUserID uint64, clientIP string, header *multipart.FileHeader, source multipart.File) (map[string]any, string, error) {
	return s.Upload(ctx, uploadOperator{ID: creatorUserID, RoleCode: "CREATOR", ClientIP: clientIP}, header, source)
}

func (s *Service) Get(ctx context.Context, current identity.CurrentAdmin, fileID uint64) (map[string]any, string, error) {
	item, code, err := s.findReadable(ctx, current, fileID)
	if err != nil || code != "" {
		return nil, code, err
	}
	return fileResponse(item), "", nil
}

// LinkToObject 供后续业务模块在同一事务中绑定文件用途。业务表只保存 fileId，不接触 storageKey。
func (s *Service) LinkToObject(tx *gorm.DB, operatorID, fileID, objectID uint64, objectType, fileRole string, sortNo int) error {
	if tx == nil || fileID == 0 || objectID == 0 || strings.TrimSpace(objectType) == "" || strings.TrimSpace(fileRole) == "" || sortNo < 0 {
		return fmt.Errorf("invalid file relation")
	}
	var item fileObject
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ? AND upload_status = ?", fileID, "ACTIVE", "SUCCESS").First(&item).Error; err != nil {
		return err
	}
	relation := fileRelation{ID: idgen.Next(), CreatedBy: operatorID, FileID: fileID, ObjectType: strings.ToUpper(strings.TrimSpace(objectType)), ObjectID: objectID, FileRole: strings.ToUpper(strings.TrimSpace(fileRole)), SortNo: sortNo}
	if err := tx.Create(&relation).Error; err != nil {
		return err
	}
	return audit.WriteLog(tx, "FILE_LINKED", "FILE", fileID, nil, map[string]any{"objectType": relation.ObjectType, "objectId": fmt.Sprint(objectID), "fileRole": relation.FileRole}, "SUCCESS", audit.Operator{Type: "USER", ID: operatorID})
}

// CreateDownloadURL 在权限判断后签发短期地址，并保留下载审计，不向调用端泄漏存储键。
func (s *Service) CreateDownloadURL(ctx context.Context, current identity.CurrentAdmin, clientIP string, fileID uint64) (map[string]any, string, error) {
	item, code, err := s.findReadable(ctx, current, fileID)
	if err != nil || code != "" {
		return nil, code, err
	}
	url, err := s.storage.SignDownloadURL(ctx, item.StorageKey, s.ttl)
	if err != nil {
		return nil, "FILE_DOWNLOAD_URL_FAILED", err
	}
	response := map[string]any{"fileId": fmt.Sprint(item.ID), "downloadURL": url, "expiresAt": time.Now().Add(s.ttl)}
	operator := uploadOperator{ID: current.UserID, RoleCode: firstRole(current), ClientIP: clientIP}
	if err := audit.WriteLog(s.db.WithContext(ctx), "FILE_DOWNLOAD_URL_ISSUED", "FILE", item.ID, nil, map[string]any{"fileId": response["fileId"], "expiresAt": response["expiresAt"]}, "SUCCESS", toAuditOperator(operator)); err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return response, "", nil
}

func (s *Service) findReadable(ctx context.Context, current identity.CurrentAdmin, fileID uint64) (fileObject, string, error) {
	var item fileObject
	if err := s.db.WithContext(ctx).Where("id = ?", fileID).First(&item).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fileObject{}, "RESOURCE_NOT_FOUND", nil
		}
		return fileObject{}, "INTERNAL_ERROR", err
	}
	if item.Status != "ACTIVE" || item.UploadStatus != "SUCCESS" {
		return fileObject{}, "RESOURCE_NOT_FOUND", nil
	}
	if item.CreatedBy != current.UserID && !current.HasPermission(adminConfigPermission) {
		return fileObject{}, "AUTH_FORBIDDEN", nil
	}
	return item, "", nil
}

func classify(policy config.FilePolicyConfig, extension string) (string, int64, bool) {
	for category, value := range map[string]config.FileCategoryPolicy{"images": policy.Images, "documents": policy.Documents, "media": policy.Media} {
		for _, allowed := range value.Extensions {
			if extension == strings.ToLower(strings.TrimPrefix(allowed, ".")) {
				return category, value.MaxSizeMB * 1024 * 1024, true
			}
		}
	}
	return "", 0, false
}

func validMIME(category, mimeType string) bool {
	switch category {
	case "images":
		return mimeType == "image/jpeg" || mimeType == "image/png" || mimeType == "image/webp"
	case "documents":
		return mimeType == "application/pdf" || mimeType == "application/msword" || mimeType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" || mimeType == "application/vnd.ms-excel" || mimeType == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "media":
		return strings.HasPrefix(mimeType, "video/mp4") || strings.HasPrefix(mimeType, "audio/mpeg") || strings.HasPrefix(mimeType, "audio/wav") || strings.HasPrefix(mimeType, "audio/x-wav")
	}
	return false
}

func fileResponse(item fileObject) map[string]any {
	return map[string]any{"id": fmt.Sprint(item.ID), "fileNo": item.FileNo, "originalName": item.OriginalName, "mimeType": item.MimeType, "fileSize": item.FileSize, "fileHash": item.FileHash, "securityLevel": item.SecurityLevel, "uploadStatus": item.UploadStatus, "createdAt": item.CreatedAt}
}

func firstRole(current identity.CurrentAdmin) string {
	if len(current.RoleCodes) > 0 {
		return current.RoleCodes[0]
	}
	return ""
}

func toAuditOperator(operator uploadOperator) audit.Operator {
	return audit.Operator{Type: "USER", ID: operator.ID, RoleCode: operator.RoleCode, ClientIP: operator.ClientIP}
}

func mapDatabaseError(err error) (map[string]any, string, error) {
	if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
		return nil, "RESOURCE_CONFLICT", nil
	}
	return nil, "INTERNAL_ERROR", err
}

// countingReader 在流式上传过程中统计字节数，避免只信任 multipart 提供的声明大小。
type countingReader struct {
	reader io.Reader
	size   int64
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	r.size += int64(n)
	return n, err
}

package avatar

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	archivezip "github.com/yeka/zip"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idgen"
)

const archiveZIPKeyReference = "ZIP_AES_256_PASSWORD_V1"

func (s *Service) ProcessDueExports(ctx context.Context, limit int) (int, error) {
	if limit < 1 {
		limit = 20
	}
	var ids []uint64
	if e := s.db.WithContext(ctx).Table("avatar_export_job").Where("status='PENDING'").Order("created_at").Limit(limit).Pluck("id", &ids).Error; e != nil {
		return 0, e
	}
	n := 0
	for _, id := range ids {
		ok, e := s.processExport(ctx, id)
		if e != nil {
			return n, e
		}
		if ok {
			n++
		}
	}
	return n, nil
}

func (s *Service) processExport(ctx context.Context, id uint64) (bool, error) {
	// Claim first, so multiple workers cannot build or expose the same package twice.
	claim := s.db.WithContext(ctx).Table("avatar_export_job").Where("id=? AND status='PENDING'", id).Updates(map[string]any{"status": "PROCESSING", "updated_at": time.Now()})
	if claim.Error != nil || claim.RowsAffected == 0 {
		return false, claim.Error
	}
	var j struct {
		ID, CityOrgID uint64
		ExportType    string
	}
	if e := s.db.WithContext(ctx).Table("avatar_export_job").Where("id=?", id).Take(&j).Error; e != nil {
		return false, e
	}
	if s.storage == nil {
		return s.markExportWait(ctx, id, "storage is not configured")
	}
	if j.ExportType == "ARCHIVE_PACKAGE" {
		return s.processArchivePackage(ctx, j.ID, j.CityOrgID)
	}
	if j.ExportType != "PROGRESS_LIST" {
		return s.markExportWait(ctx, id, "unsupported export type")
	}
	return s.processProgressList(ctx, j.ID, j.CityOrgID)
}

func (s *Service) processProgressList(ctx context.Context, id, cityID uint64) (bool, error) {
	var rows []struct {
		ProfileNo, CityCode, Status, CollectionStatus, QualityStatus, ArchiveStatus string
		ArchivedAt, RetentionUntil                                                  *time.Time
	}
	if e := s.db.WithContext(ctx).Table("avatar_profile").Select("profile_no,city_code,status,collection_status,quality_status,archive_status,archived_at,retention_until").Where("city_org_id=?", cityID).Order("created_at").Find(&rows).Error; e != nil {
		return false, e
	}
	b := bytes.Buffer{}
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"档案编号", "城市编码", "档案状态", "采集状态", "质检状态", "归档状态", "归档时间", "保存到期日"})
	for _, r := range rows {
		_ = w.Write([]string{r.ProfileNo, r.CityCode, r.Status, r.CollectionStatus, r.QualityStatus, r.ArchiveStatus, formatTime(r.ArchivedAt), formatTime(r.RetentionUntil)})
	}
	w.Flush()
	if e := w.Error(); e != nil {
		return false, e
	}
	key := fmt.Sprintf("avatar/export/%d/progress-%d.csv", cityID, id)
	if e := s.storage.Put(ctx, key, "text/csv; charset=utf-8", bytes.NewReader(b.Bytes())); e != nil {
		return s.markExportWait(ctx, id, "write export result failed")
	}
	sum := sha256.Sum256(b.Bytes())
	now := time.Now()
	e := s.db.WithContext(ctx).Table("avatar_export_job").Where("id=? AND status='PROCESSING'", id).Updates(map[string]any{"status": "SUCCEEDED", "item_count": len(rows), "result_key_cipher": mustEncrypt(s.auth.JWTSecret, key), "result_content_hash": fmt.Sprintf("%x", sum[:]), "result_file_name": fmt.Sprintf("avatar-progress-%d.csv", id), "result_file_size": b.Len(), "download_expires_at": now.Add(24 * time.Hour), "completed_at": now, "updated_at": now}).Error
	return e == nil, e
}

type archiveProfile struct {
	ID        uint64
	ProfileNo string
	Assets    []archiveAsset
}
type archiveAsset struct {
	ID                uint64
	AssetNo, MIMEType string
	StorageKeyCipher  []byte
	ContentHash       string
	FileSize          uint64
}
type archiveManifestItem struct {
	ProfileNo string                 `json:"profileNo"`
	Assets    []archiveManifestAsset `json:"assets"`
}
type archiveManifestAsset struct {
	AssetNo     string `json:"assetNo"`
	FileName    string `json:"fileName"`
	ContentHash string `json:"contentHash"`
	MIMEType    string `json:"mimeType"`
	FileSize    uint64 `json:"fileSize"`
}
type archiveManifest struct {
	Format    string                `json:"format"`
	CreatedAt string                `json:"createdAt"`
	Items     []archiveManifestItem `json:"items"`
}

func (s *Service) processArchivePackage(ctx context.Context, id, cityID uint64) (bool, error) {
	profiles, skipped, e := s.loadArchiveProfiles(ctx, id, cityID)
	if e != nil {
		return s.markExportWait(ctx, id, "read archive scope failed")
	}
	if len(profiles) == 0 {
		_ = s.writeArchiveItems(ctx, id, nil, skipped)
		return s.markExportWait(ctx, id, "no archived profile with available assets")
	}
	password, e := randomArchivePassword()
	if e != nil {
		return s.markExportWait(ctx, id, "generate archive password failed")
	}
	data, included, skipped, e := s.buildArchiveZIP(ctx, profiles, password, skipped)
	if e != nil {
		_ = s.writeArchiveItems(ctx, id, included, skipped)
		return s.markExportWait(ctx, id, "build encrypted archive package failed")
	}
	if len(included) == 0 {
		_ = s.writeArchiveItems(ctx, id, nil, skipped)
		return s.markExportWait(ctx, id, "no archive asset could be read")
	}
	key := fmt.Sprintf("avatar/export/%d/archive-%d.zip", cityID, id)
	if e := s.storage.Put(ctx, key, "application/zip", bytes.NewReader(data)); e != nil {
		return s.markExportWait(ctx, id, "write encrypted archive package failed")
	}
	if e := s.writeArchiveItems(ctx, id, included, skipped); e != nil {
		return false, e
	}
	sum := sha256.Sum256(data)
	now := time.Now()
	e = s.db.WithContext(ctx).Table("avatar_export_job").Where("id=? AND status='PROCESSING'", id).Updates(map[string]any{
		"status": "SUCCEEDED", "item_count": len(included), "result_key_cipher": mustEncrypt(s.auth.JWTSecret, key), "result_content_hash": fmt.Sprintf("%x", sum[:]), "result_file_name": fmt.Sprintf("avatar-archive-%d.zip", id), "result_file_size": len(data), "encryption_key_reference": archiveZIPKeyReference, "password_cipher": mustEncrypt(s.auth.JWTSecret, password), "download_expires_at": now.Add(24 * time.Hour), "completed_at": now, "updated_at": now,
	}).Error
	return e == nil, e
}

func (s *Service) loadArchiveProfiles(ctx context.Context, id, cityID uint64) ([]archiveProfile, map[uint64]string, error) {
	var j struct{ FilterSnapshotJSON []byte }
	if e := s.db.WithContext(ctx).Table("avatar_export_job").Select("filter_snapshot_json").Where("id=?", id).Take(&j).Error; e != nil {
		return nil, nil, e
	}
	var filter struct {
		ProfileIDs []uint64 `json:"profileIds"`
	}
	if len(j.FilterSnapshotJSON) > 0 && json.Unmarshal(j.FilterSnapshotJSON, &filter) != nil {
		return nil, nil, fmt.Errorf("invalid filter snapshot")
	}
	q := s.db.WithContext(ctx).Table("avatar_profile").Select("id,profile_no").Where("city_org_id=? AND status='ARCHIVED' AND archive_status='ARCHIVED'", cityID)
	if len(filter.ProfileIDs) > 0 {
		q = q.Where("id IN ?", filter.ProfileIDs)
	}
	var base []struct {
		ID        uint64
		ProfileNo string
	}
	if e := q.Order("created_at").Find(&base).Error; e != nil {
		return nil, nil, e
	}
	profiles := make([]archiveProfile, 0, len(base))
	skipped := make(map[uint64]string)
	for _, p := range base {
		var assets []archiveAsset
		if e := s.db.WithContext(ctx).Table("avatar_asset").Select("id,asset_no,mime_type,storage_key_cipher,content_hash,file_size").Where("profile_id=? AND status='CONFIRMED'", p.ID).Order("created_at").Find(&assets).Error; e != nil {
			return nil, nil, e
		}
		if len(assets) == 0 {
			skipped[p.ID] = "NO_AVAILABLE_ASSET"
			continue
		}
		profiles = append(profiles, archiveProfile{ID: p.ID, ProfileNo: p.ProfileNo, Assets: assets})
	}
	return profiles, skipped, nil
}

func (s *Service) buildArchiveZIP(ctx context.Context, profiles []archiveProfile, password string, skipped map[uint64]string) ([]byte, []archiveProfile, map[uint64]string, error) {
	b := bytes.Buffer{}
	zw := archivezip.NewWriter(&b)
	manifest := archiveManifest{Format: "XINGYANSHU_AVATAR_ARCHIVE_V1", CreatedAt: time.Now().Format(time.RFC3339)}
	included := make([]archiveProfile, 0, len(profiles))
	for _, profile := range profiles {
		contents := make([][]byte, len(profile.Assets))
		valid := true
		for i, asset := range profile.Assets {
			key, e := decrypt(s.auth.JWTSecret, asset.StorageKeyCipher)
			if e != nil {
				valid = false
				break
			}
			r, e := s.storage.Get(ctx, key)
			if e != nil {
				valid = false
				break
			}
			contents[i], e = io.ReadAll(r)
			r.Close()
			if e != nil {
				valid = false
				break
			}
		}
		if !valid {
			skipped[profile.ID] = "ASSET_READ_FAILED"
			continue
		}
		item := archiveManifestItem{ProfileNo: profile.ProfileNo}
		for i, asset := range profile.Assets {
			name := filepath.ToSlash(filepath.Join("archives", safeArchiveName(profile.ProfileNo), safeArchiveName(asset.AssetNo)+mimeExtension(asset.MIMEType)))
			w, e := zw.Encrypt(name, password, archivezip.AES256Encryption)
			if e != nil {
				return nil, nil, skipped, e
			}
			if _, e = w.Write(contents[i]); e != nil {
				return nil, nil, skipped, e
			}
			item.Assets = append(item.Assets, archiveManifestAsset{AssetNo: asset.AssetNo, FileName: name, ContentHash: asset.ContentHash, MIMEType: asset.MIMEType, FileSize: asset.FileSize})
		}
		manifest.Items = append(manifest.Items, item)
		included = append(included, profile)
	}
	if len(included) == 0 {
		_ = zw.Close()
		return nil, included, skipped, nil
	}
	manifestBody, e := json.Marshal(manifest)
	if e != nil {
		return nil, nil, skipped, e
	}
	mw, e := zw.Encrypt("manifest.json", password, archivezip.AES256Encryption)
	if e != nil {
		return nil, nil, skipped, e
	}
	if _, e = mw.Write(manifestBody); e != nil {
		return nil, nil, skipped, e
	}
	if e = zw.Close(); e != nil {
		return nil, nil, skipped, e
	}
	return b.Bytes(), included, skipped, nil
}

func (s *Service) writeArchiveItems(ctx context.Context, jobID uint64, included []archiveProfile, skipped map[uint64]string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, p := range included {
			if e := tx.Exec(`INSERT INTO avatar_export_item (id,export_job_id,profile_id,included_asset_count,status,skip_reason_code)
VALUES (?,?,?,?,?,NULL)
ON DUPLICATE KEY UPDATE included_asset_count=VALUES(included_asset_count),status=VALUES(status),skip_reason_code=NULL`, idgen.Next(), jobID, p.ID, len(p.Assets), "INCLUDED").Error; e != nil {
				return e
			}
		}
		for profileID, reason := range skipped {
			if e := tx.Exec(`INSERT INTO avatar_export_item (id,export_job_id,profile_id,included_asset_count,status,skip_reason_code)
VALUES (?,?,?,?,?,?)
ON DUPLICATE KEY UPDATE included_asset_count=0,status=VALUES(status),skip_reason_code=VALUES(skip_reason_code)`, idgen.Next(), jobID, profileID, 0, "SKIPPED", reason).Error; e != nil {
				return e
			}
		}
		return nil
	})
}

func randomArchivePassword() (string, error) {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func safeArchiveName(v string) string {
	v = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, v)
	if v == "" {
		return "asset"
	}
	return v
}
func mimeExtension(value string) string {
	if exts, _ := mime.ExtensionsByType(value); len(exts) > 0 {
		return exts[0]
	}
	return ".bin"
}

func (s *Service) markExportWait(ctx context.Context, id uint64, reason string) (bool, error) {
	e := s.db.WithContext(ctx).Table("avatar_export_job").Where("id=? AND status='PROCESSING'", id).Updates(map[string]any{"status": "FAILED", "status_reason_code": "DEPENDENCY_UNAVAILABLE", "status_reason": reason, "updated_at": time.Now()}).Error
	return e == nil, e
}

func (s *Service) exportDownload(ctx context.Context, c identity.CurrentAdmin, idText, ip string) (any, *httpx.AppError) {
	id, ok := parseUint(idText)
	if !ok {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "导出任务编号不正确", nil)
	}
	var j struct {
		ID, RequesterOrgID                      uint64
		Status, ResultKeyCipher, ResultFileName string
		DownloadExpiresAt                       *time.Time
		DownloadLimit, DownloadCount            uint8
	}
	if e := s.db.WithContext(ctx).Table("avatar_export_job").Where("id=?", id).Take(&j).Error; e != nil {
		return nil, avatarError(404, "AVATAR_EXPORT_NOT_FOUND", "导出任务不存在", nil)
	}
	if !hasAdminRole(c, "PLATFORM_ADMIN") && j.RequesterOrgID != c.OrgID {
		return nil, avatarError(403, "AUTH_FORBIDDEN", "无权下载该导出", nil)
	}
	if j.Status != "SUCCEEDED" || j.DownloadExpiresAt == nil || j.DownloadExpiresAt.Before(time.Now()) || j.DownloadCount >= j.DownloadLimit {
		return nil, avatarError(422, "AVATAR_EXPORT_DOWNLOAD_UNAVAILABLE", "导出文件不可下载", nil)
	}
	key, e := decrypt(s.auth.JWTSecret, []byte(j.ResultKeyCipher))
	if e != nil || s.storage == nil {
		return nil, avatarError(503, "AVATAR_STORAGE_UNAVAILABLE", "导出存储暂不可用", nil)
	}
	url, e := s.storage.SignDownloadURL(ctx, key, 10*time.Minute)
	if e != nil {
		return nil, avatarError(503, "AVATAR_STORAGE_UNAVAILABLE", "导出存储暂不可用", nil)
	}
	if e = s.db.WithContext(ctx).Table("avatar_export_job").Where("id=? AND download_count<?", id, j.DownloadLimit).Updates(map[string]any{"download_count": gorm.Expr("download_count+1"), "updated_at": time.Now()}).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "记录下载失败", nil)
	}
	s.auditExport(ctx, c, id, "EXPORT_DOWNLOAD", ip)
	return map[string]any{"fileName": j.ResultFileName, "downloadURL": url, "expiresIn": 600}, nil
}
func formatTime(v *time.Time) string {
	if v == nil {
		return ""
	}
	return v.Format(time.RFC3339)
}

var _ = http.MethodGet

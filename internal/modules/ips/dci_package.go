package ips

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/idgen"
)

const dciPackageMaxBytes int64 = 200 * 1024 * 1024

type dciPackageSource struct {
	FileID       uint64
	StorageKey   string
	OriginalName string
	Folder       string
	Label        string
}

// CreateDCIAssistancePackage 生成仅供版权/合规线下办理使用的资料 ZIP。
// 联系方式只写入 ZIP 内的 Markdown，不写入审计日志或接口响应。
func (s *Service) CreateDCIAssistancePackage(ctx context.Context, current identity.CurrentAdmin, requestID uint64, input DCIPackageRequest, clientIP string) (map[string]any, string, error) {
	if !current.HasPermission(complianceIPPermission) {
		return nil, "AUTH_FORBIDDEN", nil
	}
	if s.storage == nil {
		return nil, "DCI_PACKAGE_STORAGE_FAILED", nil
	}

	data, sources, code, err := s.loadDCIPackageData(ctx, requestID, input.IncludeEvaluationReport)
	if err != nil || code != "" {
		return nil, code, err
	}
	zipData, err := s.buildDCIPackageZIP(ctx, data, sources)
	if err != nil {
		return nil, "DCI_PACKAGE_STORAGE_FAILED", err
	}
	if int64(len(zipData)) > dciPackageMaxBytes {
		return nil, "DCI_PACKAGE_SOURCE_MISSING", nil
	}

	packageID := idgen.Next()
	now := time.Now()
	fileName := fmt.Sprintf("DCI代办资料包_%s_%s.zip", safeFileName(data.RequestNo), safeFileName(data.IPNo))
	storageKey := fmt.Sprintf("%s/generated/dci-assistance/%04d/%02d/%d.zip", strings.Trim(s.filePrefix, "/"), now.Year(), now.Month(), packageID)
	storageKey = strings.TrimPrefix(storageKey, "/")
	if err := s.storage.Put(ctx, storageKey, "application/zip", bytes.NewReader(zipData)); err != nil {
		return nil, "DCI_PACKAGE_STORAGE_FAILED", err
	}
	hash := sha256.Sum256(zipData)
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		file := map[string]any{
			"id": packageID, "status": "ACTIVE", "version": 1, "created_by": current.UserID,
			"file_no": fmt.Sprintf("FILE-%d", packageID), "storage_key": storageKey, "original_name": fileName,
			"mime_type": "application/zip", "file_size": len(zipData), "file_hash": hex.EncodeToString(hash[:]),
			"security_level": "PRIVATE", "upload_status": "SUCCESS",
		}
		if err := tx.Table("file_object").Create(file).Error; err != nil {
			return err
		}
		if err := tx.Table("file_relation").Create(map[string]any{"id": idgen.Next(), "created_by": current.UserID, "file_id": packageID, "object_type": "IP_DCI_ASSISTANCE_REQUEST", "object_id": requestID, "file_role": "DCI_ASSISTANCE_PACKAGE", "sort_no": 0}).Error; err != nil {
			return err
		}
		return audit.WriteLog(tx, "DCI_ASSISTANCE_PACKAGE_GENERATED", "IP_DCI_ASSISTANCE_REQUEST", requestID, nil, map[string]any{"packageFileId": fmt.Sprint(packageID), "includeEvaluationReport": input.IncludeEvaluationReport}, "SUCCESS", ipOperator(current, clientIP))
	}); err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	url, err := s.storage.SignDownloadURL(ctx, storageKey, s.fileTTL)
	if err != nil {
		return nil, "DCI_PACKAGE_STORAGE_FAILED", err
	}
	return map[string]any{"packageFileId": fmt.Sprint(packageID), "fileName": fileName, "downloadUrl": url, "expiresAt": now.Add(s.fileTTL)}, "", nil
}

type dciPackageData struct {
	RequestNo            string
	IPNo                 string
	IPName               string
	IPType               string
	SourceType           string
	CityOrgName          string
	ContactName          string
	ContactMobile        string
	PreferredContactTime string
	RequestNote          string
	HolderName           string
	HolderType           string
	AcquisitionMethod    string
	RightsDescription    string
	ShortIntro           string
	DetailContent        string
	CultureStory         string
	IPFeature            string
	CreativeDirections   string
	RightDocuments       []string
	MaterialManifestHash string
	MaterialItems        []string
	EvaluationReports    []string
}

func (s *Service) loadDCIPackageData(ctx context.Context, requestID uint64, includeEvaluation bool) (dciPackageData, []dciPackageSource, string, error) {
	var row struct {
		RequestNo            string `gorm:"column:request_no"`
		IPID                 uint64 `gorm:"column:ip_id"`
		HandlingStatus       string `gorm:"column:handling_status"`
		ContactNameCipher    []byte `gorm:"column:contact_name_cipher"`
		ContactMobileCipher  []byte `gorm:"column:contact_mobile_cipher"`
		PreferredContactTime string `gorm:"column:preferred_contact_time"`
		RequestNote          string `gorm:"column:request_note"`
		IPNo                 string `gorm:"column:ip_no"`
		IPName               string `gorm:"column:ip_name"`
		IPType               string `gorm:"column:ip_type"`
		SourceType           string `gorm:"column:source_type"`
		CityOrgName          string `gorm:"column:city_org_name"`
		HolderNameCipher     []byte `gorm:"column:holder_name_cipher"`
		HolderType           string `gorm:"column:holder_type"`
		AcquisitionMethod    string `gorm:"column:acquisition_method"`
		RightsDescription    string `gorm:"column:rights_description"`
		ShortIntro           string `gorm:"column:short_intro"`
		DetailContent        string `gorm:"column:detail_content"`
		CultureStory         string `gorm:"column:culture_story"`
		IPFeature            string `gorm:"column:ip_feature"`
		CreativeDirections   string `gorm:"column:creative_directions_json"`
	}
	err := s.db.WithContext(ctx).Table("ip_dci_assistance_request r").
		Joins("JOIN ip_asset a ON a.id=r.ip_id").
		Joins("LEFT JOIN sys_org o ON o.id=a.main_city_org_id").
		Joins("JOIN ip_right_holder h ON h.ip_id=a.id").
		Where("r.id=?", requestID).
		Select("r.request_no,r.ip_id,r.handling_status,r.contact_name_cipher,r.contact_mobile_cipher,r.preferred_contact_time,r.request_note,a.ip_no,a.ip_name,a.ip_type,a.source_type,o.org_name AS city_org_name,h.holder_name_cipher,h.holder_type,h.acquisition_method,h.rights_description,a.short_intro,a.detail_content,a.culture_story,a.ip_feature,a.creative_directions_json").
		Take(&row).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return dciPackageData{}, nil, "DCI_ASSISTANCE_REQUEST_NOT_FOUND", nil
		}
		return dciPackageData{}, nil, "INTERNAL_ERROR", err
	}
	if row.HandlingStatus == "CLOSED" {
		return dciPackageData{}, nil, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	contactName, err := decrypt(s.secret, row.ContactNameCipher)
	if err != nil {
		return dciPackageData{}, nil, "INTERNAL_ERROR", err
	}
	contactMobile, err := decrypt(s.secret, row.ContactMobileCipher)
	if err != nil {
		return dciPackageData{}, nil, "INTERNAL_ERROR", err
	}
	holderName, err := decrypt(s.secret, row.HolderNameCipher)
	if err != nil {
		return dciPackageData{}, nil, "INTERNAL_ERROR", err
	}
	data := dciPackageData{RequestNo: row.RequestNo, IPNo: row.IPNo, IPName: row.IPName, IPType: row.IPType, SourceType: row.SourceType, CityOrgName: row.CityOrgName, ContactName: contactName, ContactMobile: contactMobile, PreferredContactTime: row.PreferredContactTime, RequestNote: row.RequestNote, HolderName: holderName, HolderType: row.HolderType, AcquisitionMethod: row.AcquisitionMethod, RightsDescription: row.RightsDescription, ShortIntro: row.ShortIntro, DetailContent: row.DetailContent, CultureStory: row.CultureStory, IPFeature: row.IPFeature, CreativeDirections: row.CreativeDirections}
	sources := make([]dciPackageSource, 0)

	var rights []struct {
		DocumentType, DocumentNo, StorageKey, OriginalName string
		FileID                                             uint64
	}
	if err := s.db.WithContext(ctx).Table("ip_right_document d").Joins("JOIN file_object f ON f.id=d.file_id AND f.status='ACTIVE' AND f.upload_status='SUCCESS'").Where("d.ip_id=?", row.IPID).Select("d.document_type,d.document_no,d.file_id,f.storage_key,f.original_name").Order("d.document_no").Scan(&rights).Error; err != nil {
		return dciPackageData{}, nil, "INTERNAL_ERROR", err
	}
	if len(rights) == 0 {
		return dciPackageData{}, nil, "DCI_PACKAGE_SOURCE_MISSING", nil
	}
	for _, item := range rights {
		data.RightDocuments = append(data.RightDocuments, fmt.Sprintf("- %s：%s（文件：%s）", item.DocumentType, item.DocumentNo, item.OriginalName))
		sources = append(sources, dciPackageSource{FileID: item.FileID, StorageKey: item.StorageKey, OriginalName: item.OriginalName, Folder: "01_权属证明材料", Label: item.DocumentNo})
	}
	var assets []struct {
		FileRole, StorageKey, OriginalName string
		FileID                             uint64
		SortNo                             int
	}
	if err := s.db.WithContext(ctx).Table("file_relation r").Joins("JOIN file_object f ON f.id=r.file_id AND f.status='ACTIVE' AND f.upload_status='SUCCESS'").Where("r.object_type='IP' AND r.object_id=? AND r.file_role IN ?", row.IPID, []string{"IP_COVER", "IP_DETAIL"}).Select("r.file_role,r.sort_no,r.file_id,f.storage_key,f.original_name").Order("r.file_role,r.sort_no").Scan(&assets).Error; err != nil {
		return dciPackageData{}, nil, "INTERNAL_ERROR", err
	}
	if len(assets) == 0 {
		return dciPackageData{}, nil, "DCI_PACKAGE_SOURCE_MISSING", nil
	}
	for _, item := range assets {
		sources = append(sources, dciPackageSource{FileID: item.FileID, StorageKey: item.StorageKey, OriginalName: item.OriginalName, Folder: "02_IP作品资料", Label: item.FileRole})
	}
	var materialPack struct {
		ManifestHash string `gorm:"column:manifest_hash"`
	}
	if err := s.db.WithContext(ctx).Table("ip_material_pack").Where("ip_id=? AND submission_locked_at IS NOT NULL", row.IPID).Select("manifest_hash").Take(&materialPack).Error; err != nil || materialPack.ManifestHash == "" {
		if err != nil && err != gorm.ErrRecordNotFound {
			return dciPackageData{}, nil, "INTERNAL_ERROR", err
		}
		return dciPackageData{}, nil, "DCI_PACKAGE_SOURCE_MISSING", nil
	}
	data.MaterialManifestHash = materialPack.ManifestHash
	var materialItems []struct {
		MaterialRole, StorageKey, OriginalName, FileHash string
		FileID                                           uint64
		SortNo                                           int
	}
	if err := s.db.WithContext(ctx).Table("ip_material_pack_item i").Joins("JOIN file_object f ON f.id=i.file_id AND f.status='ACTIVE' AND f.upload_status='SUCCESS'").Joins("JOIN ip_material_pack p ON p.id=i.material_pack_id").Where("p.ip_id=? AND p.manifest_hash=?", row.IPID, materialPack.ManifestHash).Select("i.material_role,i.sort_no,i.file_id,f.storage_key,f.original_name,i.file_hash").Order("i.sort_no,i.id").Scan(&materialItems).Error; err != nil {
		return dciPackageData{}, nil, "INTERNAL_ERROR", err
	}
	if len(materialItems) == 0 {
		return dciPackageData{}, nil, "DCI_PACKAGE_SOURCE_MISSING", nil
	}
	for _, item := range materialItems {
		data.MaterialItems = append(data.MaterialItems, fmt.Sprintf("- %s：%s（SHA-256：%s）", item.MaterialRole, item.OriginalName, item.FileHash))
		sources = append(sources, dciPackageSource{FileID: item.FileID, StorageKey: item.StorageKey, OriginalName: item.OriginalName, Folder: "03_素材包", Label: item.MaterialRole})
	}
	if includeEvaluation {
		var reports []struct {
			ReportNo, InstitutionName, StorageKey, OriginalName string
			FileID                                              uint64
		}
		if err := s.db.WithContext(ctx).Table("ip_evaluation_report e").Joins("JOIN file_object f ON f.id=e.file_id AND f.status='ACTIVE' AND f.upload_status='SUCCESS'").Where("e.ip_id=? AND e.status='ACTIVE'", row.IPID).Select("e.report_no,e.institution_name,e.file_id,f.storage_key,f.original_name").Order("e.report_no").Scan(&reports).Error; err != nil {
			return dciPackageData{}, nil, "INTERNAL_ERROR", err
		}
		for _, item := range reports {
			data.EvaluationReports = append(data.EvaluationReports, fmt.Sprintf("- %s（%s，文件：%s）", item.ReportNo, item.InstitutionName, item.OriginalName))
			sources = append(sources, dciPackageSource{FileID: item.FileID, StorageKey: item.StorageKey, OriginalName: item.OriginalName, Folder: "03_评估报告", Label: item.ReportNo})
		}
	}
	return data, sources, "", nil
}

func (s *Service) buildDCIPackageZIP(ctx context.Context, data dciPackageData, sources []dciPackageSource) ([]byte, error) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	if err := writeZipFile(writer, "00_DCI代办申请资料.md", []byte(renderDCIPackageMarkdown(data))); err != nil {
		return nil, err
	}
	if err := writeZipFile(writer, "02_IP作品资料/IP作品说明.md", []byte(renderIPDescriptionMarkdown(data))); err != nil {
		return nil, err
	}
	usedNames := map[string]int{}
	var copied int64
	for index, source := range sources {
		body, err := s.storage.Get(ctx, source.StorageKey)
		if err != nil {
			return nil, err
		}
		name := uniqueZipName(source.Folder, index+1, source.Label, source.OriginalName, usedNames)
		entry, err := writer.Create(name)
		if err == nil {
			var written int64
			written, err = io.Copy(entry, io.LimitReader(body, dciPackageMaxBytes-copied+1))
			copied += written
			if copied > dciPackageMaxBytes {
				err = fmt.Errorf("dci package source files exceed limit")
			}
		}
		closeErr := body.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func writeZipFile(writer *zip.Writer, name string, content []byte) error {
	entry, err := writer.Create(name)
	if err != nil {
		return err
	}
	_, err = entry.Write(content)
	return err
}

func renderDCIPackageMarkdown(data dciPackageData) string {
	lines := []string{
		"# DCI 代办申请资料", "",
		"## 申请信息", fmt.Sprintf("- 代办申请单号：%s", data.RequestNo), fmt.Sprintf("- IP 编号：%s", data.IPNo), fmt.Sprintf("- IP 名称：%s", data.IPName), fmt.Sprintf("- IP 类型：%s", data.IPType), fmt.Sprintf("- 来源类型：%s", data.SourceType), fmt.Sprintf("- 主归属城市/组织：%s", data.CityOrgName), "",
		"## 联系与代办说明", fmt.Sprintf("- 联系人：%s", data.ContactName), fmt.Sprintf("- 联系电话：%s", data.ContactMobile), fmt.Sprintf("- 期望联系时间：%s", valueOrDash(data.PreferredContactTime)), fmt.Sprintf("- 代办说明：%s", valueOrDash(data.RequestNote)), "",
		"## 原始权利人", fmt.Sprintf("- 名称：%s", data.HolderName), fmt.Sprintf("- 主体类型：%s", data.HolderType), fmt.Sprintf("- 权利取得方式：%s", data.AcquisitionMethod), fmt.Sprintf("- 权属说明：%s", data.RightsDescription), "",
		"## 权属证明材料", strings.Join(data.RightDocuments, "\n"), "", "## IP 作品资料", "- 详见 `02_IP作品资料/IP作品说明.md` 及同目录附件。", "", "## 锁定素材包", fmt.Sprintf("- 素材清单 SHA-256：%s", data.MaterialManifestHash), "- 素材文件详见 `03_素材包/`：", strings.Join(data.MaterialItems, "\n"),
	}
	if len(data.EvaluationReports) > 0 {
		lines = append(lines, "", "## 附带评估报告", strings.Join(data.EvaluationReports, "\n"))
	}
	lines = append(lines, "", fmt.Sprintf("> 本资料包由系统于 %s 生成，仅用于 DCI 线下代办。", time.Now().Format("2006-01-02 15:04:05")))
	return strings.Join(lines, "\n") + "\n"
}

func renderIPDescriptionMarkdown(data dciPackageData) string {
	return strings.Join([]string{"# IP 作品说明", "", fmt.Sprintf("- IP 编号：%s", data.IPNo), fmt.Sprintf("- IP 名称：%s", data.IPName), fmt.Sprintf("- 简介：%s", data.ShortIntro), "", "## 详细介绍", data.DetailContent, "", "## 文化故事", data.CultureStory, "", "## IP 特色", data.IPFeature, "", "## 可创作方向", valueOrDash(data.CreativeDirections)}, "\n") + "\n"
}

func uniqueZipName(folder string, index int, label, originalName string, used map[string]int) string {
	base := fmt.Sprintf("%02d_%s_%s", index, safeFileName(label), safeFileName(originalName))
	name := filepath.ToSlash(filepath.Join(folder, base))
	used[name]++
	if used[name] > 1 {
		ext := filepath.Ext(base)
		base = strings.TrimSuffix(base, ext) + fmt.Sprintf("_%d", used[name]) + ext
		name = filepath.ToSlash(filepath.Join(folder, base))
	}
	return name
}

var unsafeFileName = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1F]`)

func safeFileName(value string) string {
	value = unsafeFileName.ReplaceAllString(strings.TrimSpace(value), "_")
	value = strings.Trim(value, ". ")
	if value == "" {
		return "未命名文件"
	}
	if len([]rune(value)) > 100 {
		return string([]rune(value)[:100])
	}
	return value
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

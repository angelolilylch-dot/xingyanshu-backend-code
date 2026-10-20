package ips

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/evidence"
	"xingyanshu-backend/internal/platform/idgen"
)

type MaterialPackInput struct {
	PackName         string              `json:"packName" binding:"required,max=200"`
	ScopeDescription string              `json:"scopeDescription" binding:"omitempty,max=5000"`
	Items            []MaterialItemInput `json:"items" binding:"required,min=1,max=200"`
}

type MaterialItemInput struct {
	FileID           uint64 `json:"fileId,string" binding:"required"`
	MaterialRole     string `json:"materialRole" binding:"required,max=40"`
	SortNo           int    `json:"sortNo" binding:"min=0,max=10000"`
	Deliverable      bool   `json:"deliverable"`
	ScopeDescription string `json:"scopeDescription" binding:"omitempty,max=2000"`
}

type materialFileRow struct {
	ID       uint64 `gorm:"column:id"`
	FileHash string `gorm:"column:file_hash"`
}

func (s *Service) validateMaterialPack(ctx context.Context, input *MaterialPackInput) string {
	if input == nil || strings.TrimSpace(input.PackName) == "" || len(input.Items) == 0 {
		return "MATERIAL_PACK_REQUIRED"
	}
	seen := map[uint64]bool{}
	for _, item := range input.Items {
		if item.FileID == 0 || strings.TrimSpace(item.MaterialRole) == "" || seen[item.FileID] {
			return "VALIDATION_FORMAT_INVALID"
		}
		seen[item.FileID] = true
		if !s.fileActive(ctx, item.FileID) {
			return "MATERIAL_ITEM_FILE_NOT_FOUND"
		}
	}
	return ""
}

func (s *Service) materialPackLocked(ctx context.Context, ipID uint64) (bool, error) {
	var row struct{ Count int64 }
	err := s.db.WithContext(ctx).Table("ip_material_pack").Select("COUNT(*) AS count").Where("ip_id=? AND (submission_locked_at IS NOT NULL OR frozen_at IS NOT NULL OR status='FROZEN')", ipID).Take(&row).Error
	if err != nil {
		return false, err
	}
	return row.Count > 0, nil
}

// replaceMaterialPack 仅允许草稿/补充材料阶段调用；调用方已校验 IP 主状态。
func (s *Service) replaceMaterialPack(tx *gorm.DB, ipID, userID uint64, input *MaterialPackInput) (string, error) {
	var existing struct {
		ID                 uint64
		Status             string
		SubmissionLockedAt *time.Time `gorm:"column:submission_locked_at"`
		FrozenAt           *time.Time `gorm:"column:frozen_at"`
	}
	err := tx.Table("ip_material_pack").Where("ip_id=?", ipID).Take(&existing).Error
	if err == nil && (existing.SubmissionLockedAt != nil || existing.FrozenAt != nil || existing.Status == "FROZEN") {
		return "", fmt.Errorf("material pack locked")
	}
	if err != nil && err != gorm.ErrRecordNotFound {
		return "", err
	}
	if err == nil {
		if err := tx.Table("ip_material_pack_item").Where("material_pack_id=?", existing.ID).Delete(map[string]any{}).Error; err != nil {
			return "", err
		}
		if err := tx.Table("file_relation").Where("object_type='IP_MATERIAL_PACK' AND object_id=?", existing.ID).Delete(map[string]any{}).Error; err != nil {
			return "", err
		}
		if err := tx.Table("ip_material_pack").Where("id=?", existing.ID).Delete(map[string]any{}).Error; err != nil {
			return "", err
		}
	}
	files := make(map[uint64]materialFileRow, len(input.Items))
	for _, item := range input.Items {
		var file materialFileRow
		if err := tx.Table("file_object").Select("id,file_hash").Where("id=? AND status='ACTIVE' AND upload_status='SUCCESS'", item.FileID).Take(&file).Error; err != nil || file.FileHash == "" {
			return "", fmt.Errorf("material file unavailable")
		}
		files[item.FileID] = file
	}
	manifest, err := materialManifest(input, files)
	if err != nil {
		return "", err
	}
	packID := idgen.Next()
	if err := tx.Table("ip_material_pack").Create(map[string]any{"id": packID, "status": "DRAFT", "version": 1, "created_by": userID, "ip_id": ipID, "version_no": 1, "pack_name": strings.TrimSpace(input.PackName), "scope_description": nullable(input.ScopeDescription), "manifest_hash": manifest, "enabled": true}).Error; err != nil {
		return "", err
	}
	for _, item := range input.Items {
		file := files[item.FileID]
		itemID := idgen.Next()
		if err := tx.Table("ip_material_pack_item").Create(map[string]any{"id": itemID, "material_pack_id": packID, "file_id": item.FileID, "file_hash": file.FileHash, "material_role": item.MaterialRole, "sort_no": item.SortNo, "deliverable": item.Deliverable, "scope_description": nullable(item.ScopeDescription), "created_by": userID}).Error; err != nil {
			return "", err
		}
		if err := s.linkFile(tx, item.FileID, "IP_MATERIAL_PACK", packID, "MATERIAL_ITEM", item.SortNo, userID); err != nil {
			return "", err
		}
	}
	return manifest, nil
}

func materialManifest(input *MaterialPackInput, files map[uint64]materialFileRow) (string, error) {
	type manifestItem struct {
		FileID      string `json:"fileId"`
		FileHash    string `json:"fileHash"`
		Role        string `json:"role"`
		SortNo      int    `json:"sortNo"`
		Deliverable bool   `json:"deliverable"`
		Scope       string `json:"scope"`
	}
	items := make([]manifestItem, 0, len(input.Items))
	for _, item := range input.Items {
		items = append(items, manifestItem{FileID: fmt.Sprint(item.FileID), FileHash: files[item.FileID].FileHash, Role: strings.TrimSpace(item.MaterialRole), SortNo: item.SortNo, Deliverable: item.Deliverable, Scope: strings.TrimSpace(item.ScopeDescription)})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].SortNo != items[j].SortNo {
			return items[i].SortNo < items[j].SortNo
		}
		return items[i].FileID < items[j].FileID
	})
	payload := struct {
		PackName string         `json:"packName"`
		Scope    string         `json:"scope"`
		Items    []manifestItem `json:"items"`
	}{PackName: strings.TrimSpace(input.PackName), Scope: strings.TrimSpace(input.ScopeDescription), Items: items}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func (s *Service) lockMaterialPack(tx *gorm.DB, ipID uint64, now time.Time) (string, error) {
	var pack struct {
		ID                 uint64
		ManifestHash       string     `gorm:"column:manifest_hash"`
		SubmissionLockedAt *time.Time `gorm:"column:submission_locked_at"`
		Status             string
	}
	if err := tx.Clauses().Table("ip_material_pack").Where("ip_id=?", ipID).Take(&pack).Error; err != nil {
		return "", err
	}
	if pack.ManifestHash == "" {
		return "", fmt.Errorf("material manifest missing")
	}
	if pack.SubmissionLockedAt == nil {
		if err := tx.Table("ip_material_pack").Where("id=? AND submission_locked_at IS NULL", pack.ID).Updates(map[string]any{"submission_locked_at": now, "submission_manifest_hash": pack.ManifestHash, "updated_at": now}).Error; err != nil {
			return "", err
		}
	}
	return pack.ManifestHash, nil
}

func (s *Service) submitMaterialEvidence(ctx context.Context, ipID uint64) (uint64, string, error) {
	var pack struct {
		ID             uint64
		ManifestHash   string  `gorm:"column:manifest_hash"`
		EvidenceID     *uint64 `gorm:"column:evidence_id"`
		EvidenceStatus string  `gorm:"column:evidence_status"`
	}
	if err := s.db.WithContext(ctx).Table("ip_material_pack").Where("ip_id=?", ipID).Take(&pack).Error; err != nil {
		return 0, "MATERIAL_PACK_REQUIRED", nil
	}
	if pack.EvidenceStatus == "EVIDENCED" && pack.EvidenceID != nil {
		return *pack.EvidenceID, "", nil
	}
	id, code, err := s.evidence.Submit(ctx, evidence.SubmitRequest{ObjectType: "IP_MATERIAL_PACK", ObjectID: pack.ID, EvidenceType: "IP_MATERIAL_PACK_MANIFEST", HashValue: pack.ManifestHash})
	if code != "" || err != nil {
		return 0, code, err
	}
	var row struct {
		Status          string
		ReceiptNo       string `gorm:"column:receipt_no"`
		TransactionHash string `gorm:"column:chain_transaction_hash"`
	}
	if err := s.db.WithContext(ctx).Table("evidence_record").Where("id=?", id).Take(&row).Error; err != nil {
		return 0, "INTERNAL_ERROR", err
	}
	if row.Status != "SUCCESS" || row.ReceiptNo == "" || row.TransactionHash == "" {
		return 0, "EVIDENCE_NOT_CONFIRMED", nil
	}
	err = s.db.WithContext(ctx).Table("ip_material_pack").Where("id=?", pack.ID).Updates(map[string]any{"evidence_id": id, "evidence_status": "EVIDENCED", "evidence_receipt_no": row.ReceiptNo, "evidence_transaction_hash": row.TransactionHash, "updated_at": time.Now()}).Error
	if err != nil {
		return 0, "INTERNAL_ERROR", err
	}
	return id, "", nil
}

func (s *Service) freezeMaterialPack(tx *gorm.DB, ipID uint64, now time.Time) error {
	updated := tx.Table("ip_material_pack").Where("ip_id=? AND evidence_status='EVIDENCED' AND submission_locked_at IS NOT NULL", ipID).Updates(map[string]any{"status": "FROZEN", "frozen_at": now, "enabled": true, "updated_at": now})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return fmt.Errorf("material pack not evidenced")
	}
	return audit.WriteStatusHistory(tx, "IP_MATERIAL_PACK", ipID, "EVIDENCED", "FROZEN", "IP确权后冻结素材包", audit.Operator{Type: "SYSTEM", RoleCode: "SYSTEM"})
}

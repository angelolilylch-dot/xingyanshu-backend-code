package authorizations

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/modules/files"
	"xingyanshu-backend/internal/platform/idgen"
)

type Service struct {
	db    *gorm.DB
	files *files.Service
}

func NewService(db *gorm.DB, fileService *files.Service) *Service {
	return &Service{db: db, files: fileService}
}

type authorizationRow struct {
	ID                      uint64
	IPID                    uint64 `gorm:"column:ip_id"`
	AuthorizationNo         string `gorm:"column:authorization_no"`
	Status                  string
	AuthorizationMode       string    `gorm:"column:authorization_mode"`
	StartAt                 time.Time `gorm:"column:start_at"`
	EndAt                   time.Time `gorm:"column:end_at"`
	AuthOrderID             uint64    `gorm:"column:auth_order_id"`
	AuthOrderNo             string    `gorm:"column:auth_order_no"`
	Amount                  string
	Currency                string
	PaidAt                  *time.Time `gorm:"column:paid_at"`
	PurchaseSnapshot        []byte     `gorm:"column:purchase_snapshot"`
	ContractID              *uint64    `gorm:"column:contract_id"`
	ContractNo              *string    `gorm:"column:contract_no"`
	ContractFileID          *uint64    `gorm:"column:contract_file_id"`
	ContractFileHash        *string    `gorm:"column:contract_file_hash"`
	ContractStatus          string     `gorm:"column:contract_status"`
	EvidenceStatus          string     `gorm:"column:evidence_status"`
	EvidenceReceiptNo       *string    `gorm:"column:evidence_receipt_no"`
	EvidenceTransactionHash *string    `gorm:"column:evidence_transaction_hash"`
	CertificateID           *uint64    `gorm:"column:certificate_id"`
	CertificateNo           *string    `gorm:"column:certificate_no"`
	CertificateFileID       *uint64    `gorm:"column:certificate_file_id"`
}

type MaterialDownloadInput struct {
	MaterialItemID string `json:"materialItemId" binding:"required"`
}

func (s *Service) List(ctx context.Context, current creators.CurrentCreator, status, ipID string, pageNo, pageSize int) (map[string]any, string, error) {
	if pageNo < 1 {
		pageNo = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	query := s.baseQuery(ctx, current.CreatorID)
	if status != "" {
		query = query.Where("a.status=?", status)
	}
	if ipID != "" {
		var parsed uint64
		if _, err := fmt.Sscan(ipID, &parsed); err != nil || parsed == 0 {
			return map[string]any{"fieldErrors": []map[string]string{{"field": "ipId", "message": "必须为正整数"}}}, "VALIDATION_FORMAT_INVALID", nil
		}
		query = query.Where("a.ip_id=?", parsed)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	var rows []authorizationRow
	if err := selectAuthorizationRows(query).Order("a.created_at DESC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, authorizationData(row, false))
	}
	return map[string]any{"items": items, "pageNo": pageNo, "pageSize": pageSize, "total": total, "totalPages": (total + int64(pageSize) - 1) / int64(pageSize)}, "", nil
}

func (s *Service) Get(ctx context.Context, current creators.CurrentCreator, authorizationID uint64) (map[string]any, string, error) {
	row, code, err := s.find(ctx, current, authorizationID)
	if err != nil || code != "" {
		return nil, code, err
	}
	return authorizationData(row, true), "", nil
}

func (s *Service) ContractDownload(ctx context.Context, current creators.CurrentCreator, authorizationID uint64, clientIP string) (map[string]any, string, error) {
	row, code, err := s.find(ctx, current, authorizationID)
	if err != nil || code != "" {
		return nil, code, err
	}
	if row.ContractFileID == nil || *row.ContractFileID == 0 || row.ContractStatus != "GENERATED" {
		return map[string]any{"contractStatus": row.ContractStatus, "evidenceStatus": row.EvidenceStatus}, "CONTRACT_NOT_READY", nil
	}
	download, code, err := s.files.CreateDownloadURLForCreator(ctx, current.UserID, clientIP, *row.ContractFileID)
	if err != nil || code != "" {
		return nil, code, err
	}
	return map[string]any{"authorizationId": fmt.Sprint(row.ID), "contractNo": stringValue(row.ContractNo), "fileId": fmt.Sprint(*row.ContractFileID), "fileHash": stringValue(row.ContractFileHash), "downloadUrl": download["downloadURL"], "expiresAt": download["expiresAt"], "evidenceStatus": row.EvidenceStatus, "evidenceReceiptNo": stringValue(row.EvidenceReceiptNo), "evidenceTransactionHash": stringValue(row.EvidenceTransactionHash)}, "", nil
}

func (s *Service) CertificateDownload(ctx context.Context, current creators.CurrentCreator, authorizationID uint64, clientIP string) (map[string]any, string, error) {
	row, code, err := s.find(ctx, current, authorizationID)
	if err != nil || code != "" {
		return nil, code, err
	}
	if row.CertificateFileID == nil || *row.CertificateFileID == 0 {
		return map[string]any{"contractStatus": row.ContractStatus}, "CERTIFICATE_NOT_READY", nil
	}
	download, code, err := s.files.CreateDownloadURLForCreator(ctx, current.UserID, clientIP, *row.CertificateFileID)
	if err != nil || code != "" {
		return nil, code, err
	}
	return map[string]any{"authorizationId": fmt.Sprint(row.ID), "certificateNo": stringValue(row.CertificateNo), "fileId": fmt.Sprint(*row.CertificateFileID), "downloadUrl": download["downloadURL"], "expiresAt": download["expiresAt"]}, "", nil
}

func (s *Service) MaterialPacks(ctx context.Context, current creators.CurrentCreator, authorizationID uint64) (map[string]any, string, error) {
	row, code, err := s.find(ctx, current, authorizationID)
	if err != nil || code != "" {
		return nil, code, err
	}
	if row.Status != "ACTIVE" || !row.EndAt.After(time.Now()) {
		return map[string]any{"authorizationStatus": row.Status}, "AUTHORIZATION_EXPIRED", nil
	}
	expectedManifest := materialManifestFromSnapshot(row.PurchaseSnapshot)
	if expectedManifest == "" {
		return nil, "MATERIAL_PACK_SNAPSHOT_MISMATCH", nil
	}
	var packs []struct {
		ID                      uint64
		PackName                string `gorm:"column:pack_name"`
		ManifestHash            string `gorm:"column:manifest_hash"`
		EvidenceReceiptNo       string `gorm:"column:evidence_receipt_no"`
		EvidenceTransactionHash string `gorm:"column:evidence_transaction_hash"`
		ScopeDescription        string `gorm:"column:scope_description"`
	}
	if err := s.db.WithContext(ctx).Table("ip_material_pack").Select("id,pack_name,manifest_hash,evidence_receipt_no,evidence_transaction_hash,COALESCE(scope_description,'') AS scope_description").Where("ip_id=? AND status='FROZEN' AND evidence_status='EVIDENCED' AND enabled=1", row.IPID).Order("id").Find(&packs).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	items := make([]map[string]any, 0)
	resultPacks := make([]map[string]any, 0, len(packs))
	for _, pack := range packs {
		if pack.ManifestHash != expectedManifest {
			continue
		}
		var materialItems []struct {
			ID               uint64
			FileID           uint64 `gorm:"column:file_id"`
			FileHash         string `gorm:"column:file_hash"`
			MaterialRole     string `gorm:"column:material_role"`
			SortNo           int    `gorm:"column:sort_no"`
			ScopeDescription string `gorm:"column:scope_description"`
			OriginalName     string `gorm:"column:original_name"`
			MimeType         string `gorm:"column:mime_type"`
			FileSize         int64  `gorm:"column:file_size"`
		}
		if err := s.db.WithContext(ctx).Table("ip_material_pack_item i").Select("i.id,i.file_id,i.file_hash,i.material_role,i.sort_no,COALESCE(i.scope_description,'') AS scope_description,f.original_name,f.mime_type,f.file_size").Joins("JOIN file_object f ON f.id=i.file_id AND f.status='ACTIVE' AND f.upload_status='SUCCESS'").Where("i.material_pack_id=? AND i.deliverable=1", pack.ID).Order("i.sort_no,i.id").Find(&materialItems).Error; err != nil {
			return nil, "INTERNAL_ERROR", err
		}
		packItems := make([]map[string]any, 0, len(materialItems))
		for _, item := range materialItems {
			data := map[string]any{"materialItemId": fmt.Sprint(item.ID), "fileId": fmt.Sprint(item.FileID), "fileName": item.OriginalName, "mimeType": item.MimeType, "fileSize": item.FileSize, "fileHash": item.FileHash, "materialRole": item.MaterialRole, "scopeDescription": item.ScopeDescription}
			packItems = append(packItems, data)
			items = append(items, data)
		}
		resultPacks = append(resultPacks, map[string]any{"materialPackId": fmt.Sprint(pack.ID), "packName": pack.PackName, "manifestHash": pack.ManifestHash, "scopeDescription": pack.ScopeDescription, "evidenceReceiptNo": pack.EvidenceReceiptNo, "evidenceTransactionHash": pack.EvidenceTransactionHash, "items": packItems})
	}
	if len(resultPacks) == 0 {
		return nil, "MATERIAL_PACK_SNAPSHOT_MISMATCH", nil
	}
	return map[string]any{"authorizationId": fmt.Sprint(row.ID), "manifestReferencedInOrder": expectedManifest, "materialPacks": resultPacks, "items": items}, "", nil
}

func (s *Service) MaterialDownload(ctx context.Context, current creators.CurrentCreator, authorizationID uint64, input MaterialDownloadInput, clientIP string) (map[string]any, string, error) {
	itemID, err := parsePositiveID(input.MaterialItemID)
	if err != nil {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	row, code, err := s.find(ctx, current, authorizationID)
	if err != nil || code != "" {
		return nil, code, err
	}
	if row.Status != "ACTIVE" || !row.EndAt.After(time.Now()) {
		return map[string]any{"authorizationStatus": row.Status}, "AUTHORIZATION_EXPIRED", nil
	}
	var material struct {
		ItemID         uint64 `gorm:"column:item_id"`
		PackID         uint64 `gorm:"column:pack_id"`
		FileID         uint64 `gorm:"column:file_id"`
		ManifestHash   string `gorm:"column:manifest_hash"`
		PackStatus     string `gorm:"column:pack_status"`
		EvidenceStatus string `gorm:"column:evidence_status"`
		Deliverable    bool
	}
	err = s.db.WithContext(ctx).Table("ip_material_pack_item i").Select("i.id AS item_id,i.file_id,p.id AS pack_id,p.manifest_hash,p.status AS pack_status,p.evidence_status,i.deliverable").Joins("JOIN ip_material_pack p ON p.id=i.material_pack_id").Joins("JOIN file_object f ON f.id=i.file_id AND f.status='ACTIVE' AND f.upload_status='SUCCESS'").Where("i.id=? AND p.ip_id=?", itemID, row.IPID).Take(&material).Error
	if err == gorm.ErrRecordNotFound {
		return nil, "MATERIAL_ITEM_NOT_FOUND", nil
	}
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	if !material.Deliverable {
		return nil, "MATERIAL_ITEM_NOT_DELIVERABLE", nil
	}
	if material.PackStatus != "FROZEN" {
		return nil, "MATERIAL_PACK_NOT_FROZEN", nil
	}
	if material.EvidenceStatus != "EVIDENCED" {
		return nil, "MATERIAL_PACK_EVIDENCE_PENDING", nil
	}
	if expected := materialManifestFromSnapshot(row.PurchaseSnapshot); expected == "" || expected != material.ManifestHash {
		return nil, "MATERIAL_PACK_SNAPSHOT_MISMATCH", nil
	}
	download, code, err := s.files.CreateDownloadURLForAuthorizedCreator(ctx, current.UserID, clientIP, material.FileID)
	if err != nil || code != "" {
		return nil, code, err
	}
	var contract struct{ ID uint64 }
	contractID := any(nil)
	if err := s.db.WithContext(ctx).Table("auth_contract").Select("id").Where("authorization_id=?", row.ID).Take(&contract).Error; err == nil {
		contractID = contract.ID
	}
	if err := s.db.WithContext(ctx).Table("material_download_log").Create(map[string]any{"id": idgen.Next(), "created_by": current.UserID, "authorization_id": row.ID, "contract_id": contractID, "material_pack_id": material.PackID, "material_item_id": material.ItemID, "file_id": material.FileID, "manifest_hash": material.ManifestHash, "client_ip": clientIP, "download_result": "ISSUED"}).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"authorizationId": fmt.Sprint(row.ID), "materialItemId": fmt.Sprint(material.ItemID), "materialPackId": fmt.Sprint(material.PackID), "manifestHash": material.ManifestHash, "downloadUrl": download["downloadURL"], "expiresAt": download["expiresAt"]}, "", nil
}

func (s *Service) find(ctx context.Context, current creators.CurrentCreator, authorizationID uint64) (authorizationRow, string, error) {
	var row authorizationRow
	err := selectAuthorizationRows(s.baseQuery(ctx, current.CreatorID)).Where("a.id=?", authorizationID).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return row, "AUTHORIZATION_NOT_FOUND", nil
	}
	if err != nil {
		return row, "INTERNAL_ERROR", err
	}
	return row, "", nil
}

func (s *Service) baseQuery(ctx context.Context, creatorID uint64) *gorm.DB {
	return s.db.WithContext(ctx).Table("creator_authorization a").Joins("JOIN auth_order o ON o.id=a.auth_order_id").Joins("LEFT JOIN auth_contract c ON c.authorization_id=a.id").Joins("LEFT JOIN auth_certificate cert ON cert.authorization_id=a.id").Where("a.creator_id=?", creatorID)
}

func selectAuthorizationRows(query *gorm.DB) *gorm.DB {
	return query.Select("a.id,a.ip_id,a.authorization_no,a.status,a.authorization_mode,a.start_at,a.end_at,a.auth_order_id,o.auth_order_no,o.amount,o.currency,o.paid_at,o.purchase_snapshot,o.contract_status,c.id AS contract_id,c.contract_no,c.contract_file_id,c.file_hash AS contract_file_hash,COALESCE(c.evidence_status,o.evidence_status) AS evidence_status,c.evidence_receipt_no,c.evidence_transaction_hash,cert.id AS certificate_id,cert.certificate_no,cert.file_id AS certificate_file_id")
}

func parsePositiveID(raw string) (uint64, error) {
	var id uint64
	_, err := fmt.Sscan(raw, &id)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("invalid id")
	}
	return id, nil
}
func materialManifestFromSnapshot(raw []byte) string {
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	if m, ok := v["materialPack"].(map[string]any); ok {
		return fmt.Sprint(m["manifestHash"])
	}
	return ""
}

func authorizationData(row authorizationRow, includeSnapshot bool) map[string]any {
	var snapshot map[string]any
	_ = json.Unmarshal(row.PurchaseSnapshot, &snapshot)
	result := map[string]any{"id": fmt.Sprint(row.ID), "authorizationNo": row.AuthorizationNo, "status": row.Status, "authorizationMode": row.AuthorizationMode, "startAt": row.StartAt, "endAt": row.EndAt, "authOrderId": fmt.Sprint(row.AuthOrderID), "authOrderNo": row.AuthOrderNo, "amount": row.Amount, "currency": row.Currency, "paidAt": row.PaidAt, "contract": map[string]any{"status": row.ContractStatus, "contractNo": stringValue(row.ContractNo), "downloadReady": row.ContractFileID != nil && *row.ContractFileID != 0 && row.ContractStatus == "GENERATED", "downloadPath": fmt.Sprintf("/api/v1/creator/authorizations/%d/contract", row.ID), "fileHash": stringValue(row.ContractFileHash)}, "certificate": map[string]any{"certificateNo": stringValue(row.CertificateNo), "downloadReady": row.CertificateFileID != nil && *row.CertificateFileID != 0, "downloadPath": fmt.Sprintf("/api/v1/creator/authorizations/%d/certificate", row.ID)}, "evidence": map[string]any{"status": row.EvidenceStatus, "receiptNo": stringValue(row.EvidenceReceiptNo), "transactionHash": stringValue(row.EvidenceTransactionHash)}}
	if ip, ok := snapshot["ip"]; ok {
		result["ip"] = ip
	}
	if plan, ok := snapshot["authorization"]; ok {
		result["authorizationScope"] = plan
	}
	if includeSnapshot {
		result["purchaseSnapshot"] = snapshot
	}
	return result
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

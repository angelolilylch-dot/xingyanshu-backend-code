package works

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
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/contentmoderation"
	"xingyanshu-backend/internal/platform/evidence"
	"xingyanshu-backend/internal/platform/idgen"
)

const reviewPermission = "WORK_REVIEW_COMPLIANCE"

type Service struct {
	db         *gorm.DB
	moderation contentmoderation.Provider
	evidence   *evidence.Service
}

func NewService(db *gorm.DB, moderation contentmoderation.Provider, evidenceService *evidence.Service) *Service {
	return &Service{db: db, moderation: moderation, evidence: evidenceService}
}

type SubmitRequest struct {
	AuthorizationID            uint64          `json:"authorizationId" binding:"required"`
	WorkName                   string          `json:"workName" binding:"required,max=200"`
	WorkType                   string          `json:"workType" binding:"required"`
	WorkIntro                  string          `json:"workIntro"`
	CreationDescription        string          `json:"creationDescription" binding:"required"`
	CompletedAt                *time.Time      `json:"completedAt"`
	PublicCopyrightNotice      string          `json:"publicCopyrightNotice"`
	CopyrightDeclaration       string          `json:"copyrightDeclaration" binding:"required"`
	Declarations               map[string]bool `json:"declarations" binding:"required"`
	AIAssisted                 bool            `json:"aiAssisted"`
	AIAssistanceDescription    string          `json:"aiAssistanceDescription"`
	CoCreatorDescription       string          `json:"coCreatorDescription"`
	NewExpressionRatio         uint8           `json:"newExpressionRatio" binding:"required"`
	NewExpressionDimensions    []string        `json:"newExpressionDimensions" binding:"required"`
	NewExpressionDescription   string          `json:"newExpressionDescription" binding:"required"`
	OriginalIPUsageDescription string          `json:"originalIPUsageDescription" binding:"required"`
	PreviewFileIDs             []uint64        `json:"previewFileIds" binding:"required"`
	SourceFileIDs              []uint64        `json:"sourceFileIds" binding:"required"`
	ProcessFileIDs             []uint64        `json:"processFileIds"`
	MaterialItemIDs            []uint64        `json:"materialItemIds" binding:"required"`
}
type ReviewRequest struct {
	ReviewNode      string `json:"reviewNode" binding:"required"`
	Result          string `json:"result" binding:"required"`
	Comment         string `json:"comment"`
	SupplementItems string `json:"supplementItems"`
}
type ConfirmRequest struct {
	Comment                    string `json:"comment"`
	ReviewedNewExpressionRatio uint8  `json:"reviewedNewExpressionRatio" binding:"required"`
}

var requiredDeclarations = []string{"SELF_CREATED", "AUTHORIZED_IP_MATERIAL", "NO_UNAUTHORIZED_THIRD_PARTY", "NO_THIRD_PARTY_RIGHTS_INFRINGEMENT", "AUTHENTIC_AND_LAWFUL"}
var allowedTypes = map[string]bool{"IMAGE": true, "VIDEO": true, "GRAPHIC_DESIGN": true, "ARTICLE": true, "MUSIC_AUDIO": true, "OTHER": true}

func (s *Service) Submit(ctx context.Context, current creators.CurrentCreator, input SubmitRequest) (map[string]any, string, error) {
	return s.submit(ctx, current, input, 0)
}

// Resubmit 仅允许被退回、驳回或文本检测失败的本人作品新建版本；已确权作品永不覆盖。
func (s *Service) Resubmit(ctx context.Context, current creators.CurrentCreator, workID uint64, input SubmitRequest) (map[string]any, string, error) {
	return s.submit(ctx, current, input, workID)
}

func (s *Service) submit(ctx context.Context, current creators.CurrentCreator, input SubmitRequest, existingWorkID uint64) (map[string]any, string, error) {
	if !allowedTypes[input.WorkType] || input.NewExpressionRatio < 51 || input.NewExpressionRatio > 100 || len(input.NewExpressionDimensions) == 0 || !allDeclarations(input.Declarations) || len(input.PreviewFileIDs) == 0 || len(input.SourceFileIDs) == 0 || len(input.MaterialItemIDs) == 0 {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	if input.AIAssisted && strings.TrimSpace(input.AIAssistanceDescription) == "" {
		return nil, "VALIDATION_REQUIRED", nil
	}
	var output map[string]any
	var submittedWorkID uint64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var auth struct {
			ID, IPID          uint64
			Status            string
			EndAt             time.Time `gorm:"column:end_at"`
			DerivativeAllowed bool      `gorm:"column:derivative_allowed_snapshot"`
			ManifestHash      string    `gorm:"column:material_pack_manifest_hash"`
		}
		if err := tx.Table("creator_authorization AS ca").Select("ca.id,ca.ip_id,ca.status,ca.end_at,ao.derivative_allowed_snapshot,ao.material_pack_manifest_hash").Joins("JOIN auth_order ao ON ao.id=ca.auth_order_id").Where("ca.id=? AND ca.creator_id=?", input.AuthorizationID, current.CreatorID).Take(&auth).Error; err != nil {
			return business("AUTHORIZATION_NOT_FOUND")
		}
		if auth.Status != "ACTIVE" || !auth.EndAt.After(time.Now()) {
			return business("AUTHORIZATION_NOT_ACTIVE")
		}
		if !auth.DerivativeAllowed {
			return business("AUTHORIZATION_DERIVATIVE_NOT_ALLOWED")
		}
		var pack struct {
			ID                                   uint64
			Status, EvidenceStatus, ManifestHash string
			Enabled                              bool
		}
		if err := tx.Table("ip_material_pack").Where("ip_id=? AND status='FROZEN' AND evidence_status='EVIDENCED' AND enabled=1", auth.IPID).Order("id DESC").Take(&pack).Error; err != nil {
			return business("MATERIAL_PACK_NOT_AVAILABLE")
		}
		if auth.ManifestHash != "" && pack.ManifestHash != auth.ManifestHash {
			return business("MATERIAL_PACK_SNAPSHOT_MISMATCH")
		}
		var materials []struct {
			ID, FileID uint64
			FileHash   string `gorm:"column:file_hash"`
		}
		if err := tx.Table("ip_material_pack_item").Where("material_pack_id=? AND id IN ?", pack.ID, input.MaterialItemIDs).Find(&materials).Error; err != nil {
			return err
		}
		if len(materials) != len(uniqueIDs(input.MaterialItemIDs)) {
			return business("MATERIAL_ITEM_NOT_FOUND")
		}
		if err := validateFiles(tx, append(append(input.PreviewFileIDs, input.SourceFileIDs...), input.ProcessFileIDs...)); err != nil {
			return err
		}
		workID, versionID := existingWorkID, idgen.Next()
		submittedWorkID = workID
		now := time.Now()
		dims, _ := json.Marshal(input.NewExpressionDimensions)
		versionNo, fromStatus := 1, "SUBMITTING"
		if workID == 0 {
			workID = idgen.Next()
			submittedWorkID = workID
			if err := tx.Table("derivative_work").Create(map[string]any{"id": workID, "work_no": fmt.Sprintf("WORK-%d", workID), "creator_id": current.CreatorID, "ip_id": auth.IPID, "authorization_id": auth.ID, "current_version_id": versionID, "status": "TEXT_CHECKING", "version": 1, "created_by": current.UserID, "updated_by": current.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}).Error; err != nil {
				return err
			}
		} else {
			var old struct {
				CreatorID, IPID uint64
				Status          string
			}
			if err := tx.Table("derivative_work").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", workID).Take(&old).Error; err != nil {
				return business("WORK_NOT_FOUND")
			}
			if old.CreatorID != current.CreatorID {
				return business("WORK_NOT_FOUND")
			}
			if old.IPID != auth.IPID {
				return business("WORK_RESUBMIT_IP_MISMATCH")
			}
			if old.Status != "RETURNED" && old.Status != "REJECTED" && old.Status != "TEXT_REJECTED" && old.Status != "TEXT_CHECK_ERROR" {
				return business("WORK_STATUS_INVALID")
			}
			var count int64
			if err := tx.Table("work_version").Where("work_id=?", workID).Count(&count).Error; err != nil {
				return err
			}
			versionNo = int(count) + 1
			fromStatus = old.Status
			if err := tx.Table("derivative_work").Where("id=?", workID).Updates(map[string]any{"authorization_id": auth.ID, "current_version_id": versionID, "status": "TEXT_CHECKING", "updated_by": current.UserID, "updated_at": now, "version": gorm.Expr("version+1")}).Error; err != nil {
				return err
			}
		}
		version := map[string]any{"id": versionID, "work_id": workID, "version_no": versionNo, "work_name": input.WorkName, "work_type": input.WorkType, "work_intro": input.WorkIntro, "creation_description": input.CreationDescription, "completed_at": input.CompletedAt, "public_copyright_notice": input.PublicCopyrightNotice, "copyright_declaration": input.CopyrightDeclaration, "declaration_confirmed": true, "ai_assisted": input.AIAssisted, "ai_assistance_description": input.AIAssistanceDescription, "co_creator_description": input.CoCreatorDescription, "new_expression_ratio": input.NewExpressionRatio, "new_expression_dimensions_json": string(dims), "new_expression_description": input.NewExpressionDescription, "original_ip_usage_description": input.OriginalIPUsageDescription, "status": "TEXT_CHECKING", "detection_status": "PENDING", "version": 1, "created_by": current.UserID, "updated_by": current.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}
		if err := tx.Table("work_version").Create(version).Error; err != nil {
			return err
		}
		if err := writeFiles(tx, versionID, current.UserID, input); err != nil {
			return err
		}
		for _, m := range materials {
			if err := tx.Table("work_material_usage").Create(map[string]any{"id": idgen.Next(), "work_version_id": versionID, "material_pack_id": pack.ID, "material_item_id": m.ID, "file_id": m.FileID, "file_hash": m.FileHash, "created_by": current.UserID}).Error; err != nil {
				return err
			}
		}
		return audit.WriteStatusHistory(tx, "DERIVATIVE_WORK", workID, fromStatus, "TEXT_CHECKING", "创作者提交二创作品", audit.Operator{Type: "USER", ID: current.UserID})
	})
	if err != nil {
		return nil, codeOf(err), unwrap(err)
	}
	decision, err := s.moderation.Moderate(ctx, contentmoderation.Request{Text: strings.Join([]string{input.WorkName, input.WorkIntro, input.CreationDescription, input.CopyrightDeclaration}, "\n"), DataID: fmt.Sprint(submittedWorkID), SessionID: fmt.Sprint(submittedWorkID), UserID: fmt.Sprint(current.UserID), Language: "zh"})
	if err != nil {
		decision.Decision = "ERROR"
	}
	status, detection := "PENDING_COPYRIGHT_REVIEW", "PASS"
	reason := ""
	if decision.Decision == contentmoderation.DecisionBlock {
		status, detection, reason = "TEXT_REJECTED", "BLOCK", "文本检测未通过"
	} else if decision.Decision == "ERROR" {
		status, detection, reason = "TEXT_CHECK_ERROR", "ERROR", "文本检测服务异常"
	} else if decision.Decision == contentmoderation.DecisionReview {
		detection = "REVIEW"
	}
	_ = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		tx.Table("derivative_work").Where("id=?", submittedWorkID).Updates(map[string]any{"status": status, "status_reason": reason, "updated_at": time.Now(), "version": gorm.Expr("version+1")})
		tx.Table("work_version").Where("work_id=?", submittedWorkID).Updates(map[string]any{"status": status, "detection_status": detection, "updated_at": time.Now(), "version": gorm.Expr("version+1")})
		return audit.WriteStatusHistory(tx, "DERIVATIVE_WORK", submittedWorkID, "TEXT_CHECKING", status, "文本检测完成", audit.Operator{Type: "SYSTEM"})
	})
	output = map[string]any{"workId": submittedWorkID, "status": status, "detectionStatus": detection}
	return output, "", nil
}

func (s *Service) Review(ctx context.Context, current identity.CurrentAdmin, versionID uint64, input ReviewRequest) (map[string]any, string, error) {
	if !current.HasPermission(reviewPermission) || (input.ReviewNode != "COPYRIGHT_INITIAL" && input.ReviewNode != "COMPLIANCE_REVIEW") || (input.Result != "PASS" && input.Result != "RETURN" && input.Result != "REJECT") {
		return nil, "AUTH_FORBIDDEN", nil
	}
	var out map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var v struct {
			ID, WorkID uint64
			Status     string
		}
		if e := tx.Table("work_version").Where("id=?", versionID).Take(&v).Error; e != nil {
			return business("WORK_VERSION_NOT_FOUND")
		}
		expected := "PENDING_COPYRIGHT_REVIEW"
		if input.ReviewNode == "COMPLIANCE_REVIEW" {
			expected = "PENDING_COMPLIANCE_REVIEW"
		}
		if v.Status != expected {
			return business("WORK_STATUS_INVALID")
		}
		next := ""
		if input.Result == "PASS" {
			if input.ReviewNode == "COPYRIGHT_INITIAL" {
				next = "PENDING_COMPLIANCE_REVIEW"
			} else {
				next = "RIGHTS_REGISTERING"
			}
		} else if input.Result == "RETURN" {
			next = "RETURNED"
		} else {
			next = "REJECTED"
		}
		now := time.Now()
		if e := tx.Table("work_review_record").Create(map[string]any{"id": idgen.Next(), "work_version_id": v.ID, "review_node": input.ReviewNode, "review_result": input.Result, "review_comment": input.Comment, "supplement_items": input.SupplementItems, "reviewer_id": current.UserID, "created_by": current.UserID}).Error; e != nil {
			return e
		}
		if e := tx.Table("work_version").Where("id=?", v.ID).Updates(map[string]any{"status": next, "updated_by": current.UserID, "updated_at": now, "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
		if e := tx.Table("derivative_work").Where("id=?", v.WorkID).Updates(map[string]any{"status": next, "updated_by": current.UserID, "updated_at": now, "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
		out = map[string]any{"workId": v.WorkID, "workVersionId": v.ID, "status": next}
		return audit.WriteStatusHistory(tx, "DERIVATIVE_WORK", v.WorkID, expected, next, "作品审核", audit.Operator{Type: "USER", ID: current.UserID})
	})
	if err != nil {
		return nil, codeOf(err), unwrap(err)
	}
	return out, "", nil
}

func (s *Service) Confirm(ctx context.Context, current identity.CurrentAdmin, workID uint64, input ConfirmRequest) (map[string]any, string, error) {
	if !current.HasPermission(reviewPermission) {
		return nil, "AUTH_FORBIDDEN", nil
	}
	if input.ReviewedNewExpressionRatio < 51 || input.ReviewedNewExpressionRatio > 100 {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	var confirmationID uint64
	var snapshotHash string
	var versionID uint64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var w struct {
			ID, CreatorID, IPID, AuthorizationID, CurrentVersionID uint64
			Status                                                 string
		}
		if e := tx.Table("derivative_work").Where("id=?", workID).Take(&w).Error; e != nil {
			return business("WORK_NOT_FOUND")
		}
		if w.Status != "RIGHTS_REGISTERING" {
			return business("WORK_STATUS_INVALID")
		}
		versionID = w.CurrentVersionID
		snapshot, e := buildSnapshot(tx, w)
		if e != nil {
			return e
		}
		raw, _ := json.Marshal(snapshot)
		sum := sha256.Sum256(raw)
		snapshotHash = hex.EncodeToString(sum[:])
		confirmationID = idgen.Next()
		now := time.Now()
		if e := tx.Table("work_rights_confirmation").Create(map[string]any{"id": confirmationID, "work_id": w.ID, "work_version_id": versionID, "rights_no": fmt.Sprintf("WR-%d", confirmationID), "confirmation_result": "CONFIRMED", "file_hash": snapshotHash, "confirmation_snapshot_hash": snapshotHash, "new_expression_ratio": input.ReviewedNewExpressionRatio, "evidence_status": "PENDING_EVIDENCE", "status": "PENDING_EVIDENCE", "created_by": current.UserID, "updated_by": current.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}).Error; e != nil {
			return e
		}
		if e := tx.Table("derivative_work").Where("id=?", w.ID).Updates(map[string]any{"rights_confirmation_id": confirmationID, "updated_by": current.UserID, "updated_at": now}).Error; e != nil {
			return e
		}
		return audit.WriteLog(tx, "DERIVATIVE_WORK_RIGHTS_CONFIRMATION_CREATED", "DERIVATIVE_WORK", w.ID, nil, map[string]any{"snapshotHash": snapshotHash, "reviewedNewExpressionRatio": input.ReviewedNewExpressionRatio, "comment": input.Comment}, "SUCCESS", audit.Operator{Type: "USER", ID: current.UserID})
	})
	if err != nil {
		return nil, codeOf(err), unwrap(err)
	}
	evidenceID, code, e := s.evidence.Submit(ctx, evidence.SubmitRequest{ObjectType: "DERIVATIVE_WORK", ObjectID: workID, EvidenceType: "DERIVATIVE_WORK_RIGHTS_CONFIRMATION", HashValue: snapshotHash})
	if e != nil || code != "" {
		return map[string]any{"workId": workID, "rightsConfirmationId": confirmationID, "status": "PENDING_EVIDENCE"}, code, e
	}
	var ev struct {
		Status          string
		ReceiptNo       string
		TransactionHash string `gorm:"column:chain_transaction_hash"`
	}
	s.db.WithContext(ctx).Table("evidence_record").Select("status,receipt_no,chain_transaction_hash").Where("id=?", evidenceID).Take(&ev)
	status := "PENDING_EVIDENCE"
	if ev.Status == "SUCCESS" {
		status = "RIGHTS_CONFIRMED"
	}
	_ = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		tx.Table("work_rights_confirmation").Where("id=?", confirmationID).Updates(map[string]any{"evidence_id": evidenceID, "evidence_status": ev.Status, "evidence_receipt_no": ev.ReceiptNo, "evidence_transaction_hash": ev.TransactionHash, "evidenced_at": time.Now(), "status": status, "updated_at": time.Now()})
		tx.Table("derivative_work").Where("id=?", workID).Updates(map[string]any{"status": status, "updated_at": time.Now()})
		tx.Table("work_version").Where("id=?", versionID).Updates(map[string]any{"status": status, "updated_at": time.Now()})
		return audit.WriteStatusHistory(tx, "DERIVATIVE_WORK", workID, "RIGHTS_REGISTERING", status, "作品确权存证完成", audit.Operator{Type: "SYSTEM"})
	})
	return map[string]any{"workId": workID, "rightsConfirmationId": confirmationID, "status": status, "confirmationSnapshotHash": snapshotHash, "evidenceStatus": ev.Status, "evidenceTransactionHash": ev.TransactionHash}, "", nil
}

func (s *Service) List(ctx context.Context, creatorID uint64, pageNo, pageSize int) (map[string]any, string, error) {
	var rows []map[string]any
	var total int64
	q := s.db.WithContext(ctx).Table("derivative_work w").Select("w.id,w.work_no,w.status,w.ip_id,w.authorization_id,w.current_version_id,w.created_at,w.updated_at,v.work_name,v.work_type,v.new_expression_ratio,rc.rights_no,rc.confirmation_snapshot_hash,rc.evidence_status,rc.evidence_transaction_hash").Joins("LEFT JOIN work_version v ON v.id=w.current_version_id").Joins("LEFT JOIN work_rights_confirmation rc ON rc.id=w.rights_confirmation_id").Where("w.creator_id=?", creatorID)
	q.Count(&total)
	q.Order("w.id DESC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Find(&rows)
	return map[string]any{"items": rows, "pageNo": pageNo, "pageSize": pageSize, "total": total}, "", nil
}

// Get 仅向作品所属创作者返回完整提交、审核与确权资料；已确权版本用于展示，不提供覆盖编辑。
func (s *Service) Get(ctx context.Context, creatorID, workID uint64) (map[string]any, string, error) {
	var work map[string]any
	if err := s.db.WithContext(ctx).Table("derivative_work").Where("id=? AND creator_id=?", workID, creatorID).Take(&work).Error; err != nil {
		return nil, "WORK_NOT_FOUND", nil
	}
	var version map[string]any
	if err := s.db.WithContext(ctx).Table("work_version").Where("work_id=?", workID).Order("version_no DESC").Take(&version).Error; err != nil {
		return nil, "WORK_VERSION_NOT_FOUND", nil
	}
	versionID, ok := version["id"].(uint64)
	if !ok || versionID == 0 {
		return nil, "WORK_VERSION_NOT_FOUND", nil
	}
	var files, materials, reviews []map[string]any
	s.db.WithContext(ctx).Table("work_file wf").Select("wf.file_id,wf.file_role,wf.sort_no,fo.original_name,fo.mime_type,fo.file_hash").Joins("JOIN file_object fo ON fo.id=wf.file_id").Where("wf.work_version_id=?", versionID).Order("wf.file_role,wf.sort_no").Find(&files)
	s.db.WithContext(ctx).Table("work_material_usage").Where("work_version_id=?", versionID).Order("material_item_id").Find(&materials)
	s.db.WithContext(ctx).Table("work_review_record").Where("work_version_id=?", versionID).Order("created_at").Find(&reviews)
	var confirmation map[string]any
	s.db.WithContext(ctx).Table("work_rights_confirmation").Where("work_id=?", workID).Take(&confirmation)
	return map[string]any{"work": work, "version": version, "files": files, "materialUsages": materials, "reviews": reviews, "rightsConfirmation": confirmation}, "", nil
}

// Pending 为版权合规人员提供按当前审核节点筛选的作品队列。
func (s *Service) Pending(ctx context.Context, pageNo, pageSize int, reviewNode, status string) (map[string]any, string, error) {
	allowed := map[string]bool{"PENDING_COPYRIGHT_REVIEW": true, "PENDING_COMPLIANCE_REVIEW": true, "RIGHTS_REGISTERING": true}
	if status != "" && !allowed[status] {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	if reviewNode == "COPYRIGHT_INITIAL" {
		status = "PENDING_COPYRIGHT_REVIEW"
	} else if reviewNode == "COMPLIANCE_REVIEW" {
		status = "PENDING_COMPLIANCE_REVIEW"
	} else if reviewNode != "" {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	q := s.db.WithContext(ctx).Table("derivative_work w").Select("w.id,w.work_no,w.status,w.creator_id,w.ip_id,w.current_version_id,w.created_at,v.work_name,v.work_type,v.new_expression_ratio,v.detection_status").Joins("JOIN work_version v ON v.id=w.current_version_id")
	if status != "" {
		q = q.Where("w.status=?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	var items []map[string]any
	if err := q.Order("w.created_at ASC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"items": items, "pageNo": pageNo, "pageSize": pageSize, "total": total}, "", nil
}

func allDeclarations(m map[string]bool) bool {
	for _, k := range requiredDeclarations {
		if !m[k] {
			return false
		}
	}
	return true
}
func uniqueIDs(ids []uint64) []uint64 {
	seen := map[uint64]bool{}
	out := []uint64{}
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
func validateFiles(tx *gorm.DB, ids []uint64) error {
	ids = uniqueIDs(ids)
	var count int64
	if len(ids) == 0 {
		return business("WORK_FILE_REQUIRED")
	}
	if e := tx.Table("file_object").Where("id IN ? AND upload_status='SUCCESS'", ids).Count(&count).Error; e != nil {
		return e
	}
	if int(count) != len(ids) {
		return business("WORK_FILE_NOT_FOUND")
	}
	return nil
}
func writeFiles(tx *gorm.DB, vid, user uint64, in SubmitRequest) error {
	groups := []struct {
		ids  []uint64
		role string
	}{{in.PreviewFileIDs, "WORK_PREVIEW"}, {in.SourceFileIDs, "WORK_SOURCE"}, {in.ProcessFileIDs, "WORK_PROCESS_EVIDENCE"}}
	for _, g := range groups {
		for n, id := range uniqueIDs(g.ids) {
			row := map[string]any{"id": idgen.Next(), "work_version_id": vid, "file_id": id, "file_role": g.role, "sort_no": n, "created_by": user}
			if e := tx.Table("work_file").Create(row).Error; e != nil {
				return e
			}
			if e := tx.Table("file_relation").Create(map[string]any{"id": idgen.Next(), "file_id": id, "object_type": "WORK_VERSION", "object_id": vid, "file_role": g.role, "sort_no": n, "created_by": user}).Error; e != nil {
				return e
			}
		}
	}
	return nil
}
func buildSnapshot(tx *gorm.DB, w struct {
	ID, CreatorID, IPID, AuthorizationID, CurrentVersionID uint64
	Status                                                 string
}) (map[string]any, error) {
	var v map[string]any
	if e := tx.Table("work_version").Where("id=?", w.CurrentVersionID).Take(&v).Error; e != nil {
		return nil, e
	}
	var files, materials []map[string]any
	tx.Table("work_file wf").Select("wf.file_role,wf.sort_no,fo.file_hash,fo.original_name,fo.mime_type").Joins("JOIN file_object fo ON fo.id=wf.file_id").Where("wf.work_version_id=?", w.CurrentVersionID).Order("wf.file_role,wf.sort_no").Find(&files)
	tx.Table("work_material_usage").Select("material_pack_id,material_item_id,file_id,file_hash").Where("work_version_id=?", w.CurrentVersionID).Order("material_item_id").Find(&materials)
	sort.Slice(files, func(i, j int) bool { return fmt.Sprint(files[i]) < fmt.Sprint(files[j]) })
	return map[string]any{"work": w, "version": v, "files": files, "materialUsages": materials, "rule": "新增独创表达须经平台审核认定不少于51%，不改变原始IP权属"}, nil
}

type codedError struct{ code string }

func (e codedError) Error() string { return e.code }
func business(code string) error   { return codedError{code} }
func codeOf(e error) string {
	if x, ok := e.(codedError); ok {
		return x.code
	}
	if e == gorm.ErrRecordNotFound {
		return "RESOURCE_NOT_FOUND"
	}
	return "INTERNAL_ERROR"
}
func unwrap(e error) error {
	if _, ok := e.(codedError); ok {
		return nil
	}
	return e
}

package avatar

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"time"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idgen"
)

const consentPurpose = "为建立个人数字形象档案，采集照片、朗读视频及可选体型资料；使用姓名、身份证号和正面照进行本人一致性核验；首次归档后保存365天。"
const consentAgreementContent = "个人数字形象采集与入库授权\n\n您同意平台为建立个人数字形象档案，采集八张面部照片、朗读视频及您主动填写的可选体型资料。平台将在您提交采集资料后，使用名单中的姓名、身份证号和正面照向腾讯云人脸核身服务发起本人一致性核验。素材仅用于建档、实名核验与质量管理，不用于对外使用授权。\n\n档案首次归档后保存365天；您可在保存期内申请删除。"

func (s *Service) currentConsent(ctx context.Context, cur Current) any {
	var v struct {
		ID               uint64
		AgreementVersion string
		SignedAt         time.Time
		RetentionUntil   *time.Time
	}
	e := s.db.WithContext(ctx).Table("avatar_consent").Select("id,agreement_version,signed_at,retention_until").Where("account_id=? AND status='ACTIVE' AND consent_type='COLLECT_AND_ARCHIVE'", cur.AccountID).Order("signed_at DESC").Take(&v).Error
	if e != nil {
		return gin.H{"status": "REQUIRED", "consentType": "COLLECT_AND_ARCHIVE", "agreementVersion": consentAgreementVersion, "agreementContent": consentAgreementContent, "retentionDays": 365, "purpose": consentPurpose}
	}
	return gin.H{"status": "ACTIVE", "consentId": fmt.Sprint(v.ID), "consentType": "COLLECT_AND_ARCHIVE", "agreementVersion": v.AgreementVersion, "agreementContent": consentAgreementContent, "retentionDays": 365, "retentionUntil": v.RetentionUntil}
}
func (s *Service) acceptConsent(ctx context.Context, cur Current, r consentRequest, ip string) (any, *httpx.AppError) {
	if r.ConsentType != "COLLECT_AND_ARCHIVE" || !r.Accepted || r.AgreementVersion != consentAgreementVersion {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "授权信息不正确", nil)
	}
	var p struct {
		ID                     uint64
		Status, RealNameStatus string
	}
	if e := s.db.WithContext(ctx).Table("avatar_profile").Where("account_id=?", cur.AccountID).Take(&p).Error; e != nil || p.RealNameStatus != "PASSED" {
		return nil, avatarError(422, "AVATAR_REAL_NAME_REQUIRED", "请先完成实名与名单匹配", nil)
	}
	now := time.Now()
	id := idgen.Next()
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Table("avatar_consent").Where("account_id=? AND status='ACTIVE' AND consent_type='COLLECT_AND_ARCHIVE'", cur.AccountID).Updates(map[string]any{"status": "REVOKED", "revoked_at": now, "updated_at": now}).Error; e != nil {
			return e
		}
		return tx.Table("avatar_consent").Create(map[string]any{"id": id, "status": "ACTIVE", "version": 1, "consent_no": fmt.Sprintf("AVC-%d", id), "profile_id": p.ID, "account_id": cur.AccountID, "consent_type": "COLLECT_AND_ARCHIVE", "agreement_version": r.AgreementVersion, "agreement_digest": hash(consentPurpose + ":" + r.AgreementVersion), "purpose_snapshot": consentPurpose, "retention_days": 365, "retention_notice_snapshot": "首次归档后保存365天", "signed_at": now, "sign_ip_hash": hash(ip), "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(cur.AccountID)}).Error
	})
	if e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "保存授权失败", nil)
	}
	return gin.H{"consentId": fmt.Sprint(id), "status": "ACTIVE", "retentionDays": 365}, nil
}
func (s *Service) profileMe(ctx context.Context, cur Current) (any, *httpx.AppError) {
	var p struct {
		ID                                                                     uint64
		CurrentCollectionSessionID                                             *uint64
		Version                                                                uint32
		ProfileNo                                                              string
		Status, RealNameStatus, CollectionStatus, QualityStatus, ArchiveStatus string
		RetentionUntil, ArchivedAt                                             *time.Time
	}
	if e := s.db.WithContext(ctx).Table("avatar_profile").Where("account_id=?", cur.AccountID).Take(&p).Error; e != nil {
		return nil, avatarError(404, "AVATAR_PROFILE_NOT_FOUND", "尚未完成名单绑定", nil)
	}
	var collectionSummary struct {
		BodyDataJSONCipher []byte
		SubmittedAt        *time.Time
	}
	var assetSummary struct{ Photos, Videos int64 }
	if p.CurrentCollectionSessionID != nil {
		s.db.WithContext(ctx).Table("avatar_collection_session").Select("submitted_at,body_data_json_cipher").Where("id=?", *p.CurrentCollectionSessionID).Take(&collectionSummary)
		s.db.WithContext(ctx).Table("avatar_asset").Select("SUM(CASE WHEN asset_type='FACE_PHOTO' AND status='CONFIRMED' THEN 1 ELSE 0 END) photos,SUM(CASE WHEN asset_type='VOICE_VIDEO' AND status='CONFIRMED' THEN 1 ELSE 0 END) videos").Where("collection_session_id=?", *p.CurrentCollectionSessionID).Scan(&assetSummary)
	}
	var n int64
	s.db.WithContext(ctx).Table("avatar_deletion_request").Where("profile_id=? AND status IN ('PENDING','APPROVED','PROCESSING')", p.ID).Count(&n)
	latestSessionID := ""
	if p.CurrentCollectionSessionID != nil {
		latestSessionID = fmt.Sprint(*p.CurrentCollectionSessionID)
	}
	return gin.H{"profileId": fmt.Sprint(p.ID), "profileNo": p.ProfileNo, "version": p.Version, "status": p.Status, "realNameStatus": p.RealNameStatus, "collectionStatus": p.CollectionStatus, "qualityStatus": p.QualityStatus, "archiveStatus": p.ArchiveStatus, "archivedAt": p.ArchivedAt, "retentionUntil": p.RetentionUntil, "latestSessionId": latestSessionID, "summary": gin.H{"photoCount": assetSummary.Photos, "videoCount": assetSummary.Videos, "bodyDataFilled": len(collectionSummary.BodyDataJSONCipher) > 0}, "submittedAt": collectionSummary.SubmittedAt, "canRequestDeletion": n == 0}, nil
}
func (s *Service) requestDeletion(ctx context.Context, cur Current, r deletionRequest) (any, *httpx.AppError) {
	var data gin.H
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var p struct {
			ID     uint64
			Status string
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("avatar_profile").Where("account_id=?", cur.AccountID).Take(&p).Error; e != nil {
			return e
		}
		if p.Status != "ARCHIVED" {
			return fmt.Errorf("state")
		}
		var n int64
		if e := tx.Table("avatar_deletion_request").Where("profile_id=? AND status IN ('PENDING','APPROVED','PROCESSING')", p.ID).Count(&n).Error; e != nil {
			return e
		}
		if n > 0 {
			return fmt.Errorf("pending")
		}
		id := idgen.Next()
		now := time.Now()
		if e := tx.Table("avatar_deletion_request").Create(map[string]any{"id": id, "status": "PENDING", "version": 1, "request_no": fmt.Sprintf("AVD-%d", id), "profile_id": p.ID, "account_id": cur.AccountID, "request_reason": nullable(r.Reason), "requested_at": now, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(cur.AccountID)}).Error; e != nil {
			return e
		}
		if e := tx.Table("avatar_profile").Where("id=?", p.ID).Updates(map[string]any{"status": "DELETION_REQUESTED", "status_changed_at": now, "updated_at": now}).Error; e != nil {
			return e
		}
		data = gin.H{"deletionRequestId": fmt.Sprint(id), "status": "PENDING"}
		return nil
	})
	if err != nil {
		if err.Error() == "pending" {
			return nil, avatarError(http.StatusConflict, "AVATAR_PROFILE_DELETION_PENDING", "删除申请处理中", nil)
		}
		if err.Error() == "state" {
			return nil, avatarError(http.StatusConflict, "AVATAR_PROFILE_STATE_INVALID", "仅已归档档案可申请删除", nil)
		}
		return nil, avatarError(500, "INTERNAL_ERROR", "提交删除申请失败", nil)
	}
	return data, nil
}
func (s *Service) latestDeletionRequest(ctx context.Context, cur Current) (any, *httpx.AppError) {
	var v struct {
		ID                             uint64
		Status, Decision, StatusReason string
		RequestedAt                    time.Time
		ApprovedAt, PurgedAt           *time.Time
	}
	if e := s.db.WithContext(ctx).Table("avatar_deletion_request d").Select("d.id,d.status,d.decision,d.status_reason,d.requested_at,d.approved_at,d.purged_at").Joins("JOIN avatar_profile p ON p.id=d.profile_id").Where("p.account_id=?", cur.AccountID).Order("d.created_at DESC").Take(&v).Error; e != nil {
		return gin.H{"status": "NONE"}, nil
	}
	return gin.H{"deletionRequestId": fmt.Sprint(v.ID), "status": v.Status, "decision": v.Decision, "reason": v.StatusReason, "requestedAt": v.RequestedAt, "approvedAt": v.ApprovedAt, "purgedAt": v.PurgedAt}, nil
}
func (s *Service) reshoot(ctx context.Context, cur Current, r reshootRequest, ip string) (any, *httpx.AppError) {
	var profile struct{ Status string }
	if e := s.db.WithContext(ctx).Table("avatar_profile").Select("status").Where("account_id=?", cur.AccountID).Take(&profile).Error; e != nil {
		return nil, avatarError(404, "AVATAR_PROFILE_NOT_FOUND", "尚未完成名单绑定", nil)
	}
	if profile.Status != "RESHOOT_REQUIRED" {
		return nil, avatarError(http.StatusConflict, "AVATAR_PROFILE_STATE_INVALID", "当前档案状态不可补采", nil)
	}
	data, e := s.startCollection(ctx, cur, true)
	if e != nil {
		return nil, e
	}
	if id, ok := parseUint(fmt.Sprint(data.(gin.H)["sessionId"])); ok {
		_ = s.db.WithContext(ctx).Table("avatar_access_audit").Create(map[string]any{"id": idgen.Next(), "audit_no": fmt.Sprintf("AVAUD-%d", idgen.Next()), "actor_type": "USER", "actor_id": fmt.Sprint(cur.AccountID), "action": "AVATAR_RESHOOT", "object_type": "AVATAR_COLLECTION_SESSION", "object_id": id, "purpose_code": "RESHOOT", "result": "SUCCEEDED", "client_ip_hash": hash(ip)})
	}
	data.(gin.H)["reshoot"] = true
	return data, nil
}
func (s *Service) saveBodyData(ctx context.Context, cur Current, idText string, r bodyDataRequest) (any, *httpx.AppError) {
	id, ok := parseUint(idText)
	if !ok || (r.Gender != "MALE" && r.Gender != "FEMALE") || (r.BustCm != nil && (*r.BustCm <= 0 || *r.BustCm > 300)) || (r.WaistCm != nil && (*r.WaistCm <= 0 || *r.WaistCm > 300)) || (r.HipsCm != nil && (*r.HipsCm <= 0 || *r.HipsCm > 300)) {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "体型资料格式不正确", nil)
	}
	raw, _ := json.Marshal(gin.H{"gender": r.Gender, "heightCm": r.HeightCm, "bodyType": r.BodyType, "bustCm": r.BustCm, "waistCm": r.WaistCm, "hipsCm": r.HipsCm})
	cipher, e := encrypt(s.auth.JWTSecret, string(raw))
	if e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "加密体型资料失败", nil)
	}
	var n int64
	if e := s.db.WithContext(ctx).Table("avatar_collection_session cs").Joins("JOIN avatar_profile p ON p.id=cs.profile_id").Where("cs.id=? AND p.account_id=? AND cs.status='COLLECTING'", id, cur.AccountID).Count(&n).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "查询采集会话失败", nil)
	}
	if n == 0 {
		return nil, avatarError(404, "AVATAR_COLLECTION_NOT_FOUND", "采集会话不存在或不可编辑", nil)
	}
	if e := s.db.WithContext(ctx).Table("avatar_collection_session").Where("id=?", id).Updates(map[string]any{"body_data_json_cipher": cipher, "updated_at": time.Now()}).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "保存体型资料失败", nil)
	}
	return gin.H{"sessionId": fmt.Sprint(id), "status": "SAVED"}, nil
}

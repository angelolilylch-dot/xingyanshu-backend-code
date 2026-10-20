// Package creators 实现创作者注册、实名资料和授权委托书留存。
package creators

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/modules/files"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/idgen"
)

const (
	creatorRoleCode = "CREATOR"
	smsTTL          = 5 * time.Minute
)

var mobilePattern = regexp.MustCompile(`^1[3-9][0-9]{9}$`)

// Service 集中处理创作者身份业务；真实短信和实名服务接入时仅替换 Provider。
type Service struct {
	db          *gorm.DB
	redis       *redis.Client
	auth        config.AuthConfig
	environment string
}

// NewService 创建创作者身份服务。测试环境的验证码和实名核验均由 Mock 逻辑完成。
func NewService(db *gorm.DB, redisClient *redis.Client, auth config.AuthConfig, environment string) *Service {
	return &Service{db: db, redis: redisClient, auth: auth, environment: environment}
}

type creatorUser struct {
	ID           uint64 `gorm:"column:id;primaryKey"`
	Status       string `gorm:"column:status"`
	Version      uint32 `gorm:"column:version"`
	Username     string `gorm:"column:username"`
	PasswordHash string `gorm:"column:password_hash"`
	DisplayName  string `gorm:"column:display_name"`
	Mobile       string `gorm:"column:mobile"`
}

func (creatorUser) TableName() string { return "sys_user" }

type creatorProfile struct {
	ID             uint64 `gorm:"column:id;primaryKey"`
	UserID         uint64 `gorm:"column:user_id"`
	Status         string `gorm:"column:status"`
	Version        uint32 `gorm:"column:version"`
	SubjectType    string `gorm:"column:subject_type"`
	DisplayName    string `gorm:"column:display_name"`
	IdentityStatus string `gorm:"column:identity_status"`
	LetterStatus   string `gorm:"column:letter_status"`
}

func (creatorProfile) TableName() string { return "creator_profile" }

type identityVerification struct {
	ID                uint64    `gorm:"column:id;primaryKey"`
	CreatorID         uint64    `gorm:"column:creator_id"`
	Status            string    `gorm:"column:status"`
	Version           uint32    `gorm:"column:version"`
	SubjectType       string    `gorm:"column:subject_type"`
	LegalName         string    `gorm:"column:legal_name"`
	VerificationNo    string    `gorm:"column:verification_no"`
	CredentialType    string    `gorm:"column:credential_type"`
	CredentialCipher  []byte    `gorm:"column:credential_cipher"`
	CredentialHash    string    `gorm:"column:credential_hash"`
	ProviderCode      string    `gorm:"column:provider_code"`
	ProviderRequestNo string    `gorm:"column:provider_request_no"`
	ResultStatus      string    `gorm:"column:result_status"`
	ResultSummary     string    `gorm:"column:result_summary"`
	CreatedAt         time.Time `gorm:"column:created_at"`
}

func (identityVerification) TableName() string { return "creator_identity_verification" }

type authorizationLetter struct {
	ID        uint64 `gorm:"column:id;primaryKey"`
	CreatorID uint64 `gorm:"column:creator_id"`
	Status    string `gorm:"column:status"`
	Version   uint32 `gorm:"column:version"`
	VersionNo uint32 `gorm:"column:version_no"`
	FileID    uint64 `gorm:"column:file_id"`
}

func (authorizationLetter) TableName() string { return "creator_authorization_letter" }

// authorizationLetterDraft 是服务端自动生成的、尚待签署盖章的 Word 草稿，不等同于 authorizationLetter 留存件。
type authorizationLetterDraft struct {
	ID                     uint64 `gorm:"column:id;primaryKey"`
	Status                 string `gorm:"column:status"`
	Version                uint32 `gorm:"column:version"`
	CreatorID              uint64 `gorm:"column:creator_id"`
	IdentityVerificationID uint64 `gorm:"column:identity_verification_id"`
	FileID                 uint64 `gorm:"column:file_id"`
}

func (authorizationLetterDraft) TableName() string { return "creator_authorization_letter_draft" }

type session struct {
	ID               uint64    `gorm:"column:id;primaryKey"`
	SubjectType      string    `gorm:"column:subject_type"`
	SubjectID        uint64    `gorm:"column:subject_id"`
	ClientType       string    `gorm:"column:client_type"`
	RefreshTokenHash string    `gorm:"column:refresh_token_hash"`
	TokenVersion     uint32    `gorm:"column:token_version"`
	Status           string    `gorm:"column:status"`
	ExpiresAt        time.Time `gorm:"column:expires_at"`
}

func (session) TableName() string { return "auth_session" }

type RegisterRequest struct {
	Mobile      string `json:"mobile" binding:"required"`
	Password    string `json:"password" binding:"required,min=8,max=64"`
	SMSCode     string `json:"smsCode" binding:"required,len=6"`
	DisplayName string `json:"displayName" binding:"required,min=2,max=128"`
}
type LoginRequest struct {
	Mobile   string `json:"account" binding:"required"`
	Password string `json:"password" binding:"required,min=8,max=64"`
}
type SMSRequest struct {
	Mobile string `json:"mobile" binding:"required"`
}
type IdentityRequest struct {
	SubjectType    string `json:"subjectType" binding:"required,oneof=PERSONAL ENTERPRISE STUDIO"`
	LegalName      string `json:"legalName" binding:"required,min=2,max=128"`
	CredentialType string `json:"credentialType" binding:"required,max=40"`
	CredentialNo   string `json:"credentialNo" binding:"required,min=6,max=128"`
}
type LetterRequest struct {
	FileID string `json:"fileId" binding:"required"`
}
type StatusRequest struct {
	Status string `json:"status" binding:"required,oneof=ACTIVE DISABLED"`
	Reason string `json:"reason" binding:"max=500"`
}

// SendSMS 生成测试验证码。真实短信服务接入后保持相同接口，仅替换发送实现。
func (s *Service) SendSMS(ctx context.Context, mobile string) (map[string]any, string, error) {
	if !mobilePattern.MatchString(mobile) {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	code := "123456"
	if err := s.redis.Set(ctx, "creator:sms:register:"+mobile, code, smsTTL).Err(); err != nil {
		return nil, "SMS_UNAVAILABLE", err
	}
	data := map[string]any{"expiresIn": int(smsTTL.Seconds())}
	if s.environment == "test" {
		data["mockCode"] = code
	}
	return data, "", nil
}

// Register 校验一次性短信验证码后创建创作者账号、档案、CREATOR 角色及会话。
func (s *Service) Register(ctx context.Context, request RegisterRequest, clientIP string) (map[string]any, string, error) {
	request.Mobile, request.DisplayName = strings.TrimSpace(request.Mobile), strings.TrimSpace(request.DisplayName)
	if !mobilePattern.MatchString(request.Mobile) {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	if len(request.Password) > 72 {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	value, err := s.redis.Eval(ctx, "local v=redis.call('GET',KEYS[1]); if v then redis.call('DEL',KEYS[1]); end; return v", []string{"creator:sms:register:" + request.Mobile}).Text()
	if err == redis.Nil || value != request.SMSCode {
		return nil, "SMS_CODE_INVALID", nil
	}
	if err != nil {
		return nil, "SMS_UNAVAILABLE", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(request.Password), 12)
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	userID, profileID := idgen.Next(), idgen.Next()
	user := creatorUser{ID: userID, Status: "ACTIVE", Version: 1, Username: "creator_" + request.Mobile, PasswordHash: string(hash), DisplayName: request.DisplayName, Mobile: request.Mobile}
	profile := creatorProfile{ID: profileID, UserID: userID, Status: "ACTIVE", Version: 1, SubjectType: "PERSONAL", DisplayName: request.DisplayName, IdentityStatus: "UNVERIFIED", LetterStatus: "NOT_REQUIRED"}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var role struct{ ID uint64 }
		if err := tx.Table("sys_role").Select("id").Where("role_code = ? AND status = ?", creatorRoleCode, "ACTIVE").First(&role).Error; err != nil {
			return err
		}
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		if err := tx.Create(&profile).Error; err != nil {
			return err
		}
		if err := tx.Table("sys_user_role").Create(map[string]any{"id": idgen.Next(), "user_id": userID, "role_id": role.ID, "is_primary": true, "created_by": userID}).Error; err != nil {
			return err
		}
		return audit.WriteLog(tx, "CREATOR_REGISTER", "CREATOR", profileID, nil, map[string]any{"creatorId": fmt.Sprint(profileID), "displayName": profile.DisplayName}, "SUCCESS", audit.Operator{Type: "USER", ID: userID, RoleCode: creatorRoleCode, ClientIP: clientIP})
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return nil, "RESOURCE_CONFLICT", nil
		}
		return nil, "INTERNAL_ERROR", err
	}
	tokens, err := s.issueTokens(ctx, user, profile)
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return tokens, "", nil
}

// Login 供统一登录入口的创作者分支使用，手机号和密码均由注册时写入的统一账号表校验。
func (s *Service) Login(ctx context.Context, request LoginRequest) (map[string]any, string, error) {
	if !mobilePattern.MatchString(strings.TrimSpace(request.Mobile)) {
		return nil, "AUTH_LOGIN_INVALID", nil
	}
	var user creatorUser
	if err := s.db.WithContext(ctx).Where("mobile=?", request.Mobile).First(&user).Error; err != nil || user.Status != "ACTIVE" {
		return nil, "AUTH_LOGIN_INVALID", nil
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(request.Password)) != nil {
		return nil, "AUTH_LOGIN_INVALID", nil
	}
	var profile creatorProfile
	if err := s.db.WithContext(ctx).Where("user_id=? AND status='ACTIVE'", user.ID).First(&profile).Error; err != nil {
		return nil, "AUTH_LOGIN_INVALID", nil
	}
	data, err := s.issueTokens(ctx, user, profile)
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return data, "", nil
}

// VerifyIdentity 使用 Mock 核验主体；资料通过后即可购买，企业和工作室不等待委托书确认。
func (s *Service) VerifyIdentity(ctx context.Context, current CurrentCreator, request IdentityRequest, clientIP string) (map[string]any, string, error) {
	request.LegalName, request.CredentialNo = strings.TrimSpace(request.LegalName), strings.TrimSpace(request.CredentialNo)
	if request.LegalName == "" || request.CredentialNo == "" {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	var profile creatorProfile
	if err := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", current.CreatorID, current.UserID).First(&profile).Error; err != nil {
		return nil, "CREATOR_NOT_FOUND", nil
	}
	if profile.IdentityStatus == "VERIFIED" {
		return nil, "IDENTITY_ALREADY_VERIFIED", nil
	}
	credentialHash := sha256.Sum256([]byte(request.SubjectType + ":" + request.CredentialNo))
	item := identityVerification{ID: idgen.Next(), CreatorID: profile.ID, Status: "ACTIVE", Version: 1, SubjectType: request.SubjectType, LegalName: request.LegalName, VerificationNo: fmt.Sprintf("IDV-%d", idgen.Next()), CredentialType: request.CredentialType, CredentialCipher: []byte(request.CredentialNo), CredentialHash: hex.EncodeToString(credentialHash[:]), ProviderCode: "MOCK", ProviderRequestNo: fmt.Sprintf("MOCK-IDV-%d", time.Now().UnixNano()), ResultStatus: "VERIFIED", ResultSummary: "Mock 实名核验通过"}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", profile.ID).First(&profile).Error; err != nil {
			return err
		}
		if err := tx.Create(&item).Error; err != nil {
			return err
		}
		letterStatus := "NOT_REQUIRED"
		if request.SubjectType != "PERSONAL" {
			letterStatus = "OPTIONAL"
		}
		if err := tx.Model(&creatorProfile{}).Where("id=? AND version=?", profile.ID, profile.Version).Updates(map[string]any{"subject_type": request.SubjectType, "identity_status": "VERIFIED", "letter_status": letterStatus, "version": profile.Version + 1, "updated_by": current.UserID, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		return audit.WriteLog(tx, "CREATOR_IDENTITY_VERIFIED", "CREATOR", profile.ID, nil, map[string]any{"creatorId": fmt.Sprint(profile.ID), "subjectType": request.SubjectType, "identityStatus": "VERIFIED"}, "SUCCESS", audit.Operator{Type: "USER", ID: current.UserID, RoleCode: creatorRoleCode, ClientIP: clientIP})
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return nil, "RESOURCE_CONFLICT", nil
		}
		return nil, "INTERNAL_ERROR", err
	}
	return verificationResponse(item), "", nil
}

func (s *Service) LatestIdentity(ctx context.Context, current CurrentCreator) (map[string]any, string, error) {
	var item identityVerification
	err := s.db.WithContext(ctx).Where("creator_id=?", current.CreatorID).Order("created_at DESC").First(&item).Error
	if err == gorm.ErrRecordNotFound {
		return map[string]any{"identityStatus": "UNVERIFIED"}, "", nil
	}
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return verificationResponse(item), "", nil
}

// GenerateAuthorizationLetterDraft 在企业或工作室实名通过后生成或复用私有 Word 草稿，并签发短期下载链接。
func (s *Service) GenerateAuthorizationLetterDraft(ctx context.Context, current CurrentCreator, fileService *files.Service, clientIP string) (map[string]any, string, error) {
	if fileService == nil {
		return nil, "INTERNAL_ERROR", fmt.Errorf("file service unavailable")
	}
	var profile creatorProfile
	if err := s.db.WithContext(ctx).Where("id=? AND user_id=? AND status='ACTIVE'", current.CreatorID, current.UserID).First(&profile).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, "CREATOR_NOT_FOUND", nil
		}
		return nil, "INTERNAL_ERROR", err
	}
	if profile.SubjectType == "PERSONAL" || profile.IdentityStatus != "VERIFIED" {
		return map[string]any{"currentStatus": profile.IdentityStatus, "action": "GENERATE_AUTHORIZATION_LETTER_DRAFT", "reasonCode": "CREATOR_SUBJECT_NOT_ELIGIBLE"}, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	var verification identityVerification
	if err := s.db.WithContext(ctx).Where("creator_id=? AND result_status='VERIFIED'", profile.ID).Order("created_at DESC").First(&verification).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, "IDENTITY_VERIFICATION_NOT_FOUND", nil
		}
		return nil, "INTERNAL_ERROR", err
	}
	var existing authorizationLetterDraft
	if err := s.db.WithContext(ctx).Where("creator_id=? AND status='ACTIVE'", profile.ID).First(&existing).Error; err == nil {
		return draftDownloadResponse(ctx, fileService, current, clientIP, existing.FileID)
	} else if err != gorm.ErrRecordNotFound {
		return nil, "INTERNAL_ERROR", err
	}

	content, err := buildAuthorizationLetterDOCX(authorizationLetterDraftContent{
		LegalName:     verificationLegalName(verification, profile),
		CredentialNo:  string(verification.CredentialCipher),
		SubjectType:   profile.SubjectType,
		GeneratedDate: time.Now(),
	})
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	name := fmt.Sprintf("创作者主体授权委托书草稿-%s.docx", safeDraftFilename(verificationLegalName(verification, profile)))
	file, code, err := fileService.CreateGeneratedForCreator(ctx, current.UserID, clientIP, name, authorizationLetterDOCXContentType, content)
	if err != nil || code != "" {
		return nil, code, err
	}
	fileID, parseErr := parseResponseID(file, "id")
	if parseErr != nil {
		return nil, "INTERNAL_ERROR", parseErr
	}
	draft := authorizationLetterDraft{ID: idgen.Next(), Status: "ACTIVE", Version: 1, CreatorID: profile.ID, IdentityVerificationID: verification.ID, FileID: fileID}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&draft).Error; err != nil {
			return err
		}
		return audit.WriteLog(tx, "CREATOR_AUTHORIZATION_LETTER_DRAFT_GENERATED", "CREATOR", profile.ID, nil, map[string]any{"draftId": fmt.Sprint(draft.ID), "fileId": fmt.Sprint(fileID)}, "SUCCESS", audit.Operator{Type: "USER", ID: current.UserID, RoleCode: creatorRoleCode, ClientIP: clientIP})
	}); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			var concurrent authorizationLetterDraft
			if findErr := s.db.WithContext(ctx).Where("creator_id=? AND status='ACTIVE'", profile.ID).First(&concurrent).Error; findErr == nil {
				return draftDownloadResponse(ctx, fileService, current, clientIP, concurrent.FileID)
			}
		}
		return nil, "INTERNAL_ERROR", err
	}
	return draftDownloadResponse(ctx, fileService, current, clientIP, fileID)
}

func (s *Service) AuthorizationLetterDraft(ctx context.Context, current CurrentCreator, fileService *files.Service, clientIP string) (map[string]any, string, error) {
	return s.GenerateAuthorizationLetterDraft(ctx, current, fileService, clientIP)
}

func draftDownloadResponse(ctx context.Context, fileService *files.Service, current CurrentCreator, clientIP string, fileID uint64) (map[string]any, string, error) {
	download, code, err := fileService.CreateDownloadURLForCreator(ctx, current.UserID, clientIP, fileID)
	if err != nil || code != "" {
		return nil, code, err
	}
	return map[string]any{"authorizationLetterDraftFileId": download["fileId"], "fileName": "创作者主体授权委托书草稿.docx", "downloadUrl": download["downloadURL"], "expiresAt": download["expiresAt"]}, "", nil
}

func verificationLegalName(verification identityVerification, profile creatorProfile) string {
	if strings.TrimSpace(verification.LegalName) != "" {
		return strings.TrimSpace(verification.LegalName)
	}
	// 兼容升级前已完成的实名认证记录；新记录均使用 legal_name。
	if strings.TrimSpace(profile.DisplayName) != "" {
		return strings.TrimSpace(profile.DisplayName)
	}
	return "主体名称待补充"
}

func parseResponseID(value map[string]any, key string) (uint64, error) {
	text, ok := value[key].(string)
	if !ok {
		return 0, fmt.Errorf("missing response id")
	}
	var id uint64
	if _, err := fmt.Sscan(text, &id); err != nil || id == 0 {
		return 0, fmt.Errorf("invalid response id")
	}
	return id, nil
}

func safeDraftFilename(value string) string {
	value = strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-", "?", "-", "\"", "-", "<", "-", ">", "-", "|", "-").Replace(strings.TrimSpace(value))
	if value == "" {
		return "主体"
	}
	if len([]rune(value)) > 80 {
		return string([]rune(value)[:80])
	}
	return value
}

// FileLetter 留存企业或工作室签署后的委托书。它不是认证和购买的前置条件。
func (s *Service) FileLetter(ctx context.Context, current CurrentCreator, fileID uint64, clientIP string) (map[string]any, string, error) {
	var profile creatorProfile
	if err := s.db.WithContext(ctx).Where("id=? AND user_id=?", current.CreatorID, current.UserID).First(&profile).Error; err != nil {
		return nil, "CREATOR_NOT_FOUND", nil
	}
	if profile.SubjectType == "PERSONAL" {
		return map[string]any{"currentStatus": profile.SubjectType, "action": "FILE_AUTHORIZATION_LETTER", "reasonCode": "PERSONAL_SUBJECT_NOT_REQUIRED"}, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	var file struct{ ID uint64 }
	if err := s.db.WithContext(ctx).Table("file_object").Select("id").Where("id=? AND created_by=? AND status='ACTIVE' AND upload_status='SUCCESS'", fileID, current.UserID).First(&file).Error; err != nil {
		return nil, "AUTHORIZATION_LETTER_FILE_NOT_FOUND", nil
	}
	item := authorizationLetter{ID: idgen.Next(), CreatorID: profile.ID, Status: "ACTIVE", Version: 1, FileID: fileID}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var max uint32
		if err := tx.Table("creator_authorization_letter").Select("COALESCE(MAX(version_no),0)").Where("creator_id=?", profile.ID).Scan(&max).Error; err != nil {
			return err
		}
		item.VersionNo = max + 1
		if err := tx.Create(&item).Error; err != nil {
			return err
		}
		if err := tx.Model(&creatorProfile{}).Where("id=?", profile.ID).Updates(map[string]any{"letter_status": "FILED", "updated_by": current.UserID, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		return audit.WriteLog(tx, "CREATOR_AUTHORIZATION_LETTER_FILED", "CREATOR", profile.ID, nil, map[string]any{"letterId": fmt.Sprint(item.ID), "fileId": fmt.Sprint(fileID)}, "SUCCESS", audit.Operator{Type: "USER", ID: current.UserID, RoleCode: creatorRoleCode, ClientIP: clientIP})
	})
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"id": fmt.Sprint(item.ID), "fileId": fmt.Sprint(fileID), "status": "FILED", "versionNo": item.VersionNo}, "", nil
}

// ChangeStatus 由平台管理员停用或恢复创作者。停用时撤销全部创作者会话，后续写操作立即失效。
func (s *Service) ChangeStatus(ctx context.Context, creatorID uint64, version uint32, request StatusRequest, operator identityOperator, clientIP string) (map[string]any, string, error) {
	var profile creatorProfile
	if err := s.db.WithContext(ctx).Where("id = ?", creatorID).First(&profile).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, "CREATOR_NOT_FOUND", nil
		}
		return nil, "INTERNAL_ERROR", err
	}
	if profile.Version != version {
		return nil, "VERSION_CONFLICT", nil
	}
	if request.Status == profile.Status {
		return profileResponse(profile), "", nil
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&creatorProfile{}).Where("id = ? AND version = ?", creatorID, version).Updates(map[string]any{"status": request.Status, "status_reason": strings.TrimSpace(request.Reason), "status_changed_at": time.Now(), "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(operator.UserID), "updated_by": operator.UserID, "updated_at": time.Now(), "version": version + 1})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Model(&creatorUser{}).Where("id = ?", profile.UserID).Updates(map[string]any{"status": request.Status, "updated_by": operator.UserID, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		if request.Status == "DISABLED" {
			if err := tx.Model(&session{}).Where("subject_type = 'CREATOR' AND subject_id = ? AND status = 'ACTIVE'", profile.UserID).Updates(map[string]any{"status": "REVOKED", "last_used_at": time.Now()}).Error; err != nil {
				return err
			}
		}
		return audit.WriteLog(tx, "CREATOR_STATUS_CHANGED", "CREATOR", creatorID, map[string]any{"status": profile.Status}, map[string]any{"status": request.Status, "reason": request.Reason}, "SUCCESS", audit.Operator{Type: "USER", ID: operator.UserID, RoleCode: operator.RoleCode, ClientIP: clientIP})
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, "VERSION_CONFLICT", nil
		}
		return nil, "INTERNAL_ERROR", err
	}
	profile.Status, profile.Version = request.Status, profile.Version+1
	return profileResponse(profile), "", nil
}

type identityOperator struct {
	UserID   uint64
	RoleCode string
}

func profileResponse(profile creatorProfile) map[string]any {
	return map[string]any{"id": fmt.Sprint(profile.ID), "status": profile.Status, "version": profile.Version, "displayName": profile.DisplayName, "subjectType": profile.SubjectType, "identityStatus": profile.IdentityStatus, "letterStatus": profile.LetterStatus}
}

func verificationResponse(item identityVerification) map[string]any {
	return map[string]any{"id": fmt.Sprint(item.ID), "verificationNo": item.VerificationNo, "subjectType": item.SubjectType, "credentialType": item.CredentialType, "identityStatus": item.ResultStatus, "providerCode": item.ProviderCode, "resultSummary": item.ResultSummary, "createdAt": item.CreatedAt}
}

func (s *Service) issueTokens(ctx context.Context, user creatorUser, profile creatorProfile) (map[string]any, error) {
	// 刷新令牌前缀携带会话 ID，统一认证入口可先定位会话，再校验 bcrypt 哈希。
	sessionID := idgen.Next()
	refresh := fmt.Sprint(sessionID) + "." + randomToken()
	hash, err := bcrypt.GenerateFromPassword([]byte(refresh), 12)
	if err != nil {
		return nil, err
	}
	ses := session{ID: sessionID, SubjectType: "CREATOR", SubjectID: user.ID, ClientType: "CREATOR_WEB", RefreshTokenHash: string(hash), TokenVersion: 1, Status: "ACTIVE", ExpiresAt: time.Now().Add(s.auth.RefreshTokenTTL)}
	if err := s.db.WithContext(ctx).Create(&ses).Error; err != nil {
		return nil, err
	}
	now := time.Now()
	claims := jwt.MapClaims{"sub": fmt.Sprint(user.ID), "sid": fmt.Sprint(ses.ID), "tv": "1", "clientType": "CREATOR_WEB", "iat": now.Unix(), "exp": now.Add(s.auth.AccessTokenTTL).Unix(), "jti": randomToken()}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.auth.JWTSecret))
	if err != nil {
		return nil, err
	}
	return map[string]any{"accessToken": token, "refreshToken": refresh, "expiresIn": int(s.auth.AccessTokenTTL.Seconds()), "tokenType": "Bearer", "creator": map[string]any{"id": fmt.Sprint(profile.ID), "displayName": profile.DisplayName, "identityStatus": profile.IdentityStatus}}, nil
}
func randomToken() string { b := make([]byte, 24); _, _ = rand.Read(b); return hex.EncodeToString(b) }

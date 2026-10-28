package avatar

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"strings"
	"time"

	common "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	faceid "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/faceid/v20180301"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/platform/idgen"
)

const faceIdentityVerificationType = "FACE_IMAGE_1V1"

type faceIdentityVerifier interface {
	Verify(context.Context, faceIdentityInput) (faceIdentityResult, error)
}

type faceIdentityInput struct {
	Name, IDNumber string
	Image          []byte
	Extra          string
}

type faceIdentityResult struct {
	ProviderCode, ProviderRequestNo, ResultCode, Description string
	Similarity                                               float64
}

type unavailableFaceIdentityVerifier struct{}

func (unavailableFaceIdentityVerifier) Verify(context.Context, faceIdentityInput) (faceIdentityResult, error) {
	return faceIdentityResult{}, fmt.Errorf("face identity provider is not configured")
}

type tencentImageRecognitionVerifier struct {
	client    *faceid.Client
	publicKey *rsa.PublicKey
}

func newFaceIdentityVerifier(cfg config.FaceIdentityConfig) (faceIdentityVerifier, error) {
	if !faceIdentityConfigPresent(cfg) {
		return unavailableFaceIdentityVerifier{}, nil
	}
	if cfg.Provider != "tencent_image_recognition_v2" || cfg.SecretID == "" || cfg.SecretKey == "" || cfg.PublicKey == "" {
		return nil, fmt.Errorf("incomplete Tencent image recognition configuration")
	}
	publicKey, err := parseTencentRSAPublicKey(cfg.PublicKey)
	if err != nil {
		return nil, err
	}
	cp := profile.NewClientProfile()
	cp.HttpProfile.Endpoint = "faceid.tencentcloudapi.com"
	client, err := faceid.NewClient(common.NewCredential(cfg.SecretID, cfg.SecretKey), "", cp)
	if err != nil {
		return nil, fmt.Errorf("create Tencent FaceID client: %w", err)
	}
	return &tencentImageRecognitionVerifier{client: client, publicKey: publicKey}, nil
}

func faceIdentityConfigPresent(cfg config.FaceIdentityConfig) bool {
	return strings.TrimSpace(cfg.Provider) != "" || strings.TrimSpace(cfg.SecretID) != "" || strings.TrimSpace(cfg.SecretKey) != "" || strings.TrimSpace(cfg.PublicKey) != "" || cfg.Timeout != 0
}

func parseTencentRSAPublicKey(value string) (*rsa.PublicKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("decode Tencent FaceID RSA public key: %w", err)
	}
	block, _ := pem.Decode(decoded)
	if block == nil {
		return nil, fmt.Errorf("decode Tencent FaceID RSA public key PEM")
	}
	key, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse Tencent FaceID RSA public key: %w", err)
	}
	return key, nil
}

func (p *tencentImageRecognitionVerifier) Verify(ctx context.Context, input faceIdentityInput) (faceIdentityResult, error) {
	if strings.TrimSpace(input.Name) == "" || !validChineseIDNumber(input.IDNumber) || len(input.Image) == 0 || len(input.Image) > 3*1024*1024 {
		return faceIdentityResult{}, fmt.Errorf("invalid face identity input")
	}
	idCard, name, encryption, err := encryptTencentFaceIdentity(p.publicKey, input.IDNumber, input.Name)
	if err != nil {
		return faceIdentityResult{}, err
	}
	request := faceid.NewImageRecognitionV2Request()
	request.IdCard = common.StringPtr(idCard)
	request.Name = common.StringPtr(name)
	request.ImageBase64 = common.StringPtr(base64.StdEncoding.EncodeToString(input.Image))
	request.Encryption = encryption
	if input.Extra != "" {
		request.Extra = common.StringPtr(input.Extra)
	}
	response, err := p.client.ImageRecognitionV2WithContext(ctx, request)
	if err != nil {
		return faceIdentityResult{}, fmt.Errorf("Tencent ImageRecognitionV2: %w", err)
	}
	if response == nil || response.Response == nil || response.Response.RequestId == nil {
		return faceIdentityResult{}, fmt.Errorf("Tencent ImageRecognitionV2 returned incomplete response")
	}
	result := faceIdentityResult{ProviderCode: "TENCENT_IMAGE_RECOGNITION_V2", ProviderRequestNo: *response.Response.RequestId, ResultCode: stringValue(response.Response.Result), Description: stringValue(response.Response.Description)}
	if response.Response.Sim != nil {
		result.Similarity = *response.Response.Sim
	}
	return result, nil
}

func encryptTencentFaceIdentity(publicKey *rsa.PublicKey, idNumber, name string) (string, string, *faceid.Encryption, error) {
	key := make([]byte, 32)
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", "", nil, fmt.Errorf("generate FaceID encryption key: %w", err)
	}
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", "", nil, fmt.Errorf("generate FaceID encryption IV: %w", err)
	}
	idCipher, err := aes256CBCEncrypt(key, iv, []byte(idNumber))
	if err != nil {
		return "", "", nil, err
	}
	nameCipher, err := aes256CBCEncrypt(key, iv, []byte(name))
	if err != nil {
		return "", "", nil, err
	}
	keyCipher, err := rsa.EncryptPKCS1v15(rand.Reader, publicKey, key)
	if err != nil {
		return "", "", nil, fmt.Errorf("encrypt FaceID symmetric key: %w", err)
	}
	idCipherText := base64.StdEncoding.EncodeToString(idCipher)
	nameCipherText := base64.StdEncoding.EncodeToString(nameCipher)
	keyCipherText := base64.StdEncoding.EncodeToString(keyCipher)
	ivText := base64.StdEncoding.EncodeToString(iv)
	algorithm := "AES-256-CBC"
	idField, nameField := "IdCard", "Name"
	return idCipherText, nameCipherText, &faceid.Encryption{EncryptList: []*string{&idField, &nameField}, CiphertextBlob: &keyCipherText, Iv: &ivText, Algorithm: &algorithm}, nil
}

func aes256CBCEncrypt(key, iv, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padding := block.BlockSize() - len(plaintext)%block.BlockSize()
	padded := append(append([]byte{}, plaintext...), bytes.Repeat([]byte{byte(padding)}, padding)...)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)
	return ciphertext, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// ProcessPendingFaceIdentity calls the third party only after a collection has
// been submitted. A row is atomically claimed first, so one collection cannot
// produce two external calls even when workers overlap.
func (s *Service) ProcessPendingFaceIdentity(ctx context.Context, limit int) (int, error) {
	if _, unavailable := s.faceIDV.(unavailableFaceIdentityVerifier); unavailable {
		return 0, nil
	}
	if limit <= 0 {
		limit = 20
	}
	var rows []struct{ ID uint64 }
	if err := s.db.WithContext(ctx).Table("avatar_identity_verification").Select("id").Where("status='PENDING' AND verification_type=?", faceIdentityVerificationType).Order("created_at ASC").Limit(limit).Find(&rows).Error; err != nil {
		return 0, err
	}
	processed := 0
	for _, row := range rows {
		if err := s.processPendingFaceIdentity(ctx, row.ID); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

func (s *Service) processPendingFaceIdentity(ctx context.Context, verificationID uint64) error {
	var item struct {
		ID, AccountID, RosterMemberID, ProfileID, CollectionSessionID, AssetID uint64
		NameCipher, IDNumberCipher                                             []byte
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT v.id,v.account_id,v.roster_member_id,v.profile_id,v.collection_session_id,v.asset_id,m.name_cipher,m.id_number_cipher
			FROM avatar_identity_verification v JOIN avatar_roster_member m ON m.id=v.roster_member_id
			WHERE v.id=? AND v.status='PENDING' AND v.verification_type=? FOR UPDATE`, verificationID, faceIdentityVerificationType).Scan(&item).Error; err != nil {
			return err
		}
		if item.ID == 0 {
			return nil
		}
		return tx.Table("avatar_identity_verification").Where("id=? AND status='PENDING'", item.ID).Updates(map[string]any{"status": "PROCESSING", "updated_at": time.Now(), "status_changed_at": time.Now(), "status_changed_by_type": "SYSTEM"}).Error
	})
	if err != nil || item.ID == 0 {
		return err
	}
	name, nameErr := decrypt(s.auth.JWTSecret, item.NameCipher)
	idNumber, idErr := decrypt(s.auth.JWTSecret, item.IDNumberCipher)
	if nameErr != nil || idErr != nil {
		return s.finishFaceIdentityError(ctx, item.ID, item.ProfileID, "IDENTITY_DATA_DECRYPT_FAILED", "实名信息不可用")
	}
	var asset struct{ StorageKeyCipher []byte }
	if err := s.db.WithContext(ctx).Table("avatar_asset").Select("storage_key_cipher").Where("id=? AND collection_session_id=? AND capture_slot='FACE_01' AND status='CONFIRMED'", item.AssetID, item.CollectionSessionID).Take(&asset).Error; err != nil {
		return s.finishFaceIdentityError(ctx, item.ID, item.ProfileID, "FACE_ASSET_NOT_FOUND", "正面照不可用")
	}
	key, err := decrypt(s.auth.JWTSecret, asset.StorageKeyCipher)
	if err != nil {
		return s.finishFaceIdentityError(ctx, item.ID, item.ProfileID, "FACE_ASSET_DECRYPT_FAILED", "正面照不可用")
	}
	if s.storage == nil {
		return s.finishFaceIdentityError(ctx, item.ID, item.ProfileID, "FACE_STORAGE_UNAVAILABLE", "正面照存储不可用")
	}
	body, err := s.storage.Get(ctx, key)
	if err != nil {
		return s.finishFaceIdentityError(ctx, item.ID, item.ProfileID, "FACE_ASSET_NOT_FOUND", "正面照不可用")
	}
	image, readErr := io.ReadAll(io.LimitReader(body, 3*1024*1024+1))
	closeErr := body.Close()
	if readErr != nil || closeErr != nil || len(image) == 0 || len(image) > 3*1024*1024 {
		return s.finishFaceIdentityError(ctx, item.ID, item.ProfileID, "FACE_IMAGE_INVALID", "正面照不符合核验要求")
	}
	result, verifyErr := s.faceIDV.Verify(ctx, faceIdentityInput{Name: name, IDNumber: idNumber, Image: image})
	if verifyErr != nil {
		return s.finishFaceIdentityError(ctx, item.ID, item.ProfileID, "FACE_IDENTITY_PROVIDER_ERROR", "人脸核验服务暂不可用")
	}
	status := "REJECTED"
	if result.ResultCode == "Success" && result.Similarity >= 70 {
		status = "PASSED"
	}
	now := time.Now()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		update := tx.Table("avatar_identity_verification").Where("id=? AND status='PROCESSING'", item.ID).Updates(map[string]any{
			"status": status, "provider_code": result.ProviderCode, "provider_request_no": result.ProviderRequestNo, "provider_result_code": result.ResultCode,
			"face_similarity": result.Similarity, "response_digest": hash(result.ResultCode + ":" + result.ProviderRequestNo), "status_reason_code": nullable(result.ResultCode), "status_reason": nullable(result.Description),
			"verified_at": now, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "THIRD_PARTY",
		})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected == 0 {
			return nil
		}
		if e := tx.Table("avatar_profile").Where("id=?", item.ProfileID).Updates(map[string]any{"real_name_status": status, "updated_at": now}).Error; e != nil {
			return e
		}
		if status != "PASSED" {
			return nil
		}
		var profile struct{ QualityStatus string }
		if e := tx.Table("avatar_profile").Select("quality_status").Where("id=?", item.ProfileID).Take(&profile).Error; e != nil {
			return e
		}
		if profile.QualityStatus != "PASSED" {
			return nil
		}
		return s.archivePassedCollection(tx, item.ProfileID, item.CollectionSessionID, now, now.AddDate(0, 0, 365))
	})
}

func (s *Service) finishFaceIdentityError(ctx context.Context, id, profileID uint64, code, message string) error {
	now := time.Now()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		update := tx.Table("avatar_identity_verification").Where("id=? AND status='PROCESSING'", id).Updates(map[string]any{"status": "ERROR", "status_reason_code": code, "status_reason": message, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"})
		if update.Error != nil || update.RowsAffected == 0 {
			return update.Error
		}
		return tx.Table("avatar_profile").Where("id=?", profileID).Updates(map[string]any{"real_name_status": "ERROR", "updated_at": now}).Error
	})
}

func (s *Service) createFaceIdentityVerification(tx *gorm.DB, accountID, profileID, rosterMemberID, sessionID, assetID uint64) error {
	id := idgen.Next()
	now := time.Now()
	return tx.Table("avatar_identity_verification").Create(map[string]any{"id": id, "status": "PENDING", "version": 1, "account_id": accountID, "profile_id": profileID, "roster_member_id": rosterMemberID, "collection_session_id": sessionID, "asset_id": assetID, "verification_no": fmt.Sprintf("AVI-%d", id), "provider_code": "TENCENT_IMAGE_RECOGNITION_V2", "verification_type": faceIdentityVerificationType, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error
}

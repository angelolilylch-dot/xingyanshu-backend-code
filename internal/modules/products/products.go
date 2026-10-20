package products

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"strings"
	"time"
	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/contentmoderation"
	"xingyanshu-backend/internal/platform/filestore"
	"xingyanshu-backend/internal/platform/idgen"
)

const reviewPermission = "WORK_REVIEW_COMPLIANCE"

type Service struct {
	db         *gorm.DB
	moderation contentmoderation.Provider
	storage    filestore.Storage
}

func NewService(db *gorm.DB, m contentmoderation.Provider, storage filestore.Storage) *Service {
	return &Service{db: db, moderation: m, storage: storage}
}

type SKU struct {
	SKUName    string            `json:"skuName" binding:"required,max=200"`
	Spec       map[string]string `json:"spec"`
	SKUFileID  *uint64           `json:"skuFileId"`
	SalePrice  string            `json:"salePrice" binding:"required"`
	StockTotal uint              `json:"stockTotal"`
}
type Attachment struct {
	FileID   uint64 `json:"fileId" binding:"required"`
	FileType string `json:"fileType" binding:"required"`
}
type Input struct {
	AuthorizationID       uint64       `json:"authorizationId" binding:"required"`
	WorkID                uint64       `json:"workId" binding:"required"`
	ProductName           string       `json:"productName" binding:"required,max=200"`
	ProductIntro          string       `json:"productIntro"`
	ProductType           string       `json:"productType" binding:"required"`
	Brand                 string       `json:"brand"`
	Manufacturer          string       `json:"manufacturer"`
	Barcode               string       `json:"barcode"`
	TaxRate               *string      `json:"taxRate"`
	CoverFileID           uint64       `json:"coverFileId" binding:"required"`
	DetailFileIDs         []uint64     `json:"detailFileIds"`
	Attachments           []Attachment `json:"attachments"`
	PublicCopyrightNotice string       `json:"publicCopyrightNotice" binding:"required"`
	SaleRegionMode        string       `json:"saleRegionMode" binding:"required"`
	SaleRegionCodes       []string     `json:"saleRegionCodes"`
	FulfillmentMode       string       `json:"fulfillmentMode" binding:"required"`
	FreightMode           string       `json:"freightMode" binding:"required"`
	FreightAmount         string       `json:"freightAmount"`
	AfterSaleTemplateID   uint64       `json:"afterSaleTemplateId" binding:"required"`
	SKUs                  []SKU        `json:"skus" binding:"required,min=1"`
}
type ReviewInput struct {
	ReviewNode      string `json:"reviewNode" binding:"required"`
	Result          string `json:"result" binding:"required"`
	Comment         string `json:"comment"`
	SupplementItems string `json:"supplementItems"`
}
type ListInput struct {
	Keyword         string
	IPID            uint64
	Status          string
	FulfillmentMode string
	PageNo          int
	PageSize        int
}

func (s *Service) Create(ctx context.Context, u creators.CurrentCreator, in Input) (map[string]any, string, error) {
	return s.save(ctx, u, 0, in)
}
func (s *Service) Update(ctx context.Context, u creators.CurrentCreator, id uint64, in Input) (map[string]any, string, error) {
	return s.save(ctx, u, id, in)
}
func (s *Service) save(ctx context.Context, u creators.CurrentCreator, pid uint64, in Input) (map[string]any, string, error) {
	if !valid(in) {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	newProduct := pid == 0
	if newProduct {
		pid = idgen.Next()
	}
	allFileIDs := append([]uint64{in.CoverFileID}, append(append(in.DetailFileIDs, skuFiles(in.SKUs)...), attachmentFiles(in.Attachments)...)...)
	if e := validFiles(s.db.WithContext(ctx), allFileIDs); e != nil {
		return nil, code(e), unwrap(e)
	}
	images, e := s.productImages(ctx, in)
	if e != nil {
		return nil, code(e), unwrap(e)
	}
	imageModerator, ok := s.moderation.(contentmoderation.ImageProvider)
	if !ok {
		return nil, "CHECK_ERROR", fmt.Errorf("product image moderation provider is unavailable")
	}
	detectionText := strings.Join([]string{in.ProductName, in.ProductIntro, in.PublicCopyrightNotice}, "\n")
	detections := []detectionOutcome{}
	detectionDataID := fmt.Sprintf("%d-%d", pid, idgen.Next())
	detection, moderationErr := s.moderation.Moderate(ctx, contentmoderation.Request{Text: detectionText, DataID: detectionDataID, UserID: fmt.Sprint(u.UserID), Language: "zh"})
	detections = append(detections, detectionOutcome{source: "TEXT", value: detection, err: moderationErr})
	for _, image := range images {
		url, signErr := s.storage.SignDownloadURL(ctx, image.StorageKey, 10*time.Minute)
		var result contentmoderation.Result
		if signErr == nil {
			result, signErr = imageModerator.ModerateImage(ctx, contentmoderation.ImageRequest{URL: url, DataID: fmt.Sprintf("%d-%d-%d", pid, image.ID, idgen.Next()), UserID: fmt.Sprint(u.UserID)})
		}
		detections = append(detections, detectionOutcome{source: "IMAGE:" + fmt.Sprint(image.ID), value: result, err: signErr})
	}
	status, check := "PENDING_COPYRIGHT_REVIEW", "PASS"
	for _, item := range detections {
		if item.err != nil {
			status, check = "CHECK_ERROR", "ERROR"
			break
		}
		if item.value.Decision == contentmoderation.DecisionBlock {
			status, check = "CHECK_REJECTED", "BLOCK"
			break
		}
		if item.value.Decision == contentmoderation.DecisionReview {
			check = "REVIEW"
		}
	}
	var out map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		createVersion := newProduct
		var auth struct {
			ID, IPID    uint64
			IPNo        string `gorm:"column:ip_no"`
			Status      string
			EndAt       time.Time
			Commercial  bool   `gorm:"column:commercial_allowed_snapshot"`
			ContentType string `gorm:"column:content_type_snapshot"`
		}
		if e := tx.Table("creator_authorization ca").Select("ca.id,ca.ip_id,a.ip_no,ca.status,ca.end_at,ao.commercial_allowed_snapshot,ao.content_type_snapshot").Joins("JOIN auth_order ao ON ao.id=ca.auth_order_id").Joins("JOIN ip_asset a ON a.id=ca.ip_id").Where("ca.id=? AND ca.creator_id=?", in.AuthorizationID, u.CreatorID).Take(&auth).Error; e != nil {
			return biz("AUTHORIZATION_NOT_FOUND")
		}
		if auth.Status != "ACTIVE" || !auth.EndAt.After(time.Now()) || !auth.Commercial {
			return biz("AUTHORIZATION_NOT_ACTIVE")
		}
		var work struct {
			ID, IPID uint64
			Status   string
			WorkNo   string `gorm:"column:work_no"`
		}
		if e := tx.Table("derivative_work").Where("id=? AND creator_id=?", in.WorkID, u.CreatorID).Take(&work).Error; e != nil {
			return biz("WORK_NOT_FOUND")
		}
		if work.IPID != auth.IPID || work.Status != "RIGHTS_CONFIRMED" {
			return biz("WORK_NOT_RIGHTS_CONFIRMED")
		}
		var tpl struct {
			ID         uint64
			Status     string
			BizType    string `gorm:"column:biz_type"`
			Conditions string `gorm:"column:conditions_json"`
			RefundRule string `gorm:"column:refund_rule_json"`
		}
		if e := tx.Table("after_sale_rule_template").Where("id=? AND enabled=1", in.AfterSaleTemplateID).Take(&tpl).Error; e != nil || tpl.BizType != "PRODUCT" {
			return biz("AFTER_SALE_TEMPLATE_NOT_FOUND")
		}
		if e := validFiles(tx, append([]uint64{in.CoverFileID}, append(append(in.DetailFileIDs, skuFiles(in.SKUs)...), attachmentFiles(in.Attachments)...)...)); e != nil {
			return e
		}
		now := time.Now()
		versionID := idgen.Next()
		versionNo := 1
		if newProduct {
			if e := tx.Table("product").Create(map[string]any{"id": pid, "product_no": fmt.Sprintf("PRODUCT-%d", pid), "creator_id": u.CreatorID, "ip_id": auth.IPID, "authorization_id": auth.ID, "work_id": work.ID, "current_version_id": versionID, "status": status, "version": 1, "fulfillment_mode": in.FulfillmentMode, "sale_region_mode": in.SaleRegionMode, "created_by": u.UserID, "updated_by": u.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(u.UserID)}).Error; e != nil {
				return e
			}
		} else {
			var p struct {
				CreatorID        uint64
				Status           string
				CurrentVersionID uint64
			}
			if e := tx.Table("product").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", pid).Take(&p).Error; e != nil {
				return biz("PRODUCT_NOT_FOUND")
			}
			if p.CreatorID != u.CreatorID {
				return biz("PRODUCT_NOT_FOUND")
			}
			if p.Status == "PUBLISHED" {
				return biz("PRODUCT_MUST_DELIST_FIRST")
			}
			if p.Status == "DELISTED" {
				var count int64
				if e := tx.Table("product_version").Where("product_id=?", pid).Count(&count).Error; e != nil {
					return e
				}
				versionID, versionNo, createVersion = idgen.Next(), int(count)+1, true
				if e := tx.Table("product").Where("id=?", pid).Updates(map[string]any{"current_version_id": versionID, "status": status, "authorization_id": auth.ID, "work_id": work.ID, "updated_by": u.UserID, "updated_at": now, "version": gorm.Expr("version+1")}).Error; e != nil {
					return e
				}
			} else if p.Status != "RETURNED" && p.Status != "REJECTED" && p.Status != "CHECK_REJECTED" && p.Status != "CHECK_ERROR" {
				return biz("PRODUCT_STATUS_INVALID")
			} else {
				var old struct {
					VersionNo int `gorm:"column:version_no"`
				}
				tx.Table("product_version").Where("id=?", p.CurrentVersionID).Take(&old)
				versionID = p.CurrentVersionID
				versionNo = old.VersionNo
				tx.Table("product_sku").Where("product_version_id=?", versionID).Delete(nil)
				tx.Table("product_version").Where("id=?", versionID).Updates(map[string]any{"status": status, "updated_by": u.UserID, "updated_at": now, "version": gorm.Expr("version+1")})
				tx.Table("product").Where("id=?", pid).Updates(map[string]any{"status": status, "authorization_id": auth.ID, "work_id": work.ID, "updated_by": u.UserID, "updated_at": now, "version": gorm.Expr("version+1")})
			}
		}
		details, _ := json.Marshal(in.DetailFileIDs)
		attachments, _ := json.Marshal(in.Attachments)
		regions, _ := json.Marshal(in.SaleRegionCodes)
		rule, _ := json.Marshal(map[string]any{"conditions": tpl.Conditions, "refundRule": tpl.RefundRule, "templateId": tpl.ID})
		if !createVersion {
			tx.Table("product_version").Where("id=?", versionID).Updates(map[string]any{"product_name": in.ProductName, "product_intro": in.ProductIntro, "product_type": in.ProductType, "brand": blank(in.Brand), "manufacturer": blank(in.Manufacturer), "barcode": blank(in.Barcode), "tax_rate": in.TaxRate, "cover_file_id": in.CoverFileID, "detail_files_json": string(details), "attachment_files_json": string(attachments), "public_copyright_notice": in.PublicCopyrightNotice, "sale_region_codes_json": string(regions), "freight_mode": in.FreightMode, "freight_amount": in.FreightAmount, "after_sale_template_id": tpl.ID, "rule_snapshot": string(rule), "check_status": check})
		} else if e := tx.Table("product_version").Create(map[string]any{"id": versionID, "product_id": pid, "version_no": versionNo, "status": status, "product_name": in.ProductName, "product_intro": in.ProductIntro, "product_type": in.ProductType, "brand": blank(in.Brand), "manufacturer": blank(in.Manufacturer), "barcode": blank(in.Barcode), "tax_rate": in.TaxRate, "cover_file_id": in.CoverFileID, "detail_files_json": string(details), "attachment_files_json": string(attachments), "public_copyright_notice": in.PublicCopyrightNotice, "sale_region_codes_json": string(regions), "freight_mode": in.FreightMode, "freight_amount": in.FreightAmount, "after_sale_template_id": tpl.ID, "rule_snapshot": string(rule), "check_status": check, "version": 1, "created_by": u.UserID, "updated_by": u.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(u.UserID)}).Error; e != nil {
			return e
		}
		productNo := fmt.Sprintf("PRODUCT-%d", pid)
		for idx, k := range in.SKUs {
			spec, _ := json.Marshal(k.Spec)
			if e := tx.Table("product_sku").Create(map[string]any{"id": idgen.Next(), "product_version_id": versionID, "sku_code": generatedSKUCode(auth.IPNo, work.WorkNo, productNo, idx+1), "sku_name": k.SKUName, "spec_json": string(spec), "sku_file_id": k.SKUFileID, "sale_price": k.SalePrice, "stock_total": k.StockTotal, "sku_status": "ON_SALE", "status": "ACTIVE", "version": 1, "created_by": u.UserID}).Error; e != nil {
				return e
			}
		}
		for _, item := range detections {
			contentHash := fmt.Sprintf("%x", sha256.Sum256([]byte(item.source+"\n"+detectionText)))
			row := map[string]any{"id": idgen.Next(), "status": check, "detection_no": fmt.Sprintf("DETECT-%d", idgen.Next()), "object_type": "PRODUCT", "object_id": pid, "text_hash": contentHash, "provider_code": item.value.Provider, "provider_request_no": blank(item.value.ProviderRequestNo), "detection_status": check, "risk_category": blank(item.value.Label), "created_by": u.UserID}
			if item.err != nil {
				row["error_summary"] = item.err.Error()
			}
			if e := tx.Table("content_detection_record").Create(row).Error; e != nil {
				return e
			}
		}
		out = map[string]any{"productId": fmt.Sprint(pid), "productVersionId": fmt.Sprint(versionID), "status": status, "checkStatus": check}
		return audit.WriteStatusHistory(tx, "PRODUCT", pid, "", status, "创作者提交商品", audit.Operator{Type: "USER", ID: u.UserID})
	})
	if err != nil {
		return nil, code(err), unwrap(err)
	}
	return out, "", nil
}

type productImage struct {
	ID         uint64
	StorageKey string `gorm:"column:storage_key"`
}
type detectionOutcome struct {
	source string
	value  contentmoderation.Result
	err    error
}

// productImages 校验封面、详情和 SKU 配图均是图片，同时把图片类附件一并纳入审核。
func (s *Service) productImages(ctx context.Context, in Input) ([]productImage, error) {
	required := unique(append([]uint64{in.CoverFileID}, append(in.DetailFileIDs, skuFiles(in.SKUs)...)...))
	all := unique(append(required, attachmentFiles(in.Attachments)...))
	var images []productImage
	if e := s.db.WithContext(ctx).Table("file_object").Select("id,storage_key").Where("id IN ? AND mime_type LIKE 'image/%'", all).Find(&images).Error; e != nil {
		return nil, e
	}
	found := make(map[uint64]bool, len(images))
	for _, image := range images {
		found[image.ID] = true
	}
	for _, id := range required {
		if !found[id] {
			return nil, biz("PRODUCT_IMAGE_REQUIRED")
		}
	}
	if s.storage == nil {
		return nil, fmt.Errorf("product image moderation storage is unavailable")
	}
	return images, nil
}
func (s *Service) Review(ctx context.Context, u identity.CurrentAdmin, vid uint64, in ReviewInput) (map[string]any, string, error) {
	if !u.HasPermission(reviewPermission) || !((in.ReviewNode == "COPYRIGHT_INITIAL") || (in.ReviewNode == "COMPLIANCE_REVIEW")) || !((in.Result == "PASS") || (in.Result == "RETURN") || (in.Result == "REJECT")) {
		return nil, "AUTH_FORBIDDEN", nil
	}
	var out map[string]any
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var v struct {
			ID, ProductID uint64
			Status        string
			VersionNo     int `gorm:"column:version_no"`
		}
		if x := tx.Table("product_version").Where("id=?", vid).Take(&v).Error; x != nil {
			return biz("PRODUCT_VERSION_NOT_FOUND")
		}
		expected := "PENDING_COPYRIGHT_REVIEW"
		if in.ReviewNode == "COMPLIANCE_REVIEW" {
			expected = "PENDING_COMPLIANCE_REVIEW"
		}
		if v.Status != expected {
			return biz("PRODUCT_STATUS_INVALID")
		}
		next := ""
		if in.Result == "PASS" {
			next = "PENDING_COMPLIANCE_REVIEW"
			if in.ReviewNode == "COMPLIANCE_REVIEW" {
				next = "PUBLISHED"
			}
		} else if in.Result == "RETURN" {
			next = "RETURNED"
		} else {
			next = "REJECTED"
		}
		if x := tx.Table("product_review_record").Create(map[string]any{"id": idgen.Next(), "product_version_id": vid, "review_node": in.ReviewNode, "review_result": in.Result, "review_comment": in.Comment, "supplement_items": in.SupplementItems, "reviewer_id": u.UserID, "created_by": u.UserID}).Error; x != nil {
			return x
		}
		now := time.Now()
		tx.Table("product_version").Where("id=?", vid).Updates(map[string]any{"status": next, "updated_at": now, "updated_by": u.UserID})
		tx.Table("product").Where("id=?", v.ProductID).Updates(map[string]any{"status": next, "updated_at": now, "updated_by": u.UserID})
		if next == "PUBLISHED" {
			snapshot, _ := json.Marshal(map[string]any{"reviewNode": in.ReviewNode, "reviewResult": in.Result, "productVersionId": fmt.Sprint(vid)})
			if x := tx.Table("content_publish_record").Create(map[string]any{"id": idgen.Next(), "status": "PUBLISHED", "object_type": "PRODUCT", "object_id": v.ProductID, "version_no": v.VersionNo, "check_snapshot": string(snapshot), "published_at": now, "created_by": u.UserID, "updated_by": u.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(u.UserID)}).Error; x != nil {
				return x
			}
		}
		out = map[string]any{"productId": fmt.Sprint(v.ProductID), "productVersionId": fmt.Sprint(vid), "status": next}
		return audit.WriteStatusHistory(tx, "PRODUCT", v.ProductID, expected, next, "商品审核", audit.Operator{Type: "USER", ID: u.UserID})
	})
	if e != nil {
		return nil, code(e), unwrap(e)
	}
	return out, "", nil
}
func (s *Service) Delist(ctx context.Context, cid, pid uint64) (map[string]any, string, error) {
	r := s.db.WithContext(ctx).Table("product").Where("id=? AND creator_id=? AND status='PUBLISHED'", pid, cid).Updates(map[string]any{"status": "DELISTED", "updated_at": time.Now()})
	if r.Error != nil {
		return nil, "INTERNAL_ERROR", r.Error
	}
	if r.RowsAffected == 0 {
		return nil, "PRODUCT_STATUS_INVALID", nil
	}
	return map[string]any{"productId": fmt.Sprint(pid), "status": "DELISTED"}, "", nil
}
func (s *Service) List(ctx context.Context, cid uint64, in ListInput) (map[string]any, string, error) {
	if in.PageNo <= 0 {
		in.PageNo = 1
	}
	if in.PageSize <= 0 {
		in.PageSize = 20
	}
	if in.PageSize > 100 {
		in.PageSize = 100
	}
	var rows []map[string]any
	q := s.db.WithContext(ctx).Table("product p").Select("p.id,p.product_no,p.status,p.current_version_id,p.fulfillment_mode,p.created_at,v.product_name,v.product_type").Joins("LEFT JOIN product_version v ON v.id=p.current_version_id").Where("p.creator_id=?", cid)
	if in.IPID > 0 {
		q = q.Where("p.ip_id=?", in.IPID)
	}
	if strings.TrimSpace(in.Status) != "" {
		q = q.Where("p.status=?", in.Status)
	}
	if strings.TrimSpace(in.FulfillmentMode) != "" {
		q = q.Where("p.fulfillment_mode=?", in.FulfillmentMode)
	}
	if keyword := strings.TrimSpace(in.Keyword); keyword != "" {
		q = q.Where("p.product_no LIKE ? OR v.product_name LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	var total int64
	if e := q.Count(&total).Error; e != nil {
		return nil, "", e
	}
	e := q.Order("p.id DESC").Offset((in.PageNo - 1) * in.PageSize).Limit(in.PageSize).Find(&rows).Error
	return map[string]any{"items": rows, "pageNo": in.PageNo, "pageSize": in.PageSize, "total": total}, "", e
}
func (s *Service) Get(ctx context.Context, cid, pid uint64) (map[string]any, string, error) {
	var p map[string]any
	if e := s.db.WithContext(ctx).Table("product").Where("id=? AND creator_id=?", pid, cid).Take(&p).Error; e != nil {
		return nil, "PRODUCT_NOT_FOUND", nil
	}
	var v, skus, reviews []map[string]any
	s.db.WithContext(ctx).Table("product_version").Where("product_id=?", pid).Order("version_no DESC").Find(&v)
	s.db.WithContext(ctx).Table("product_sku").Where("product_version_id=?", p["current_version_id"]).Find(&skus)
	s.db.WithContext(ctx).Table("product_review_record").Where("product_version_id=?", p["current_version_id"]).Find(&reviews)
	return map[string]any{"product": p, "versions": v, "skus": skus, "reviews": reviews}, "", nil
}
func (s *Service) Pending(ctx context.Context, node string) (map[string]any, string, error) {
	status := "PENDING_COPYRIGHT_REVIEW"
	if node == "COMPLIANCE_REVIEW" {
		status = "PENDING_COMPLIANCE_REVIEW"
	}
	if node != "" && node != "COPYRIGHT_INITIAL" && node != "COMPLIANCE_REVIEW" {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	var rows []map[string]any
	e := s.db.WithContext(ctx).Table("product p").Select("p.id,p.product_no,p.status,p.current_version_id,v.product_name,v.product_type").Joins("JOIN product_version v ON v.id=p.current_version_id").Where("p.status=?", status).Find(&rows).Error
	return map[string]any{"items": rows}, "", e
}
func valid(i Input) bool {
	if len(i.SKUs) == 0 || i.SaleRegionMode == "" || i.FulfillmentMode == "" || i.FreightMode == "" || !((i.SaleRegionMode == "NATIONAL") || (i.SaleRegionMode == "SPECIFIED" && len(i.SaleRegionCodes) > 0)) || !((i.FulfillmentMode == "CREATOR_SHIP") || (i.FulfillmentMode == "PLATFORM_SHIP")) || !((i.FreightMode == "FREE") || (i.FreightMode == "FIXED")) {
		return false
	}
	freight, e := strconv.ParseFloat(i.FreightAmount, 64)
	if (i.FreightMode == "FREE" && strings.TrimSpace(i.FreightAmount) != "" && (e != nil || freight != 0)) || (i.FreightMode == "FIXED" && (e != nil || freight <= 0)) {
		return false
	}
	if i.TaxRate != nil {
		v, e := strconv.ParseFloat(*i.TaxRate, 64)
		if e != nil || v < 0 || v > 1 {
			return false
		}
	}
	for _, sku := range i.SKUs {
		price, e := strconv.ParseFloat(sku.SalePrice, 64)
		if strings.TrimSpace(sku.SKUName) == "" || e != nil || price <= 0 {
			return false
		}
	}
	for _, item := range i.Attachments {
		if item.FileID == 0 || (item.FileType != "PRODUCT_VIDEO" && item.FileType != "MANUAL" && item.FileType != "QUALITY_REPORT" && item.FileType != "PRODUCTION_LICENSE" && item.FileType != "OTHER") {
			return false
		}
	}
	return true
}
func skuFiles(s []SKU) []uint64 {
	r := []uint64{}
	for _, x := range s {
		if x.SKUFileID != nil {
			r = append(r, *x.SKUFileID)
		}
	}
	return r
}
func attachmentFiles(items []Attachment) []uint64 {
	r := make([]uint64, 0, len(items))
	for _, item := range items {
		r = append(r, item.FileID)
	}
	return r
}
func validFiles(tx *gorm.DB, ids []uint64) error {
	var n int64
	tx.Table("file_object").Where("id IN ? AND status='ACTIVE' AND upload_status='SUCCESS'", ids).Count(&n)
	if n != int64(len(unique(ids))) {
		return biz("FILE_NOT_FOUND")
	}
	return nil
}
func unique(a []uint64) []uint64 {
	m := map[uint64]bool{}
	r := []uint64{}
	for _, x := range a {
		if x > 0 && !m[x] {
			m[x] = true
			r = append(r, x)
		}
	}
	return r
}
func blank(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
func generatedSKUCode(ipNo, workNo, productNo string, serial int) string {
	if len(productNo) > 6 {
		productNo = productNo[len(productNo)-6:]
	}
	return fmt.Sprintf("SKU-%s-%s-%s-%02d", ipNo, workNo, productNo, serial)
}

type coded struct{ v string }

func (e coded) Error() string { return e.v }
func biz(v string) error      { return coded{v} }
func code(e error) string {
	if x, ok := e.(coded); ok {
		return x.v
	}
	return "INTERNAL_ERROR"
}
func unwrap(e error) error {
	if _, ok := e.(coded); ok {
		return nil
	}
	return e
}

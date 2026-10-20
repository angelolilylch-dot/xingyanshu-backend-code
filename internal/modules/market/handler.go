package market

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"strings"
	"time"
	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/platform/httpx"
)

// RegisterRoutes 提供创作者商城只读查询；下单和名额预占由下一任务实现。
func RegisterRoutes(g *gin.RouterGroup, db *gorm.DB, auth gin.HandlerFunc) {
	r := g.Group("/creator/ip-mall", auth)
	r.GET("", func(c *gin.Context) {
		p, _ := strconv.Atoi(c.DefaultQuery("pageNo", "1"))
		s, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
		if p < 1 {
			p = 1
		}
		if s < 1 || s > 100 {
			s = 20
		}
		var total int64
		q := db.WithContext(c).Table("ip_asset a").Joins("JOIN sys_org o ON o.id=a.main_city_org_id AND o.status='ACTIVE'").Joins("JOIN ip_auth_binding b ON b.ip_id=a.id AND b.status='ACTIVE'").Joins("JOIN auth_plan_version v ON v.id=b.auth_plan_version_id AND v.status='ENABLED'").Where("a.status='LISTED' AND a.mall_visible=1")
		if k := c.Query("keyword"); k != "" {
			q = q.Where("a.ip_name LIKE ? OR a.ip_no LIKE ?", "%"+k+"%", "%"+k+"%")
		}
		if value := strings.TrimSpace(c.Query("regionCode")); value != "" {
			q = q.Where("o.city_code=?", value)
		}
		if value := strings.TrimSpace(c.Query("ipType")); value != "" {
			q = q.Where("a.ip_type=?", value)
		}
		if value := strings.TrimSpace(c.Query("authorizationMode")); value != "" {
			q = q.Where("a.authorization_mode=?", value)
		}
		if value := strings.TrimSpace(c.Query("contentType")); value != "" {
			q = q.Where("v.content_type=?", value)
		}
		if value := strings.TrimSpace(c.Query("term")); value != "" {
			q = q.Where("CAST(v.term_value AS CHAR)=? OR v.term_type=?", value, value)
		}
		for _, tag := range splitCSV(c.Query("tagCodes")) {
			q = q.Where("(JSON_CONTAINS(a.region_tag_codes_json, JSON_QUOTE(?)) OR JSON_CONTAINS(a.culture_tag_codes_json, JSON_QUOTE(?)) OR JSON_CONTAINS(a.category_tag_codes_json, JSON_QUOTE(?)) OR JSON_CONTAINS(a.commercialization_tag_codes_json, JSON_QUOTE(?)))", tag, tag, tag, tag)
		}
		if value := strings.TrimSpace(c.Query("minPrice")); value != "" {
			q = q.Where("a.suggested_transaction_price_amount>=?", value)
		}
		if value := strings.TrimSpace(c.Query("maxPrice")); value != "" {
			q = q.Where("a.suggested_transaction_price_amount<=?", value)
		}
		if err := q.Count(&total).Error; err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "商城列表查询失败"))
			return
		}
		sortBy := strings.TrimSpace(c.Query("sortBy"))
		switch sortBy {
		case "priceAsc":
			q = q.Order("a.suggested_transaction_price_amount ASC, a.updated_at DESC")
		case "priceDesc":
			q = q.Order("a.suggested_transaction_price_amount DESC, a.updated_at DESC")
		case "updatedAtAsc":
			q = q.Order("a.updated_at ASC")
		default:
			q = q.Order("COALESCE(a.status_changed_at,a.updated_at) DESC")
		}
		var rows []mallRow
		if err := q.Select("a.id,a.ip_no,a.ip_name,a.short_intro,a.ip_type,a.cover_file_id,a.authorization_mode,a.suggested_transaction_price_amount AS price_amount,v.term_type,v.term_value,v.content_type,v.quota_total,COALESCE(a.status_changed_at,a.updated_at) AS listed_at").Offset((p - 1) * s).Limit(s).Find(&rows).Error; err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "商城列表查询失败"))
			return
		}
		current, _ := creators.CurrentCreatorFromContext(c)
		purchaseFlags := purchaseEligibility(c, db, current.CreatorID, rows)
		items := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			items = append(items, map[string]any{"ipId": fmt.Sprint(row.ID), "ipName": row.IPName, "coverFileId": stringID(row.CoverFileID), "introduction": row.ShortIntro, "price": row.PriceAmount, "authorizationMode": row.AuthorizationMode, "termType": row.TermType, "termValue": row.TermValue, "contentType": row.ContentType, "canPurchase": purchaseFlags[row.ID], "listedAt": row.ListedAt})
		}
		httpx.Success(c, http.StatusOK, map[string]any{"items": items, "pageNo": p, "pageSize": s, "total": total, "totalPages": (total + int64(s) - 1) / int64(s)})
	})
	r.GET("/:ipId", func(c *gin.Context) {
		id, ok := id(c)
		if !ok {
			return
		}
		var row mallDetailRow
		err := db.WithContext(c).Table("ip_asset a").Joins("JOIN ip_auth_binding b ON b.ip_id=a.id AND b.status='ACTIVE'").Joins("JOIN auth_plan_version v ON v.id=b.auth_plan_version_id AND v.status='ENABLED'").Where("a.id=? AND a.status='LISTED' AND a.mall_visible=1", id).Select("a.id,a.ip_no,a.ip_name,a.ip_type,a.short_intro,a.detail_content,a.culture_story,a.ip_feature,a.cover_file_id,a.public_copyright_notice,a.purchase_notice,a.authorization_mode,a.suggested_transaction_price_amount AS price_amount,v.term_type,v.term_value,v.region_mode,v.scenes_json,v.content_type").Take(&row).Error
		if err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusNotFound, "IP_NOT_LISTED", "IP未上架或不可购买"))
			return
		}
		httpx.Success(c, http.StatusOK, map[string]any{"ipId": fmt.Sprint(row.ID), "ipNo": row.IPNo, "ipName": row.IPName, "ipType": row.IPType, "introduction": row.ShortIntro, "detailContent": row.DetailContent, "cultureStory": row.CultureStory, "ipFeature": row.IPFeature, "coverFileId": stringID(row.CoverFileID), "publicCopyrightNotice": row.PublicCopyrightNotice, "purchaseNotice": row.PurchaseNotice, "price": row.PriceAmount, "authorizationMode": row.AuthorizationMode, "termType": row.TermType, "termValue": row.TermValue, "regionMode": row.RegionMode, "scenes": row.ScenesJSON, "contentType": row.ContentType})
	})
	r.GET("/:ipId/purchase-preview", func(c *gin.Context) {
		id, ok := id(c)
		if !ok {
			return
		}
		cur, _ := creators.CurrentCreatorFromContext(c)
		var row struct {
			IdentityStatus string `gorm:"column:identity_status"`
			Status         string
			PriceAmount    string `gorm:"column:price_amount"`
			IPName         string `gorm:"column:ip_name"`
			QuotaTotal     *int   `gorm:"column:quota_total"`
			Mode           string `gorm:"column:authorization_mode"`
		}
		err := db.WithContext(c).Table("creator_profile cp").Joins("JOIN ip_asset a ON a.id=? AND a.status='LISTED' AND a.mall_visible=1", id).Joins("JOIN ip_auth_binding b ON b.ip_id=a.id AND b.status='ACTIVE'").Joins("JOIN auth_plan_version v ON v.id=b.auth_plan_version_id AND v.status='ENABLED'").Where("cp.id=?", cur.CreatorID).Select("cp.identity_status,cp.status,a.ip_name,a.authorization_mode,a.suggested_transaction_price_amount AS price_amount,v.quota_total").Take(&row).Error
		can := err == nil && row.IdentityStatus == "VERIFIED" && row.Status == "ACTIVE"
		code := ""
		msg := "可购买"
		if err != nil {
			code = "IP_NOT_LISTED"
			msg = "IP未上架或不可购买"
		} else if !can {
			code = "CREATOR_IDENTITY_REQUIRED"
			msg = "请先完成实名认证"
		} else {
			var mine int64
			db.WithContext(c).Table("creator_authorization").Where("creator_id=? AND ip_id=? AND status='ACTIVE' AND active_flag=1", cur.CreatorID, id).Count(&mine)
			if mine > 0 {
				can, code, msg = false, "AUTHORIZATION_ALREADY_ACTIVE", "已持有该IP的有效授权"
			}
			if can {
				var sold int64
				db.WithContext(c).Table("creator_authorization").Where("ip_id=? AND status='ACTIVE' AND active_flag=1", id).Count(&sold)
				var reserved int64
				db.WithContext(c).Table("auth_quota_reservation").Where("ip_id=? AND reservation_status='RESERVED' AND expires_at>?", id, time.Now()).Count(&reserved)
				if !canPurchase(true, false, row.Mode, row.QuotaTotal, sold+reserved) {
					can, code, msg = false, "AUTHORIZATION_QUOTA_SOLD_OUT", "授权名额已售罄"
				}
			}
		}
		httpx.Success(c, http.StatusOK, map[string]any{"canPurchase": can, "blockCode": code, "blockMessage": msg, "ip": map[string]any{"id": fmt.Sprint(id), "ipName": row.IPName}, "priceAmount": row.PriceAmount})
	})
}

type mallRow struct {
	ID                uint64    `gorm:"column:id"`
	IPName            string    `gorm:"column:ip_name"`
	ShortIntro        string    `gorm:"column:short_intro"`
	CoverFileID       uint64    `gorm:"column:cover_file_id"`
	AuthorizationMode string    `gorm:"column:authorization_mode"`
	PriceAmount       string    `gorm:"column:price_amount"`
	TermType          string    `gorm:"column:term_type"`
	TermValue         int       `gorm:"column:term_value"`
	ContentType       string    `gorm:"column:content_type"`
	QuotaTotal        *int      `gorm:"column:quota_total"`
	ListedAt          time.Time `gorm:"column:listed_at"`
}

type mallDetailRow struct {
	ID                    uint64 `gorm:"column:id"`
	IPNo                  string `gorm:"column:ip_no"`
	IPName                string `gorm:"column:ip_name"`
	IPType                string `gorm:"column:ip_type"`
	ShortIntro            string `gorm:"column:short_intro"`
	DetailContent         string `gorm:"column:detail_content"`
	CultureStory          string `gorm:"column:culture_story"`
	IPFeature             string `gorm:"column:ip_feature"`
	CoverFileID           uint64 `gorm:"column:cover_file_id"`
	PublicCopyrightNotice string `gorm:"column:public_copyright_notice"`
	PurchaseNotice        string `gorm:"column:purchase_notice"`
	AuthorizationMode     string `gorm:"column:authorization_mode"`
	PriceAmount           string `gorm:"column:price_amount"`
	TermType              string `gorm:"column:term_type"`
	TermValue             int    `gorm:"column:term_value"`
	RegionMode            string `gorm:"column:region_mode"`
	ScenesJSON            string `gorm:"column:scenes_json"`
	ContentType           string `gorm:"column:content_type"`
}

type authorizationCount struct {
	IPID  uint64 `gorm:"column:ip_id"`
	Total int64  `gorm:"column:total"`
}

// purchaseEligibility 与 purchase-preview 保持同一购买资格口径：未实名、已持有有效授权、普通授权或独家买断名额售罄均不能购买。
func purchaseEligibility(c *gin.Context, db *gorm.DB, creatorID uint64, rows []mallRow) map[uint64]bool {
	flags := make(map[uint64]bool, len(rows))
	if creatorID == 0 || len(rows) == 0 {
		return flags
	}
	var profile struct {
		Status         string
		IdentityStatus string `gorm:"column:identity_status"`
	}
	if err := db.WithContext(c).Table("creator_profile").Select("status,identity_status").Where("id=?", creatorID).Take(&profile).Error; err != nil || profile.Status != "ACTIVE" || profile.IdentityStatus != "VERIFIED" {
		return flags
	}
	ids := make([]uint64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	var ownedRows []authorizationCount
	if err := db.WithContext(c).Table("creator_authorization").Select("ip_id,COUNT(*) AS total").Where("creator_id=? AND ip_id IN ? AND status='ACTIVE' AND active_flag=1", creatorID, ids).Group("ip_id").Scan(&ownedRows).Error; err != nil {
		return flags
	}
	owned := map[uint64]bool{}
	for _, row := range ownedRows {
		owned[row.IPID] = row.Total > 0
	}
	var soldRows []authorizationCount
	if err := db.WithContext(c).Table("creator_authorization").Select("ip_id,COUNT(*) AS total").Where("ip_id IN ? AND status='ACTIVE' AND active_flag=1", ids).Group("ip_id").Scan(&soldRows).Error; err != nil {
		return flags
	}
	sold := map[uint64]int64{}
	for _, row := range soldRows {
		sold[row.IPID] = row.Total
	}
	for _, row := range rows {
		var reserved int64
		db.WithContext(c).Table("auth_quota_reservation").Where("ip_id=? AND reservation_status='RESERVED' AND expires_at>?", row.ID, time.Now()).Count(&reserved)
		flags[row.ID] = canPurchase(true, owned[row.ID], row.AuthorizationMode, row.QuotaTotal, sold[row.ID]+reserved)
	}
	return flags
}

func canPurchase(identityVerified, alreadyAuthorized bool, authorizationMode string, quotaTotal *int, sold int64) bool {
	if !identityVerified || alreadyAuthorized {
		return false
	}
	if authorizationMode == "EXCLUSIVE_BUYOUT" {
		return sold == 0
	}
	return quotaTotal == nil || sold < int64(*quotaTotal)
}

func splitCSV(value string) []string {
	result := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
func stringID(value uint64) string {
	if value == 0 {
		return ""
	}
	return fmt.Sprint(value)
}
func id(c *gin.Context) (uint64, bool) {
	v, e := strconv.ParseUint(c.Param("ipId"), 10, 64)
	if e != nil || v == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "IP编号格式错误"))
		return 0, false
	}
	return v, true
}

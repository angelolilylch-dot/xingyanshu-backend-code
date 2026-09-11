package market

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http"
	"strconv"
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
		q := db.WithContext(c).Table("ip_asset a").Joins("JOIN ip_auth_binding b ON b.ip_id=a.id AND b.status='ACTIVE'").Joins("JOIN auth_plan_version v ON v.id=b.auth_plan_version_id AND v.status='ENABLED'").Where("a.status='LISTED' AND a.mall_visible=1")
		if k := c.Query("keyword"); k != "" {
			q = q.Where("a.ip_name LIKE ? OR a.ip_no LIKE ?", "%"+k+"%", "%"+k+"%")
		}
		q.Count(&total)
		var items []map[string]any
		q.Select("a.id,a.ip_no,a.ip_name,a.short_intro,a.ip_type,a.cover_file_id,a.authorization_mode,a.suggested_transaction_price_amount AS price_amount,v.term_type,v.term_value,v.content_type,a.updated_at AS listed_at").Order("a.updated_at DESC").Offset((p - 1) * s).Limit(s).Find(&items)
		httpx.Success(c, http.StatusOK, map[string]any{"items": items, "pageNo": p, "pageSize": s, "total": total})
	})
	r.GET("/:ipId", func(c *gin.Context) {
		id, ok := id(c)
		if !ok {
			return
		}
		var row map[string]any
		err := db.WithContext(c).Table("ip_asset a").Joins("JOIN ip_auth_binding b ON b.ip_id=a.id AND b.status='ACTIVE'").Joins("JOIN auth_plan_version v ON v.id=b.auth_plan_version_id AND v.status='ENABLED'").Where("a.id=? AND a.status='LISTED' AND a.mall_visible=1", id).Select("a.id,a.ip_no,a.ip_name,a.ip_type,a.short_intro,a.detail_content,a.culture_story,a.ip_feature,a.cover_file_id,a.public_copyright_notice,a.purchase_notice,a.authorization_mode,a.suggested_transaction_price_amount AS price_amount,v.term_type,v.term_value,v.region_mode,v.scenes_json,v.content_type").Take(&row).Error
		if err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusNotFound, "IP_NOT_LISTED", "IP未上架或不可购买"))
			return
		}
		httpx.Success(c, http.StatusOK, row)
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
			QuotaTotal     int64  `gorm:"column:quota_total"`
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
			if can && row.Mode == "NORMAL_SHARE" {
				var sold int64
				db.WithContext(c).Table("creator_authorization").Where("ip_id=? AND status='ACTIVE' AND active_flag=1", id).Count(&sold)
				if sold >= row.QuotaTotal {
					can, code, msg = false, "AUTHORIZATION_QUOTA_SOLD_OUT", "授权名额已售罄"
				}
			}
		}
		httpx.Success(c, http.StatusOK, map[string]any{"canPurchase": can, "blockCode": code, "blockMessage": msg, "ip": map[string]any{"id": fmt.Sprint(id), "ipName": row.IPName}, "priceAmount": row.PriceAmount})
	})
}
func id(c *gin.Context) (uint64, bool) {
	v, e := strconv.ParseUint(c.Param("ipId"), 10, 64)
	if e != nil || v == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "IP编号格式错误"))
		return 0, false
	}
	return v, true
}

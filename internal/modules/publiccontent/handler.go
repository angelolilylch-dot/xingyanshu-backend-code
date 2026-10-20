package publiccontent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/platform/filestore"
	"xingyanshu-backend/internal/platform/httpx"
)

type Service struct {
	db      *gorm.DB
	storage filestore.Storage
}

func NewService(db *gorm.DB, storage filestore.Storage) *Service {
	return &Service{db: db, storage: storage}
}

func RegisterRoutes(g *gin.RouterGroup, s *Service) {
	p := g.Group("/public")
	p.GET("/home", s.home)
	p.GET("/products", s.products)
	p.GET("/products/:productId", s.product)
	p.GET("/activities", s.activities)
	p.GET("/activities/:activityId", s.activity)
	p.GET("/service-contact", s.contact)
	p.GET("/media/:fileId", s.media)
}

func (s *Service) products(c *gin.Context) {
	p, n := paging(c)
	q := s.productQuery(c)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		fail(c, err)
		return
	}
	var rows []map[string]any
	order := "p.updated_at DESC"
	if c.Query("sortBy") == "priceAsc" {
		order = "price ASC,p.updated_at DESC"
	}
	if c.Query("sortBy") == "priceDesc" {
		order = "price DESC,p.updated_at DESC"
	}
	err := q.Select("CAST(p.id AS CHAR) AS productId,v.product_name AS productName,v.product_intro AS productIntro,v.product_type AS productType,CAST(v.cover_file_id AS CHAR) AS coverFileId,COALESCE((SELECT MIN(sk.sale_price) FROM product_sku sk WHERE sk.product_version_id=v.id AND sk.sku_status='ON_SALE'),0) AS price,EXISTS(SELECT 1 FROM product_sku sk WHERE sk.product_version_id=v.id AND sk.sku_status='ON_SALE' AND sk.stock_total>sk.stock_locked+sk.stock_sold) AS purchasable,p.updated_at AS publishedAt").Order(order).Offset((p - 1) * n).Limit(n).Find(&rows).Error
	if err != nil {
		fail(c, err)
		return
	}
	addMedia(rows, "coverFileId")
	httpx.Success(c, http.StatusOK, pageData(rows, p, n, total))
}

func (s *Service) productQuery(c *gin.Context) *gorm.DB {
	q := s.db.WithContext(c).Table("product p").Joins("JOIN product_version v ON v.id=p.current_version_id AND v.status='PUBLISHED'").Where("p.status='PUBLISHED'")
	if x := strings.TrimSpace(c.Query("keyword")); x != "" {
		q = q.Where("v.product_name LIKE ?", "%"+x+"%")
	}
	if x := strings.TrimSpace(c.Query("productType")); x != "" {
		q = q.Where("v.product_type=?", x)
	}
	if x := strings.TrimSpace(c.Query("regionCode")); x != "" {
		q = q.Where("p.sale_region_mode='NATIONWIDE' OR JSON_CONTAINS(COALESCE(v.sale_region_codes_json,JSON_ARRAY()),JSON_ARRAY(?))", x)
	}
	if x := strings.TrimSpace(c.Query("minPrice")); x != "" {
		q = q.Where("(SELECT MIN(sk.sale_price) FROM product_sku sk WHERE sk.product_version_id=v.id)>=?", x)
	}
	if x := strings.TrimSpace(c.Query("maxPrice")); x != "" {
		q = q.Where("(SELECT MIN(sk.sale_price) FROM product_sku sk WHERE sk.product_version_id=v.id)<=?", x)
	}
	return q
}

func (s *Service) product(c *gin.Context) {
	id, ok := parseID(c, "productId")
	if !ok {
		return
	}
	var row map[string]any
	err := s.db.WithContext(c).Table("product p").Joins("JOIN product_version v ON v.id=p.current_version_id AND v.status='PUBLISHED'").Where("p.id=? AND p.status='PUBLISHED'", id).Select("CAST(p.id AS CHAR) AS productId,v.product_name AS productName,v.product_intro AS productIntro,v.product_type AS productType,CAST(v.cover_file_id AS CHAR) AS coverFileId,v.detail_files_json AS detailFileIds,v.public_copyright_notice AS publicCopyrightNotice,v.freight_mode AS freightMode,v.freight_amount AS freightAmount,v.rule_snapshot AS afterSaleRule").Take(&row).Error
	if err != nil {
		missing(c, "PRODUCT_NOT_FOUND", "商品不存在或已下架")
		return
	}
	addMedia([]map[string]any{row}, "coverFileId")
	row["detailUrls"] = detailURLs(fmt.Sprint(row["detailFileIds"]))
	delete(row, "detailFileIds")
	var skus []map[string]any
	if err := s.db.WithContext(c).Table("product_sku sk").Joins("JOIN product p ON p.current_version_id=sk.product_version_id").Where("p.id=?", id).Select("CAST(sk.id AS CHAR) AS skuId,sk.sku_name AS skuName,sk.spec_json AS spec,sk.sale_price AS salePrice,CAST(sk.sku_file_id AS CHAR) AS skuFileId,sk.sku_status AS skuStatus,(sk.stock_total>sk.stock_locked+sk.stock_sold AND sk.sku_status='ON_SALE') AS purchasable").Find(&skus).Error; err != nil {
		fail(c, err)
		return
	}
	addMedia(skus, "skuFileId")
	row["skus"] = skus
	httpx.Success(c, http.StatusOK, row)
}

func (s *Service) activities(c *gin.Context) {
	p, n := paging(c)
	q := s.activityQuery(c)
	var total int64
	if err := q.Distinct("a.id").Count(&total).Error; err != nil {
		fail(c, err)
		return
	}
	var rows []map[string]any
	err := q.Select("CAST(a.id AS CHAR) AS activityId,a.status,v.activity_name AS activityName,v.activity_intro AS activityIntro,a.actual_city_code AS actualCityCode,CAST(v.cover_file_id AS CHAR) AS coverFileId,MIN(s.start_at) AS startingAt,MIN(t.price) AS price").Joins("LEFT JOIN activity_ticket_type t ON t.session_id=s.id AND t.status='ACTIVE'").Group("a.id,a.status,v.activity_name,v.activity_intro,a.actual_city_code,v.cover_file_id").Order("startingAt ASC").Offset((p - 1) * n).Limit(n).Find(&rows).Error
	if err != nil {
		fail(c, err)
		return
	}
	for _, row := range rows {
		paused := fmt.Sprint(row["status"]) == "BOOKING_PAUSED"
		row["bookingPaused"] = paused
		if paused {
			row["bookingStatus"] = "报名暂停"
		} else {
			row["bookingStatus"] = "报名中"
		}
		delete(row, "status")
	}
	addMedia(rows, "coverFileId")
	httpx.Success(c, http.StatusOK, pageData(rows, p, n, total))
}

func (s *Service) activityQuery(c *gin.Context) *gorm.DB {
	q := s.db.WithContext(c).Table("activity a").Joins("JOIN activity_version v ON v.id=a.current_version_id").Joins("JOIN activity_session s ON s.activity_version_id=v.id AND s.end_at>?", time.Now()).Where("a.status IN ?", []string{"PUBLISHED", "BOOKING_PAUSED"})
	if x := strings.TrimSpace(c.Query("keyword")); x != "" {
		q = q.Where("v.activity_name LIKE ?", "%"+x+"%")
	}
	if x := strings.TrimSpace(c.Query("cityCode")); x != "" {
		q = q.Where("a.actual_city_code=?", x)
	}
	if x := strings.TrimSpace(c.Query("dateFrom")); x != "" {
		q = q.Where("s.start_at>=?", x)
	}
	if x := strings.TrimSpace(c.Query("dateTo")); x != "" {
		q = q.Where("s.start_at<=?", x)
	}
	if x := strings.TrimSpace(c.Query("minPrice")); x != "" {
		q = q.Where("EXISTS (SELECT 1 FROM activity_ticket_type x WHERE x.session_id=s.id AND x.price>=?)", x)
	}
	if x := strings.TrimSpace(c.Query("maxPrice")); x != "" {
		q = q.Where("EXISTS (SELECT 1 FROM activity_ticket_type x WHERE x.session_id=s.id AND x.price<=?)", x)
	}
	return q
}

func (s *Service) activity(c *gin.Context) {
	id, ok := parseID(c, "activityId")
	if !ok {
		return
	}
	var row map[string]any
	err := s.db.WithContext(c).Table("activity a").Joins("JOIN activity_version v ON v.id=a.current_version_id").Where("a.id=? AND a.status IN ?", id, []string{"PUBLISHED", "BOOKING_PAUSED"}).Select("CAST(a.id AS CHAR) AS activityId,a.status,a.actual_city_code AS actualCityCode,v.activity_name AS activityName,v.activity_intro AS activityIntro,CAST(v.cover_file_id AS CHAR) AS coverFileId,v.venue_name AS venueName,v.map_point_name AS mapPointName,v.longitude,v.latitude,v.notice,v.public_copyright_notice AS publicCopyrightNotice,v.rule_snapshot AS refundRule").Take(&row).Error
	if err != nil {
		missing(c, "ACTIVITY_NOT_FOUND", "活动不存在或已结束")
		return
	}
	paused := fmt.Sprint(row["status"]) == "BOOKING_PAUSED"
	delete(row, "status")
	row["bookingPaused"] = paused
	addMedia([]map[string]any{row}, "coverFileId")
	var sessions []map[string]any
	if err := s.db.WithContext(c).Table("activity_session s").Joins("JOIN activity a ON a.current_version_id=s.activity_version_id").Where("a.id=? AND s.end_at>?", id, time.Now()).Select("CAST(s.id AS CHAR) AS sessionId,s.session_name AS sessionName,s.start_at AS startAt,s.end_at AS endAt,s.quota_total AS quotaTotal").Order("s.start_at").Find(&sessions).Error; err != nil {
		fail(c, err)
		return
	}
	for _, session := range sessions {
		var tickets []map[string]any
		if err := s.db.WithContext(c).Table("activity_ticket_type").Where("session_id=? AND status='ACTIVE'", session["sessionId"]).Select("CAST(id AS CHAR) AS ticketTypeId,ticket_code AS ticketTypeCode,ticket_name AS ticketName,charge_type AS chargeType,price,quota_total AS quotaTotal,(quota_total>quota_locked+quota_used) AS bookable").Find(&tickets).Error; err != nil {
			fail(c, err)
			return
		}
		session["tickets"] = tickets
	}
	row["sessions"] = sessions
	httpx.Success(c, http.StatusOK, row)
}

func (s *Service) home(c *gin.Context) {
	p := c.Request.URL.Query()
	p.Set("pageNo", "1")
	p.Set("pageSize", "6")
	c.Request.URL.RawQuery = p.Encode()
	var products []map[string]any
	if err := s.productQuery(c).Select("CAST(p.id AS CHAR) AS productId,v.product_name AS productName,v.product_intro AS productIntro,CAST(v.cover_file_id AS CHAR) AS coverFileId,COALESCE((SELECT MIN(sk.sale_price) FROM product_sku sk WHERE sk.product_version_id=v.id),0) AS price").Order("p.updated_at DESC").Limit(6).Find(&products).Error; err != nil {
		fail(c, err)
		return
	}
	addMedia(products, "coverFileId")
	var activities []map[string]any
	if err := s.activityQuery(c).Select("CAST(a.id AS CHAR) AS activityId,a.status,v.activity_name AS activityName,v.activity_intro AS activityIntro,CAST(v.cover_file_id AS CHAR) AS coverFileId,MIN(s.start_at) AS startingAt").Group("a.id,a.status,v.activity_name,v.activity_intro,v.cover_file_id").Order("MAX(a.updated_at) DESC").Limit(6).Find(&activities).Error; err != nil {
		fail(c, err)
		return
	}
	for _, v := range activities {
		v["bookingPaused"] = fmt.Sprint(v["status"]) == "BOOKING_PAUSED"
		delete(v, "status")
	}
	addMedia(activities, "coverFileId")
	httpx.Success(c, http.StatusOK, map[string]any{"products": products, "activities": activities})
}

func (s *Service) contact(c *gin.Context) {
	var rows []map[string]any
	if err := s.db.WithContext(c).Table("sys_config").Where("status='ACTIVE' AND is_secret=0 AND config_key IN ?", []string{"PLATFORM_SERVICE_PHONE", "PLATFORM_SERVICE_HOURS", "PLATFORM_SERVICE_DESCRIPTION"}).Select("config_key,config_value").Find(&rows).Error; err != nil {
		fail(c, err)
		return
	}
	out := map[string]any{"phone": "", "serviceHours": "", "description": ""}
	for _, row := range rows {
		if row["config_key"] == "PLATFORM_SERVICE_PHONE" {
			out["phone"] = row["config_value"]
		}
		if row["config_key"] == "PLATFORM_SERVICE_HOURS" {
			out["serviceHours"] = row["config_value"]
		}
		if row["config_key"] == "PLATFORM_SERVICE_DESCRIPTION" {
			out["description"] = row["config_value"]
		}
	}
	httpx.Success(c, http.StatusOK, out)
}

func (s *Service) media(c *gin.Context) {
	id, ok := parseID(c, "fileId")
	if !ok {
		return
	}
	var item map[string]any
	q := s.db.WithContext(c).Table("file_object f").Where("f.id=? AND f.status='ACTIVE' AND f.upload_status='SUCCESS' AND f.mime_type LIKE 'image/%'", id).Where("(EXISTS (SELECT 1 FROM product p JOIN product_version v ON v.id=p.current_version_id WHERE p.status='PUBLISHED' AND v.status='PUBLISHED' AND (v.cover_file_id=f.id OR JSON_CONTAINS(COALESCE(v.detail_files_json,JSON_ARRAY()),JSON_ARRAY(f.id)))) OR EXISTS (SELECT 1 FROM product p JOIN product_version v ON v.id=p.current_version_id JOIN product_sku sk ON sk.product_version_id=v.id WHERE p.status='PUBLISHED' AND v.status='PUBLISHED' AND sk.sku_file_id=f.id) OR EXISTS (SELECT 1 FROM activity a JOIN activity_version v ON v.id=a.current_version_id WHERE a.status IN ('PUBLISHED','BOOKING_PAUSED') AND v.cover_file_id=f.id))")
	if err := q.Select("f.storage_key,f.mime_type").Take(&item).Error; err != nil {
		missing(c, "PUBLIC_MEDIA_NOT_FOUND", "展示图片不存在或已下架")
		return
	}
	body, err := s.storage.Get(c, fmt.Sprint(item["storage_key"]))
	if err != nil {
		fail(c, err)
		return
	}
	defer body.Close()
	c.Header("Cache-Control", "public, max-age=300")
	c.DataFromReader(http.StatusOK, -1, fmt.Sprint(item["mime_type"]), body, nil)
}

func paging(c *gin.Context) (int, int) {
	p, _ := strconv.Atoi(c.DefaultQuery("pageNo", "1"))
	n, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if p < 1 {
		p = 1
	}
	if n < 1 || n > 100 {
		n = 20
	}
	return p, n
}
func pageData(items []map[string]any, p, n int, total int64) map[string]any {
	return map[string]any{"items": items, "pageNo": p, "pageSize": n, "total": total, "totalPages": (total + int64(n) - 1) / int64(n)}
}
func parseID(c *gin.Context, key string) (uint64, bool) {
	id, e := strconv.ParseUint(c.Param(key), 10, 64)
	if e != nil || id == 0 {
		httpx.AbortWithError(c, httpx.NewError(http.StatusBadRequest, "VALIDATION_FORMAT_INVALID", "ID格式错误"))
		return 0, false
	}
	return id, true
}
func addMedia(rows []map[string]any, key string) {
	for _, row := range rows {
		if id := fmt.Sprint(row[key]); id != "" && id != "<nil>" && id != "0" {
			row[strings.TrimSuffix(key, "FileId")+"Url"] = "/api/v1/public/media/" + id
		}
		delete(row, key)
	}
}
func detailURLs(raw string) []string {
	var ids []uint64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			out = append(out, "/api/v1/public/media/"+fmt.Sprint(id))
		}
	}
	return out
}
func missing(c *gin.Context, code, msg string) {
	httpx.AbortWithError(c, httpx.NewError(http.StatusNotFound, code, msg))
}
func fail(c *gin.Context, err error) {
	httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "公开内容查询失败"))
}

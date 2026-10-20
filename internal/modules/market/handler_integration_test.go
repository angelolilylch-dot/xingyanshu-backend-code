package market

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/platform/idgen"
)

// TestIPMallListHTTPWithMySQL 模拟创作者从浏览器访问商城：写入完整的上架测试数据后，
// 通过真实 Gin 路由传入筛选参数，并校验统一响应、字段和值均符合前端契约。
func TestIPMallListHTTPWithMySQL(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1 to run MySQL HTTP integration test")
	}
	root := filepath.Join("..", "..", "..")
	cfg, err := config.Load(filepath.Join(root, "config", "test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.OpenMySQLConfig(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	suffix := time.Now().UnixNano()
	cityID, creatorID := idgen.Next(), idgen.Next()
	planID, planVersionID := idgen.Next(), idgen.Next()
	ipID, bindingID := idgen.Next(), idgen.Next()
	defer cleanupMallFixture(db, cityID, creatorID, planID, planVersionID, ipID, bindingID)

	mustMallExec(t, db.Table("sys_org").Create(map[string]any{
		"id": cityID, "status": "ACTIVE", "version": 1, "org_code": fmt.Sprintf("MALL_CITY_%d", suffix),
		"org_name": "商城接口测试城市", "org_type": "CITY", "city_code": "359999",
	}).Error)
	mustMallExec(t, db.Table("creator_profile").Create(map[string]any{
		"id": creatorID, "user_id": idgen.Next(), "status": "ACTIVE", "version": 1,
		"subject_type": "PERSONAL", "display_name": "商城测试创作者", "identity_status": "VERIFIED", "letter_status": "NOT_REQUIRED",
	}).Error)
	mustMallExec(t, db.Table("auth_plan").Create(map[string]any{
		"id": planID, "status": "ENABLED", "version": 1, "plan_no": fmt.Sprintf("MALL_PLAN_%d", suffix),
		"plan_name": "商城接口测试授权方案", "current_version_id": planVersionID,
	}).Error)
	mustMallExec(t, db.Table("auth_plan_version").Create(map[string]any{
		"id": planVersionID, "status": "ENABLED", "version": 1, "plan_id": planID, "version_no": 1,
		"authorization_mode": "EXCLUSIVE_BUYOUT", "term_type": "MONTH", "term_value": 12, "region_mode": "NATIONWIDE",
		"scenes_json": "[\"PRODUCT_SALE\"]", "content_type": "PRODUCT_ONLY", "quota_total": 1,
	}).Error)
	mustMallExec(t, db.Table("ip_asset").Create(map[string]any{
		"id": ipID, "status": "LISTED", "version": 1, "ip_no": fmt.Sprintf("MALL-IP-%d", suffix),
		"ip_name": "福文化测试 IP", "ip_type": "CULTURAL", "source_type": "CITY_COLLECTION", "main_city_org_id": cityID,
		"short_intro": "用于商城真实接口测试", "detail_content": "用于商城真实接口测试的完整介绍", "culture_story": "福文化故事",
		"ip_feature": "民俗文化", "creative_directions_json": "[\"文创商品\"]", "applicable_product_types_json": "[\"CULTURAL_PRODUCT\"]",
		"applicable_activity_types_json": "[\"EXHIBITION\"]", "culture_tag_codes_json": "[\"CULTURE_FOLK\"]",
		"public_copyright_notice": "测试版权说明", "purchase_notice": "测试购买须知", "authorization_mode": "EXCLUSIVE_BUYOUT",
		"mall_visible": true, "suggested_transaction_price_amount": "199.00",
	}).Error)
	mustMallExec(t, db.Table("ip_auth_binding").Create(map[string]any{
		"id": bindingID, "status": "ACTIVE", "version": 1, "ip_id": ipID, "auth_plan_version_id": planVersionID,
		"distribution_plan_id": idgen.Next(), "distribution_version_no": 1, "confirmed_by": 1, "confirmed_at": time.Now(),
	}).Error)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1"), db, func(c *gin.Context) {
		c.Set("creators.current", creators.CurrentCreator{CreatorID: creatorID})
		c.Next()
	})
	listURL := "/api/v1/creator/ip-mall?regionCode=359999&ipType=CULTURAL&authorizationMode=EXCLUSIVE_BUYOUT&contentType=PRODUCT_ONLY&term=12&tagCodes=CULTURE_FOLK&minPrice=100&maxPrice=200&sortBy=priceDesc&pageNo=1&pageSize=10"
	req := httptest.NewRequest(http.MethodGet, listURL, nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /creator/ip-mall status=%d, body=%s", resp.Code, resp.Body.String())
	}
	var body struct {
		Code string `json:"code"`
		Data struct {
			Items []struct {
				IPID        string `json:"ipId"`
				IPName      string `json:"ipName"`
				Price       string `json:"price"`
				TermValue   int    `json:"termValue"`
				ContentType string `json:"contentType"`
				CanPurchase bool   `json:"canPurchase"`
			} `json:"items"`
			PageNo     int   `json:"pageNo"`
			PageSize   int   `json:"pageSize"`
			Total      int64 `json:"total"`
			TotalPages int64 `json:"totalPages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode mall response: %v, body=%s", err, resp.Body.String())
	}
	if body.Code != "OK" || body.Data.Total != 1 || body.Data.PageNo != 1 || body.Data.PageSize != 10 || body.Data.TotalPages != 1 || len(body.Data.Items) != 1 {
		t.Fatalf("unexpected mall list response: %#v", body)
	}
	item := body.Data.Items[0]
	if item.IPID != fmt.Sprint(ipID) || item.IPName != "福文化测试 IP" || item.Price != "199.00" || item.TermValue != 12 || item.ContentType != "PRODUCT_ONLY" || !item.CanPurchase {
		t.Fatalf("unexpected mall item: %#v", item)
	}

	assertPurchasePreview(t, router, ipID, true, "")
	mustMallExec(t, db.Table("creator_authorization").Create(map[string]any{
		"id": idgen.Next(), "status": "ACTIVE", "version": 1, "authorization_no": fmt.Sprintf("MALL_AUTH_%d", suffix),
		"creator_id": idgen.Next(), "ip_id": ipID, "auth_order_id": idgen.Next(), "authorization_mode": "EXCLUSIVE_BUYOUT",
		"start_at": time.Now(), "end_at": time.Now().AddDate(1, 0, 0), "active_flag": true,
	}).Error)

	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, listURL, nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /creator/ip-mall after exclusive sale status=%d, body=%s", resp.Code, resp.Body.String())
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode sold-out mall response: %v, body=%s", err, resp.Body.String())
	}
	if len(body.Data.Items) != 1 || body.Data.Items[0].CanPurchase {
		t.Fatalf("exclusive buyout must be unavailable after sale: %#v", body.Data.Items)
	}
	assertPurchasePreview(t, router, ipID, false, "AUTHORIZATION_QUOTA_SOLD_OUT")
}

func assertPurchasePreview(t *testing.T, router *gin.Engine, ipID uint64, wantCanPurchase bool, wantBlockCode string) {
	t.Helper()
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/creator/ip-mall/%d/purchase-preview", ipID), nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("GET purchase-preview status=%d, body=%s", resp.Code, resp.Body.String())
	}
	var body struct {
		Code string `json:"code"`
		Data struct {
			CanPurchase bool   `json:"canPurchase"`
			BlockCode   string `json:"blockCode"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode purchase-preview response: %v, body=%s", err, resp.Body.String())
	}
	if body.Code != "OK" || body.Data.CanPurchase != wantCanPurchase || body.Data.BlockCode != wantBlockCode {
		t.Fatalf("unexpected purchase-preview response: %#v", body)
	}
}

func mustMallExec(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func cleanupMallFixture(db *gorm.DB, cityID, creatorID, planID, planVersionID, ipID, bindingID uint64) {
	db.Exec("DELETE FROM ip_auth_binding WHERE id = ?", bindingID)
	db.Exec("DELETE FROM creator_authorization WHERE ip_id = ?", ipID)
	db.Exec("DELETE FROM ip_asset WHERE id = ?", ipID)
	db.Exec("DELETE FROM auth_plan_version WHERE id = ?", planVersionID)
	db.Exec("DELETE FROM auth_plan WHERE id = ?", planID)
	db.Exec("DELETE FROM creator_profile WHERE id = ?", creatorID)
	db.Exec("DELETE FROM sys_org WHERE id = ?", cityID)
}

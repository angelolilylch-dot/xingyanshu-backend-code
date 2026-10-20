package activities

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/contentmoderation"
	"xingyanshu-backend/internal/platform/idgen"
)

type Service struct {
	db         *gorm.DB
	moderation contentmoderation.Provider
}

func NewService(db *gorm.DB, moderation contentmoderation.Provider) *Service {
	return &Service{db: db, moderation: moderation}
}

type Ticket struct {
	TicketTypeCode string `json:"ticketTypeCode" binding:"required"`
	ChargeType     string `json:"chargeType" binding:"required"`
	Price          string `json:"price" binding:"required"`
	QuotaTotal     uint   `json:"quotaTotal" binding:"required"`
}
type Session struct {
	SessionNo   string    `json:"sessionNo" binding:"required,max=64"`
	SessionName string    `json:"sessionName" binding:"required,max=200"`
	StartAt     time.Time `json:"startAt" binding:"required"`
	EndAt       time.Time `json:"endAt" binding:"required"`
	QuotaTotal  uint      `json:"quotaTotal" binding:"required"`
	Tickets     []Ticket  `json:"tickets" binding:"required,min=1"`
}
type Input struct {
	AuthorizationID       uint64    `json:"authorizationId" binding:"required"`
	ActivityName          string    `json:"activityName" binding:"required,max=200"`
	ActivityIntro         string    `json:"activityIntro"`
	CoverFileID           uint64    `json:"coverFileId" binding:"required"`
	VenueName             string    `json:"venueName" binding:"required,max=256"`
	MapPointName          string    `json:"mapPointName" binding:"required,max=256"`
	Longitude             string    `json:"longitude" binding:"required"`
	Latitude              string    `json:"latitude" binding:"required"`
	ActualCityCode        string    `json:"actualCityCode" binding:"required"`
	Notice                string    `json:"notice"`
	PublicCopyrightNotice string    `json:"publicCopyrightNotice" binding:"required"`
	RefundTemplateID      uint64    `json:"refundTemplateId" binding:"required"`
	Sessions              []Session `json:"sessions" binding:"required,min=1"`
}
type ListInput struct {
	Keyword  string
	IPID     uint64
	CityCode string
	Status   string
	PageNo   int
	PageSize int
}

func (s *Service) Create(ctx context.Context, current creators.CurrentCreator, input Input) (map[string]any, string, error) {
	if !valid(input) {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	activityID := idgen.Next()
	detectionText := strings.Join([]string{input.ActivityName, input.ActivityIntro, input.VenueName, input.MapPointName, input.Notice, input.PublicCopyrightNotice}, "\n")
	detection, detectionErr := s.moderation.Moderate(ctx, contentmoderation.Request{Text: detectionText, DataID: fmt.Sprintf("activity-%d-%d", activityID, idgen.Next()), UserID: fmt.Sprint(current.UserID), Language: "zh"})
	status, check := "PUBLISHED", "PASS"
	if detectionErr != nil {
		status, check = "CHECK_ERROR", "ERROR"
	} else if detection.Decision != contentmoderation.DecisionPass {
		status, check = "CHECK_REJECTED", "BLOCK"
	}
	var output map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		auth, e := loadAuthorization(tx, current.CreatorID, input.AuthorizationID)
		if e != nil {
			return e
		}
		if !auth.allowsActivity(input.ActualCityCode) {
			return business("ACTIVITY_AUTHORIZATION_NOT_ALLOWED")
		}
		if e := activeFile(tx, input.CoverFileID); e != nil {
			return e
		}
		var tmpl struct {
			ID         uint64
			BizType    string `gorm:"column:biz_type"`
			Conditions string `gorm:"column:conditions_json"`
			RefundRule string `gorm:"column:refund_rule_json"`
		}
		if e := tx.Table("after_sale_rule_template").Where("id=? AND enabled=1", input.RefundTemplateID).Take(&tmpl).Error; e != nil || tmpl.BizType != "ACTIVITY" {
			return business("ACTIVITY_REFUND_TEMPLATE_NOT_FOUND")
		}
		if e := s.validTicketTypes(tx, input.Sessions); e != nil {
			return e
		}
		now, versionID := time.Now(), idgen.Next()
		if e := tx.Table("activity").Create(map[string]any{"id": activityID, "activity_no": fmt.Sprintf("ACTIVITY-%d", activityID), "creator_id": current.CreatorID, "ip_id": auth.IPID, "authorization_id": auth.ID, "actual_city_code": input.ActualCityCode, "current_version_id": versionID, "status": status, "version": 1, "created_by": current.UserID, "updated_by": current.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}).Error; e != nil {
			return e
		}
		rule, _ := json.Marshal(map[string]any{"templateId": tmpl.ID, "conditions": tmpl.Conditions, "refundRule": tmpl.RefundRule})
		if e := tx.Table("activity_version").Create(map[string]any{"id": versionID, "activity_id": activityID, "version_no": 1, "status": status, "activity_name": input.ActivityName, "activity_intro": blank(input.ActivityIntro), "cover_file_id": input.CoverFileID, "venue_name": input.VenueName, "map_point_name": input.MapPointName, "longitude": input.Longitude, "latitude": input.Latitude, "notice": blank(input.Notice), "public_copyright_notice": input.PublicCopyrightNotice, "refund_template_id": tmpl.ID, "rule_snapshot": string(rule), "check_status": check, "version": 1, "created_by": current.UserID, "updated_by": current.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}).Error; e != nil {
			return e
		}
		for _, session := range input.Sessions {
			sessionID := idgen.Next()
			if e := tx.Table("activity_session").Create(map[string]any{"id": sessionID, "activity_version_id": versionID, "session_no": session.SessionNo, "session_name": session.SessionName, "start_at": session.StartAt, "end_at": session.EndAt, "quota_total": session.QuotaTotal, "status": status, "version": 1, "created_by": current.UserID, "updated_by": current.UserID}).Error; e != nil {
				return e
			}
			for _, ticket := range session.Tickets {
				name := ticket.TicketTypeCode
				if e := tx.Table("sys_dict_item").Select("dict_name").Where("dict_type='ACTIVITY_TICKET_TYPE' AND dict_code=?", ticket.TicketTypeCode).Take(&name).Error; e != nil {
					return e
				}
				if e := tx.Table("activity_ticket_type").Create(map[string]any{"id": idgen.Next(), "session_id": sessionID, "ticket_code": ticket.TicketTypeCode, "ticket_name": name, "charge_type": ticket.ChargeType, "price": ticket.Price, "quota_total": ticket.QuotaTotal, "status": status, "version": 1, "created_by": current.UserID, "updated_by": current.UserID}).Error; e != nil {
					return e
				}
			}
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(detectionText)))
		record := map[string]any{"id": idgen.Next(), "status": check, "detection_no": fmt.Sprintf("DETECT-%d", idgen.Next()), "object_type": "ACTIVITY", "object_id": activityID, "text_hash": hash, "provider_code": detection.Provider, "provider_request_no": blank(detection.ProviderRequestNo), "detection_status": check, "risk_category": blank(detection.Label), "created_by": current.UserID}
		if detectionErr != nil {
			record["error_summary"] = detectionErr.Error()
		}
		if e := tx.Table("content_detection_record").Create(record).Error; e != nil {
			return e
		}
		if status == "PUBLISHED" {
			snapshot, _ := json.Marshal(map[string]any{"authorizationId": fmt.Sprint(auth.ID), "checkStatus": check})
			if e := tx.Table("content_publish_record").Create(map[string]any{"id": idgen.Next(), "status": "PUBLISHED", "object_type": "ACTIVITY", "object_id": activityID, "version_no": 1, "check_snapshot": string(snapshot), "published_at": now, "created_by": current.UserID, "updated_by": current.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}).Error; e != nil {
				return e
			}
		}
		output = map[string]any{"activityId": fmt.Sprint(activityID), "activityVersionId": fmt.Sprint(versionID), "status": status, "checkStatus": check}
		return audit.WriteStatusHistory(tx, "ACTIVITY", activityID, "", status, "创作者提交活动", audit.Operator{Type: "USER", ID: current.UserID})
	})
	if err != nil {
		return nil, businessCode(err), unwrap(err)
	}
	return output, "", nil
}

func (s *Service) validTicketTypes(tx *gorm.DB, sessions []Session) error {
	for _, session := range sessions {
		seen := map[string]bool{}
		var sum uint
		for _, ticket := range session.Tickets {
			if seen[ticket.TicketTypeCode] || ticket.QuotaTotal == 0 {
				return business("VALIDATION_FORMAT_INVALID")
			}
			seen[ticket.TicketTypeCode] = true
			sum += ticket.QuotaTotal
			var count int64
			if e := tx.Table("sys_dict_item").Where("dict_type='ACTIVITY_TICKET_TYPE' AND dict_code=? AND enabled=1", ticket.TicketTypeCode).Count(&count).Error; e != nil || count != 1 {
				return business("ACTIVITY_TICKET_TYPE_NOT_FOUND")
			}
			price, e := strconv.ParseFloat(ticket.Price, 64)
			if e != nil || (ticket.ChargeType == "FREE" && price != 0) || (ticket.ChargeType == "PAID" && price <= 0) || (ticket.ChargeType != "FREE" && ticket.ChargeType != "PAID") {
				return business("VALIDATION_FORMAT_INVALID")
			}
		}
		if sum > session.QuotaTotal {
			return business("ACTIVITY_TICKET_QUOTA_EXCEEDS_SESSION")
		}
	}
	return nil
}

func (s *Service) List(ctx context.Context, creatorID uint64, input ListInput) (map[string]any, string, error) {
	if input.PageNo < 1 {
		input.PageNo = 1
	}
	if input.PageSize < 1 {
		input.PageSize = 20
	}
	if input.PageSize > 100 {
		input.PageSize = 100
	}
	q := s.db.WithContext(ctx).Table("activity a").Select("a.id,a.activity_no,a.status,a.actual_city_code,a.current_version_id,a.created_at,v.activity_name,v.venue_name").Joins("LEFT JOIN activity_version v ON v.id=a.current_version_id").Where("a.creator_id=?", creatorID)
	if input.IPID > 0 {
		q = q.Where("a.ip_id=?", input.IPID)
	}
	if input.CityCode != "" {
		q = q.Where("a.actual_city_code=?", input.CityCode)
	}
	if input.Status != "" {
		q = q.Where("a.status=?", input.Status)
	}
	if keyword := strings.TrimSpace(input.Keyword); keyword != "" {
		q = q.Where("a.activity_no LIKE ? OR v.activity_name LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	var total int64
	if e := q.Count(&total).Error; e != nil {
		return nil, "", e
	}
	var rows []map[string]any
	e := q.Order("a.id DESC").Offset((input.PageNo - 1) * input.PageSize).Limit(input.PageSize).Find(&rows).Error
	return map[string]any{"items": rows, "pageNo": input.PageNo, "pageSize": input.PageSize, "total": total}, "", e
}

func (s *Service) Get(ctx context.Context, creatorID, activityID uint64) (map[string]any, string, error) {
	var activity map[string]any
	if e := s.db.WithContext(ctx).Table("activity").Where("id=? AND creator_id=?", activityID, creatorID).Take(&activity).Error; e != nil {
		return nil, "ACTIVITY_NOT_FOUND", nil
	}
	var versions, sessions, tickets []map[string]any
	s.db.WithContext(ctx).Table("activity_version").Where("activity_id=?", activityID).Order("version_no DESC").Find(&versions)
	s.db.WithContext(ctx).Table("activity_session").Where("activity_version_id=?", activity["current_version_id"]).Find(&sessions)
	ids := []uint64{}
	for _, row := range sessions {
		if id, e := strconv.ParseUint(fmt.Sprint(row["id"]), 10, 64); e == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 {
		s.db.WithContext(ctx).Table("activity_ticket_type").Where("session_id IN ?", ids).Find(&tickets)
	}
	return map[string]any{"activity": activity, "versions": versions, "sessions": sessions, "tickets": tickets}, "", nil
}

func (s *Service) ChangeBooking(ctx context.Context, current creators.CurrentCreator, activityID uint64, resume bool) (map[string]any, string, error) {
	target := "BOOKING_PAUSED"
	if resume {
		target = "PUBLISHED"
	}
	var out map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			ID, AuthorizationID uint64
			Status              string
			ActualCityCode      string `gorm:"column:actual_city_code"`
		}
		if e := tx.Table("activity").Where("id=? AND creator_id=?", activityID, current.CreatorID).Take(&row).Error; e != nil {
			return business("ACTIVITY_NOT_FOUND")
		}
		if (!resume && row.Status != "PUBLISHED") || (resume && row.Status != "BOOKING_PAUSED") {
			return business("ACTIVITY_STATUS_INVALID")
		}
		if resume {
			auth, e := loadAuthorization(tx, current.CreatorID, row.AuthorizationID)
			if e != nil {
				return e
			}
			if !auth.allowsActivity(row.ActualCityCode) {
				return business("ACTIVITY_AUTHORIZATION_NOT_ALLOWED")
			}
		}
		now := time.Now()
		if e := tx.Table("activity").Where("id=?", activityID).Updates(map[string]any{"status": target, "updated_by": current.UserID, "updated_at": now}).Error; e != nil {
			return e
		}
		if e := tx.Table("activity_version").Where("id=(SELECT current_version_id FROM activity WHERE id=?)", activityID).Updates(map[string]any{"status": target, "updated_by": current.UserID, "updated_at": now}).Error; e != nil {
			return e
		}
		out = map[string]any{"activityId": fmt.Sprint(activityID), "status": target}
		return audit.WriteStatusHistory(tx, "ACTIVITY", activityID, row.Status, target, "活动报名状态调整", audit.Operator{Type: "USER", ID: current.UserID})
	})
	if err != nil {
		return nil, businessCode(err), unwrap(err)
	}
	return out, "", nil
}

func (s *Service) Delist(ctx context.Context, current creators.CurrentCreator, activityID uint64) (map[string]any, string, error) {
	var out map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct{ Status string }
		if e := tx.Table("activity").Where("id=? AND creator_id=?", activityID, current.CreatorID).Take(&row).Error; e != nil {
			return business("ACTIVITY_NOT_FOUND")
		}
		if row.Status != "PUBLISHED" {
			return business("ACTIVITY_STATUS_INVALID")
		}
		var bookings int64
		if e := tx.Table("activity_booking").Where("activity_id=? AND status IN ?", activityID, []string{"PENDING_VERIFICATION", "VERIFIED"}).Count(&bookings).Error; e != nil {
			return e
		}
		if bookings > 0 {
			return business("ACTIVITY_HAS_SUCCESSFUL_BOOKINGS")
		}
		now := time.Now()
		tx.Table("activity").Where("id=?", activityID).Updates(map[string]any{"status": "DELISTED", "updated_by": current.UserID, "updated_at": now})
		tx.Table("activity_version").Where("id=(SELECT current_version_id FROM activity WHERE id=?)", activityID).Updates(map[string]any{"status": "DELISTED", "updated_by": current.UserID, "updated_at": now})
		out = map[string]any{"activityId": fmt.Sprint(activityID), "status": "DELISTED"}
		return audit.WriteStatusHistory(tx, "ACTIVITY", activityID, "PUBLISHED", "DELISTED", "创作者下架活动", audit.Operator{Type: "USER", ID: current.UserID})
	})
	if err != nil {
		return nil, businessCode(err), unwrap(err)
	}
	return out, "", nil
}

// CloseEnded 将已结束的活动从报名中/暂停报名转为 ENDED；可由 worker 安全重复调用。
func (s *Service) CloseEnded(ctx context.Context) (int64, error) {
	now := time.Now()
	var ids []uint64
	if e := s.db.WithContext(ctx).Table("activity a").Where("a.status IN ? AND NOT EXISTS (SELECT 1 FROM activity_session s WHERE s.activity_version_id=a.current_version_id AND s.end_at>?)", []string{"PUBLISHED", "BOOKING_PAUSED"}, now).Pluck("a.id", &ids).Error; e != nil || len(ids) == 0 {
		return int64(len(ids)), e
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Table("activity").Where("id IN ?", ids).Updates(map[string]any{"status": "ENDED", "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM", "status_changed_by_id": "activity-end-worker"}).Error; e != nil {
			return e
		}
		if e := tx.Table("activity_version").Where("activity_id IN ? AND id IN (SELECT current_version_id FROM activity WHERE id IN ?)", ids, ids).Updates(map[string]any{"status": "ENDED", "updated_at": now}).Error; e != nil {
			return e
		}
		return tx.Table("activity_session").Where("activity_version_id IN (SELECT current_version_id FROM activity WHERE id IN ?)", ids).Updates(map[string]any{"status": "ENDED", "updated_at": now}).Error
	})
	return int64(len(ids)), err
}

type authorization struct {
	ID          uint64
	IPID        uint64 `gorm:"column:ip_id"`
	Status      string
	ContentType string `gorm:"column:content_type_snapshot"`
	EndAt       time.Time
	ScopeJSON   []byte `gorm:"column:scope_json"`
	Commercial  bool   `gorm:"column:commercial_allowed_snapshot"`
}

func loadAuthorization(tx *gorm.DB, creatorID, authorizationID uint64) (authorization, error) {
	var row authorization
	e := tx.Table("creator_authorization ca").Select("ca.id,ca.ip_id,ca.status,ca.end_at,ao.commercial_allowed_snapshot,ao.content_type_snapshot,ass.scope_json").Joins("JOIN auth_order ao ON ao.id=ca.auth_order_id").Joins("JOIN auth_scope_snapshot ass ON ass.authorization_id=ca.id").Where("ca.id=? AND ca.creator_id=?", authorizationID, creatorID).Take(&row).Error
	if e != nil {
		return row, business("AUTHORIZATION_NOT_FOUND")
	}
	return row, nil
}
func (a authorization) allowsActivity(city string) bool {
	if a.Status != "ACTIVE" || !a.EndAt.After(time.Now()) || !a.Commercial || !(a.ContentType == "ACTIVITY_ONLY" || a.ContentType == "PRODUCT_AND_ACTIVITY") {
		return false
	}
	var scope struct {
		RegionMode  string   `json:"regionMode"`
		RegionCodes []string `json:"regionCodes"`
		Scenes      []string `json:"scenes"`
	}
	if json.Unmarshal(a.ScopeJSON, &scope) != nil || !contains(scope.Scenes, "ACTIVITY") {
		return false
	}
	return scope.RegionMode == "NATIONWIDE" || contains(scope.RegionCodes, city)
}
func activeFile(tx *gorm.DB, id uint64) error {
	var n int64
	tx.Table("file_object").Where("id=? AND status='ACTIVE' AND upload_status='SUCCESS'", id).Count(&n)
	if n != 1 {
		return business("FILE_NOT_FOUND")
	}
	return nil
}
func valid(i Input) bool {
	if i.ActualCityCode == "" || i.ActualCityCode == "000000" || len(i.Sessions) == 0 {
		return false
	}
	lon, e1 := strconv.ParseFloat(i.Longitude, 64)
	lat, e2 := strconv.ParseFloat(i.Latitude, 64)
	if e1 != nil || e2 != nil || lon < -180 || lon > 180 || lat < -90 || lat > 90 {
		return false
	}
	now := time.Now()
	seen := map[string]bool{}
	for _, s := range i.Sessions {
		if seen[s.SessionNo] || s.SessionName == "" || s.QuotaTotal == 0 || !s.EndAt.After(s.StartAt) || !s.StartAt.After(now) || len(s.Tickets) == 0 {
			return false
		}
		seen[s.SessionNo] = true
	}
	return true
}
func contains(items []string, target string) bool {
	for _, x := range items {
		if x == target {
			return true
		}
	}
	return false
}
func blank(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

type coded struct{ code string }

func (e coded) Error() string    { return e.code }
func business(code string) error { return coded{code} }
func businessCode(e error) string {
	if x, ok := e.(coded); ok {
		return x.code
	}
	return "INTERNAL_ERROR"
}
func unwrap(e error) error {
	if _, ok := e.(coded); ok {
		return nil
	}
	return e
}

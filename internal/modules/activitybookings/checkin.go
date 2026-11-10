package activitybookings

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/skip2/go-qrcode"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/idgen"
)

const checkinOpenAhead = 2 * time.Hour

type CheckinConfirmInput struct {
	ActivityBookingID string           `json:"activityBookingId" binding:"required"`
	Location          *CheckinLocation `json:"location"`
}

// CheckinLocation is optional evidence supplied by the browser. It is audit
// information only: a user whose browser denies location permission is not
// rejected solely for that reason.
type CheckinLocation struct {
	Longitude  float64 `json:"longitude"`
	Latitude   float64 `json:"latitude"`
	Accuracy   float64 `json:"accuracy"`
	CapturedAt string  `json:"capturedAt"`
}

type activityCheckinQRCode struct {
	ID         uint64
	QRCodeNo   string `gorm:"column:qrcode_no"`
	ActivityID uint64 `gorm:"column:activity_id"`
	EntryCode  string `gorm:"column:entry_code"`
	Status     string
	ScanCount  uint64    `gorm:"column:scan_count"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at"`
}

type checkinCandidate struct {
	BookingID          uint64    `gorm:"column:booking_id"`
	BookingNo          string    `gorm:"column:booking_no"`
	SessionID          uint64    `gorm:"column:session_id"`
	SessionName        string    `gorm:"column:session_name"`
	TicketName         string    `gorm:"column:ticket_name"`
	Quantity           int       `gorm:"column:quantity"`
	StartAt            time.Time `gorm:"column:start_at"`
	EndAt              time.Time `gorm:"column:end_at"`
	VerificationCodeID uint64    `gorm:"column:verification_code_id"`
}

func randomEntryCode() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Service) checkinH5URL(code string) (string, error) {
	base := strings.TrimSpace(s.activity.CheckinH5URL)
	if base == "" {
		return "", fmt.Errorf("activity check-in H5 URL is empty")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid activity check-in H5 URL")
	}
	q := u.Query()
	q.Set("code", code)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *Service) qrResponse(row activityCheckinQRCode) (gin.H, error) {
	entryURL, err := s.checkinH5URL(row.EntryCode)
	if err != nil {
		return nil, err
	}
	return gin.H{
		"qrcodeId": fmt.Sprint(row.ID), "qrcodeNo": row.QRCodeNo,
		"activityId": fmt.Sprint(row.ActivityID), "entryCode": row.EntryCode,
		"checkinURL": entryURL, "status": row.Status, "scanCount": row.ScanCount,
		"createdAt": row.CreatedAt, "updatedAt": row.UpdatedAt,
	}, nil
}

func (s *Service) createActivityQRCode(tx *gorm.DB, activityID, creatorID, userID uint64) (activityCheckinQRCode, string, error) {
	var exists int64
	if err := tx.Table("activity").Where("id=? AND creator_id=?", activityID, creatorID).Count(&exists).Error; err != nil {
		return activityCheckinQRCode{}, "", err
	}
	if exists != 1 {
		return activityCheckinQRCode{}, "ACTIVITY_NOT_FOUND", nil
	}
	var row activityCheckinQRCode
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("activity_checkin_qrcode").Where("activity_id=? AND status IN ?", activityID, []string{"ACTIVE", "PAUSED"}).Order("created_at DESC").Take(&row).Error
	if err == nil {
		return row, "", nil
	}
	if err != gorm.ErrRecordNotFound {
		return activityCheckinQRCode{}, "", err
	}
	code, err := randomEntryCode()
	if err != nil {
		return activityCheckinQRCode{}, "", err
	}
	now := time.Now()
	id := idgen.Next()
	row = activityCheckinQRCode{ID: id, QRCodeNo: fmt.Sprintf("ACTQR-%d", id), ActivityID: activityID, EntryCode: code, Status: "ACTIVE", CreatedAt: now, UpdatedAt: now}
	err = tx.Table("activity_checkin_qrcode").Create(map[string]any{
		"id": id, "status": row.Status, "version": 1, "qrcode_no": row.QRCodeNo,
		"activity_id": activityID, "entry_code": code, "created_by": userID, "updated_by": userID,
		"status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(userID),
	}).Error
	return row, "", err
}

func (s *Service) CreateOrGetActivityQRCode(ctx context.Context, current creators.CurrentCreator, activityID uint64) (gin.H, string, error) {
	var row activityCheckinQRCode
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		row, code, err = s.createActivityQRCode(tx, activityID, current.CreatorID, current.UserID)
		return err
	})
	if err != nil || code != "" {
		return nil, code, err
	}
	result, err := s.qrResponse(row)
	return result, "", err
}

func (s *Service) GetActivityQRCode(ctx context.Context, current creators.CurrentCreator, activityID uint64) (gin.H, string, error) {
	var row activityCheckinQRCode
	err := s.db.WithContext(ctx).Table("activity_checkin_qrcode q").Joins("JOIN activity a ON a.id=q.activity_id").Where("q.activity_id=? AND a.creator_id=? AND q.status IN ?", activityID, current.CreatorID, []string{"ACTIVE", "PAUSED"}).Order("q.created_at DESC").Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, "ACTIVITY_CHECKIN_QRCODE_NOT_FOUND", nil
	}
	if err != nil {
		return nil, "", err
	}
	result, err := s.qrResponse(row)
	return result, "", err
}

func (s *Service) RotateActivityQRCode(ctx context.Context, current creators.CurrentCreator, activityID uint64) (gin.H, string, error) {
	var row activityCheckinQRCode
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old activityCheckinQRCode
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("activity_checkin_qrcode q").Joins("JOIN activity a ON a.id=q.activity_id").Where("q.activity_id=? AND a.creator_id=? AND q.status IN ?", activityID, current.CreatorID, []string{"ACTIVE", "PAUSED"}).Order("q.created_at DESC").Take(&old).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				code = "ACTIVITY_CHECKIN_QRCODE_NOT_FOUND"
				return nil
			}
			return err
		}
		now := time.Now()
		if err := tx.Table("activity_checkin_qrcode").Where("id=?", old.ID).Updates(map[string]any{"status": "REVOKED", "updated_by": current.UserID, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}).Error; err != nil {
			return err
		}
		var err error
		row, code, err = s.createActivityQRCode(tx, activityID, current.CreatorID, current.UserID)
		return err
	})
	if err != nil || code != "" {
		return nil, code, err
	}
	result, err := s.qrResponse(row)
	if err == nil {
		result["rotated"] = true
	}
	return result, "", err
}

func (s *Service) DownloadActivityQRCode(ctx context.Context, current creators.CurrentCreator, activityID uint64) ([]byte, string, string, error) {
	row, code, err := s.GetActivityQRCode(ctx, current, activityID)
	if err != nil || code != "" {
		return nil, "", code, err
	}
	png, err := qrcode.Encode(fmt.Sprint(row["checkinURL"]), qrcode.Medium, 512)
	if err != nil {
		return nil, "", "", err
	}
	return png, fmt.Sprint(row["qrcodeNo"]) + ".png", "", nil
}

func (s *Service) loadCheckinQR(tx *gorm.DB, entryCode string, lock bool) (activityCheckinQRCode, string, error) {
	if strings.TrimSpace(entryCode) == "" {
		return activityCheckinQRCode{}, "ACTIVITY_CHECKIN_QRCODE_INVALID", nil
	}
	q := tx.Table("activity_checkin_qrcode")
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row activityCheckinQRCode
	if err := q.Where("entry_code=?", entryCode).Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return row, "ACTIVITY_CHECKIN_QRCODE_INVALID", nil
		}
		return row, "", err
	}
	if row.Status != "ACTIVE" {
		return row, "ACTIVITY_CHECKIN_QRCODE_UNAVAILABLE", nil
	}
	return row, "", nil
}

func (s *Service) checkinCandidates(tx *gorm.DB, consumerID, activityID uint64, lock bool) ([]checkinCandidate, error) {
	q := tx.Table("activity_booking b").Select("b.id booking_id,b.booking_no,b.session_id,s.session_name,t.ticket_name,b.quantity,s.start_at,s.end_at,vc.id verification_code_id").Joins("JOIN activity_session s ON s.id=b.session_id").Joins("JOIN activity_ticket_type t ON t.id=b.ticket_type_id").Joins("JOIN verification_code vc ON vc.booking_id=b.id AND vc.status='ACTIVE' AND vc.expires_at>?", time.Now()).Where("b.consumer_id=? AND b.activity_id=? AND b.status=? AND b.verification_status='PENDING'", consumerID, activityID, statusPendingVerification)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []checkinCandidate
	return rows, q.Order("s.start_at ASC,b.created_at ASC").Find(&rows).Error
}

func checkinOpen(c checkinCandidate, now time.Time) bool {
	return !now.Before(c.StartAt.Add(-checkinOpenAhead)) && now.Before(c.EndAt)
}

func candidateData(c checkinCandidate) gin.H {
	return gin.H{"activityBookingId": fmt.Sprint(c.BookingID), "bookingNo": c.BookingNo, "sessionId": fmt.Sprint(c.SessionID), "sessionName": c.SessionName, "ticketName": c.TicketName, "quantity": c.Quantity, "checkinOpenAt": c.StartAt.Add(-checkinOpenAhead), "startAt": c.StartAt, "endAt": c.EndAt}
}

func (s *Service) PreviewCheckin(ctx context.Context, current identity.CurrentConsumer, entryCode string) (gin.H, string, error) {
	var result gin.H
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		qr, c, err := s.loadCheckinQR(tx, entryCode, false)
		if err != nil || c != "" {
			code = c
			return err
		}
		var activity struct {
			Name, Venue, MapPoint string
			ActivityID            uint64 `gorm:"column:activity_id"`
		}
		if err = tx.Table("activity a").Select("a.id activity_id,v.activity_name name,v.venue_name venue,v.map_point_name map_point").Joins("JOIN activity_version v ON v.id=a.current_version_id").Where("a.id=?", qr.ActivityID).Take(&activity).Error; err != nil {
			return err
		}
		rows, err := s.checkinCandidates(tx, current.ConsumerID, qr.ActivityID, false)
		if err != nil {
			return err
		}
		now := time.Now()
		items := make([]gin.H, 0, len(rows))
		for _, r := range rows {
			if checkinOpen(r, now) {
				items = append(items, candidateData(r))
			}
		}
		if len(items) == 0 {
			if len(rows) > 0 {
				code = "ACTIVITY_CHECKIN_NOT_OPEN"
			} else {
				code = "ACTIVITY_CHECKIN_NO_ELIGIBLE_BOOKING"
			}
			return nil
		}
		result = gin.H{"activityId": fmt.Sprint(qr.ActivityID), "activityName": activity.Name, "venueName": activity.Venue, "mapPointName": activity.MapPoint, "checkinQrcodeId": fmt.Sprint(qr.ID), "eligibleBookings": items, "locationOptional": true}
		return nil
	})
	return result, code, err
}

func validLocation(v *CheckinLocation) bool {
	if v == nil {
		return true
	}
	return v.Longitude >= -180 && v.Longitude <= 180 && v.Latitude >= -90 && v.Latitude <= 90 && v.Accuracy >= 0 && v.Accuracy <= 100000
}

func (s *Service) ConfirmCheckin(ctx context.Context, current identity.CurrentConsumer, entryCode string, in CheckinConfirmInput, clientIP string) (gin.H, string, error) {
	bookingID, ok := parseID(in.ActivityBookingID)
	if !ok || !validLocation(in.Location) {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	var result gin.H
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		qr, c, err := s.loadCheckinQR(tx, entryCode, true)
		if err != nil || c != "" {
			code = c
			return err
		}
		rows, err := s.checkinCandidates(tx, current.ConsumerID, qr.ActivityID, true)
		if err != nil {
			return err
		}
		var selected *checkinCandidate
		for i := range rows {
			if rows[i].BookingID == bookingID {
				selected = &rows[i]
				break
			}
		}
		if selected == nil {
			code = "ACTIVITY_CHECKIN_BOOKING_INVALID"
			return nil
		}
		if !checkinOpen(*selected, time.Now()) {
			code = "ACTIVITY_CHECKIN_NOT_OPEN"
			return nil
		}
		now := time.Now()
		locationJSON, _ := json.Marshal(in.Location)
		if err = tx.Table("activity_booking").Where("id=? AND consumer_id=? AND status=? AND verification_status='PENDING'", selected.BookingID, current.ConsumerID, statusPendingVerification).Updates(map[string]any{"status": "VERIFIED", "verification_status": "VERIFIED", "version": gorm.Expr("version+1"), "updated_by": current.ConsumerID, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.ConsumerID)}).Error; err != nil {
			return err
		}
		if err = tx.Table("verification_code").Where("id=? AND status='ACTIVE'", selected.VerificationCodeID).Updates(map[string]any{"status": "USED", "used_at": now, "updated_by": current.ConsumerID, "updated_at": now, "version": gorm.Expr("version+1")}).Error; err != nil {
			return err
		}
		if err = tx.Table("verification_record").Create(map[string]any{"id": idgen.Next(), "status": "SUCCESS", "version": 1, "booking_id": selected.BookingID, "verification_code_id": selected.VerificationCodeID, "operator_id": current.ConsumerID, "operator_type": "CONSUMER_SELF_SCAN", "checkin_qrcode_id": qr.ID, "verification_result": "SUCCESS", "location_json": string(locationJSON), "created_by": current.ConsumerID, "updated_by": current.ConsumerID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.ConsumerID)}).Error; err != nil {
			return err
		}
		if err = tx.Table("activity_checkin_qrcode").Where("id=?", qr.ID).Update("scan_count", gorm.Expr("scan_count+1")).Error; err != nil {
			return err
		}
		op := audit.Operator{Type: "USER", ID: current.ConsumerID, RoleCode: "CONSUMER", ClientIP: clientIP}
		if err = audit.WriteStatusHistory(tx, "ACTIVITY_BOOKING", selected.BookingID, statusPendingVerification, "VERIFIED", "消费者现场扫码确认核销", op); err != nil {
			return err
		}
		result = gin.H{"activityBookingId": fmt.Sprint(selected.BookingID), "activityId": fmt.Sprint(qr.ActivityID), "status": "VERIFIED", "verificationStatus": "VERIFIED", "verifiedAt": now}
		return nil
	})
	return result, code, err
}

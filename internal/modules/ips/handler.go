package ips

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idempotency"
)

// RegisterRoutes 注册 API-IP-01 已确认的城市 IP 录入、查询、材料和提交接口。
func RegisterRoutes(group *gin.RouterGroup, service *Service, auth gin.HandlerFunc) {
	protected := group.Group("", auth)
	read := protected.Group("", identity.RequireAnyPermission(cityIPPermission, "IP_REVIEW_COMPLIANCE", "AUTH_PLAN_MANAGE", "DASHBOARD_VIEW_ALL"))
	read.GET("/ips", func(c *gin.Context) {
		page, ok := httpx.MustParsePageQuery(c)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.List(c.Request.Context(), current, c.Query("keyword"), page.PageNo, page.PageSize)
		respond(c, http.StatusOK, data, code, err)
	})
	read.GET("/ips/:ipId", func(c *gin.Context) {
		id, ok := ipID(c)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.Get(c.Request.Context(), current, id)
		respond(c, http.StatusOK, data, code, err)
	})
	city := protected.Group("", identity.RequireAnyPermission(cityIPPermission))
	city.POST("/ips", func(c *gin.Context) {
		var input IPInput
		raw, ok := bindIdempotentJSON(c, &input)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		executeIdempotent(c, current, service.db, raw, http.StatusCreated, func() (map[string]any, string, error) {
			return service.Create(c.Request.Context(), current, input, c.ClientIP())
		})
	})
	city.PATCH("/ips/:ipId", func(c *gin.Context) {
		id, version, ok := ipIDVersion(c)
		if !ok {
			return
		}
		var input IPInput
		if !httpx.BindJSON(c, &input) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.Update(c.Request.Context(), current, id, version, input, c.ClientIP())
		respond(c, http.StatusOK, data, code, err)
	})
	city.POST("/ips/:ipId/submit", func(c *gin.Context) {
		id, version, ok := ipIDVersion(c)
		if !ok {
			return
		}
		var input SubmitRequest
		raw, ok := bindIdempotentJSON(c, &input)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		executeIdempotent(c, current, service.db, raw, http.StatusOK, func() (map[string]any, string, error) {
			return service.Submit(c.Request.Context(), current, id, version, input, c.ClientIP())
		})
	})
	city.POST("/ips/:ipId/dci-assistance-requests", func(c *gin.Context) {
		id, version, ok := ipIDVersion(c)
		if !ok {
			return
		}
		var input DCIRequest
		raw, ok := bindIdempotentJSON(c, &input)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		executeIdempotent(c, current, service.db, raw, http.StatusCreated, func() (map[string]any, string, error) {
			return service.CreateDCIAssistance(c.Request.Context(), current, id, version, input, c.ClientIP())
		})
	})
	// 版权/合规拥有代办联系信息的完整查看与处理权限；读取明文联系方式会单独写审计日志。
	compliance := protected.Group("", identity.RequireAnyPermission(complianceIPPermission))
	// 待审核 IP 是版权/合规的主工作队列；DCI 代办属于辅助待办，保留在独立页签查询。
	compliance.GET("/compliance/ips/pending", func(c *gin.Context) {
		page, ok := httpx.MustParsePageQuery(c)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.ListPendingCompliance(c.Request.Context(), current, c.Query("keyword"), c.Query("dciCertificateStatus"), page.PageNo, page.PageSize)
		respond(c, http.StatusOK, data, code, err)
	})
	compliance.GET("/compliance/dci-assistance-requests", func(c *gin.Context) {
		page, ok := httpx.MustParsePageQuery(c)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.ListDCIAssistance(c.Request.Context(), current, c.Query("handlingStatus"), c.Query("keyword"), page.PageNo, page.PageSize, c.ClientIP())
		respond(c, http.StatusOK, data, code, err)
	})
	compliance.POST("/compliance/dci-assistance-requests/:requestId/handle", func(c *gin.Context) {
		requestID, version, ok := assistanceIDVersion(c)
		if !ok {
			return
		}
		var input DCIHandleRequest
		raw, ok := bindIdempotentJSON(c, &input)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		executeIdempotent(c, current, service.db, raw, http.StatusOK, func() (map[string]any, string, error) {
			return service.HandleDCIAssistance(c.Request.Context(), current, requestID, version, input, c.ClientIP())
		})
	})
	compliance.POST("/ips/:ipId/dci-verifications", func(c *gin.Context) {
		id, version, ok := ipIDVersion(c)
		if !ok {
			return
		}
		var input DCIVerificationRequest
		raw, ok := bindIdempotentJSON(c, &input)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		executeIdempotent(c, current, service.db, raw, http.StatusOK, func() (map[string]any, string, error) {
			return service.VerifyDCI(c.Request.Context(), current, id, version, input, c.ClientIP())
		})
	})
	compliance.POST("/ips/:ipId/rights-reviews", func(c *gin.Context) {
		id, version, ok := ipIDVersion(c)
		if !ok {
			return
		}
		var input RightsReviewRequest
		raw, ok := bindIdempotentJSON(c, &input)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		executeIdempotent(c, current, service.db, raw, http.StatusOK, func() (map[string]any, string, error) {
			return service.ReviewRights(c.Request.Context(), current, id, version, input, c.ClientIP())
		})
	})
	listing := protected.Group("", identity.RequireAnyPermission("AUTH_PLAN_MANAGE"))
	listing.POST("/ips/:ipId/pause", func(c *gin.Context) {
		id, version, ok := ipIDVersion(c)
		if !ok {
			return
		}
		var input IPStatusChangeRequest
		raw, ok := bindIdempotentJSON(c, &input)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		executeIdempotent(c, current, service.db, raw, http.StatusOK, func() (map[string]any, string, error) {
			return service.Pause(c.Request.Context(), current, id, version, input, c.ClientIP())
		})
	})
	listing.POST("/ips/:ipId/resume", func(c *gin.Context) {
		id, version, ok := ipIDVersion(c)
		if !ok {
			return
		}
		var input IPStatusChangeRequest
		raw, ok := bindIdempotentJSON(c, &input)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		executeIdempotent(c, current, service.db, raw, http.StatusOK, func() (map[string]any, string, error) {
			return service.Resume(c.Request.Context(), current, id, version, input, c.ClientIP())
		})
	})
	listing.POST("/ips/:ipId/delist", func(c *gin.Context) {
		id, version, ok := ipIDVersion(c)
		if !ok {
			return
		}
		var input IPStatusChangeRequest
		raw, ok := bindIdempotentJSON(c, &input)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		executeIdempotent(c, current, service.db, raw, http.StatusOK, func() (map[string]any, string, error) {
			return service.Delist(c.Request.Context(), current, id, version, input, c.ClientIP())
		})
	})
	// 评估报告不作为城市运营录入IP的必填项；城市运营和版权/合规均可在各自允许的状态补传。
	evaluation := protected.Group("", identity.RequireAnyPermission(cityIPPermission, "IP_REVIEW_COMPLIANCE"))
	evaluation.POST("/ips/:ipId/evaluation-reports", func(c *gin.Context) {
		id, ok := ipID(c)
		if !ok {
			return
		}
		var input EvaluationRequest
		raw, ok := bindIdempotentJSON(c, &input)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		executeIdempotent(c, current, service.db, raw, http.StatusCreated, func() (map[string]any, string, error) {
			return service.CreateEvaluation(c.Request.Context(), current, id, input, c.ClientIP())
		})
	})
}

// bindIdempotentJSON 保留原始请求体用于计算幂等请求摘要，再复位 Body 交给统一 JSON 校验器。
func bindIdempotentJSON(c *gin.Context, target any) ([]byte, bool) {
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		httpx.AbortWithError(c, httpx.NewError(http.StatusBadRequest, "VALIDATION_FORMAT_INVALID", "请求体读取失败"))
		return nil, false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	if !httpx.BindJSON(c, target) {
		return nil, false
	}
	return raw, true
}

// executeIdempotent 将同一用户、同一路径和幂等键的重复请求返回首次结果，防止重复创建 IP 或代办单。
func executeIdempotent(c *gin.Context, current identity.CurrentAdmin, db *gorm.DB, raw []byte, successStatus int, action func() (map[string]any, string, error)) {
	result, replayed, err := idempotency.Execute(c.Request.Context(), db, idempotency.Scope{SubjectType: "USER", SubjectID: current.UserID, Method: c.Request.Method, Path: c.FullPath()}, c.GetHeader("Idempotency-Key"), raw, func(_ *gorm.DB) (idempotency.Result, error) {
		data, code, actionErr := action()
		if actionErr != nil {
			return idempotency.Result{}, actionErr
		}
		status := successStatus
		if code != "" {
			status = ipErrorStatus(code)
		}
		return idempotency.Result{HTTPStatus: status, Code: code, Message: "IP操作完成", Data: data}, nil
	})
	if err != nil {
		httpx.AbortWithError(c, httpx.AppErrorFromIdempotency(err))
		return
	}
	if replayed {
		c.Header("Idempotency-Replayed", "true")
	}
	data, _ := result.Data.(map[string]any)
	code := result.Code
	if code == "OK" {
		code = ""
	}
	respond(c, result.HTTPStatus, data, code, nil)
}

func ipErrorStatus(code string) int {
	switch code {
	case "RESOURCE_NOT_FOUND":
		return http.StatusNotFound
	case "AUTH_FORBIDDEN":
		return http.StatusForbidden
	case "VERSION_CONFLICT":
		return http.StatusConflict
	case "STATE_TRANSITION_NOT_ALLOWED", "IP_DCI_CERTIFICATE_REQUIRED", "IP_LISTING_PREREQUISITE_MISSING", "CONTENT_DETECTION_REJECTED", "CONTENT_DETECTION_UNAVAILABLE":
		return http.StatusUnprocessableEntity
	case "EXTERNAL_SERVICE_UNAVAILABLE", "EVIDENCE_UNAVAILABLE", "EVIDENCE_NOT_CONFIRMED":
		return http.StatusBadGateway
	default:
		return http.StatusBadRequest
	}
}

func ipID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("ipId"), 10, 64)
	if err != nil || id == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "IP编号格式错误"))
		return 0, false
	}
	return id, true
}
func ipIDVersion(c *gin.Context) (uint64, uint32, bool) {
	id, ok := ipID(c)
	if !ok {
		return 0, 0, false
	}
	version, ok := httpx.MustParseIfMatchVersion(c)
	return id, version, ok
}
func assistanceIDVersion(c *gin.Context) (uint64, uint32, bool) {
	id, err := strconv.ParseUint(c.Param("requestId"), 10, 64)
	if err != nil || id == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "DCI代办申请编号格式错误"))
		return 0, 0, false
	}
	version, ok := httpx.MustParseIfMatchVersion(c)
	return id, version, ok
}
func respond(c *gin.Context, status int, data map[string]any, code string, err error) {
	if err != nil {
		httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "IP服务暂不可用"))
		return
	}
	switch code {
	case "":
		httpx.Success(c, status, data)
	case "RESOURCE_NOT_FOUND":
		httpx.AbortWithError(c, httpx.NewError(http.StatusNotFound, code, "IP、文件或关联方案不存在"))
	case "AUTH_FORBIDDEN":
		httpx.AbortWithError(c, httpx.NewError(http.StatusForbidden, code, "无权操作该城市IP"))
	case "VERSION_CONFLICT":
		httpx.AbortWithError(c, httpx.NewError(http.StatusConflict, code, "数据已更新，请刷新后重试"))
	case "STATE_TRANSITION_NOT_ALLOWED", "IP_DCI_CERTIFICATE_REQUIRED", "IP_LISTING_PREREQUISITE_MISSING", "CONTENT_DETECTION_REJECTED", "CONTENT_DETECTION_UNAVAILABLE":
		httpx.AbortWithError(c, httpx.NewError(http.StatusUnprocessableEntity, code, "当前IP不满足操作条件"))
	default:
		httpx.AbortWithError(c, httpx.NewError(http.StatusBadRequest, code, "IP字段或分账比例不符合要求"))
	}
}

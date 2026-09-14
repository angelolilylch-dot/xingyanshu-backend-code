package creators

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/modules/files"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/httpx"
)

// RegisterRoutes 注册创作者注册、实名和委托书留存接口。
func RegisterRoutes(group *gin.RouterGroup, service *Service, loginService *identity.LoginService, fileService *files.Service, creatorAuth gin.HandlerFunc, adminAuth gin.HandlerFunc) {
	group.POST("/auth/login", func(c *gin.Context) {
		var request struct {
			Account     string `json:"account" binding:"required"`
			Password    string `json:"password" binding:"required"`
			CaptchaID   string `json:"captchaId" binding:"required"`
			CaptchaCode string `json:"captchaCode" binding:"required,min=4,max=8"`
		}
		if !httpx.BindJSON(c, &request) {
			return
		}
		// 统一登录只校验一次验证码，再根据账号是否存在于后台账号表自动识别账号类型。
		ok, err := loginService.VerifyCaptcha(c.Request.Context(), request.CaptchaID, request.CaptchaCode)
		if err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusServiceUnavailable, "CAPTCHA_UNAVAILABLE", "验证码服务暂不可用"))
			return
		}
		if !ok {
			httpx.AbortWithError(c, httpx.NewError(http.StatusUnauthorized, "AUTH_CAPTCHA_INVALID", "图形验证码错误或已失效"))
			return
		}
		if loginService.IsAdminAccount(c.Request.Context(), request.Account) {
			data, code, err := loginService.LoginAdminAfterCaptcha(c.Request.Context(), identity.AdminLoginRequest{Username: request.Account, Password: request.Password})
			if err != nil || code != "" {
				httpx.AbortWithError(c, httpx.NewError(http.StatusUnauthorized, "AUTH_LOGIN_INVALID", "账号、密码或验证码错误"))
				return
			}
			httpx.Success(c, http.StatusOK, data)
			return
		}
		data, code, err := service.Login(c.Request.Context(), LoginRequest{Mobile: request.Account, Password: request.Password})
		respond(c, http.StatusOK, data, code, err)
	})
	group.POST("/creator/auth/sms/send", func(c *gin.Context) {
		var request SMSRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		data, code, err := service.SendSMS(c.Request.Context(), request.Mobile)
		respond(c, http.StatusOK, data, code, err)
	})
	group.POST("/creator/auth/register", func(c *gin.Context) {
		var request RegisterRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		data, code, err := service.Register(c.Request.Context(), request, c.ClientIP())
		respond(c, http.StatusCreated, data, code, err)
	})
	creator := group.Group("/creator", creatorAuth)
	creator.POST("/files", func(c *gin.Context) {
		header, err := c.FormFile("file")
		if err != nil {
			httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_REQUIRED", "请选择需要上传的委托书文件"))
			return
		}
		source, err := header.Open()
		if err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusBadRequest, "FILE_CONTENT_INVALID", "文件无法读取"))
			return
		}
		defer source.Close()
		current, _ := CurrentCreatorFromContext(c)
		data, code, err := fileService.UploadForCreator(c.Request.Context(), current.UserID, c.ClientIP(), header, source)
		respond(c, http.StatusCreated, data, code, err)
	})
	creator.POST("/identity-verifications", func(c *gin.Context) {
		var request IdentityRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := CurrentCreatorFromContext(c)
		data, code, err := service.VerifyIdentity(c.Request.Context(), current, request, c.ClientIP())
		respond(c, http.StatusCreated, data, code, err)
	})
	creator.GET("/identity-verifications/latest", func(c *gin.Context) {
		current, _ := CurrentCreatorFromContext(c)
		data, code, err := service.LatestIdentity(c.Request.Context(), current)
		respond(c, http.StatusOK, data, code, err)
	})
	creator.GET("/authorization-letter-template", func(c *gin.Context) {
		httpx.Success(c, http.StatusOK, map[string]any{"templateName": "创作者主体授权委托书", "format": "TEXT", "variables": []string{"${principal_name}", "${principal_certificate_no}", "${principal_type}", "${agent_name}", "${agent_id_type}", "${agent_id_no}", "${agent_mobile}", "${authorization_start_date}", "${authorization_end_date}", "${principal_seal}", "${principal_representative_name}", "${signed_date}"}, "content": authorizationLetterTemplate})
	})
	creator.POST("/authorization-letters", func(c *gin.Context) {
		var request LetterRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		fileID, err := strconv.ParseUint(request.FileID, 10, 64)
		if err != nil || fileID == 0 {
			httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "委托书文件编号格式不正确"))
			return
		}
		current, _ := CurrentCreatorFromContext(c)
		data, code, err := service.FileLetter(c.Request.Context(), current, fileID, c.ClientIP())
		respond(c, http.StatusCreated, data, code, err)
	})

	// 创作者启停是平台管理员的账号治理操作，委托书不再存在人工确认接口。
	admin := group.Group("/admin", adminAuth, identity.RequireAnyPermission("ADMIN_ACCOUNT_MANAGE"))
	admin.PATCH("/creators/:creatorId/status", func(c *gin.Context) {
		creatorID, err := strconv.ParseUint(c.Param("creatorId"), 10, 64)
		if err != nil || creatorID == 0 {
			httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "创作者编号格式不正确"))
			return
		}
		version, ok := httpx.MustParseIfMatchVersion(c)
		if !ok {
			return
		}
		var request StatusRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.ChangeStatus(c.Request.Context(), creatorID, version, request, identityOperator{UserID: current.UserID, RoleCode: firstRole(current)}, c.ClientIP())
		respond(c, http.StatusOK, data, code, err)
	})
}

const authorizationLetterTemplate = `创作者主体授权委托书

委托方（主体名称）：${principal_name}
统一社会信用代码/登记证号：${principal_certificate_no}
主体类型：${principal_type}

受托人姓名：${agent_name}
证件类型：${agent_id_type}
证件号码：${agent_id_no}
联系电话：${agent_mobile}

一、委托事项
委托方授权受托人代表委托方在“星衍树”平台注册和使用创作者账号、提交实名认证资料、购买 IP 授权、在有效授权范围内提交作品或创建商品、活动，并查询与前述事项相关的合同、订单和结算资料。

二、授权期限
本授权自 ${authorization_start_date} 起生效，至 ${authorization_end_date} 止有效。

三、责任声明
委托方确认受托人具备合法授权；受托人在授权范围内实施的操作视为委托方行为。委托方保证所提交资料真实、准确、完整、有效，并同意平台留存本委托书及相关操作记录用于业务留痕、审计与争议处理。

委托方（盖章）：${principal_seal}
法定代表人/负责人签字：${principal_representative_name}
受托人签字：${agent_name}
签署日期：${signed_date}`

func respond(c *gin.Context, status int, data map[string]any, code string, err error) {
	if err != nil {
		httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "创作者身份服务暂不可用"))
		return
	}
	switch code {
	case "":
		httpx.Success(c, status, data)
	case "VALIDATION_FORMAT_INVALID":
		httpx.AbortWithError(c, httpx.NewError(http.StatusBadRequest, code, "提交信息格式不正确"))
	case "SMS_CODE_INVALID":
		httpx.AbortWithError(c, httpx.NewError(http.StatusUnprocessableEntity, code, "短信验证码错误或已失效"))
	case "RESOURCE_CONFLICT", "IDENTITY_ALREADY_VERIFIED", "VERSION_CONFLICT":
		httpx.AbortWithError(c, httpx.NewError(http.StatusConflict, code, "账号或实名主体已存在，或已完成认证"))
	case "RESOURCE_NOT_FOUND":
		httpx.AbortWithError(c, httpx.NewError(http.StatusNotFound, code, "创作者或委托书文件不存在"))
	case "STATE_TRANSITION_NOT_ALLOWED":
		httpx.AbortWithError(c, httpx.NewError(http.StatusUnprocessableEntity, code, "个人主体无需上传授权委托书"))
	case "SMS_UNAVAILABLE":
		httpx.AbortWithError(c, httpx.NewError(http.StatusBadGateway, code, "短信服务暂不可用"))
	default:
		httpx.AbortWithError(c, httpx.NewError(http.StatusBadRequest, code, "创作者身份操作失败"))
	}
}

func firstRole(current identity.CurrentAdmin) string {
	if len(current.RoleCodes) > 0 {
		return current.RoleCodes[0]
	}
	return ""
}

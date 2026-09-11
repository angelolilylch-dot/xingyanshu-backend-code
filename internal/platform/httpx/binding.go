package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

const (
	defaultPageNo   = 1
	defaultPageSize = 20
	maxPageSize     = 100
)

// PageQuery 是列表接口统一使用的分页入参。
type PageQuery struct {
	PageNo   int `form:"pageNo"`
	PageSize int `form:"pageSize"`
}

// PageResult 是列表接口统一的分页响应结构。
type PageResult[T any] struct {
	Items      []T   `json:"items"`
	PageNo     int   `json:"pageNo"`
	PageSize   int   `json:"pageSize"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"totalPages"`
}

// BindJSON 绑定 JSON 请求体，并将字段校验失败转换为 OpenAPI 约定的统一错误。
func BindJSON(c *gin.Context, request any) bool {
	if err := c.ShouldBindJSON(request); err != nil {
		AbortWithError(c, bindingError(err, reflect.TypeOf(request)))
		return false
	}
	return true
}

// ParsePageQuery 解析并限制分页参数，避免业务仓储接收不受控的分页值。
func ParsePageQuery(c *gin.Context) (PageQuery, *AppError) {
	pageNo, appErr := parsePositiveQuery(c, "pageNo", defaultPageNo)
	if appErr != nil {
		return PageQuery{}, appErr
	}
	pageSize, appErr := parsePositiveQuery(c, "pageSize", defaultPageSize)
	if appErr != nil {
		return PageQuery{}, appErr
	}
	if pageSize > maxPageSize {
		return PageQuery{}, NewValidationError("VALIDATION_FORMAT_INVALID", "参数格式错误", FieldError{Field: "pageSize", Message: "不能大于 100"})
	}
	return PageQuery{PageNo: pageNo, PageSize: pageSize}, nil
}

// NewPageResult 依据查询总数构造分页结果，避免各列表接口自行计算页数。
func NewPageResult[T any](items []T, page PageQuery, total int64) PageResult[T] {
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(page.PageSize) - 1) / int64(page.PageSize))
	}
	if items == nil {
		items = make([]T, 0)
	}
	return PageResult[T]{Items: items, PageNo: page.PageNo, PageSize: page.PageSize, Total: total, TotalPages: totalPages}
}

func parsePositiveQuery(c *gin.Context, field string, defaultValue int) (int, *AppError) {
	raw := c.Query(field)
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, NewValidationError("VALIDATION_FORMAT_INVALID", "参数格式错误", FieldError{Field: field, Message: "必须是大于等于 1 的整数"})
	}
	return value, nil
}

func bindingError(err error, requestType reflect.Type) *AppError {
	var validationErrors validator.ValidationErrors
	if errors.As(err, &validationErrors) {
		fields := make([]FieldError, 0, len(validationErrors))
		code := "VALIDATION_FORMAT_INVALID"
		for _, fieldError := range validationErrors {
			fieldName := jsonFieldName(requestType, fieldError.StructField())
			message := "格式不正确"
			if fieldError.Tag() == "required" {
				code = "VALIDATION_REQUIRED"
				message = "不能为空"
			}
			fields = append(fields, FieldError{Field: fieldName, Message: message})
		}
		return NewValidationError(code, "参数校验失败", fields...)
	}
	return NewError(http.StatusBadRequest, "VALIDATION_FORMAT_INVALID", "请求体格式错误")
}

func jsonFieldName(requestType reflect.Type, structFieldName string) string {
	if requestType == nil {
		return structFieldName
	}
	if requestType.Kind() == reflect.Ptr {
		requestType = requestType.Elem()
	}
	if requestType.Kind() != reflect.Struct {
		return structFieldName
	}
	field, found := requestType.FieldByName(structFieldName)
	if !found {
		return structFieldName
	}
	name := strings.Split(field.Tag.Get("json"), ",")[0]
	if name == "" || name == "-" {
		return structFieldName
	}
	return name
}

// MustParsePageQuery 适合 Handler 直接使用；解析失败时自动写入错误响应。
func MustParsePageQuery(c *gin.Context) (PageQuery, bool) {
	page, appErr := ParsePageQuery(c)
	if appErr != nil {
		AbortWithError(c, appErr)
		return PageQuery{}, false
	}
	return page, true
}

// ValidateSortField 检查排序字段是否在接口显式允许的白名单内。
func ValidateSortField(field string, allowed map[string]string) (string, *AppError) {
	if field == "" {
		return "", nil
	}
	mapped, ok := allowed[field]
	if !ok {
		return "", NewValidationError("VALIDATION_FORMAT_INVALID", "参数格式错误", FieldError{Field: "sortBy", Message: fmt.Sprintf("不支持排序字段 %s", field)})
	}
	return mapped, nil
}

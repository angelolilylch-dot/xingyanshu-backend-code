package identity

import (
	"fmt"
	"regexp"

	"gorm.io/gorm"
)

var scopeColumnPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

// ScopeColumns 指定业务表中组织、城市和创建人的列名；列名仅由服务端固定代码传入。
type ScopeColumns struct {
	OrgIDColumn    string
	CityCodeColumn string
	OwnerIDColumn  string
}

// ApplyDataScope 为业务查询增加当前用户的数据范围条件，不信任请求中的归属字段。
func (current CurrentAdmin) ApplyDataScope(query *gorm.DB, columns ScopeColumns) (*gorm.DB, error) {
	switch current.DataScope {
	case "ALL":
		return query, nil
	case "CITY":
		if current.CityCode == "" {
			return nil, fmt.Errorf("city data scope requires city organization")
		}
		if !validScopeColumn(columns.CityCodeColumn) {
			return nil, fmt.Errorf("invalid city scope column")
		}
		return query.Where(columns.CityCodeColumn+" = ?", current.CityCode), nil
	case "ORG":
		if current.OrgID == 0 || !validScopeColumn(columns.OrgIDColumn) {
			return nil, fmt.Errorf("organization data scope requires organization column")
		}
		return query.Where(columns.OrgIDColumn+" = ?", current.OrgID), nil
	case "SELF":
		if current.UserID == 0 || !validScopeColumn(columns.OwnerIDColumn) {
			return nil, fmt.Errorf("self data scope requires owner column")
		}
		return query.Where(columns.OwnerIDColumn+" = ?", current.UserID), nil
	default:
		return nil, fmt.Errorf("unsupported data scope: %s", current.DataScope)
	}
}

func validScopeColumn(value string) bool {
	return scopeColumnPattern.MatchString(value)
}

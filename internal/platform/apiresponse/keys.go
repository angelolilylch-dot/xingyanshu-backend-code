// Package apiresponse 统一处理 API 返回字段的命名格式。
package apiresponse

import "strings"

// CamelizeMap 将数据库查询得到的 snake_case 键递归转换为接口契约使用的 camelCase。
// 数据库直接扫描到 map 时不会经过结构体 JSON 标签，本函数避免把数据库列名泄露给前端。
func CamelizeMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[snakeToCamel(key)] = camelizeValue(value)
	}
	return result
}

// CamelizeMaps 批量转换列表查询结果，保持查询排序不变。
func CamelizeMaps(input []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(input))
	for _, item := range input {
		result = append(result, CamelizeMap(item))
	}
	return result
}

func camelizeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return CamelizeMap(typed)
	case []map[string]any:
		return CamelizeMaps(typed)
	default:
		return value
	}
}

func snakeToCamel(value string) string {
	parts := strings.Split(value, "_")
	if len(parts) == 1 {
		return value
	}
	for i := 1; i < len(parts); i++ {
		if parts[i] == "" {
			continue
		}
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

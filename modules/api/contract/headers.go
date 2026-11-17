package contract

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/cloudwego/thriftgo/parser"
)

var ErrLegacyHeader = errors.New("旧版请求头路由注解已移除，请使用 api.router.header.<名称>.key/value")

// Reject removed syntax before parsing, without inspecting comments or literals.
func validateRouterSyntax(source string) error {
	for i := 0; i < len(source); {
		start := i
		switch {
		case source[i] == '\'' || source[i] == '"':
			quote := source[i]
			i++
			for i < len(source) {
				c := source[i]
				i++
				if c == '\\' && i < len(source) {
					i++
				} else if c == quote {
					break
				}
			}
		case source[i] == '#' || strings.HasPrefix(source[i:], "//"):
			for i < len(source) && source[i] != '\n' {
				i++
			}
		case strings.HasPrefix(source[i:], "/*"):
			i += 2
			for i < len(source) && !strings.HasPrefix(source[i:], "*/") {
				i++
			}
			if i < len(source) {
				i += 2
			}
		case identifierByte(source[i]):
			for i < len(source) && identifierByte(source[i]) {
				i++
			}
			token := source[start:i]
			if (strings.HasPrefix(token, "api.header.") && !strings.HasPrefix(token, "api.header.map.")) || strings.HasPrefix(token, "apinto_route_header_") {
				return ErrLegacyHeader
			}
		default:
			i++
		}
	}
	return nil
}

func identifierByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}

// HeadersDisjoint proves two sets of equality predicates cannot match one request.
func HeadersDisjoint(a, b map[string]string) bool {
	for key, value := range a {
		if other, ok := b[key]; ok && value != other {
			return true
		}
	}
	return false
}

// Named route predicates generate exact HTTP header matching conditions.
// Keep the original annotations intact for snapshot restore and Kitex compilation.
func parseRouterHeaders(headers map[string]string, annotations parser.Annotations) error {
	groups := map[string]map[string]string{}
	for _, ann := range annotations {
		if !strings.HasPrefix(ann.Key, "api.router.") {
			continue
		}
		parts := strings.Split(ann.Key, ".")
		if len(parts) != 5 || parts[2] != "header" || !routeConditionName.MatchString(parts[3]) || (parts[4] != "key" && parts[4] != "value") {
			return fmt.Errorf("不支持的路由注解 %s，请使用 api.router.header.<名称>.key/value", ann.Key)
		}
		group := groups[parts[3]]
		if group == nil {
			group = map[string]string{}
			groups[parts[3]] = group
		}
		if _, exists := group[parts[4]]; exists || len(ann.Values) != 1 {
			return fmt.Errorf("注解 %s 必须且只能指定一个值", ann.Key)
		}
		group[parts[4]] = ann.Values[0]
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		group := groups[name]
		key, hasKey := group["key"]
		value, hasValue := group["value"]
		if !hasKey || !hasValue || key == "" || strings.TrimSpace(value) == "" {
			return fmt.Errorf("路由条件 %s 必须同时指定非空 key 和 value", name)
		}
		if !routeHeaderName.MatchString(key) {
			return fmt.Errorf("路由条件 %s 的请求头名称无效: %q", name, key)
		}
		for _, c := range value {
			if c < 32 || c == 127 {
				return fmt.Errorf("请求头 %s 的匹配值不能包含控制字符", key)
			}
		}
		key = strings.ToLower(key)
		if _, exists := headers[key]; exists {
			return fmt.Errorf("重复声明请求头 %s，请勿在不同路由条件中重复配置", key)
		}
		headers[key] = value
	}
	return nil
}

var routeConditionName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
var routeHeaderName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// api.header.map.<metadata-key> = '<HTTP-header>' declares a transport mapping.
func parseHeaderMapping(annotations parser.Annotations) (map[string]string, error) {
	mapping := map[string]string{}
	targets := map[string]bool{}
	for _, ann := range annotations {
		if !strings.HasPrefix(ann.Key, "api.header.map.") {
			continue
		}
		key := strings.TrimPrefix(ann.Key, "api.header.map.")
		if !routeHeaderName.MatchString(key) || len(ann.Values) != 1 || !routeHeaderName.MatchString(ann.Values[0]) {
			return nil, fmt.Errorf("注解 %s 必须指定有效元信息键及一个 HTTP 请求头名称", ann.Key)
		}
		source := strings.ToLower(ann.Values[0])
		if _, exists := mapping[source]; exists || targets[key] {
			return nil, fmt.Errorf("请求头映射重复: %s", ann.Key)
		}
		mapping[source] = key
		targets[key] = true
	}
	return mapping, nil
}

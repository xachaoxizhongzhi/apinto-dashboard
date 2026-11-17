package contract

import (
	"fmt"
	"github.com/cloudwego/thriftgo/parser"
	"regexp"
	"strings"
)

type Auth struct {
	Mode     string `json:"mode,omitempty"`
	Plugin   string `json:"plugin"`
	Position string `json:"position"`
	Key      string `json:"key"`
	Value    string `json:"value"`
}

func (e Endpoint) DisplayName() string {
	for _, line := range strings.Split(e.Description, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			return name
		}
	}
	return e.Service + "." + e.Method
}

func cleanComments(comments string) string {
	var lines []string
	for _, line := range strings.Split(comments, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "//")
		line = strings.TrimPrefix(line, "/*")
		line = strings.TrimSuffix(line, "*/")
		line = strings.TrimPrefix(strings.TrimSpace(line), "*")
		lines = append(lines, strings.TrimSpace(line))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// AuthRules preserves historical single-rule and multi-rule snapshots.
func (e Endpoint) AuthRules() []*Auth {
	if len(e.Auths) != 0 {
		return e.Auths
	}
	if e.Auth != nil {
		return []*Auth{e.Auth}
	}
	return nil
}

func (a Auth) MatchText() string {
	if a.Mode == "regex" {
		return "~=" + a.Value
	}
	return a.Value
}

func parsePolicy(ep *Endpoint, annotations parser.Annotations) error {
	groups := map[string]map[string]string{"api.auth": {}, "api.permission": {}}
	seen := map[string]bool{}
	for _, ann := range annotations {
		if ann.Key != "api.response.wrap" && !strings.HasPrefix(ann.Key, "api.auth.") && !strings.HasPrefix(ann.Key, "api.permission.") {
			continue
		}
		if seen[ann.Key] || len(ann.Values) != 1 {
			return fmt.Errorf("注解 %s 必须且只能指定一个值", ann.Key)
		}
		seen[ann.Key] = true
		value := ann.Values[0]
		if ann.Key == "api.response.wrap" {
			if value != "true" && value != "false" {
				return fmt.Errorf("api.response.wrap 只能为 true 或 false")
			}
			enabled := value == "true"
			ep.ResponseWrap = &enabled
			continue
		}
		prefix, field := "api.auth", strings.TrimPrefix(ann.Key, "api.auth.")
		if strings.HasPrefix(ann.Key, "api.permission.") {
			prefix = "api.permission"
			field = strings.TrimPrefix(ann.Key, "api.permission.")
		}
		switch field {
		case "plugins", "position", "key", "mode", "value":
		default:
			return fmt.Errorf("不支持的鉴权注解: %s", ann.Key)
		}
		groups[prefix][field] = value
	}
	rules := []*Auth{}
	for _, prefix := range []string{"api.auth", "api.permission"} {
		values := groups[prefix]
		if len(values) == 0 {
			continue
		}
		required := []string{"plugins", "position", "key", "value"}
		if _, hasMode := values["mode"]; hasMode || prefix == "api.permission" {
			required = append(required, "mode")
		}
		for _, field := range required {
			if strings.TrimSpace(values[field]) == "" {
				return fmt.Errorf("鉴权注解缺少 %s.%s", prefix, field)
			}
		}
		auth := &Auth{Plugin: values["plugins"], Position: values["position"], Key: values["key"], Mode: values["mode"], Value: values["value"]}
		if !regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`).MatchString(auth.Plugin) || auth.Plugin == "response_wrap" || auth.Plugin == "http_to_kitex" {
			return fmt.Errorf("%s.plugins 必须引用已注册的校验插件", prefix)
		}
		switch auth.Position {
		case "header", "query", "body":
		default:
			return fmt.Errorf("%s.position 只支持 header、query、body", prefix)
		}
		if auth.Position == "header" && !routeHeaderName.MatchString(auth.Key) {
			return fmt.Errorf("无效请求头 %s", auth.Key)
		}
		if prefix == "api.permission" {
			auth.Value = strings.TrimSpace(auth.Value)
			if (auth.Mode != "contains" && auth.Mode != "contains_all") || auth.Position != "header" || strings.ContainsAny(auth.Value, "\r\n") {
				return fmt.Errorf("api.permission必须使用header和contains或contains_all匹配")
			}
			permissions := strings.Split(auth.Value, ",")
			if auth.Mode == "contains" && len(permissions) != 1 {
				return fmt.Errorf("api.permission的contains只能指定一个完整权限项")
			}
			for i, item := range permissions {
				permissions[i] = strings.TrimSpace(item)
				if permissions[i] == "" {
					return fmt.Errorf("api.permission不能包含空权限项")
				}
			}
			auth.Value = strings.Join(permissions, ",")
		} else {
			if auth.Mode != "" && auth.Mode != "regex" {
				return fmt.Errorf("api.auth.mode 只支持 regex")
			}
			pattern := strings.TrimSpace(auth.Value)
			isRegex := auth.Mode == "regex" || strings.HasPrefix(pattern, "~=") || strings.HasPrefix(pattern, "~*=")
			if isRegex {
				if strings.HasPrefix(pattern, "~*=") {
					pattern = "(?i)" + strings.TrimSpace(pattern[3:])
				} else if strings.HasPrefix(pattern, "~=") {
					pattern = strings.TrimSpace(pattern[2:])
				}
				if pattern == "" {
					return fmt.Errorf("api.auth.value 正则不能为空")
				}
				if _, err := regexp.Compile(pattern); err != nil {
					return fmt.Errorf("api.auth.value 正则无效: %w", err)
				}
				if auth.Mode == "regex" {
					auth.Value = pattern
				}
			}
			ep.Auth = auth
		}
		if len(rules) > 0 && rules[0].Plugin == auth.Plugin {
			return fmt.Errorf("身份与权限校验不能使用同一个插件实例 %s", auth.Plugin)
		}
		rules = append(rules, auth)
	}
	// Keep legacy single-rule JSON readable; new rules execute identity then permission.
	if len(rules) > 1 || len(groups["api.permission"]) > 0 {
		ep.Auths = rules
	}
	return nil
}

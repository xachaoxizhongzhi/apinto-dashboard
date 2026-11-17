package api_service

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	dto "github.com/eolinker/apinto-dashboard/modules/api/api-dto"
	"github.com/eolinker/apinto-dashboard/modules/api/contract"
	plugin_entry "github.com/eolinker/apinto-dashboard/modules/plugin/plugin-entry"
)

const authDriver = "eolinker.com:apinto:params_check_v2"
const permissionDriver = "eolinker.com:apinto:user_permissions"

func authPluginDriver(auth *contract.Auth) string {
	if auth.Mode == "contains" || auth.Mode == "contains_all" {
		return permissionDriver
	}
	return authDriver
}

const wrapDriver = "eolinker.com:apinto:response_wrap"

func requiredContractPlugins(bundle *contract.Bundle) map[string]string {
	required := map[string]string{}
	for _, ep := range bundle.Endpoints {
		for _, auth := range ep.AuthRules() {
			required[auth.Plugin] = authPluginDriver(auth)
		}
		if ep.ResponseWrap != nil && *ep.ResponseWrap {
			required["response_wrap"] = wrapDriver
		}
	}
	return required
}

func (a *apiService) validateContractPlugins(ctx context.Context, ns int, bundle *contract.Bundle) error {
	// A named instance cannot select different drivers across endpoints.
	drivers := map[string]string{}
	for _, ep := range bundle.Endpoints {
		for _, auth := range ep.AuthRules() {
			driver := authPluginDriver(auth)
			if old, ok := drivers[auth.Plugin]; ok && old != driver {
				return fmt.Errorf("插件 %s 在不同接口中要求了不同驱动", auth.Plugin)
			}
			drivers[auth.Plugin] = driver
		}
	}
	for name, driver := range requiredContractPlugins(bundle) {
		plugin, err := a.contractPluginCatalog.GetByName(ctx, ns, name)
		if err != nil || plugin == nil || plugin.Plugin == nil {
			return fmt.Errorf("契约引用的插件 %s 不存在，请先在节点插件中注册", name)
		}
		if plugin.Extended != driver {
			return fmt.Errorf("插件 %s 的驱动必须为 %s", name, driver)
		}
	}
	return nil
}

func authPluginConfig(auth *contract.Auth) string {
	if auth.Mode == "contains" || auth.Mode == "contains_all" {
		raw, _ := json.Marshal(map[string]string{"position": auth.Position, "key": auth.Key, "mode": auth.Mode, "value": auth.Value})
		return string(raw)
	}
	config := map[string]interface{}{"logic": "and", "params": []interface{}{map[string]string{"position": auth.Position, "name": auth.Key, "match_mode": "all", "match_text": auth.MatchText()}}}
	raw, _ := json.Marshal(config)
	return string(raw)
}

func samePluginConfig(a, b string) bool {
	var left, right interface{}
	return json.Unmarshal([]byte(a), &left) == nil && json.Unmarshal([]byte(b), &right) == nil && reflect.DeepEqual(left, right)
}

// The contract owns declared plugin configurations. Unrelated manual plugins survive.
func syncContractPlugins(plugins []*dto.APIPlugin, previous, next contract.Endpoint) []*dto.APIPlugin {
	out := make([]*dto.APIPlugin, 0, len(plugins)+2)
	var wrap *dto.APIPlugin
	previousAuth, nextAuth := map[string]*contract.Auth{}, map[string]*contract.Auth{}
	for _, auth := range previous.AuthRules() {
		previousAuth[auth.Plugin] = auth
	}
	for _, auth := range next.AuthRules() {
		nextAuth[auth.Plugin] = auth
	}
	for _, plugin := range plugins {
		if nextAuth[plugin.Name] != nil {
			continue
		}
		if auth := previousAuth[plugin.Name]; auth != nil && !plugin.Disable && samePluginConfig(plugin.Config, authPluginConfig(auth)) {
			continue
		}
		if plugin.Name == "response_wrap" {
			if next.ResponseWrap != nil {
				copy := *plugin
				wrap = &copy
				continue
			}
			if previous.ResponseWrap != nil && samePluginConfig(plugin.Config, "{}") {
				continue
			}
		}
		out = append(out, plugin)
	}
	for _, auth := range next.AuthRules() {
		out = append(out, &dto.APIPlugin{Name: auth.Plugin, Config: authPluginConfig(auth)})
	}
	if next.ResponseWrap != nil {
		if wrap == nil {
			wrap = &dto.APIPlugin{Name: "response_wrap", Config: "{}"}
		}
		wrap.Disable = !*next.ResponseWrap
		out = append(out, wrap)
	}
	return out
}

func validatePublishedContractPlugins(bundle *contract.Bundle, plugins []*plugin_entry.CluPluginPublishConfig) error {
	available := map[string]*plugin_entry.CluPluginPublishConfig{}
	for _, plugin := range plugins {
		if plugin != nil && plugin.ClusterPlugin != nil {
			available[plugin.ClusterPlugin.PluginName] = plugin
		}
	}
	for name, driver := range requiredContractPlugins(bundle) {
		plugin := available[name]
		if plugin == nil || plugin.Extended != driver || (plugin.ClusterPlugin.Status != 2 && plugin.ClusterPlugin.Status != 3) {
			return fmt.Errorf("请先启用并发布契约引用的插件 %s", name)
		}
	}
	for _, ep := range bundle.Endpoints {
		for _, auth := range ep.AuthRules() {
			current := available[auth.Plugin]
			if current == nil || current.Extended != authPluginDriver(auth) {
				return fmt.Errorf("插件 %s 的驱动不匹配", auth.Plugin)
			}
			if ep.ResponseWrap != nil && *ep.ResponseWrap && available["response_wrap"].Sort >= current.Sort {
				return fmt.Errorf("请将 response_wrap 排在 %s 之前并发布，才能包装校验失败的响应", auth.Plugin)
			}

		}
	}
	return nil
}

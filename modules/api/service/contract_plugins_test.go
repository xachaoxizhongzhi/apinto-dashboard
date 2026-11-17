package api_service

import (
	"context"
	dto "github.com/eolinker/apinto-dashboard/modules/api/api-dto"
	entry "github.com/eolinker/apinto-dashboard/modules/api/api-entry"
	"github.com/eolinker/apinto-dashboard/modules/api/contract"
	"github.com/eolinker/apinto-dashboard/modules/plugin"
	plugin_entry "github.com/eolinker/apinto-dashboard/modules/plugin/plugin-entry"
	plugin_model "github.com/eolinker/apinto-dashboard/modules/plugin/plugin-model"
	"gorm.io/gorm"
	"testing"
)

func TestPublishedContractPolicies(t *testing.T) {
	enabled := true
	bundle := &contract.Bundle{Endpoints: []contract.Endpoint{{Auth: &contract.Auth{Plugin: "identity"}, ResponseWrap: &enabled}}}
	auth := &plugin_entry.CluPluginPublishConfig{ClusterPlugin: &plugin_entry.ClusterPlugin{PluginName: "identity", Status: 2}, Extended: authDriver, Sort: 20}
	wrap := &plugin_entry.CluPluginPublishConfig{ClusterPlugin: &plugin_entry.ClusterPlugin{PluginName: "response_wrap", Status: 2}, Extended: wrapDriver, Sort: 10}
	if err := validatePublishedContractPlugins(bundle, []*plugin_entry.CluPluginPublishConfig{wrap, auth}); err != nil {
		t.Fatal(err)
	}
	if validatePublishedContractPlugins(bundle, []*plugin_entry.CluPluginPublishConfig{auth}) == nil {
		t.Fatal("missing wrapper accepted")
	}
	wrap.Sort = 30
	if validatePublishedContractPlugins(bundle, []*plugin_entry.CluPluginPublishConfig{wrap, auth}) == nil {
		t.Fatal("wrapper after rejection plugin accepted")
	}
	wrap.Sort = 10
	auth.ClusterPlugin.Status = 1
	if validatePublishedContractPlugins(bundle, []*plugin_entry.CluPluginPublishConfig{wrap, auth}) == nil {
		t.Fatal("disabled auth plugin accepted")
	}
}

func TestContractPluginOverrides(t *testing.T) {
	yes, no := true, false
	auth := &contract.Auth{Plugin: "identity", Position: "header", Key: "x-user-id", Value: "**"}
	manual := &dto.APIPlugin{Name: "custom", Config: `{"keep":true}`}
	plugins := []*dto.APIPlugin{manual, {Name: "response_wrap", Config: `{"status_code":200}`, Disable: true}}
	next := contract.Endpoint{Auth: auth, ResponseWrap: &yes}
	updated := syncContractPlugins(plugins, contract.Endpoint{}, next)
	if updated[0] != manual || updated[2].Disable || updated[2].Config != `{"status_code":200}` {
		t.Fatal("manual settings lost")
	}
	disabled := syncContractPlugins(updated, next, contract.Endpoint{ResponseWrap: &no})
	if len(disabled) != 2 || !disabled[1].Disable {
		t.Fatal("explicit false did not disable wrapper")
	}
}

func TestSyncContractHeaderMapping(t *testing.T) {
	current := map[string]string{"X-User-Id": "userId", "X-Trace": "traceId", "x-edited": "custom"}
	previous := map[string]string{"x-user-id": "userId", "x-edited": "old"}
	next := map[string]string{"x-identity": "userId", "x-device-id": "deviceId"}
	got := syncContractHeaderMapping(current, previous, next)
	if len(got) != 4 || got["x-identity"] != "userId" || got["x-device-id"] != "deviceId" || got["x-trace"] != "traceId" || got["x-edited"] != "custom" {
		t.Fatal(got)
	}
	removed := syncContractHeaderMapping(got, next, nil)
	if len(removed) != 2 || removed["x-trace"] != "traceId" {
		t.Fatal("manual mapping lost on removal", removed)
	}
}

func TestMultipleContractPolicies(t *testing.T) {
	yes := true
	ep := contract.Endpoint{Auths: []*contract.Auth{
		{Plugin: "identity", Position: "header", Key: "x-user-id", Mode: "regex", Value: "^[a-z]+$"},
		{Plugin: "permission", Position: "header", Key: "x-user-permission", Mode: "contains", Value: "user:banner:list"},
	}, ResponseWrap: &yes}
	manual := &dto.APIPlugin{Name: "manual", Config: `{"keep":true}`}
	generated := syncContractPlugins([]*dto.APIPlugin{manual}, contract.Endpoint{}, ep)
	if len(generated) != 4 || generated[1].Name != "identity" || generated[2].Name != "permission" {
		t.Fatal(generated)
	}
	if !samePluginConfig(generated[2].Config, `{"position":"header","key":"x-user-permission","mode":"contains","value":"user:banner:list"}`) {
		t.Fatal(generated[2].Config)
	}
	next := ep
	next.Auths = next.Auths[:1]
	removed := syncContractPlugins(generated, ep, next)
	if len(removed) != 3 || removed[0] != manual {
		t.Fatal("removal lost manual or kept old permission")
	}
	restored := savedContractEndpoints([]*dto.APIInfo{{Plugins: append(generated, &dto.APIPlugin{Name: "http_to_kitex", Config: `{"idl_service":"S","method":"M"}`})}})
	if len(restored) != 1 || len(restored[0].AuthRules()) != 2 {
		t.Fatal("snapshot lost auth rules", restored)
	}
	cleaned := syncContractPlugins(generated, restored[0], contract.Endpoint{})
	if len(cleaned) != 1 || cleaned[0] != manual {
		t.Fatal("snapshot cleanup lost ownership")
	}
	makePlugin := func(name, driver string, sort int) *plugin_entry.CluPluginPublishConfig {
		return &plugin_entry.CluPluginPublishConfig{ClusterPlugin: &plugin_entry.ClusterPlugin{PluginName: name, Status: 2}, Extended: driver, Sort: sort}
	}
	wrap := makePlugin("response_wrap", wrapDriver, 10)
	identity := makePlugin("identity", authDriver, 20)
	permission := makePlugin("permission", permissionDriver, 30)
	transport := makePlugin("http_to_kitex", "eolinker.com:apinto:http_to_kitex", 40)
	bundle := &contract.Bundle{Endpoints: []contract.Endpoint{ep}}
	plugins := []*plugin_entry.CluPluginPublishConfig{wrap, identity, permission, transport}
	if err := validatePublishedContractPlugins(bundle, plugins); err != nil {
		t.Fatal(err)
	}
	permission.Sort = 15
	if err := validatePublishedContractPlugins(bundle, plugins); err != nil {
		t.Fatal("permission before identity must be allowed", err)
	}
	permission.Sort = 30
	transport.Sort = 5
	if err := validatePublishedContractPlugins(bundle, plugins); err != nil {
		t.Fatal("transport before validation plugins must be allowed", err)
	}
	wrap.Sort = 25
	if validatePublishedContractPlugins(bundle, plugins) == nil {
		t.Fatal("wrapper after identity accepted")
	}
	permission.Sort = 15
	wrap.Sort = 18
	if validatePublishedContractPlugins(bundle, plugins) == nil {
		t.Fatal("wrapper after permission accepted")
	}
	wrap.Sort = 10
	permission.Sort = 30
	transport.Sort = 40
	permission.Extended = authDriver
	if validatePublishedContractPlugins(bundle, plugins) == nil {
		t.Fatal("wrong permission driver accepted")
	}
	permission.Extended = permissionDriver
	permission.ClusterPlugin.Status = 1
	if validatePublishedContractPlugins(bundle, plugins) == nil {
		t.Fatal("disabled permission accepted")
	}
}

func TestImportNamedPolicies(t *testing.T) {
	source := `struct Req {1: optional string id} struct Res {1: optional string value}
 service Catalog { Res Get(1: Req req) (api.get='/banner'
 api.auth.plugins='param_check_header_user_id'
 api.auth.position='header' api.auth.key='x-user-id' api.auth.mode='regex' api.auth.value='^[a-z]+$'
 api.permission.plugins='user_permissions'
 api.permission.position='header' api.permission.key='x-user-permission' api.permission.mode='contains' api.permission.value='user:banner:list'
 api.response.wrap='true') }`
	bundle, err := contract.Parse(map[string]string{"idl/api.thrift": source})
	if err != nil {
		t.Fatal(err)
	}
	svc := &apiService{contractPluginCatalog: contractCatalog{}}
	if err = svc.validateContractPlugins(context.Background(), 1, bundle); err != nil {
		t.Fatal(err)
	}
	configs := syncContractPlugins(nil, contract.Endpoint{}, bundle.Endpoints[0])
	if len(configs) != 3 || !samePluginConfig(configs[0].Config, `{"logic":"and","params":[{"position":"header","name":"x-user-id","match_mode":"all","match_text":"~=^[a-z]+$"}]}`) {
		t.Fatalf("wrong generated identity: %+v", configs)
	}
	if configs[1].Name != "user_permissions" || !samePluginConfig(configs[1].Config, `{"position":"header","key":"x-user-permission","mode":"contains","value":"user:banner:list"}`) {
		t.Fatal(configs[1])
	}
	copy := bundle.Endpoints[0]
	copy.Auths = []*contract.Auth{{Plugin: "param_check_header_user_id", Position: "header", Key: "x-user-permission", Mode: "contains", Value: "read"}}
	bundle.Endpoints = append(bundle.Endpoints, copy)
	if svc.validateContractPlugins(context.Background(), 1, bundle) == nil {
		t.Fatal("conflicting driver usage accepted")
	}
}

type contractCatalog struct{ plugin.IPluginService }

func (contractCatalog) GetByName(ctx context.Context, ns int, name string) (*plugin_model.Plugin, error) {
	driver := ""
	if name == "param_check_header_user_id" {
		driver = authDriver
	}
	if name == "user_permissions" {
		driver = permissionDriver
	}
	if name == "response_wrap" {
		driver = wrapDriver
	}
	if driver == "" {
		return nil, gorm.ErrRecordNotFound
	}
	return &plugin_model.Plugin{Plugin: &plugin_entry.Plugin{Name: name, Extended: driver}}, nil
}

func TestSyncContractHeaders(t *testing.T) {
	manual := &entry.MatchConf{Position: "query", Key: "page", MatchType: "EQUAL", Pattern: "1"}
	matches := []*entry.MatchConf{manual, {Position: "header", Key: "X-App-Code", MatchType: "EQUAL", Pattern: "old"}}
	next := syncContractHeaders(matches, map[string]string{"x-app-code": "old"}, map[string]string{"x-app-code": "new", "x-env": "sit"})
	if len(next) != 3 || next[0] != manual || next[1].Pattern != "new" || next[2].Key != "x-env" {
		t.Fatalf("bad update: %#v", next)
	}
	removed := syncContractHeaders(next, map[string]string{"x-app-code": "new", "x-env": "sit"}, nil)
	if len(removed) != 1 || removed[0] != manual {
		t.Fatal("generated conditions not removed or manual conditions lost")
	}
}

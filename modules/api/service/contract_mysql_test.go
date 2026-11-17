package api_service

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	api "github.com/eolinker/apinto-dashboard/modules/api"
	dto "github.com/eolinker/apinto-dashboard/modules/api/api-dto"
	entry "github.com/eolinker/apinto-dashboard/modules/api/api-entry"
	"github.com/eolinker/apinto-dashboard/modules/api/contract"
	"github.com/eolinker/apinto-dashboard/modules/dynamic"
	dynamic_entry "github.com/eolinker/apinto-dashboard/modules/dynamic/dynamic-entry"
	"github.com/eolinker/apinto-dashboard/modules/group"
	group_entry "github.com/eolinker/apinto-dashboard/modules/group/group-entry"
	group_model "github.com/eolinker/apinto-dashboard/modules/group/group-model"
	"github.com/eolinker/apinto-dashboard/store"
	"github.com/eolinker/apinto-dashboard/store/store_mysql"
	"github.com/go-basic/uuid"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Other services are isolated; API/version/history/quote and contract writes use real MySQL.
type contractGroups struct {
	group.ICommonGroupService
	db store.IDB
}

func (g contractGroups) GetGroupByName(ctx context.Context, ns int, kind, name, parent string) (*group_model.CommonGroupBasic, error) {
	var row group_entry.CommonGroup
	err := g.db.DB(ctx).Where("namespace = ? AND name = ?", ns, name).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &group_model.CommonGroupBasic{Uuid: row.Uuid}, nil
}
func (g contractGroups) CreateGroup(ctx context.Context, ns, operator int, kind, tag, name, id, parent string) (string, error) {
	row := group_entry.CommonGroup{Uuid: uuid.New(), NamespaceId: ns, Type: kind, Name: name, CreateTime: time.Now(), UpdateTime: time.Now()}
	return row.Uuid, g.db.DB(ctx).Create(&row).Error
}
func (g contractGroups) IsGroupExist(ctx context.Context, id string) (bool, error) {
	var n int64
	err := g.db.DB(ctx).Model(&group_entry.CommonGroup{}).Where("uuid = ?", id).Count(&n).Error
	return n > 0, err
}

type contractUpstreams struct {
	dynamic.IDynamicService
	db store.IDB
}

func (d contractUpstreams) GetIDByName(ctx context.Context, ns int, profession, name string) (int, error) {
	var u dynamic_entry.Dynamic
	err := d.db.DB(ctx).Where("namespace = ? AND profession = ? AND name = ?", ns, profession, name).First(&u).Error
	return u.Id, err
}

func TestContractMySQL(t *testing.T) {
	dsn := os.Getenv("TEST_CONTRACT_MYSQL_DSN")
	if dsn == "" {
		t.Skip("requires explicitly configured MySQL development database")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	// Initialize current models before starting the rollback-only data transaction.
	idb := store_mysql.NewMyDB(db, store.DBInfo{})
	store.InitStoreDB(idb)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	svc := NewAPIService().(*apiService)
	svc.commonGroup = contractGroups{db: idb}
	svc.contractPluginCatalog = contractCatalog{}
	svc.dynamicService = contractUpstreams{db: idb}
	ctx := context.WithValue(context.Background(), store.TxContextKey, tx)
	up := dynamic_entry.Dynamic{NamespaceId: 71, Name: "contract-test-" + uuid.New(), Profession: "service", Driver: "http", CreateTime: time.Now(), UpdateTime: time.Now()}
	if err = tx.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	parse := func(source string) *contract.Bundle {
		t.Helper()
		b, e := contract.Parse(map[string]string{
			"idl/api.thrift":         `struct Req {1: optional string id} struct Res {1: optional string value} ` + source,
			"idl/large_types.thrift": strings.Repeat("// 契约容量回归：包含中文和多文件内容\n", 3000) + "struct Metadata { 1: optional string value }",
		})
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	first := parse(`service Catalog { Res Get(1: Req req) (api.post="/contract/get" api.router.header.contract.key="x-contract" api.router.header.contract.value="first") Res List(1: Req req) (api.get="/contract/list") }`)
	v1, err := svc.SaveContract(ctx, 71, 1, up.Name, 0, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(v1.APIs) != 2 {
		t.Fatal("missing generated APIs")
	}
	var getID, listID string
	for _, input := range v1.APIs {
		if input.ApiName == "Catalog.Get" {
			getID = input.UUID
		} else {
			listID = input.UUID
		}
	}
	info, _ := svc.GetAPIVersionInfo(ctx, 71, getID)
	if len(info.Version.Match) != 1 || info.Version.Match[0].Key != "x-contract" || info.Version.Match[0].Pattern != "first" {
		t.Fatal("header annotation did not create route match")
	}

	rawConfig, err := json.Marshal(info.Version.APIVersionConfig)
	if err != nil || len(rawConfig) >= 4096 {
		t.Fatal("API config must contain a small contract reference", len(rawConfig), err)
	}
	var transportFiles struct {
		Files map[string]string `json:"thrift_files"`
		Ref   string            `json:"contract"`
	}
	for _, p := range info.Version.Plugins {
		if p.Name == "http_to_kitex" {
			if err = json.Unmarshal([]byte(p.Config), &transportFiles); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(transportFiles.Files) != 0 || transportFiles.Ref != contract.ArtifactRef(first.Revision) {
		t.Fatal("API embedded IDL or referenced the wrong revision")
	}
	storedFiles, err := contract.LoadArtifact(tx, 71, transportFiles.Ref)
	if err != nil || !reflect.DeepEqual(storedFiles, first.Files) {
		t.Fatal("artifact content changed", err)
	}
	var snapshot entry.ContractVersion
	if err = tx.First(&snapshot, v1.VersionID).Error; err != nil {
		t.Fatal(err)
	}
	if string(snapshot.Files) != "{}" || len(snapshot.APIs) > 2048 {
		t.Fatal("snapshot duplicated payloads")
	}
	var refs []entry.ContractAPIRef
	if err = json.Unmarshal(snapshot.APIs, &refs); err != nil || refs[0].VersionID == 0 {
		t.Fatal("snapshot did not pin native API versions", err)
	}
	// Exercise the same persistent publication record without contacting a gateway.
	record := &entry.ApiPublishHistory{VersionName: info.Api.Version, ClusterId: 0, NamespaceId: 71, VersionId: info.Version.Id, Target: info.Api.Id, APIVersionConfig: info.Version.APIVersionConfig, OptType: 1, Operator: 1, OptTime: time.Now()}
	if err = svc.apiPublishHistory.Insert(ctx, record); err != nil {
		t.Fatal("large publication record", err)
	}
	published, err := svc.apiPublishHistory.GetByVersionName(ctx, record.VersionName, record.Target)
	if err != nil {
		t.Fatal(err)
	}
	publishedJSON, err := json.Marshal(published.APIVersionConfig)
	if err != nil || string(publishedJSON) != string(rawConfig) {
		t.Fatal("large publication record roundtrip failed", err)
	}
	historyInfo := &entry.ApiHistoryInfo{Api: *info.Api, Config: info.Version.APIVersionConfig}
	if err = svc.apiHistory.HistoryEdit(ctx, 71, info.Api.Id, historyInfo, historyInfo, 1); err != nil {
		t.Fatal("large old/new history", err)
	}
	var oldData, newData string
	if err = tx.Raw("SELECT old_value, new_value FROM history WHERE kind = ? AND target = ? ORDER BY id DESC LIMIT 1", "api", info.Api.Id).Row().Scan(&oldData, &newData); err != nil {
		t.Fatal(err)
	}
	historyJSON, _ := json.Marshal(historyInfo)
	if oldData != string(historyJSON) || newData != string(historyJSON) {
		t.Fatal("history content was truncated")
	}
	custom := contractInput(info)
	for _, p := range custom.Plugins {
		if p.Name == "http_to_kitex" {
			var config map[string]interface{}
			if err := json.Unmarshal([]byte(p.Config), &config); err != nil {
				t.Fatal(err)
			}
			config["header_mapping"] = map[string]string{"x-user-id": "x-user-id", "x-device-id": "client-device"}
			raw, _ := json.Marshal(config)
			p.Config = string(raw)
		}
	}
	custom.Plugins = append(custom.Plugins, &dto.APIPlugin{Name: "user_check", Config: `{"header":"x-user-id"}`})
	custom.Match = append(custom.Match, &entry.MatchConf{Position: "header", Key: "x-app-code", MatchType: "EQUAL", Pattern: "catalog"})
	if _, _, err = svc.CreateAPI(ctx, 71, 1, custom); err != nil {
		t.Fatal(err)
	}
	// This manual API shares the upstream and must remain unchanged after full replacements.
	manual := &dto.APIInfo{ApiName: "manual", GroupUUID: custom.GroupUUID, ServiceName: up.Name + "@upstream", Scheme: "http", RequestPath: "/manual", RequestPathLabel: "/manual", ProxyPath: "/manual", Method: []string{"POST"}, Protocols: []string{"http"}}
	manualID, _, err := svc.CreateAPI(ctx, 71, 1, manual)
	if err != nil {
		t.Fatal(err)
	}

	// A stored legacy snapshot must not prevent a full replacement using new syntax.
	legacyFiles := map[string]string{"idl/api.thrift": strings.Replace(first.Files["idl/api.thrift"], `api.router.header.contract.key="x-contract" api.router.header.contract.value="first"`, `api.header.x-contract="first"`, 1)}
	legacyRef, e := contract.PutArtifact(tx, 71, legacyFiles)
	if e != nil {
		t.Fatal(e)
	}
	if e := svc.apiStore.DB(ctx).Model(&entry.ContractVersion{}).Where("id = ?", v1.VersionID).Update("artifact", legacyRef).Error; e != nil {
		t.Fatal(e)
	}
	second := parse(`service Catalog { Res Get(1: Req req) (api.post="/contract/get-v2") }`)
	v2, err := svc.SaveContract(ctx, 71, 1, up.Name, v1.VersionID, second)
	if err != nil {
		t.Fatal(err)
	}

	if e := svc.apiStore.DB(ctx).Model(&entry.ContractVersion{}).Where("id = ?", v1.VersionID).Update("artifact", contract.ArtifactRef(first.Revision)).Error; e != nil {
		t.Fatal(e)
	}
	got, _ := svc.GetAPIVersionInfo(ctx, 71, getID)
	mappingKept := false
	for _, p := range got.Version.Plugins {
		if p.Name == "http_to_kitex" {
			var config struct {
				HeaderMapping map[string]string `json:"header_mapping"`
			}
			if err := json.Unmarshal([]byte(p.Config), &config); err != nil {
				t.Fatal(err)
			}
			mappingKept = config.HeaderMapping["x-user-id"] == "x-user-id" && config.HeaderMapping["x-device-id"] == "client-device"
		}
	}
	if !mappingKept {
		t.Fatal("contract replacement lost manual Kitex metadata mapping")
	}
	if got.Version.RequestPath != "/contract/get-v2" || len(got.Version.Match) != 1 {
		t.Fatal("identity or routing policy lost")
	}
	found := false
	for _, p := range got.Version.Plugins {
		if p.Name == "user_check" {
			found = true
		}
	}
	if !found {
		t.Fatal("manual plugin lost")
	}
	gone, _ := svc.GetAPIVersionInfo(ctx, 71, listID)
	if !gone.Api.IsDisable {
		t.Fatal("removed API still active")
	}
	manualInfo, _ := svc.GetAPIVersionInfo(ctx, 71, manualID)
	if manualInfo.Api.IsDisable {
		t.Fatal("manual API disabled")
	}
	var plugin map[string]interface{}
	for _, p := range got.Version.Plugins {
		if p.Name == "http_to_kitex" {
			json.Unmarshal([]byte(p.Config), &plugin)
		}
	}
	if plugin["idl_service"] != "Catalog" || plugin["service"] != up.Name {
		t.Fatal("wrong Kitex service mapping")
	}
	// Bad updates roll back generated APIs and versions together.
	conflicting := parse(`service Other {Res Get(1: Req req) (api.post="/manual")}`)
	if _, err = svc.SaveContract(ctx, 71, 1, up.Name, v2.VersionID, conflicting); err == nil {
		t.Fatal("manual route conflict accepted")
	}
	head, _ := svc.GetContract(ctx, 71, up.Name, 0)
	if head.VersionID != v2.VersionID {
		t.Fatal("failed import moved head")
	}
	if _, err = svc.SaveContract(ctx, 71, 1, up.Name, v1.VersionID, first); err == nil {
		t.Fatal("stale edit accepted")
	}
	if _, err = svc.GetContract(ctx, 72, up.Name, v2.VersionID); err == nil {
		t.Fatal("namespace isolation failed")
	}
	v3, err := svc.RestoreContract(ctx, 71, 1, up.Name, v2.VersionID, v1.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := svc.GetAPIVersionInfo(ctx, 71, listID)
	getRestored, _ := svc.GetAPIVersionInfo(ctx, 71, getID)
	if len(getRestored.Version.Match) != 1 || getRestored.Version.Match[0].Pattern != "first" {
		t.Fatal("header predicate not restored")
	}
	if restored.Api.IsDisable {
		t.Fatal("restore did not reenable removed API")
	}
	original, _ := svc.GetContract(ctx, 71, up.Name, v1.VersionID)
	if original.Revision != first.Revision {
		t.Fatal("history changed")
	}
	// Removing a method disables it; reintroducing it without annotations clears its old generated headers.
	empty, e := svc.SaveContract(ctx, 71, 1, up.Name, v3.VersionID, parse(`service Catalog { Res Get(1: Req req) }`))
	if e != nil {
		t.Fatal(e)
	}
	reintroduced, e := svc.SaveContract(ctx, 71, 1, up.Name, empty.VersionID, second)
	if e != nil {
		t.Fatal(e)
	}
	returned, _ := svc.GetAPIVersionInfo(ctx, 71, getID)
	if returned.Api.IsDisable || len(returned.Version.Match) != 0 {
		t.Fatal("reintroduced method retained old contract headers")
	}
	v3 = reintroduced
	// Import, update and removal of policy annotations keep API identity and manual plugins.
	policy := parse(`service Catalog {
 // 查询地址列表
 Res Get(1: Req req) (api.post="/contract/get-v2" api.router.header.appcode.key="x-contract-app" api.router.header.appcode.value="example-app" api.header.map.deviceId="x-contract-device" api.auth.plugins="param_check_header_user_id" api.auth.position="header" api.auth.key="x-user-id" api.auth.value="~=^[0-9a-f-]{36}$" api.response.wrap="true") }`)
	policyVersion, e := svc.SaveContract(ctx, 71, 1, up.Name, v3.VersionID, policy)
	if e != nil {
		t.Fatal(e)
	}
	annotated, _ := svc.GetAPIVersionInfo(ctx, 71, getID)
	if len(annotated.Version.Match) != 1 {
		t.Fatal("named header predicate not generated")
	}
	foundApp := false
	for _, m := range annotated.Version.Match {
		if m.Key == "x-contract-app" && m.Pattern == "example-app" && m.MatchType == "EQUAL" && m.Position == "header" {
			foundApp = true
		}
	}
	if !foundApp {
		t.Fatal("wrong named header predicate")
	}
	if annotated.Api.Name != "查询地址列表" {
		t.Fatal("method comment not used as API name")
	}
	enabled := map[string]string{}
	for _, p := range annotated.Version.Plugins {
		if !p.Disable {
			enabled[p.Name] = p.Config
		}
	}
	var transport struct {
		HeaderMapping map[string]string `json:"header_mapping"`
	}
	if err := json.Unmarshal([]byte(enabled["http_to_kitex"]), &transport); err != nil {
		t.Fatal(err)
	}
	if transport.HeaderMapping["x-contract-device"] != "deviceId" {
		t.Fatal("annotation missing from saved plugin", transport)
	}
	if enabled["response_wrap"] != "{}" || !strings.Contains(enabled["param_check_header_user_id"], `"match_text":"~=^[0-9a-f-]{36}$"`) {
		t.Fatal("missing generated plugin configs", enabled)
	}

	renamed := parse(strings.ReplaceAll(strings.ReplaceAll(policy.Files["idl/api.thrift"], `struct Req {1: optional string id} struct Res {1: optional string value} `, ""), `key="x-contract-app" api.router.header.appcode.value="example-app"`, `key="x-client-app" api.router.header.appcode.value="next-app"`))
	policyVersion, e = svc.SaveContract(ctx, 71, 1, up.Name, policyVersion.VersionID, renamed)
	if e != nil {
		t.Fatal(e)
	}
	renamedAPI, e := svc.GetAPIVersionInfo(ctx, 71, getID)
	if e != nil {
		t.Fatal(e)
	}
	foundApp = false
	for _, m := range renamedAPI.Version.Match {
		if m.Key == "x-contract-app" {
			t.Fatal("old named header retained")
		}
		if m.Key == "x-client-app" && m.Pattern == "next-app" {
			foundApp = true
		}
	}
	if !foundApp {
		t.Fatal("renamed header not generated on same API")
	}
	invalid := parse(`service Catalog { Res Get(1: Req req) (api.post="/contract/get-v2" api.auth.plugins="missing" api.auth.position="header" api.auth.key="id" api.auth.value="**") }`)
	if _, e = svc.SaveContract(ctx, 71, 1, up.Name, policyVersion.VersionID, invalid); e == nil {
		t.Fatal("missing plugin accepted")
	}
	updated, e := svc.SaveContract(ctx, 71, 1, up.Name, policyVersion.VersionID, second)
	if e != nil {
		t.Fatal(e)
	}
	clean, _ := svc.GetAPIVersionInfo(ctx, 71, getID)
	for _, m := range clean.Version.Match {
		if m.Key == "x-client-app" {
			t.Fatal("removed annotation left generated header")
		}
	}
	for _, p := range clean.Version.Plugins {
		if p.Name == "http_to_kitex" {
			var cfg struct {
				HeaderMapping map[string]string `json:"header_mapping"`
			}
			if err := json.Unmarshal([]byte(p.Config), &cfg); err != nil {
				t.Fatal(err)
			}
			if _, exists := cfg.HeaderMapping["x-contract-device"]; exists {
				t.Fatal("removed annotation retained transport mapping")
			}
		}

		if p.Name == "response_wrap" || p.Name == "param_check_header_user_id" {
			t.Fatal("removed annotation left generated plugin")
		}
	}
	// Migration preserves independent old sources (not the current contract), and
	// removes duplication from snapshots plus native version/history/publication data.
	legacySnapshot := *snapshotPtr(v1.VersionID, first.Revision, first.Files, v1.APIs)
	legacySnapshot.Id = 0
	legacySnapshot.NamespaceID = 71
	legacySnapshot.UpstreamID = up.Id
	for _, api := range v1.APIs {
		for _, p := range api.Plugins {
			if p.Name == "http_to_kitex" {
				var cfg map[string]interface{}
				_ = json.Unmarshal([]byte(p.Config), &cfg)
				delete(cfg, "contract")
				cfg["thrift_files"] = first.Files
				raw, _ := json.Marshal(cfg)
				p.Config = string(raw)
			}
		}
	}
	legacySnapshot.APIs, _ = json.Marshal(v1.APIs)
	if err = tx.Create(&legacySnapshot).Error; err != nil {
		t.Fatal(err)
	}
	if err = contract.MigrateArtifactData(tx, 71); err != nil {
		t.Fatal(err)
	}
	migrated, err := svc.GetContract(ctx, 71, up.Name, legacySnapshot.Id)
	if err != nil || !reflect.DeepEqual(migrated.Files, first.Files) {
		t.Fatal("historical source migration", err)
	}
	for _, api := range migrated.APIs {
		for _, p := range api.Plugins {
			if strings.Contains(p.Config, "thrift_files") {
				t.Fatal("historical API still duplicates source")
			}
		}
	}
	if err = contract.MigrateArtifactData(tx, 71); err != nil {
		t.Fatal("migration is not repeatable", err)
	}
	var count int64
	if err = tx.Model(&entry.ContractArtifact{}).Where("namespace_id = ? AND revision = ?", 71, first.Revision).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("identical bundles were duplicated", count, err)
	}
	v3 = updated
}

func snapshotPtr(id int, revision string, files map[string]string, apis []*dto.APIInfo) *entry.ContractVersion {
	raw, _ := json.Marshal(files)
	snapshot, _ := json.Marshal(apis)
	return &entry.ContractVersion{Id: id, Revision: revision, Files: raw, APIs: snapshot, CreateTime: time.Now()}
}

func TestContractMySQLManualLifecycle(t *testing.T) {
	dsn := os.Getenv("TEST_CONTRACT_MYSQL_DSN")
	if dsn == "" {
		t.Skip("requires explicitly configured MySQL development database")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	// Initialize current models before starting the rollback-only data transaction.
	idb := store_mysql.NewMyDB(db, store.DBInfo{})
	store.InitStoreDB(idb)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	svc := NewAPIService().(*apiService)
	svc.commonGroup = contractGroups{db: idb}
	svc.contractPluginCatalog = contractCatalog{}
	svc.dynamicService = contractUpstreams{db: idb}
	ctx := context.WithValue(context.Background(), store.TxContextKey, tx)
	up := dynamic_entry.Dynamic{NamespaceId: 72, Name: "contract-test-" + uuid.New(), Profession: "service", Driver: "http", CreateTime: time.Now(), UpdateTime: time.Now()}
	if err = tx.Create(&up).Error; err != nil {
		t.Fatal(err)
	}

	parse := func(file, source string, include bool) *contract.Bundle {
		t.Helper()
		b, e := contract.Parse(map[string]string{file: `struct Req {1: optional string id} struct Res {1: optional string value} ` + source})
		if e != nil {
			t.Fatal(e)
		}
		b.IncludeUnannotated = include
		return b
	}
	first := parse("idl/api.thrift", `service Catalog { Res Auto(1: Req req) (api.post='/contract/auto') Res Internal(1: Req req) void Unsupported() }`, true)
	v1, err := svc.SaveContract(ctx, 72, 1, up.Name, 0, first)
	if err != nil {
		t.Fatal(err)
	}
	autoID := v1.APIs[0].UUID
	if _, err = svc.CreateContractAPI(ctx, 72, 1, up.Name, &api.ContractManualAPI{BaseVersion: v1.VersionID, File: "idl/api.thrift", Service: "Catalog", Method: "Unsupported", HTTPMethod: "POST", Path: "/unsupported"}); err == nil {
		t.Fatal("unsupported RPC signature accepted")
	}
	v2, err := svc.CreateContractAPI(ctx, 72, 1, up.Name, &api.ContractManualAPI{BaseVersion: v1.VersionID, File: "idl/api.thrift", Service: "Catalog", Method: "Internal", HTTPMethod: "POST", Path: "/contract/manual"})
	if err != nil {
		t.Fatal(err)
	}
	var manualID string
	for _, input := range v2.APIs {
		if v2.Sources[input.UUID] == "manual" {
			manualID = input.UUID
		}
	}
	if manualID == "" || len(v2.APIs) != 2 {
		t.Fatal("missing manual binding/snapshot")
	}
	info, err := svc.GetAPIVersionInfo(ctx, 72, manualID)
	if err != nil {
		t.Fatal(err)
	}
	input := contractInput(info)
	input.Timeout = 9876
	input.RequestPath = "/contract/custom"
	input.RequestPathLabel = input.RequestPath
	input.ProxyPath = input.RequestPath
	input.Method = []string{"PATCH"}
	if _, _, err = svc.CreateAPI(ctx, 72, 1, input); err != nil {
		t.Fatal(err)
	}
	info, err = svc.GetAPIVersionInfo(ctx, 72, manualID)
	if err != nil {
		t.Fatal(err)
	}
	bad := contractInput(info)
	bad.Plugins = nil
	if _, _, err = svc.CreateAPI(ctx, 72, 1, bad); err == nil {
		t.Fatal("bound API silently detached")
	}
	// Removing annotations and moving files must not remove or overwrite a manual API.
	moved := parse("idl/moved.thrift", `service Catalog { Res Auto(1: Req req) Res Internal(1: Req req) }`, false)
	v3, err := svc.SaveContract(ctx, 72, 1, up.Name, v2.VersionID, moved)
	if err != nil {
		t.Fatal(err)
	}
	assertAPI := func(version *api.ContractInfo, id string, disabled bool, path, source string) {
		t.Helper()
		for _, item := range version.APIs {
			if item.UUID == id {
				if item.IsDisable != disabled || item.RequestPath != path || version.Sources[id] != source {
					t.Fatalf("wrong snapshot: %+v source=%s", item, version.Sources[id])
				}
				return
			}
		}
		t.Fatal("missing bound API")
	}
	assertAPI(v3, autoID, true, "/contract/auto", "auto")
	assertAPI(v3, manualID, false, "/contract/custom", "manual")
	info, err = svc.GetAPIVersionInfo(ctx, 72, manualID)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version.Timeout != 9876 || !reflect.DeepEqual(info.Version.Method, []string{"PATCH"}) {
		t.Fatal("manual settings overwritten")
	}
	var cfg map[string]string
	if err = json.Unmarshal([]byte(info.Version.Plugins[0].Config), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["main_idl"] != "idl/moved.thrift" || cfg["contract"] != contract.ArtifactRef(moved.Revision) {
		t.Fatal("manual contract reference not updated")
	}
	// Even newly added HTTP/policy annotations do not take over a manual route.
	annotated := parse("idl/moved.thrift", `service Catalog { Res Auto(1: Req req) Res Internal(1: Req req) (api.get='/ignored' api.auth.plugins='missing' api.auth.position='header' api.auth.key='id' api.auth.value='**') }`, false)
	v4, err := svc.SaveContract(ctx, 72, 1, up.Name, v3.VersionID, annotated)
	if err != nil {
		t.Fatal(err)
	}
	assertAPI(v4, manualID, false, "/contract/custom", "manual")
	// Explicit takeover after removing HTTP annotations keeps the original API UUID.
	takeover, err := svc.CreateContractAPI(ctx, 72, 1, up.Name, &api.ContractManualAPI{BaseVersion: v4.VersionID, File: "idl/moved.thrift", Service: "Catalog", Method: "Auto", HTTPMethod: "PUT", Path: "/contract/auto-manual"})
	if err != nil {
		t.Fatal(err)
	}
	assertAPI(takeover, autoID, false, "/contract/auto-manual", "manual")
	removed := parse("types.thrift", "", false)
	gone, err := svc.SaveContract(ctx, 72, 1, up.Name, takeover.VersionID, removed)
	if err != nil {
		t.Fatal(err)
	}
	assertAPI(gone, manualID, true, "/contract/custom", "manual")
	assertAPI(gone, autoID, true, "/contract/auto-manual", "manual")
	restored, err := svc.RestoreContract(ctx, 72, 1, up.Name, gone.VersionID, v4.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	assertAPI(restored, manualID, false, "/contract/custom", "manual")
	assertAPI(restored, autoID, true, "/contract/auto", "auto")
	var binding entry.ContractBinding
	if err = tx.Where("api_uuid = ?", autoID).First(&binding).Error; err != nil || binding.Source != "auto" {
		t.Fatal("rollback did not restore source", err)
	}
	// Rolling back before manual creation disables the later API without changing its UUID.
	before, err := svc.RestoreContract(ctx, 72, 1, up.Name, restored.VersionID, v1.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	assertAPI(before, autoID, false, "/contract/auto", "auto")
	assertAPI(before, manualID, true, "/contract/custom", "manual")
	again, err := svc.RestoreContract(ctx, 72, 1, up.Name, before.VersionID, v2.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	assertAPI(again, manualID, false, "/contract/manual", "manual")
	// An outdated modal cannot create a duplicate binding or change a newer version.
	if _, err = svc.CreateContractAPI(ctx, 72, 1, up.Name, &api.ContractManualAPI{BaseVersion: v2.VersionID, File: "idl/api.thrift", Service: "Catalog", Method: "Internal", HTTPMethod: "POST", Path: "/duplicate"}); err == nil {
		t.Fatal("stale manual create accepted")
	}
}

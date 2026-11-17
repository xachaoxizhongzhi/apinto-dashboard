package controller

import (
	api "github.com/eolinker/apinto-dashboard/modules/api"
	dto "github.com/eolinker/apinto-dashboard/modules/api/api-dto"
	"testing"
)

func TestContractRowsMergeMethodCatalogueAndSnapshots(t *testing.T) {
	snapshot := func(id, method string, disabled bool) *dto.APIInfo {
		return &dto.APIInfo{UUID: id, ApiName: method, Method: []string{"POST"}, RequestPath: "/old", IsDisable: disabled, Plugins: []*dto.APIPlugin{{Name: "http_to_kitex", Config: `{"main_idl":"auth.thrift","idl_service":"Auth","method":"` + method + `"}`}}}
	}
	info := &api.ContractInfo{
		IncludeUnannotated: true,
		Files: map[string]string{"auth.thrift": `service Auth {
   // 登录
   string Login() (api.post='/login')
   void Internal()
   void Logout()
  }`},
		APIs: []*dto.APIInfo{snapshot("login", "Login", false), snapshot("logout", "Logout", true), snapshot("removed", "Removed", true)},
	}
	rows, err := contractRows(info)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows: %+v", rows)
	}
	if rows[0].Contract != "Auth.Login" || rows[0].Name != "登录" || rows[0].UUID != "login" || rows[0].RequestPath != "/login" {
		t.Fatalf("login: %+v", rows[0])
	}
	if rows[1].Contract != "Auth.Internal" || rows[1].UUID != "" || len(rows[1].Method) != 0 || rows[1].Removed {
		t.Fatalf("internal: %+v", rows[1])
	}
	if rows[2].UUID != "logout" || !rows[2].IsDisable || len(rows[2].Method) != 0 || rows[2].RequestPath != "" || rows[2].Removed {
		t.Fatalf("removed HTTP annotation: %+v", rows[2])
	}
	if !rows[3].Removed || rows[3].UUID != "removed" || !rows[3].IsDisable {
		t.Fatalf("removed method: %+v", rows[3])
	}
	if len(info.APIs) != 3 || info.APIs[0].RequestPath != "/old" {
		t.Fatal("display changed publication snapshots")
	}
}

func TestContractRowsUnannotatedOptIn(t *testing.T) {
	info := &api.ContractInfo{Files: map[string]string{"auth.thrift": `service Auth { void Internal() string Login() (api.post='/login') }`}}
	for _, enabled := range []bool{false, true} {
		info.IncludeUnannotated = enabled
		rows, err := contractRows(info)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if enabled {
			want = 2
		}
		if len(rows) != want {
			t.Fatalf("include_unannotated=%v: got %d rows, want %d", enabled, len(rows), want)
		}
	}
}

func TestContractRowsManualBindingWithoutCatalogOptIn(t *testing.T) {
	info := &api.ContractInfo{Files: map[string]string{"api.thrift": `service Catalog { string Internal() string Hidden() }`}, Sources: map[string]string{"manual": "manual"}, APIs: []*dto.APIInfo{{UUID: "manual", ApiName: "手动 API", Method: []string{"PATCH"}, RequestPath: "/custom", Plugins: []*dto.APIPlugin{{Name: "http_to_kitex", Config: `{"main_idl":"api.thrift","idl_service":"Catalog","method":"Internal"}`}}}}}
	rows, err := contractRows(info)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].UUID != "manual" || rows[0].Removed || rows[0].AutoConfigure || rows[0].Source != "manual" || rows[0].RequestPath != "/custom" || rows[0].Method[0] != "PATCH" {
		t.Fatalf("manual binding lost/hidden: %+v", rows)
	}
}

func TestContractRowsMovedMethodRetainsBinding(t *testing.T) {
	info := &api.ContractInfo{Files: map[string]string{"new.thrift": `service Catalog { string Get() }`}, Sources: map[string]string{"bound": "auto"}, APIs: []*dto.APIInfo{{UUID: "bound", ApiName: "Get", Method: []string{"GET"}, RequestPath: "/old", IsDisable: true, Plugins: []*dto.APIPlugin{{Name: "http_to_kitex", Config: `{"main_idl":"old.thrift","idl_service":"Catalog","method":"Get"}`}}}}}
	rows, err := contractRows(info)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].UUID != "bound" || rows[0].Removed || rows[0].File != "new.thrift" || rows[0].AutoConfigure {
		t.Fatalf("file move lost binding: %+v", rows)
	}
}

package contract

import (
	"archive/zip"
	"bytes"
	"errors"
	"github.com/cloudwego/thriftgo/parser"
	"strings"
	"testing"
)

const typesIDL = `struct Req { 1: optional string id } struct Res { 1: optional string name }`

func zipFiles(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for n, s := range files {
		w, e := z.Create(n)
		if e != nil {
			t.Fatal(e)
		}
		w.Write([]byte(s))
	}
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestMultiFileContract(t *testing.T) {
	files := map[string]string{"idl/types.thrift": typesIDL, "idl/api.thrift": `include "types.thrift"
 service Catalog { types.Res Get(1: types.Req req) (api.get="/catalog") }
 service Stock { types.Res Get(1: types.Req req) (api.post="/stock") }`}
	b, e := ReadZIP(zipFiles(t, files))
	if e != nil {
		t.Fatal(e)
	}
	if len(b.Endpoints) != 2 || b.Endpoints[0].Key() != "Catalog/Get" || b.Endpoints[1].Key() != "Stock/Get" {
		t.Fatalf("wrong endpoints: %+v", b)
	}
	same, e := Parse(files)
	if e != nil || same.Revision != b.Revision {
		t.Fatal("unstable revision", e)
	}
	moved := map[string]string{"contract/types.thrift": typesIDL, "contract/api.thrift": files["idl/api.thrift"]}
	next, e := Parse(moved)
	if e != nil || next.Endpoints[0].Key() != b.Endpoints[0].Key() {
		t.Fatal("file path changed identity", e)
	}
}
func TestRejectInvalidContracts(t *testing.T) {
	tests := map[string]map[string]string{
		"traversal":          {"../api.thrift": typesIDL},
		"non IDL":            {"idl/readme.txt": "x"},
		"missing include":    {"api.thrift": `include "missing.thrift"`},
		"cycle":              {"a.thrift": `include "b.thrift"`, "b.thrift": `include "a.thrift"`},
		"syntax":             {"api.thrift": "service {"},
		"type":               {"api.thrift": `service A { Missing Get(1: Missing req) (api.get="/a") }`},
		"duplicate identity": {"a.thrift": typesIDL + ` service A { Res Get(1: Req req) (api.get="/a") }`, "b.thrift": typesIDL + ` service A { Res Get(1: Req req) (api.get="/b") }`},
		"duplicate route":    {"api.thrift": typesIDL + ` service A { Res Get(1: Req req) (api.get="/a") Res List(1: Req req) (api.get="/a") }`},
		"multiple mappings":  {"api.thrift": typesIDL + ` service A { Res Get(1: Req req) (api.get="/a", api.post="/a") }`},
		"primitive request":  {"api.thrift": typesIDL + ` service A { Res Get(1: string req) (api.get="/a") }`},
	}
	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			if _, e := ReadZIP(zipFiles(t, files)); e == nil {
				t.Fatal("accepted invalid archive")
			}
		})
	}
	if _, e := ReadZIP([]byte("not ZIP")); e == nil {
		t.Fatal("accepted non ZIP")
	}
}
func TestEmptyHTTPSetAndAnnotationNames(t *testing.T) {
	b, e := Parse(map[string]string{"api.thrift": typesIDL + ` service A { Res Get(1: Req req) }`})
	if e != nil || len(b.Endpoints) != 0 {
		t.Fatal(b, e)
	}
	b, e = Parse(map[string]string{"api.thrift": typesIDL + ` service A { Res Get(1: Req req) (get="/not-http") }`})
	if e != nil || len(b.Endpoints) != 0 {
		t.Fatal("non api annotation interpreted as route", e)
	}
}
func TestLimitAndDuplicateZIP(t *testing.T) {
	if _, e := ReadZIP(make([]byte, MaxArchive+1)); e == nil {
		t.Fatal("size limit")
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for i := 0; i < 2; i++ {
		w, _ := z.Create("a.thrift")
		w.Write([]byte(typesIDL))
	}
	z.Close()
	if _, e := ReadZIP(b.Bytes()); e == nil {
		t.Fatal("duplicate ZIP entry")
	}
	if _, e := Parse(map[string]string{"a.thrift": strings.Repeat(" ", MaxContents+1)}); e == nil {
		t.Fatal("expanded limit")
	}
}

func TestRejectLegacyHeaderAnnotations(t *testing.T) {
	for _, annotation := range []string{`api.header.x-app-code='a'`, `api.header.appcode='a'`, `apinto_route_header_782d6170702d636f6465='a'`} {
		if _, err := Parse(map[string]string{"api.thrift": typesIDL + ` service A { Res Get(1:Req req)(api.get='/a' ` + annotation + `) }`}); !errors.Is(err, ErrLegacyHeader) {
			t.Fatalf("legacy route accepted: %s: %v", annotation, err)
		}
	}
	source := `// api.header.x-fake='comment'
 const string example="api.header.x-fake='literal'"
 struct Req {1: optional string user (api.header='x-user-id')}
 struct Res {1: optional string value}
 service A { Res Get(1:Req req)(api.get='/a') }`
	b, err := Parse(map[string]string{"api.thrift": source})
	if err != nil || b.RuntimeFiles["api.thrift"] != source {
		t.Fatal("comments, literals or native field binding changed", err)
	}
	if _, err = parser.ParseString("api.thrift", b.RuntimeFiles["api.thrift"]); err != nil {
		t.Fatal(err)
	}
}

func TestMethodPolicyAnnotations(t *testing.T) {
	source := typesIDL + ` service Address {
 // 查询地址列表
 // 返回当前用户的地址
 Res List(1: Req request) (
 api.post='/api/address/list',
 api.router.header.appcode.key='x-app-code'
 api.router.header.appcode.value='catalog'
 api.auth.plugins='param_check_header_user_id'
 api.auth.position='header'
 api.auth.key='x-user-id'
 api.auth.value='~=^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
 api.response.wrap='true'
 )
 }`
	b, err := Parse(map[string]string{"api.thrift": source})
	if err != nil {
		t.Fatal(err)
	}
	ep := b.Endpoints[0]
	if ep.DisplayName() != "查询地址列表" || ep.Description != "查询地址列表\n返回当前用户的地址" || ep.Auth == nil || ep.Auth.Plugin != "param_check_header_user_id" || ep.Auth.Key != "x-user-id" || ep.ResponseWrap == nil || !*ep.ResponseWrap {
		t.Fatalf("bad policy: %+v", ep)
	}
	for _, replacement := range []string{"api.auth.plugin='oops'", "api.auth.key=''", "api.auth.value='~=['", "api.response.wrap='yes'", "api.auth.plugins='response_wrap'"} {
		broken := strings.Replace(source, "api.auth.key='x-user-id'", replacement, 1)
		if _, err := Parse(map[string]string{"api.thrift": broken}); err == nil {
			t.Fatal("invalid policy accepted", replacement)
		}
	}
	block, err := Parse(map[string]string{"api.thrift": typesIDL + ` service A {
 /**
  * 查询列表
  * 详细说明
  */
 Res List(1:Req req)(api.get='/list')
 Res Get(1:Req req)(api.get='/get')
 }`})
	if err != nil || block.Endpoints[0].DisplayName() != "查询列表" || block.Endpoints[1].DisplayName() != "A.Get" {
		t.Fatal("comment naming", block, err)
	}
}

func TestNamedRouterHeaders(t *testing.T) {
	source := typesIDL + `
 // 系统配置服务
 service System {
 // 根据配置类型查询配置列表
 Res List(1: Req request) (
 api.get='/api/system/config/list',
 api.router.header.appcode.key='X-App-Code'
 api.router.header.appcode.value='catalog-app'
 api.router.header.env.value='sit'
 api.router.header.env.key='x-env'
 api.auth.plugins='param_check_header_user_id'
 api.auth.position='header'
 api.auth.key='x-user-id'
 api.auth.value='~=^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
 api.response.wrap='true'
 )
 Res Other(1: Req request) (api.get='/api/system/config/list' api.router.header.appcode.key='x-app-code' api.router.header.appcode.value='other')
 }`
	b, err := ReadZIP(zipFiles(t, map[string]string{"idl/system.thrift": source}))
	if err != nil {
		t.Fatal(err)
	}
	ep := b.Endpoints[0]
	if ep.DisplayName() != "根据配置类型查询配置列表" || ep.Headers["x-app-code"] != "catalog-app" || ep.Headers["x-env"] != "sit" || len(ep.Headers) != 2 || ep.Auth == nil || ep.Auth.Key != "x-user-id" || ep.ResponseWrap == nil || !*ep.ResponseWrap {
		t.Fatalf("wrong endpoint: %+v", ep)
	}
	if b.Files["idl/system.thrift"] != source || b.RuntimeFiles["idl/system.thrift"] != source {
		t.Fatal("standard annotations changed")
	}
	again, err := Parse(b.RuntimeFiles)
	if err != nil || again.Endpoints[0].Headers["x-app-code"] != "catalog-app" {
		t.Fatal("runtime snapshot roundtrip", err)
	}
	if _, err := Parse(map[string]string{"idl/system.thrift": strings.Replace(source, "value='other'", "value='catalog-app'", 1)}); err == nil {
		t.Fatal("overlapping routes accepted")
	}
}

func TestRejectInvalidNamedRouterHeaders(t *testing.T) {
	for _, annotation := range []string{
		`api.router.appcode.key='x-app-code' api.router.appcode.value='a'`,
		`api.router.header.appcode.key='x-app-code'`,
		`api.router.header.appcode.value='a'`,
		`api.router.header.appcode.key='' api.router.header.appcode.value='a'`,
		`api.router.header.appcode.key='x-app-code' api.router.header.appcode.value=''`,
		`api.router.header.appcode.key='bad key' api.router.header.appcode.value='a'`,
		`api.router.header.appcode.key='x-app-code' api.router.header.appcode.key='x-other' api.router.header.appcode.value='a'`,
		`api.router.header.appcode.key='x-app-code' api.router.header.appcode.value='a' api.router.header.appcode.value='b'`,
		`api.router.header.appcode.key='x-app-code' api.router.header.appcode.value='a' api.router.header.other.key='X-App-Code' api.router.header.other.value='b'`,
		`api.router.header.appcode.key='x-app-code' api.router.header.appcode.value='a' api.header.X-App-Code='a'`,
		`api.router.header.appcode.name='x-app-code' api.router.header.appcode.value='a'`,
		`api.router.query.appcode.key='x-app-code' api.router.query.appcode.value='a'`,
		"api.router.header.appcode.key='x-app-code' api.router.header.appcode.value='bad\nvalue'",
	} {
		t.Run(annotation, func(t *testing.T) {
			if _, err := Parse(map[string]string{"api.thrift": typesIDL + ` service A { Res Get(1:Req req)(api.get='/a' ` + annotation + `) }`}); err == nil {
				t.Fatal("invalid routing annotation accepted")
			}
		})
	}
}

func TestHeaderMetadataAnnotations(t *testing.T) {
	source := typesIDL + ` service A {
 // 查询资料
 Res Get(1:Req req)(api.get='/a'
 api.router.header.appcode.key='x-app-code' api.router.header.appcode.value='example'
 api.header.map.userId='X-User-Id'
 api.header.map.deviceId='x-device-id'
 api.response.wrap='true') }`
	b, err := ReadZIP(zipFiles(t, map[string]string{"api.thrift": source}))
	if err != nil {
		t.Fatal(err)
	}
	ep := b.Endpoints[0]
	if len(ep.HeaderMapping) != 2 || ep.HeaderMapping["x-user-id"] != "userId" || ep.HeaderMapping["x-device-id"] != "deviceId" {
		t.Fatal("wrong mapping direction", ep.HeaderMapping)
	}
	if len(ep.Headers) != 1 || ep.Headers["x-app-code"] != "example" || ep.Auth != nil {
		t.Fatal("transport mapping became route/auth policy")
	}
	if b.RuntimeFiles["api.thrift"] != source {
		t.Fatal("IDL changed")
	}
	for _, annotation := range []string{
		`api.header.map.='x-user-id'`, `api.header.map.userId=''`, `api.header.map.userId='bad name'`,
		`api.header.map.userId='x-user-id' api.header.map.userId='x-other'`,
		`api.header.map.userId='x-user-id' api.header.map.other='X-User-Id'`,
	} {
		if _, err := Parse(map[string]string{"api.thrift": typesIDL + ` service A { Res Get(1:Req req)(api.get='/a' ` + annotation + `) }`}); err == nil {
			t.Fatal("invalid mapping accepted", annotation)
		}
	}
}

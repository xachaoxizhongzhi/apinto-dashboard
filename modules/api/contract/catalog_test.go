package contract

import "testing"

func TestListMethodsIncludesUnannotatedMethods(t *testing.T) {
	files := map[string]string{
		"idl/auth.thrift": `include "types.thrift"
service Auth {
 // 用户登录
 types.Res Login(1: types.Req req) (api.post='/login')
 // 内部注销
 void Logout(1: string id, 2: string reason)
}`,
		"idl/types.thrift":  typesIDL,
		"idl/system.thrift": `service System { string Ping() }`,
	}
	methods, err := ListMethods(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 3 {
		t.Fatalf("methods: %+v", methods)
	}
	if methods[0].DisplayName() != "用户登录" || methods[0].Key() != "Auth/Login" || methods[0].HTTPMethod != "POST" || methods[0].Path != "/login" {
		t.Fatalf("login: %+v", methods[0])
	}
	if methods[1].DisplayName() != "内部注销" || methods[1].HTTPMethod != "" || methods[1].Path != "" {
		t.Fatalf("logout: %+v", methods[1])
	}
	if methods[2].DisplayName() != "System.Ping" || methods[2].File != "idl/system.thrift" {
		t.Fatalf("ping: %+v", methods[2])
	}
	bundle, err := Parse(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Endpoints) != 1 {
		t.Fatalf("unannotated methods must not create routes: %+v", bundle.Endpoints)
	}
}

func TestListMethodsRejectsInvalidIDL(t *testing.T) {
	if _, err := ListMethods(map[string]string{"bad.thrift": "service {"}); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestMethodInventoryIndependentOfHTTPAndDisplayOption(t *testing.T) {
	bundle, err := Parse(map[string]string{"api.thrift": typesIDL + ` service Catalog { Res Internal(1: Req req) void Notify() }`})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Endpoints) != 0 || len(bundle.Methods) != 2 || !bundle.Methods[0].Callable || bundle.Methods[1].Callable {
		t.Fatalf("incorrect method inventory: %+v", bundle.Methods)
	}
}

package contract

import (
	"github.com/cloudwego/thriftgo/parser"
	"strings"
	"testing"
)

const namedPolicy = `api.permission.plugins='param_check_header_user_permissions'
api.permission.position='header'
api.permission.key='x-user-permission'
api.permission.mode='contains'
api.permission.value='user:banner:list'
api.auth.plugins='param_check_header_user_id'
api.auth.position='header'
api.auth.key='x-user-id'
api.auth.mode='regex'
api.auth.value='~=^[0-9a-fA-F-]{36}$'`

func policySource(policy string) string {
	return typesIDL + ` service Catalog { Res Get(1: Req req) (api.get='/test' ` + policy + `) }`
}
func TestNamedPolicies(t *testing.T) {
	source := policySource(namedPolicy)
	b, err := ReadZIP(zipFiles(t, map[string]string{"idl/api.thrift": source}))
	if err != nil {
		t.Fatal(err)
	}
	rules := b.Endpoints[0].AuthRules()
	if len(rules) != 2 || rules[0].Plugin != "param_check_header_user_id" || rules[0].Value != "^[0-9a-fA-F-]{36}$" || rules[1].Mode != "contains" {
		t.Fatalf("wrong rules: %+v", rules)
	}
	if b.Files["idl/api.thrift"] != source || b.RuntimeFiles["idl/api.thrift"] != source {
		t.Fatal("original source modified")
	}
	if _, err = parser.ParseString("idl/api.thrift", b.RuntimeFiles["idl/api.thrift"]); err != nil {
		t.Fatal("Kitex receives invalid Thrift", err)
	}
	plain := strings.ReplaceAll(namedPolicy, "~=^", "^")
	if _, err = Parse(map[string]string{"api.thrift": policySource(plain)}); err != nil {
		t.Fatal(err)
	}
}
func TestRejectNamedPolicies(t *testing.T) {
	for name, policy := range map[string]string{
		"missing mode":               strings.Replace(namedPolicy, "api.permission.mode='contains'", "", 1),
		"duplicate key":              namedPolicy + ` api.permission.key='contains'`,
		"unknown mode":               strings.Replace(namedPolicy, "'contains'", "'substring'", 1),
		"bad regex":                  strings.Replace(namedPolicy, "~=^[0-9a-fA-F-]{36}$", "~=[", 1),
		"nested permission rejected": strings.ReplaceAll(namedPolicy, "api.permission.", "api.auth.permission."),
		"bracket syntax":             strings.ReplaceAll(namedPolicy, "api.permission.", "api.auth[1]."),
		"duplicate plugin":           strings.ReplaceAll(namedPolicy, "param_check_header_user_permissions", "param_check_header_user_id"),
		"unknown field":              namedPolicy + ` api.permission.keys='x'`,
		"multiple permissions":       strings.Replace(namedPolicy, "user:banner:list", "read,write", 1),
		"wrong position":             strings.Replace(namedPolicy, "api.permission.position='header'", "api.permission.position='query'", 1),
		"mixed syntax":               namedPolicy + ` api.auth.plugins='old'`,
		"reserved token":             strings.ReplaceAll(namedPolicy, "api.auth.mode", "api.auth.rule0.mode"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(map[string]string{"api.thrift": policySource(policy)}); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}
func TestOriginalAuthContractIsValidThrift(t *testing.T) {
	source := `// api.auth[0].key='comment'
/* api.auth[4].value='comment' */
const string literal = "api.auth[0].key='literal'"
` + policySource(namedPolicy)
	if _, err := parser.ParseString("api.thrift", source); err != nil {
		t.Fatal(err)
	}
	b, err := Parse(map[string]string{"api.thrift": source})
	if err != nil {
		t.Fatal(err)
	}
	if b.RuntimeFiles["api.thrift"] != source {
		t.Fatal("source was rewritten")
	}
	again, err := Parse(b.RuntimeFiles)
	if err != nil || len(again.Endpoints[0].AuthRules()) != 2 {
		t.Fatal("runtime reparse failed", err)
	}
}
func TestPermissionOnlyAndLegacyIdentity(t *testing.T) {
	permission := namedPolicy[:strings.Index(namedPolicy, "api.auth.plugins")]
	for _, source := range []string{permission, strings.Replace(namedPolicy, "api.auth.mode='regex'", "", 1)} {
		b, err := Parse(map[string]string{"api.thrift": policySource(source)})
		if err != nil {
			t.Fatal(err)
		}
		rules := b.Endpoints[0].AuthRules()
		if rules[len(rules)-1].Mode != "contains" {
			t.Fatal("permission missing")
		}
	}
}

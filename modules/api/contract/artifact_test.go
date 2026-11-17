package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompactHistoricalJSON(t *testing.T) {
	files := map[string]string{"idl/a.thrift": "struct A {1: string value}"}
	config, _ := json.Marshal(map[string]interface{}{"service": "svc", "main_idl": "idl/a.thrift", "method": "Get", "thrift_files": files, "header_mapping": map[string]string{"x-user-id": "userId"}})
	raw, _ := json.Marshal(map[string]interface{}{"plugins": []interface{}{map[string]interface{}{"name": "http_to_kitex", "config": string(config)}}, "unknown": json.RawMessage(`9007199254740993`), "unrelated_body": string(config)})
	puts := 0
	compact, changed, err := CompactJSON(raw, func(got map[string]string) (string, error) {
		puts++
		if got["idl/a.thrift"] != files["idl/a.thrift"] {
			t.Fatal("source changed")
		}
		return ArtifactRef(strings.Repeat("a", 64)), nil
	})
	if err != nil || !changed || puts != 1 {
		t.Fatal(changed, puts, err)
	}
	if strings.Count(string(compact), "thrift_files") != 1 || !strings.Contains(string(compact), "9007199254740993") || !strings.Contains(string(compact), "userId") {
		t.Fatal("unrelated fields changed", string(compact))
	}
	_, changed, err = CompactJSON(compact, func(map[string]string) (string, error) { t.Fatal("idempotent migration wrote again"); return "", nil })
	if err != nil || changed {
		t.Fatal(changed, err)
	}
}

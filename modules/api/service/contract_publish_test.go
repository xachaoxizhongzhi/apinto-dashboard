package api_service

import (
	"fmt"
	"strings"
	"testing"

	v2 "github.com/eolinker/apinto-dashboard/client/v2"
	"github.com/eolinker/apinto-dashboard/modules/api/contract"
)

type resourceClient struct {
	v2.IClient
	revision        string
	setErr, readErr error
	calls           int
}

func (c *resourceClient) Set(p, n string, w *v2.WorkerInfo[v2.BasicInfo]) error {
	c.calls++
	if p != "transcode" || n != "thrift-"+w.BasicInfo.Version {
		return fmt.Errorf("wrong resource")
	}
	return c.setErr
}
func (c *resourceClient) Version(p, n string) (string, error) { return c.revision, c.readErr }
func TestPrepareContractReadBack(t *testing.T) {
	revision := strings.Repeat("a", 64)
	ref := contract.ArtifactRef(revision)
	for _, tt := range []struct {
		name    string
		client  resourceClient
		wantErr bool
	}{
		{"success", resourceClient{revision: revision}, false},
		{"committed timeout", resourceClient{revision: revision, setErr: fmt.Errorf("timeout")}, false},
		{"missing after timeout", resourceClient{setErr: fmt.Errorf("timeout"), readErr: fmt.Errorf("missing")}, true},
		{"wrong revision", resourceClient{revision: "wrong"}, true},
		{"uncertain read", resourceClient{readErr: fmt.Errorf("timeout")}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ensureContractResource(&tt.client, ref, map[string]string{"a.thrift": ""})
			if (err != nil) != tt.wantErr {
				t.Fatal(err)
			}
			if tt.client.calls != 1 {
				t.Fatal("resource sent repeatedly")
			}
		})
	}
}

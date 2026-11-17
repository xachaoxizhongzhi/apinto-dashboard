package api_service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"strings"

	v2 "github.com/eolinker/apinto-dashboard/client/v2"
	api_dto "github.com/eolinker/apinto-dashboard/modules/api/api-dto"
	entry "github.com/eolinker/apinto-dashboard/modules/api/api-entry"
	"github.com/eolinker/apinto-dashboard/modules/api/contract"
	cluster "github.com/eolinker/apinto-dashboard/modules/cluster/cluster-model"
)

const contractPublishContextKey = "apinto.contract.publication"

type pinnedContractAPI struct {
	API     *entry.API
	Version *entry.APIVersion
}
type contractPublishState struct {
	pinned   map[string]pinnedContractAPI
	clients  map[string]v2.IClient
	versions map[string]map[string]string
}

func withContractPublish(ctx context.Context) context.Context {
	if ctx.Value(contractPublishContextKey) != nil {
		return ctx
	}
	state := &contractPublishState{clients: map[string]v2.IClient{}, versions: map[string]map[string]string{}}
	if ginCtx, ok := ctx.(*gin.Context); ok {
		ginCtx.Set(contractPublishContextKey, state)
		return ctx
	}
	return context.WithValue(ctx, contractPublishContextKey, state)
}

// All publication paths (including publishing a restored draft) prepare immutable
// resources before sending the route. No resource download happens on requests.
func (a *apiService) ensureAPIContracts(ctx context.Context, ns int, target *cluster.Cluster, version *entry.APIVersion) error {
	refs := map[string]bool{}
	for _, p := range version.Plugins {
		if p.Name != "http_to_kitex" {
			continue
		}
		var c struct {
			Contract string            `json:"contract"`
			Files    map[string]string `json:"thrift_files"`
		}
		if err := json.Unmarshal([]byte(p.Config), &c); err != nil {
			return err
		}
		if len(c.Files) > 0 {
			return fmt.Errorf("API 仍包含内嵌 IDL，请先迁移契约数据")
		}
		if _, err := contract.ArtifactRevision(c.Contract); err != nil {
			return err
		}
		refs[c.Contract] = true
	}
	if len(refs) == 0 {
		return nil
	}
	state, _ := ctx.Value(contractPublishContextKey).(*contractPublishState)
	if state == nil {
		state = &contractPublishState{clients: map[string]v2.IClient{}, versions: map[string]map[string]string{}}
	}
	key := target.Name + "@" + target.Addr
	client := state.clients[key]
	if client == nil {
		var err error
		client, err = v2.GetClusterClient(target.Name, target.Addr)
		if err != nil {
			return err
		}
		versions, err := client.Versions("transcode", "thrift")
		if err != nil {
			return err
		}
		state.clients[key] = client
		state.versions[key] = versions
	}
	for ref := range refs {
		revision, _ := contract.ArtifactRevision(ref)
		name := strings.TrimSuffix(ref, "@transcode")
		if current, ok := state.versions[key][name]; ok {
			if current != revision {
				return fmt.Errorf("网关契约资源 %s 校验版本不一致", name)
			}
			continue
		}
		files, err := contract.LoadArtifact(a.apiStore.DB(ctx), ns, ref)
		if err != nil {
			return err
		}
		if err = ensureContractResource(client, ref, files); err != nil {
			return err
		}
		state.versions[key][name] = revision
	}
	return nil
}

func ensureContractResource(client v2.IClient, ref string, files map[string]string) error {
	revision, err := contract.ArtifactRevision(ref)
	if err != nil {
		return err
	}
	name := strings.TrimSuffix(ref, "@transcode")
	setErr := client.Set("transcode", name, &v2.WorkerInfo[v2.BasicInfo]{BasicInfo: &v2.BasicInfo{Profession: "transcode", Name: name, Driver: "thrift", Version: revision}, Append: map[string]interface{}{"revision": revision, "thrift_files": files}})
	// A timeout may mean the resource was committed. Read its immutable revision
	// before retrying; never send routes when resource preparation is uncertain.
	actual, readErr := client.Version("transcode", name)
	if readErr == nil && actual == revision {
		return nil
	}
	if setErr != nil {
		return fmt.Errorf("契约资源准备失败: %w", setErr)
	}
	if readErr != nil {
		return fmt.Errorf("契约资源确认失败: %w", readErr)
	}
	return fmt.Errorf("契约资源版本不一致")
}

func (a *apiService) normalizeContractInput(ctx context.Context, ns int, input *api_dto.APIInfo) error {
	for _, p := range input.Plugins {
		if p.Name != "http_to_kitex" {
			continue
		}
		raw, _, err := contract.CompactJSON([]byte(p.Config), func(files map[string]string) (string, error) {
			return contract.PutArtifact(a.apiStore.DB(ctx), ns, files)
		})
		if err != nil {
			return err
		}
		var cfg struct {
			Contract string `json:"contract"`
		}
		if err = json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		revision, err := contract.ArtifactRevision(cfg.Contract)
		if err != nil {
			return err
		}
		var count int64
		if err = a.apiStore.DB(ctx).Model(&entry.ContractArtifact{}).Where("namespace_id = ? AND revision = ?", ns, revision).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("契约资源不存在或不属于当前工作空间")
		}
		p.Config = string(raw)
	}
	return a.validateContractBinding(ctx, ns, input)
}

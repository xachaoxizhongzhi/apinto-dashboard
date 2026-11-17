package api_service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	api "github.com/eolinker/apinto-dashboard/modules/api"
	dto "github.com/eolinker/apinto-dashboard/modules/api/api-dto"
	entry "github.com/eolinker/apinto-dashboard/modules/api/api-entry"
	"github.com/eolinker/apinto-dashboard/modules/api/contract"
	group "github.com/eolinker/apinto-dashboard/modules/group/group-service"
	"gorm.io/gorm"
)

func contractSource(source string) string {
	if source == "manual" {
		return source
	}
	return "auto" // Existing bindings and snapshots were generated from HTTP annotations.
}

// File names can change; the binding identity remains upstream + Service.Method.
func manualContractMethod(bundle *contract.Bundle, service, method string) (contract.Endpoint, bool, error) {
	var result contract.Endpoint
	found := false
	for _, ep := range bundle.Methods {
		if ep.Service != service || ep.Method != method {
			continue
		}
		if found {
			return result, false, fmt.Errorf("契约包含多个 %s.%s，无法确定绑定方法", service, method)
		}
		result, found = ep, true
	}
	if found && !result.Callable {
		return result, false, fmt.Errorf("%s.%s 必须为单个结构体请求及结构体响应的 unary 方法", service, method)
	}
	return result, found, nil
}

// Manual routing/policies remain owned by the API editor. Only its IDL reference moves.
func updateManualContract(input *dto.APIInfo, ep contract.Endpoint, artifact string) error {
	for _, plugin := range input.Plugins {
		if plugin.Name != "http_to_kitex" {
			continue
		}
		var config map[string]json.RawMessage
		if err := json.Unmarshal([]byte(plugin.Config), &config); err != nil || config == nil {
			return fmt.Errorf("API %s 的 Kitex 配置无效", input.ApiName)
		}
		var service, method string
		_ = json.Unmarshal(config["idl_service"], &service)
		_ = json.Unmarshal(config["method"], &method)
		if service != ep.Service || method != ep.Method {
			return fmt.Errorf("API %s 的 Kitex 方法与契约绑定不一致", input.ApiName)
		}
		config["contract"], _ = json.Marshal(artifact)
		config["main_idl"], _ = json.Marshal(ep.File)
		raw, err := json.Marshal(config)
		if err != nil {
			return err
		}
		plugin.Config = string(raw)
		return nil
	}
	return fmt.Errorf("API %s 缺少绑定契约所需的 http_to_kitex 配置", input.ApiName)
}

// CreateContractAPI creates an explicitly bound draft, then the normal API editor owns it.
func (a *apiService) CreateContractAPI(ctx context.Context, ns, operator int, name string, request *api.ContractManualAPI) (*api.ContractInfo, error) {
	if !strings.HasPrefix(request.Path, "/") || strings.ContainsAny(request.Path, "?#\r\n\t ") {
		return nil, fmt.Errorf("请输入有效的 HTTP 路径")
	}
	if request.HTTPMethod == "" {
		return nil, fmt.Errorf("请选择 HTTP 方法")
	}
	var versionID int
	err := a.apiStore.Transaction(ctx, func(tx context.Context) error {
		u, err := a.contractUpstream(tx, ns, name, true)
		if err != nil {
			return err
		}
		current, err := a.GetContract(tx, ns, name, 0)
		if err != nil {
			return err
		}
		if current.VersionID == 0 || current.VersionID != request.BaseVersion {
			return fmt.Errorf("契约已更新，请刷新后重试")
		}
		bundle, err := contract.Parse(current.Files)
		if err != nil {
			return err
		}
		ep, found, err := manualContractMethod(bundle, request.Service, request.Method)
		if err != nil {
			return err
		}
		if !found || ep.File != request.File {
			return fmt.Errorf("契约方法不存在或已变更，请刷新后重试")
		}
		if ep.HTTPMethod != "" {
			return fmt.Errorf("方法已有 HTTP 注解，请更新契约自动配置 API")
		}
		var binding entry.ContractBinding
		err = a.apiStore.DB(tx).Where("namespace_id = ? AND upstream_id = ? AND rpc_service = ? AND rpc_method = ?", ns, u.Id, ep.Service, ep.Method).First(&binding).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if binding.Id != 0 && contractSource(binding.Source) == "manual" {
			return fmt.Errorf("方法已关联手动 API，请刷新后编辑已有 API")
		}
		if binding.Id == 0 && !current.IncludeUnannotated {
			return fmt.Errorf("请先在更新契约时勾选解析无 HTTP 注解的方法")
		}

		groupName := u.Title
		if groupName == "" {
			groupName = u.Name
		}
		g, err := a.commonGroup.GetGroupByName(tx, ns, group.ApiName, groupName, "")
		if err != nil {
			return err
		}
		var groupID string
		if g != nil {
			groupID = g.Uuid
		} else {
			groupID, err = a.commonGroup.CreateGroup(tx, ns, operator, group.ApiName, group.ApiName, groupName, "", "")
			if err != nil {
				return err
			}
		}
		cfg, _ := json.Marshal(map[string]string{"contract": contract.ArtifactRef(bundle.Revision), "main_idl": ep.File, "idl_service": ep.Service, "method": ep.Method, "service": u.Name})
		input := &dto.APIInfo{ApiName: ep.DisplayName(), Desc: ep.Description, GroupUUID: groupID, Scheme: "http", Protocols: []string{"http"}, Timeout: 10000, ServiceName: u.Name + "@upstream", Method: []string{request.HTTPMethod}, RequestPath: request.Path, RequestPathLabel: request.Path, ProxyPath: request.Path, Plugins: []*dto.APIPlugin{{Name: "http_to_kitex", Config: string(cfg)}}}
		if binding.Id != 0 {
			info, e := a.GetAPIVersionInfo(tx, ns, binding.APIUUID)
			if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			if info != nil {
				input = contractInput(info)
				if e = updateManualContract(input, ep, contract.ArtifactRef(bundle.Revision)); e != nil {
					return e
				}
			}
			input.UUID = binding.APIUUID
			input.IsDisable = false
			input.Method = []string{request.HTTPMethod}
			input.RequestPath, input.RequestPathLabel, input.ProxyPath = request.Path, request.Path, request.Path
		}
		if err = a.GetAPIDriver("http").CheckInput(input); err != nil {
			return err
		}
		tx = context.WithValue(tx, contractSyncKey{}, map[string]bool{})
		id, _, err := a.CreateAPI(tx, ns, operator, input)
		if err != nil {
			return err
		}
		input.UUID = id
		if binding.Id == 0 {
			binding = entry.ContractBinding{NamespaceID: ns, UpstreamID: u.Id, RPCService: ep.Service, RPCMethod: ep.Method, APIUUID: id, Source: "manual"}
			if err = a.apiStore.DB(tx).Create(&binding).Error; err != nil {
				return err
			}
		} else if err = a.apiStore.DB(tx).Model(&binding).Update("source", "manual").Error; err != nil {
			return err
		}

		added, err := a.contractSnapshotRefs(tx, ns, map[string]*dto.APIInfo{id: input})
		if err != nil {
			return err
		}
		var previous entry.ContractVersion
		if err = a.apiStore.DB(tx).First(&previous, current.VersionID).Error; err != nil {
			return err
		}
		var refs []entry.ContractAPIRef
		if err = json.Unmarshal(previous.APIs, &refs); err != nil {
			return err
		}
		// Keep the other APIs pinned to their existing snapshot, not their current drafts.
		kept := make([]entry.ContractAPIRef, 0, len(refs)+1)
		for _, ref := range refs {
			if ref.UUID != id {
				kept = append(kept, ref)
			}
		}
		raw, err := json.Marshal(append(kept, added...))
		if err != nil {
			return err
		}
		next := &entry.ContractVersion{NamespaceID: ns, UpstreamID: u.Id, Artifact: previous.Artifact, Revision: previous.Revision, Files: []byte("{}"), APIs: raw, IncludeUnannotated: previous.IncludeUnannotated, Operator: operator, CreateTime: time.Now()}
		if err = a.apiStore.DB(tx).Create(next).Error; err != nil {
			return err
		}
		versionID = next.Id
		return nil
	})
	if err != nil {
		return nil, err
	}
	return a.GetContract(ctx, ns, name, versionID)
}

// Ordinary edits may change routing/policies, but must not silently retarget a bound API.
func (a *apiService) validateContractBinding(ctx context.Context, ns int, input *dto.APIInfo) error {
	if input.UUID == "" || ctx.Value(contractSyncKey{}) != nil {
		return nil
	}
	var b entry.ContractBinding
	err := a.apiStore.DB(ctx).Where("namespace_id = ? AND api_uuid = ?", ns, input.UUID).First(&b).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, p := range input.Plugins {
		if p.Name != "http_to_kitex" {
			continue
		}
		var cfg struct {
			Service string `json:"idl_service"`
			Method  string `json:"method"`
			Target  string `json:"service"`
		}
		if err = json.Unmarshal([]byte(p.Config), &cfg); err != nil {
			return err
		}
		u, e := a.contractUpstream(ctx, ns, strings.TrimSuffix(input.ServiceName, "@upstream"), false)
		if e != nil {
			return e
		}
		if u.Id == b.UpstreamID && cfg.Target == u.Name && cfg.Service == b.RPCService && cfg.Method == b.RPCMethod {
			return nil
		}
		break
	}
	return fmt.Errorf("已关联契约的 API 不能移除 Kitex 转换或修改绑定的上游、Service、Method")
}

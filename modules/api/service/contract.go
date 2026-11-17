package api_service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	api "github.com/eolinker/apinto-dashboard/modules/api"
	dto "github.com/eolinker/apinto-dashboard/modules/api/api-dto"
	entry "github.com/eolinker/apinto-dashboard/modules/api/api-entry"
	"github.com/eolinker/apinto-dashboard/modules/api/contract"
	model "github.com/eolinker/apinto-dashboard/modules/api/model"
	version_entry "github.com/eolinker/apinto-dashboard/modules/base/version-entry"
	dynamic "github.com/eolinker/apinto-dashboard/modules/dynamic/dynamic-entry"
	group "github.com/eolinker/apinto-dashboard/modules/group/group-service"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type contractSyncKey struct{}

func (a *apiService) contractUpstream(ctx context.Context, ns int, name string, lock bool) (*dynamic.Dynamic, error) {
	var u dynamic.Dynamic
	db := a.apiStore.DB(ctx).Where("namespace = ? AND profession = ? AND name = ?", ns, "service", name)
	if lock {
		db = db.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := db.First(&u).Error; err != nil {
		return nil, fmt.Errorf("上游不存在: %w", err)
	}
	if u.Driver != "http" {
		return nil, fmt.Errorf("契约管理仅支持 HTTP 上游")
	}
	return &u, nil
}
func (a *apiService) contractVersions(ctx context.Context, ns, id int) ([]*entry.ContractVersion, error) {
	versions := []*entry.ContractVersion{}
	err := a.apiStore.DB(ctx).Omit("files", "apis").Where("namespace_id = ? AND upstream_id = ?", ns, id).Order("id DESC").Find(&versions).Error
	return versions, err
}
func (a *apiService) GetContract(ctx context.Context, ns int, name string, version int) (*api.ContractInfo, error) {
	u, err := a.contractUpstream(ctx, ns, name, false)
	if err != nil {
		return nil, err
	}
	out := &api.ContractInfo{Files: map[string]string{}, APIs: []*dto.APIInfo{}}
	out.Versions, err = a.contractVersions(ctx, ns, u.Id)
	if err != nil {
		return nil, err
	}
	if len(out.Versions) == 0 {
		return out, nil
	}
	if version == 0 {
		version = out.Versions[0].Id
	}
	var v entry.ContractVersion
	if err = a.apiStore.DB(ctx).Where("id = ? AND namespace_id = ? AND upstream_id = ?", version, ns, u.Id).First(&v).Error; err != nil {
		return nil, err
	}
	var refs []entry.ContractAPIRef
	if err = json.Unmarshal(v.APIs, &refs); err != nil {
		return nil, err
	}
	out.Sources = map[string]string{}
	for _, ref := range refs {
		out.Sources[ref.UUID] = contractSource(ref.Source)
	}
	out.VersionID = v.Id
	out.Revision = v.Revision
	out.IncludeUnannotated = v.IncludeUnannotated
	if v.Artifact != "" {
		out.Files, err = contract.LoadArtifact(a.apiStore.DB(ctx), ns, v.Artifact)
	} else {
		err = json.Unmarshal(v.Files, &out.Files)
	}
	if err != nil {
		return nil, err
	}
	out.APIs, err = a.contractSnapshotAPIs(ctx, ns, v.APIs)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func contractInput(info *model.APIVersionInfo) *dto.APIInfo {
	v := info.Version
	item := info.Api
	out := &dto.APIInfo{UUID: item.UUID, ApiName: item.Name, Desc: item.Desc, GroupUUID: item.GroupUUID, Scheme: v.Scheme, IsDisable: v.IsDisable, RequestPath: v.RequestPathLabel, RequestPathLabel: v.RequestPathLabel, ServiceName: v.ServiceName, Method: v.Method, ProxyPath: v.ProxyPath, Hosts: v.Hosts, Timeout: v.Timeout, Retry: v.Retry, Match: v.Match, Header: v.Header, TemplateUUID: v.TemplateUUID, Protocols: v.Protocols, Plugins: []*dto.APIPlugin{}}
	if out.RequestPath == "" {
		out.RequestPath = v.RequestPath
	}
	for _, p := range v.Plugins {
		out.Plugins = append(out.Plugins, &dto.APIPlugin{Name: p.Name, Config: p.Config, Disable: p.Disable})
	}
	return out
}

func (a *apiService) SaveContract(ctx context.Context, ns, operator int, name string, base int, bundle *contract.Bundle) (*api.ContractInfo, error) {
	var id int
	err := a.apiStore.Transaction(ctx, func(tx context.Context) error {
		u, err := a.contractUpstream(tx, ns, name, true)
		if err != nil {
			return err
		}
		versions, err := a.contractVersions(tx, ns, u.Id)
		if err != nil {
			return err
		}
		current := 0
		if len(versions) > 0 {
			current = versions[0].Id
		}
		if current != base {
			return fmt.Errorf("契约已更新，请刷新后重试")
		}
		id, err = a.syncContract(tx, ns, operator, u, bundle, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return a.GetContract(ctx, ns, name, id)
}

func (a *apiService) syncContract(ctx context.Context, ns, operator int, u *dynamic.Dynamic, bundle *contract.Bundle, restore *api.ContractInfo) (int, error) {
	artifact, err := contract.PutArtifact(a.apiStore.DB(ctx), ns, bundle.Files)
	if err != nil {
		return 0, err
	}
	previousEndpoints := map[string]contract.Endpoint{}
	previousHeaders := map[string]map[string]string{}
	if restore == nil {
		previous, e := a.GetContract(ctx, ns, u.Name, 0)
		if e != nil {
			return 0, e
		}
		if previous.VersionID != 0 {
			parsed, e := contract.Parse(previous.Files)
			if errors.Is(e, contract.ErrLegacyHeader) {
				// Removed syntax is never parsed; saved API configurations supply
				// the cleanup baseline for a full replacement with a new contract.
				parsed = &contract.Bundle{Endpoints: savedContractEndpoints(previous.APIs)}
			} else if e != nil {
				return 0, e
			}
			for _, ep := range parsed.Endpoints {
				previousHeaders[ep.Key()] = ep.Headers
				previousEndpoints[ep.Key()] = ep
			}
		}
	} else {
		// Restored API snapshots already contain the target version's conditions.
		for _, ep := range bundle.Endpoints {
			previousHeaders[ep.Key()] = ep.Headers
			previousEndpoints[ep.Key()] = ep
		}
	}
	bindings := []entry.ContractBinding{}
	if err := a.apiStore.DB(ctx).Where("namespace_id = ? AND upstream_id = ?", ns, u.Id).Find(&bindings).Error; err != nil {
		return 0, err
	}
	byKey := map[string]*entry.ContractBinding{}
	ignore := map[string]bool{}
	for i := range bindings {
		b := &bindings[i]
		byKey[b.RPCService+"/"+b.RPCMethod] = b
		ignore[b.APIUUID] = true
	}
	ctx = context.WithValue(ctx, contractSyncKey{}, ignore)
	groupName := u.Title
	if groupName == "" {
		groupName = u.Name
	}
	g, err := a.commonGroup.GetGroupByName(ctx, ns, group.ApiName, groupName, "")
	if err != nil {
		return 0, err
	}
	groupID := ""
	if g != nil {
		groupID = g.Uuid
	} else {
		groupID, err = a.commonGroup.CreateGroup(ctx, ns, operator, group.ApiName, group.ApiName, groupName, "", "")
		if err != nil {
			return 0, err
		}
	}
	desired := map[string]*dto.APIInfo{}
	restored := map[string]*dto.APIInfo{}
	if restore != nil {
		for _, input := range restore.APIs {
			restored[input.UUID] = input
		}
		for i := range bindings {
			b := &bindings[i]
			if restored[b.APIUUID] == nil {
				continue
			}
			b.Source = contractSource(restore.Sources[b.APIUUID])
			if err := a.apiStore.DB(ctx).Model(b).Update("source", b.Source).Error; err != nil {
				return 0, err
			}
		}
	}
	automatic := *bundle
	automatic.Endpoints = nil
	for _, ep := range bundle.Endpoints {
		b := byKey[ep.Key()]
		if b == nil || contractSource(b.Source) != "manual" && (restore == nil || restored[b.APIUUID] != nil) {
			automatic.Endpoints = append(automatic.Endpoints, ep)
		}
	}
	if err := a.validateContractPlugins(ctx, ns, &automatic); err != nil {
		return 0, err
	}
	for _, ep := range automatic.Endpoints {
		b := byKey[ep.Key()]
		input := &dto.APIInfo{ApiName: ep.Service + "." + ep.Method, Desc: ep.Description, Scheme: "http", Protocols: []string{"http"}, Timeout: 10000, Plugins: []*dto.APIPlugin{}}
		if b != nil {
			info, e := a.GetAPIVersionInfo(ctx, ns, b.APIUUID)
			if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
				return 0, e
			}
			if info != nil {
				input = contractInput(info)
				// A method may return after being absent from the latest full snapshot.
				// Its disabled API still carries the last contract that generated its rules.
				if _, known := previousHeaders[ep.Key()]; !known && restore == nil {
					for _, plugin := range input.Plugins {
						if plugin.Name != "http_to_kitex" {
							continue
						}
						var config struct {
							Files map[string]string `json:"thrift_files"`
							Ref   string            `json:"contract"`
						}
						if e := json.Unmarshal([]byte(plugin.Config), &config); e != nil {
							return 0, e
						}
						if config.Ref != "" {
							config.Files, e = contract.LoadArtifact(a.apiStore.DB(ctx), ns, config.Ref)
							if e != nil {
								return 0, e
							}
						}
						if len(config.Files) == 0 {
							continue
						}
						old, e := contract.Parse(config.Files)
						if errors.Is(e, contract.ErrLegacyHeader) {
							old = &contract.Bundle{Endpoints: savedContractEndpoints([]*dto.APIInfo{input})}
						} else if e != nil {
							return 0, fmt.Errorf("读取旧契约路由条件失败: %w", e)
						}
						for _, endpoint := range old.Endpoints {
							if endpoint.Key() == ep.Key() {
								previousHeaders[ep.Key()] = endpoint.Headers
								previousEndpoints[ep.Key()] = endpoint
							}
						}
					}
				}
			} else {
				input.UUID = b.APIUUID
			}
		}
		input.ApiName = ep.DisplayName()
		input.Desc = ep.Description
		input.GroupUUID = groupID
		input.IsDisable = false
		input.ServiceName = u.Name + "@upstream"
		input.RequestPath = ep.Path
		input.RequestPathLabel = ep.Path
		input.ProxyPath = ep.Path
		input.Method = []string{ep.HTTPMethod}
		input.Match = syncContractHeaders(input.Match, previousHeaders[ep.Key()], ep.Headers)
		kitexConfig := map[string]interface{}{"service": u.Name, "idl_service": ep.Service, "method": ep.Method, "main_idl": ep.File, "contract": artifact}
		// Merge contract-owned transport mappings while preserving unrelated manual settings.
		existingMapping := map[string]string{}
		for _, plugin := range input.Plugins {
			if plugin.Name != "http_to_kitex" {
				continue
			}
			var previous struct {
				HeaderMapping map[string]string `json:"header_mapping"`
			}
			if err := json.Unmarshal([]byte(plugin.Config), &previous); err != nil {
				return 0, fmt.Errorf("读取 API %s 的 Kitex 请求头映射失败: %w", input.UUID, err)
			}
			if previous.HeaderMapping != nil {
				existingMapping = previous.HeaderMapping
			}
		}
		mapping := syncContractHeaderMapping(existingMapping, previousEndpoints[ep.Key()].HeaderMapping, ep.HeaderMapping)
		if len(mapping) > 0 {
			kitexConfig["header_mapping"] = mapping
		}
		cfg, _ := json.Marshal(kitexConfig)
		plugins := []*dto.APIPlugin{}
		for _, p := range input.Plugins {
			if p.Name != "http_to_kitex" {
				plugins = append(plugins, p)
			}
		}
		input.Plugins = syncContractPlugins(plugins, previousEndpoints[ep.Key()], ep)
		input.Plugins = append(input.Plugins, &dto.APIPlugin{Name: "http_to_kitex", Config: string(cfg)})
		if saved := restored[input.UUID]; saved != nil {
			input = saved
		}
		if err = a.GetAPIDriver("http").CheckInput(input); err != nil {
			return 0, err
		}
		if input.UUID == "" {
			// A newly generated broad route must not bypass a manually configured
			// route's header/auth conditions through fallback matching.
			existing, e := a.apiStore.GetListByRequestPath(ctx, ns, input.RequestPath)
			if e != nil {
				return 0, e
			}
			for _, previous := range existing {
				if ignore[previous.UUID] || previous.IsDisable {
					continue
				}
				previousVersion, e := a.GetLatestAPIVersion(ctx, previous.Id)
				if e != nil {
					return 0, e
				}
				overlap := len(previousVersion.Method) == 0
				for _, method := range previousVersion.Method {
					if method == ep.HTTPMethod {
						overlap = true
					}
				}
				previousHeaders := map[string]string{}
				for _, match := range previousVersion.Match {
					if match.Position == "header" && match.MatchType == "EQUAL" {
						previousHeaders[strings.ToLower(match.Key)] = match.Pattern
					}
				}
				if overlap && !contract.HeadersDisjoint(previousHeaders, ep.Headers) {
					return 0, fmt.Errorf("路由 %s %s 已由 API %s 管理，请先处理冲突；不会自动覆盖或接管已有 API", ep.HTTPMethod, ep.Path, previous.Name)
				}
			}
			uid, _, e := a.CreateAPI(ctx, ns, operator, input)
			if e != nil {
				return 0, e
			}
			input.UUID = uid
			b = &entry.ContractBinding{Source: "auto", NamespaceID: ns, UpstreamID: u.Id, RPCService: ep.Service, RPCMethod: ep.Method, APIUUID: uid}
			if err = a.apiStore.DB(ctx).Create(b).Error; err != nil {
				return 0, err
			}
		} else {
			// CreateAPI also recreates a manually deleted generated API with the stable UUID.
			if _, _, e := a.CreateAPI(ctx, ns, operator, input); e != nil {
				return 0, e
			}
		}
		desired[input.UUID] = input
	}
	for _, b := range bindings {
		if contractSource(b.Source) != "manual" {
			continue
		}
		var input *dto.APIInfo
		if restore != nil {
			input = restored[b.APIUUID]
			if input == nil {
				continue
			}
		} else {
			ep, found, e := manualContractMethod(bundle, b.RPCService, b.RPCMethod)
			if e != nil {
				return 0, e
			}
			if !found {
				continue
			}
			info, e := a.GetAPIVersionInfo(ctx, ns, b.APIUUID)
			if errors.Is(e, gorm.ErrRecordNotFound) {
				continue
			}
			if e != nil {
				return 0, e
			}
			input = contractInput(info)
			if e = updateManualContract(input, ep, artifact); e != nil {
				return 0, e
			}
		}
		if err = a.GetAPIDriver("http").CheckInput(input); err != nil {
			return 0, err
		}
		if _, _, err = a.CreateAPI(ctx, ns, operator, input); err != nil {
			return 0, err
		}
		desired[b.APIUUID] = input
	}
	for _, b := range bindings {
		if desired[b.APIUUID] != nil {
			continue
		}
		info, e := a.GetAPIVersionInfo(ctx, ns, b.APIUUID)
		if errors.Is(e, gorm.ErrRecordNotFound) {
			continue
		}
		if e != nil {
			return 0, e
		}
		input := contractInput(info)
		input.IsDisable = true
		if saved := restored[b.APIUUID]; saved != nil {
			input = saved
			input.IsDisable = true
		}
		if e = a.GetAPIDriver("http").CheckInput(input); e != nil {
			return 0, e
		}
		if _, _, e = a.CreateAPI(ctx, ns, operator, input); e != nil {
			return 0, e
		}
		desired[b.APIUUID] = input
	}
	// Validate against the final set as well as manually managed APIs.
	checkCtx := context.WithValue(ctx, contractSyncKey{}, map[string]bool{})
	for _, input := range desired {
		if !input.IsDisable {
			if err = a.CheckAPIReDuplicative(checkCtx, ns, input.UUID, input); err != nil {
				return 0, err
			}
		}
	}
	refs, err := a.contractSnapshotRefs(ctx, ns, desired)
	if err != nil {
		return 0, err
	}
	apis, err := json.Marshal(refs)
	if err != nil {
		return 0, err
	}
	version := &entry.ContractVersion{IncludeUnannotated: bundle.IncludeUnannotated, NamespaceID: ns, UpstreamID: u.Id, Revision: bundle.Revision, Artifact: artifact, Files: []byte("{}"), APIs: apis, Operator: operator, CreateTime: time.Now()}
	if err = a.apiStore.DB(ctx).Create(version).Error; err != nil {
		return 0, err
	}
	return version.Id, nil
}

func (a *apiService) RestoreContract(ctx context.Context, ns, operator int, name string, base, target int) (*api.ContractInfo, error) {
	old, err := a.GetContract(ctx, ns, name, target)
	if err != nil {
		return nil, err
	}
	if target <= 0 || old.VersionID != target {
		return nil, fmt.Errorf("请选择历史版本")
	}
	bundle, err := contract.Parse(old.Files)
	if err != nil {
		return nil, err
	}
	bundle.IncludeUnannotated = old.IncludeUnannotated
	var id int
	err = a.apiStore.Transaction(ctx, func(tx context.Context) error {
		u, e := a.contractUpstream(tx, ns, name, true)
		if e != nil {
			return e
		}
		versions, e := a.contractVersions(tx, ns, u.Id)
		if e != nil {
			return e
		}
		if len(versions) == 0 || versions[0].Id != base {
			return fmt.Errorf("契约已更新，请刷新后重试")
		}
		id, e = a.syncContract(tx, ns, operator, u, bundle, old)
		return e
	})
	if err != nil {
		return nil, err
	}
	return a.GetContract(ctx, ns, name, id)
}

func (a *apiService) PublishContract(ctx context.Context, ns, operator int, name string, version int, clusters []string) ([]*model.BatchListItem, error) {
	if len(clusters) == 0 {
		return nil, fmt.Errorf("请选择发布集群")
	}
	var publishToken string
	ctx = withContractPublish(ctx)
	state := ctx.Value(contractPublishContextKey).(*contractPublishState)
	// Validate a consistent draft before invoking the normal API publication flow.
	err := a.apiStore.Transaction(ctx, func(tx context.Context) error {
		u, e := a.contractUpstream(tx, ns, name, true)
		if e != nil {
			return e
		}
		versions, e := a.contractVersions(tx, ns, u.Id)
		if e != nil {
			return e
		}
		if len(versions) == 0 || versions[0].Id != version {
			return fmt.Errorf("契约已更新，请刷新后发布")
		}
		bindings := []entry.ContractBinding{}
		if e = a.apiStore.DB(tx).Where("namespace_id = ? AND upstream_id = ?", ns, u.Id).Find(&bindings).Error; e != nil {
			return e
		}
		ids := []string{}
		for _, b := range bindings {
			if _, e = a.apiStore.GetByUUID(tx, ns, b.APIUUID); e != nil {
				return fmt.Errorf("生成 API 已被删除，请重新更新契约: %s", b.APIUUID)
			}
			ids = append(ids, b.APIUUID)
		}
		if len(ids) == 0 {
			return fmt.Errorf("没有可发布的 API")
		}
		targets, e := a.clusterService.QueryListByClusterNames(tx, ns, clusters)
		if e != nil {
			return e
		}
		names := map[string]bool{}
		for _, name := range clusters {
			names[name] = true
		}
		if len(targets) != len(names) {
			return fmt.Errorf("发布集群不存在")
		}
		current, e := a.GetContract(tx, ns, name, version)
		if e != nil {
			return e
		}
		var saved entry.ContractVersion
		if e = a.apiStore.DB(tx).Where("id = ? AND namespace_id = ?", version, ns).First(&saved).Error; e != nil {
			return e
		}
		var refs []entry.ContractAPIRef
		if e = json.Unmarshal(saved.APIs, &refs); e != nil {
			return e
		}
		state.pinned = map[string]pinnedContractAPI{}
		for _, ref := range refs {
			if ref.VersionID == 0 {
				return fmt.Errorf("请先迁移旧版契约快照")
			}
			v, e := a.apiVersion.Get(tx, ref.VersionID)
			if e != nil {
				return e
			}
			if v.NamespaceID != ns {
				return fmt.Errorf("契约 API 版本不属于当前工作空间")
			}
			state.pinned[ref.UUID] = pinnedContractAPI{API: &entry.API{Id: v.ApiID, UUID: ref.UUID, Name: ref.Name, Desc: ref.Desc, GroupUUID: ref.GroupUUID, Scheme: v.Scheme, Version: ref.VersionName, IsDisable: v.IsDisable}, Version: v}
		}
		bundle, e := contract.Parse(current.Files)
		if e != nil {
			return e
		}
		manualKeys := map[string]bool{}
		for _, input := range current.APIs {
			if contractSource(current.Sources[input.UUID]) != "manual" {
				continue
			}
			for _, ep := range savedContractEndpoints([]*dto.APIInfo{input}) {
				manualKeys[ep.Key()] = true
			}
		}
		automatic := *bundle
		automatic.Endpoints = nil
		for _, ep := range bundle.Endpoints {
			if !manualKeys[ep.Key()] {
				automatic.Endpoints = append(automatic.Endpoints, ep)
			}
		}
		bundle = &automatic
		for _, target := range targets {
			published, e := a.contractPlugins.GetPublishVersion(tx, target.Id)
			if e != nil {
				return e
			}
			enabled := false
			if published != nil && published.ClusterPluginPublishVersion != nil {
				for _, p := range published.PublishedPluginsList {
					if p.ClusterPlugin != nil && p.ClusterPlugin.PluginName == "http_to_kitex" && p.ClusterPlugin.Status == 2 && p.Extended == "eolinker.com:apinto:http_to_kitex" {
						enabled = true
					}
				}
			}
			if !enabled {
				return fmt.Errorf("请先在集群 %s 启用并发布 http_to_kitex 节点插件（普通启用）", target.Title)
			}
		}
		// Check all annotation-driven plugins against the published cluster version.
		for _, target := range targets {
			published, e := a.contractPlugins.GetPublishVersion(tx, target.Id)
			if e != nil {
				return e
			}
			if e = validatePublishedContractPlugins(bundle, published.PublishedPluginsList); e != nil {
				return fmt.Errorf("集群 %s: %w", target.Title, e)
			}
		}
		checks, token, e := a.BatchOnlineCheck(tx, ns, operator, ids, clusters)
		if e != nil {
			return e
		}
		if token == "" {
			for _, c := range checks {
				if !c.Status {
					return fmt.Errorf("发布检查失败: %s", c.Result)
				}
			}
			return fmt.Errorf("发布检查未通过")
		}
		publishToken = token
		return nil
	})
	if err != nil {
		return nil, err
	}
	targets, err := a.clusterService.QueryListByClusterNames(ctx, ns, clusters)
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		for _, pinned := range state.pinned {
			if err = a.ensureAPIContracts(ctx, ns, target, pinned.Version); err != nil {
				return nil, fmt.Errorf("集群 %s: %w", target.Title, err)
			}
		}
	}
	return a.BatchOnline(ctx, ns, operator, publishToken)
}

// Remove the last contract's exact predicates and replace declared header keys.
// Other manual conditions (including manually changed former predicates) survive.
func syncContractHeaders(matches []*entry.MatchConf, previous, next map[string]string) []*entry.MatchConf {
	out := make([]*entry.MatchConf, 0, len(matches)+len(next))
	for _, match := range matches {
		if match.Position == "header" && match.MatchType == "EQUAL" {
			key := strings.ToLower(match.Key)
			old, owned := previous[key]
			_, declared := next[key]
			if declared || owned && old == match.Pattern {
				continue
			}
		}
		out = append(out, match)
	}
	keys := make([]string, 0, len(next))
	for key := range next {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, &entry.MatchConf{Position: "header", Key: key, MatchType: "EQUAL", Pattern: next[key]})
	}
	return out
}

// Historical API snapshots are a cleanup baseline, not a legacy IDL parser.
func savedContractEndpoints(apis []*dto.APIInfo) []contract.Endpoint {
	endpoints := []contract.Endpoint{}
	for _, api := range apis {
		ep := contract.Endpoint{Headers: map[string]string{}}
		for _, m := range api.Match {
			if m.Position == "header" && m.MatchType == "EQUAL" {
				ep.Headers[strings.ToLower(m.Key)] = m.Pattern
			}
		}
		for _, plugin := range api.Plugins {
			if plugin.Name == "http_to_kitex" {
				var cfg struct {
					Service string `json:"idl_service"`
					Method  string `json:"method"`
				}
				if json.Unmarshal([]byte(plugin.Config), &cfg) == nil {
					ep.Service = cfg.Service
					ep.Method = cfg.Method
				}
			} else if plugin.Name == "response_wrap" {
				enabled := !plugin.Disable
				ep.ResponseWrap = &enabled
			} else if !plugin.Disable {
				var permission contract.Auth
				if json.Unmarshal([]byte(plugin.Config), &permission) == nil && (permission.Mode == "contains" || permission.Mode == "contains_all") && permission.Position == "header" && permission.Key != "" && permission.Value != "" {
					permission.Plugin = plugin.Name
					ep.Auths = append(ep.Auths, &permission)
					continue
				}
				var cfg struct {
					Logic  string `json:"logic"`
					Params []struct {
						Position  string `json:"position"`
						Name      string `json:"name"`
						MatchMode string `json:"match_mode"`
						MatchText string `json:"match_text"`
					} `json:"params"`
				}
				if json.Unmarshal([]byte(plugin.Config), &cfg) == nil && cfg.Logic == "and" && len(cfg.Params) == 1 && cfg.Params[0].MatchMode == "all" {
					p := cfg.Params[0]
					ep.Auths = append(ep.Auths, &contract.Auth{Plugin: plugin.Name, Position: p.Position, Key: p.Name, Value: p.MatchText})
				}
			}
		}
		if ep.Service != "" && ep.Method != "" {
			endpoints = append(endpoints, ep)
		}
	}
	return endpoints
}

func syncContractHeaderMapping(current, previous, next map[string]string) map[string]string {
	out := map[string]string{}
	targets := map[string]bool{}
	for _, key := range next {
		targets[key] = true
	}
	for header, key := range current {
		header = strings.ToLower(header)
		if old, owned := previous[header]; owned && old == key {
			continue
		}
		if _, declared := next[header]; declared || targets[key] {
			continue
		}
		out[header] = key
	}
	for header, key := range next {
		out[header] = key
	}
	return out
}

func (a *apiService) contractSnapshotRefs(ctx context.Context, ns int, desired map[string]*dto.APIInfo) ([]entry.ContractAPIRef, error) {
	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	rows, err := a.apiStore.GetByUUIDs(ctx, ns, ids)
	if err != nil {
		return nil, err
	}
	apiIDs := make([]int, 0, len(rows))
	for _, row := range rows {
		apiIDs = append(apiIDs, row.Id)
	}
	versions, err := a.getAPIVersions(ctx, apiIDs)
	if err != nil {
		return nil, err
	}
	bindings := []entry.ContractBinding{}
	if err := a.apiStore.DB(ctx).Where("namespace_id = ? AND api_uuid IN ?", ns, ids).Find(&bindings).Error; err != nil {
		return nil, err
	}
	sources := map[string]string{}
	for _, b := range bindings {
		sources[b.APIUUID] = contractSource(b.Source)
	}
	refs := make([]entry.ContractAPIRef, 0, len(rows))
	for _, row := range rows {
		v := versions[row.Id]
		if v == nil {
			return nil, fmt.Errorf("API %s 版本不存在", row.UUID)
		}
		refs = append(refs, entry.ContractAPIRef{Source: sources[row.UUID], UUID: row.UUID, Name: row.Name, Desc: row.Desc, GroupUUID: row.GroupUUID, VersionID: v.Id, VersionName: row.Version})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].UUID < refs[j].UUID })
	return refs, nil
}

func (a *apiService) contractSnapshotAPIs(ctx context.Context, ns int, raw []byte) ([]*dto.APIInfo, error) {
	var refs []entry.ContractAPIRef
	if err := json.Unmarshal(raw, &refs); err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return []*dto.APIInfo{}, nil
	}
	// Existing snapshots keep their exact settings; the migration removes only IDL payloads.
	if refs[0].VersionID == 0 {
		var legacy []*dto.APIInfo
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return nil, err
		}
		return legacy, nil
	}
	ids := make([]int, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.VersionID)
	}
	var versions []version_entry.Version
	if err := a.apiStore.DB(ctx).Where("namespace = ? AND kind = ? AND id IN ?", ns, "api", ids).Find(&versions).Error; err != nil {
		return nil, err
	}
	byID := map[int]*entry.APIVersion{}
	for _, version := range versions {
		v := &entry.APIVersion{Id: version.Id, ApiID: version.Target, NamespaceID: ns}
		if err := json.Unmarshal(version.Data, &v.APIVersionConfig); err != nil {
			return nil, err
		}
		byID[v.Id] = v
	}
	out := make([]*dto.APIInfo, 0, len(refs))
	for _, ref := range refs {
		v := byID[ref.VersionID]
		if v == nil {
			return nil, fmt.Errorf("契约引用的 API 版本 %d 不存在", ref.VersionID)
		}
		out = append(out, contractInput(&model.APIVersionInfo{Api: &entry.API{UUID: ref.UUID, Name: ref.Name, Desc: ref.Desc, GroupUUID: ref.GroupUUID}, Version: v}))
	}
	return out, nil
}

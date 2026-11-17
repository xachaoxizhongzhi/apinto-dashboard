package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/eolinker/apinto-dashboard/controller"
	"github.com/eolinker/apinto-dashboard/controller/users"
	api "github.com/eolinker/apinto-dashboard/modules/api"
	"github.com/eolinker/apinto-dashboard/modules/api/contract"
	namespace "github.com/eolinker/apinto-dashboard/modules/base/namespace-controller"
	"github.com/eolinker/eosc/common/bean"
	"github.com/gin-gonic/gin"
)

type contractController struct{ service api.IContractService }

func newContractController() *contractController {
	c := &contractController{}
	bean.Autowired(&c.service)
	return c
}
func contractResult(c *gin.Context, data interface{}, err error) {
	if err != nil {
		controller.ErrorJson(c, http.StatusOK, err.Error())
		return
	}
	if info, ok := data.(*api.ContractInfo); ok && info != nil {
		apis, listErr := contractRows(info)
		if listErr != nil {
			controller.ErrorJson(c, http.StatusOK, listErr.Error())
			return
		}
		data = map[string]interface{}{"include_unannotated": info.IncludeUnannotated, "version_id": info.VersionID, "revision": info.Revision, "files": info.Files, "versions": info.Versions, "apis": apis}
	}
	c.JSON(http.StatusOK, controller.NewSuccessResult(data))
}
func (c *contractController) info(g *gin.Context) {
	version, _ := strconv.Atoi(g.Query("version"))
	data, err := c.service.GetContract(g, namespace.GetNamespaceId(g), g.Param("upstream"), version)
	contractResult(g, data, err)
}
func downloadContract(ctx context.Context, link string) ([]byte, error) {
	valid := func(raw string) bool {
		u, e := url.Parse(raw)
		return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
	}
	if !valid(link) {
		return nil, fmt.Errorf("请输入有效的 HTTP/HTTPS ZIP 下载链接")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, fmt.Errorf("下载链接无效")
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !valid(r.URL.String()) {
			return fmt.Errorf("重定向无效")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("契约下载失败，请检查链接和网络")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("契约下载失败，HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, contract.MaxArchive+1))
	if err != nil {
		return nil, fmt.Errorf("读取契约文件失败")
	}
	if len(data) > contract.MaxArchive {
		return nil, fmt.Errorf("契约 ZIP 不得超过 16 MiB")
	}
	return data, nil
}
func (c *contractController) update(g *gin.Context) {
	g.Set("logBody", "{\"operation\":\"update_contract\"}")
	g.Request.Body = http.MaxBytesReader(g.Writer, g.Request.Body, contract.MaxArchive+(1<<20))
	var raw []byte
	var err error
	var base int
	var includeUnannotated bool
	if g.ContentType() == "multipart/form-data" {
		defer func() {
			if g.Request.MultipartForm != nil {
				g.Request.MultipartForm.RemoveAll()
			}
		}()
		file, e := g.FormFile("file")
		if e != nil {
			contractResult(g, nil, fmt.Errorf("请选择 ZIP 文件，最大 16 MiB"))
			return
		}
		base, err = strconv.Atoi(g.PostForm("base_version"))
		if err != nil {
			contractResult(g, nil, fmt.Errorf("缺少契约版本"))
			return
		}
		if value := g.PostForm("include_unannotated"); value != "" {
			includeUnannotated, err = strconv.ParseBool(value)
			if err != nil {
				contractResult(g, nil, fmt.Errorf("解析无 HTTP 注解的方法选项无效"))
				return
			}
		}
		f, e := file.Open()
		if e != nil {
			contractResult(g, nil, e)
			return
		}
		raw, err = io.ReadAll(io.LimitReader(f, contract.MaxArchive+1))
		f.Close()
	} else {
		var input struct {
			URL                string `json:"url"`
			IncludeUnannotated bool   `json:"include_unannotated"`
			BaseVersion        *int   `json:"base_version"`
		}
		if err = g.ShouldBindJSON(&input); err != nil || input.BaseVersion == nil {
			contractResult(g, nil, fmt.Errorf("请输入下载链接和契约版本"))
			return
		}
		base = *input.BaseVersion
		includeUnannotated = input.IncludeUnannotated
		raw, err = downloadContract(g.Request.Context(), input.URL)
	}
	if err != nil {
		contractResult(g, nil, err)
		return
	}
	bundle, err := contract.ReadZIP(raw)
	if err != nil {
		contractResult(g, nil, err)
		return
	}
	bundle.IncludeUnannotated = includeUnannotated
	data, err := c.service.SaveContract(g, namespace.GetNamespaceId(g), users.GetUserId(g), g.Param("upstream"), base, bundle)
	contractResult(g, data, err)
}
func (c *contractController) edit(g *gin.Context) {
	g.Request.Body = http.MaxBytesReader(g.Writer, g.Request.Body, contract.MaxContents+(1<<20))
	var input struct {
		Path        string `json:"path"`
		Content     string `json:"content"`
		BaseVersion int    `json:"base_version"`
	}
	if err := g.ShouldBindJSON(&input); err != nil {
		contractResult(g, nil, fmt.Errorf("文件内容无效"))
		return
	}
	ns := namespace.GetNamespaceId(g)
	name := g.Param("upstream")
	current, err := c.service.GetContract(g, ns, name, 0)
	if err != nil {
		contractResult(g, nil, err)
		return
	}
	if input.BaseVersion != current.VersionID {
		contractResult(g, nil, fmt.Errorf("契约已更新，请刷新后重试"))
		return
	}
	if _, exists := current.Files[input.Path]; !exists {
		contractResult(g, nil, fmt.Errorf("契约文件不存在"))
		return
	}
	current.Files[input.Path] = input.Content
	bundle, err := contract.Parse(current.Files)
	if err != nil {
		contractResult(g, nil, err)
		return
	}
	bundle.IncludeUnannotated = current.IncludeUnannotated
	data, err := c.service.SaveContract(g, ns, users.GetUserId(g), name, input.BaseVersion, bundle)
	contractResult(g, data, err)
}
func (c *contractController) restore(g *gin.Context) {
	var input struct {
		Version     int `json:"version"`
		BaseVersion int `json:"base_version"`
	}
	if err := g.ShouldBindJSON(&input); err != nil {
		contractResult(g, nil, err)
		return
	}
	data, err := c.service.RestoreContract(g, namespace.GetNamespaceId(g), users.GetUserId(g), g.Param("upstream"), input.BaseVersion, input.Version)
	contractResult(g, data, err)
}
func (c *contractController) publish(g *gin.Context) {
	var input struct {
		Version  int      `json:"version"`
		Clusters []string `json:"clusters"`
	}
	if err := g.ShouldBindJSON(&input); err != nil {
		contractResult(g, nil, err)
		return
	}
	data, err := c.service.PublishContract(g, namespace.GetNamespaceId(g), users.GetUserId(g), g.Param("upstream"), input.Version, input.Clusters)
	contractResult(g, data, err)
}

// Contract rows are a view only; publishing continues to use the saved API snapshots.
type contractRow struct {
	Source        string   `json:"source"`
	AutoConfigure bool     `json:"auto_configure"`
	RPCService    string   `json:"rpc_service"`
	RPCMethod     string   `json:"rpc_method"`
	UUID          string   `json:"uuid"`
	Name          string   `json:"name"`
	Contract      string   `json:"contract"`
	File          string   `json:"file"`
	Method        []string `json:"method"`
	RequestPath   string   `json:"request_path"`
	IsDisable     bool     `json:"is_disable"`
	Removed       bool     `json:"removed"`
}

func contractRows(info *api.ContractInfo) ([]contractRow, error) {
	methods, err := contract.ListMethods(info.Files)
	if err != nil {
		return nil, err
	}
	rows := make([]contractRow, 0, len(methods))
	saved := make([]contractRow, 0, len(info.APIs))
	for _, item := range info.APIs {
		row := contractRow{Source: info.Sources[item.UUID], UUID: item.UUID, Name: item.ApiName, Method: item.Method, RequestPath: item.RequestPath, IsDisable: item.IsDisable, Removed: true}
		for _, plugin := range item.Plugins {
			if plugin.Name != "http_to_kitex" {
				continue
			}
			var cfg struct {
				Service string `json:"idl_service"`
				Method  string `json:"method"`
				File    string `json:"main_idl"`
			}
			if err := json.Unmarshal([]byte(plugin.Config), &cfg); err != nil {
				return nil, fmt.Errorf("接口 %s 的 Kitex 配置无效", item.ApiName)
			}
			row.Contract, row.File = cfg.Service+"."+cfg.Method, cfg.File
			row.RPCService, row.RPCMethod = cfg.Service, cfg.Method
		}
		saved = append(saved, row)
	}
	used := make([]bool, len(saved))
	type methodKey struct{ file, contract string }
	methodCounts := map[string]int{}
	for _, method := range methods {
		methodCounts[method.Service+"."+method.Method]++
	}
	byName := map[string]int{}
	byMethod := make(map[methodKey]int, len(saved))
	for i, row := range saved {
		byMethod[methodKey{row.File, row.Contract}] = i
		byName[row.Contract] = i
	}
	for _, method := range methods {
		row := contractRow{RPCService: method.Service, RPCMethod: method.Method, AutoConfigure: method.HTTPMethod != "", Name: method.DisplayName(), Contract: method.Service + "." + method.Method, File: method.File, Method: []string{}, RequestPath: method.Path}
		if method.HTTPMethod != "" {
			row.Method = []string{method.HTTPMethod}
		}
		i, ok := byMethod[methodKey{row.File, row.Contract}]
		if !ok && methodCounts[row.Contract] == 1 {
			i, ok = byName[row.Contract]
		}
		if ok && !used[i] {
			row.UUID, row.IsDisable = saved[i].UUID, saved[i].IsDisable
			row.Source = saved[i].Source
			if row.Source == "manual" {
				row.Method, row.RequestPath = saved[i].Method, saved[i].RequestPath
				row.AutoConfigure = false
			}
			used[i] = true
		}
		if method.HTTPMethod == "" && !info.IncludeUnannotated && row.UUID == "" {
			continue
		}
		rows = append(rows, row)
	}
	// Retain disabled APIs removed from the IDL so their publication is still visible.
	for i, row := range saved {
		if !used[i] {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (c *contractController) createAPI(g *gin.Context) {
	g.Request.Body = http.MaxBytesReader(g.Writer, g.Request.Body, 16<<10)
	var input api.ContractManualAPI
	if err := g.ShouldBindJSON(&input); err != nil {
		contractResult(g, nil, fmt.Errorf("API 配置信息无效"))
		return
	}
	data, err := c.service.CreateContractAPI(g, namespace.GetNamespaceId(g), users.GetUserId(g), g.Param("upstream"), &input)
	contractResult(g, data, err)
}

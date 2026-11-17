// Package contract validates complete Thrift archives before creating API drafts.
package contract

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/thriftgo/parser"
	"github.com/cloudwego/thriftgo/semantic"
)

const MaxArchive = 16 << 20
const MaxContents = 32 << 20
const MaxFiles = 256

type Endpoint struct {
	Callable      bool              `json:"-"`
	HeaderMapping map[string]string `json:"header_mapping,omitempty"`
	Service       string            `json:"service"`
	Method        string            `json:"method"`
	File          string            `json:"file"`
	HTTPMethod    string            `json:"http_method"`
	Path          string            `json:"path"`
	Auths         []*Auth           `json:"auths,omitempty"`
	Auth          *Auth             `json:"auth,omitempty"`
	ResponseWrap  *bool             `json:"response_wrap,omitempty"`
	Description   string            `json:"description"`
	Headers       map[string]string `json:"headers,omitempty"`
}

func (e Endpoint) Key() string { return e.Service + "/" + e.Method }

type Bundle struct {
	Methods            []Endpoint        `json:"-"`
	IncludeUnannotated bool              `json:"include_unannotated"`
	Revision           string            `json:"revision"`
	Files              map[string]string `json:"files"`
	Endpoints          []Endpoint        `json:"endpoints"`
	RuntimeFiles       map[string]string `json:"-"`
}

func validPath(name string) bool {
	return name != "" && !strings.ContainsAny(name, "\\\x00:") && !path.IsAbs(name) && path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../")
}

func ReadZIP(data []byte) (*Bundle, error) {
	if len(data) > MaxArchive {
		return nil, fmt.Errorf("契约 ZIP 不得超过 16 MiB")
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("文件不是有效 ZIP")
	}
	if len(z.File) > MaxFiles*4 {
		return nil, fmt.Errorf("ZIP 条目过多")
	}
	files := map[string]string{}
	total := 0
	for _, f := range z.File {
		name := strings.TrimSuffix(f.Name, "/")
		if !validPath(name) {
			return nil, fmt.Errorf("不安全的 ZIP 路径: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if !f.Mode().IsRegular() || path.Ext(name) != ".thrift" {
			return nil, fmt.Errorf("契约包只能包含 .thrift 文件: %s", name)
		}
		if _, ok := files[name]; ok {
			return nil, fmt.Errorf("重复文件: %s", name)
		}
		if f.UncompressedSize64 > MaxContents {
			return nil, fmt.Errorf("解压内容超过限制")
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(r, int64(MaxContents-total+1)))
		r.Close()
		if err != nil {
			return nil, err
		}
		total += len(b)
		if total > MaxContents {
			return nil, fmt.Errorf("解压内容超过 32 MiB")
		}
		files[name] = string(b)
	}
	return Parse(files)
}

func Parse(files map[string]string) (bundle *Bundle, err error) {
	defer func() {
		if r := recover(); r != nil {
			bundle = nil
			err = fmt.Errorf("契约解析失败: %v", r)
		}
	}()
	if len(files) == 0 || len(files) > MaxFiles {
		return nil, fmt.Errorf("契约包需要包含 1 至 256 个 Thrift 文件")
	}
	names := make([]string, 0, len(files))
	total := 0
	asts := map[string]*parser.Thrift{}
	runtimeFiles := map[string]string{}
	for name, content := range files {
		if !validPath(name) || path.Ext(name) != ".thrift" {
			return nil, fmt.Errorf("非法契约文件名: %s", name)
		}
		total += len(content)
		if total > MaxContents {
			return nil, fmt.Errorf("契约内容超过 32 MiB")
		}
		if !utf8.ValidString(content) {
			return nil, fmt.Errorf("%s 必须为 UTF-8", name)
		}
		e := validateRouterSyntax(content)
		if e != nil {
			return nil, fmt.Errorf("%s: %w", name, e)
		}
		runtimeFiles[name] = content
		ast, e := parser.ParseString(name, content)
		if e != nil {
			return nil, fmt.Errorf("%s: %w", name, e)
		}
		asts[name] = ast
		names = append(names, name)
	}
	sort.Strings(names)
	// Resolve includes only against the submitted snapshot; never read server files.
	state := map[string]int{}
	var visit func(string, int) error
	visit = func(name string, depth int) error {
		if depth > 32 {
			return fmt.Errorf("include 嵌套超过 32 层")
		}
		if state[name] == 1 {
			return fmt.Errorf("循环 include: %s", name)
		}
		if state[name] == 2 {
			return nil
		}
		state[name] = 1
		for _, inc := range asts[name].Includes {
			if strings.ContainsAny(inc.Path, "\\\x00:") || path.IsAbs(inc.Path) {
				return fmt.Errorf("非法 include: %s", inc.Path)
			}
			target := path.Clean(path.Join(path.Dir(name), inc.Path))
			if !validPath(target) || asts[target] == nil {
				return fmt.Errorf("%s 缺少 include: %s", name, inc.Path)
			}
			if e := visit(target, depth+1); e != nil {
				return e
			}
			inc.Reference = asts[target]
		}
		state[name] = 2
		return nil
	}
	for _, name := range names {
		if e := visit(name, 0); e != nil {
			return nil, e
		}
	}
	endpoints := []Endpoint{}
	methods := []Endpoint{}
	keys := map[string]bool{}
	routes := map[string][]Endpoint{}
	for _, name := range names {
		ast := asts[name]
		if e := semantic.ResolveSymbols(ast); e != nil {
			return nil, fmt.Errorf("%s: %w", name, e)
		}
		if _, e := semantic.NewChecker(semantic.Options{}).CheckAll(ast); e != nil {
			return nil, fmt.Errorf("%s: %w", name, e)
		}
		for _, svc := range ast.Services {
			if svc.Extends != "" {
				return nil, fmt.Errorf("第一版暂不支持 Service 继承: %s", svc.Name)
			}
			for _, fn := range svc.Functions {
				var ep *Endpoint
				headers := map[string]string{}
				for _, ann := range fn.Annotations {
					if !strings.HasPrefix(ann.Key, "api.") {
						continue
					}
					method := strings.TrimPrefix(ann.Key, "api.")
					switch method {
					case "get", "post", "put", "delete", "patch", "head", "options":
					default:
						continue
					}
					if ep != nil || len(ann.Values) != 1 {
						return nil, fmt.Errorf("%s.%s 只能声明一个 HTTP 入口", svc.Name, fn.Name)
					}
					p := ann.Values[0]
					if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "?#\r\n\t ") {
						return nil, fmt.Errorf("非法 HTTP 路径: %s", p)
					}
					ep = &Endpoint{Service: svc.Name, Method: fn.Name, File: name, HTTPMethod: strings.ToUpper(method), Path: p, Description: strings.TrimSpace(fn.ReservedComments)}
				}
				callable := !fn.Oneway && !fn.Void && len(fn.Arguments) == 1 && fn.Arguments[0].Type.Category == parser.Category_Struct && fn.FunctionType.Category == parser.Category_Struct
				if ep == nil {
					methods = append(methods, Endpoint{Service: svc.Name, Method: fn.Name, File: name, Description: cleanComments(fn.ReservedComments), Callable: callable})
					continue
				}
				ep.Callable = callable
				if e := parseRouterHeaders(headers, fn.Annotations); e != nil {
					return nil, fmt.Errorf("%s.%s: %w", svc.Name, fn.Name, e)
				}
				mapping, e := parseHeaderMapping(fn.Annotations)
				if e != nil {
					return nil, fmt.Errorf("%s.%s: %w", svc.Name, fn.Name, e)
				}
				ep.HeaderMapping = mapping
				ep.Headers = headers
				ep.Description = cleanComments(ep.Description)
				if e := parsePolicy(ep, fn.Annotations); e != nil {
					return nil, fmt.Errorf("%s.%s: %w", svc.Name, fn.Name, e)
				}
				if fn.Oneway || fn.Void || len(fn.Arguments) != 1 {
					return nil, fmt.Errorf("%s.%s 必须为单个结构体请求及结构体响应的 unary 方法", svc.Name, fn.Name)
				}
				if fn.Arguments[0].Type.Category != parser.Category_Struct || fn.FunctionType.Category != parser.Category_Struct {
					return nil, fmt.Errorf("%s.%s 的请求和响应必须为结构体", svc.Name, fn.Name)
				}
				if keys[ep.Key()] {
					return nil, fmt.Errorf("重复的 Service/Method: %s", ep.Key())
				}
				keys[ep.Key()] = true
				route := ep.HTTPMethod + " " + ep.Path
				for _, previous := range routes[route] {
					if !HeadersDisjoint(previous.Headers, ep.Headers) {
						return nil, fmt.Errorf("契约中的路由冲突: %s", route)
					}
				}
				routes[route] = append(routes[route], *ep)
				endpoints = append(endpoints, *ep)
				methods = append(methods, *ep)
			}
		}
	}
	// Empty endpoint sets are valid full replacements (all former APIs disabled).
	raw, _ := json.Marshal(files)
	hash := sha256.Sum256(raw)
	return &Bundle{Methods: methods, Revision: hex.EncodeToString(hash[:]), Files: files, Endpoints: endpoints, RuntimeFiles: runtimeFiles}, nil
}

// ListMethods reads the method catalogue without applying HTTP route validation.
// This also supports displaying historical contracts and methods without HTTP annotations.
func ListMethods(files map[string]string) ([]Endpoint, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	methods := []Endpoint{}
	for _, name := range names {
		ast, err := parser.ParseString(name, files[name])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for _, svc := range ast.Services {
			for _, fn := range svc.Functions {
				ep := Endpoint{Service: svc.Name, Method: fn.Name, File: name, Description: cleanComments(fn.ReservedComments)}
				for _, ann := range fn.Annotations {
					switch ann.Key {
					case "api.get", "api.post", "api.put", "api.delete", "api.patch", "api.head", "api.options":
						if len(ann.Values) == 1 {
							ep.HTTPMethod = strings.ToUpper(strings.TrimPrefix(ann.Key, "api."))
							ep.Path = ann.Values[0]
						}
					}
				}
				methods = append(methods, ep)
			}
		}
	}
	return methods, nil
}

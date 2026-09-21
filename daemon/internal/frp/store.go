package frp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	frpconfig "github.com/fatedier/frp/pkg/config"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/config/v1/validation"
	toml "github.com/pelletier/go-toml/v2"
)

// clientExt 是客户端配置文件的扩展名。配置内容就是 frp 原生 TOML，
// 因此这个文件可以直接交给官方 frpc 使用。
const clientExt = ".toml"

// Store 管理 <目录>/frp/clients 下的客户端配置文件，一份文件一个客户端。
//
// 文件名（去掉扩展名）就是客户端名，因此改名等于改文件名，
// 与工作区目录的做法一致。
type Store struct {
	dir string
}

// NewStore 打开（必要时创建）配置目录。
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建 FRP 配置目录失败: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir 返回配置目录。
func (s *Store) Dir() string { return s.dir }

// Path 返回某个客户端的配置文件路径。
func (s *Store) Path(name string) string { return filepath.Join(s.dir, name+clientExt) }

// Exists 判断某个客户端是否已存在。
func (s *Store) Exists(name string) bool {
	_, err := os.Stat(s.Path(name))
	return err == nil
}

// ValidateName 校验客户端名称。名称会用作文件名，因此必须挡掉路径分隔符，
// 否则界面上一句 "name": "../../x" 就能写到配置目录之外。
func ValidateName(name string) error {
	if name == "" {
		return errors.New("名称不能为空")
	}
	if strings.TrimSpace(name) != name {
		return errors.New("名称首尾不能有空白字符")
	}
	if len([]rune(name)) > 64 {
		return errors.New("名称不能超过 64 个字符")
	}
	if name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return errors.New("名称不能以点开头")
	}
	if strings.ContainsAny(name, `/\:*?"<>|`) {
		return errors.New(`名称不能包含 / \ : * ? " < > | 等字符`)
	}
	// Windows 把 CON、NUL 这类名字当作设备：写成文件会落到设备上而不是磁盘，
	// 排查起来很费解，不如直接拒绝。
	base := strings.ToUpper(name)
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	if windowsReservedNames[base] {
		return fmt.Errorf("名称 %s 是系统保留名，请换一个", name)
	}
	return nil
}

// windowsReservedNames 是 Windows 保留的设备名（不区分大小写，且与扩展名无关）。
var windowsReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// List 返回全部客户端名称，按名称排序。
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), clientExt) {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), clientExt)
		// 写入过程中的临时文件不当作客户端
		if strings.HasSuffix(name, ".tmp") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Load 读取一份客户端配置为 frp 原生结构。
//
// 返回的是完整结构而不是我们自己的模型：界面上没暴露的字段（OIDC、transport、
// 插件的私有参数等）因此能在保存时原样写回，不会因为"编辑了别的字段"被抹掉。
func (s *Store) Load(name string) (*v1.ClientConfig, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}

	result, err := frpconfig.LoadClientConfigResult(s.Path(name), false)
	if err != nil {
		return nil, fmt.Errorf("读取 FRP 配置失败: %w", err)
	}
	if result.IsLegacyFormat {
		return nil, errors.New("这是 frp 旧版 INI 格式的配置，请先转换成 TOML")
	}

	return &v1.ClientConfig{
		ClientCommonConfig: *result.Common,
		Proxies:            toTypedProxies(result.Proxies),
		Visitors:           toTypedVisitors(result.Visitors),
	}, nil
}

// Save 校验并写入配置。
//
// 先写临时文件再改名：中途失败不会留下半截配置，而半截配置会让这个客户端
// 再也起不来。
func (s *Store) Save(name string, cfg *v1.ClientConfig) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := validateClient(cfg); err != nil {
		return err
	}

	content, err := renderClientTOML(cfg)
	if err != nil {
		return err
	}

	path := s.Path(name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	return nil
}

// Delete 删除配置文件。文件不存在不视为错误。
func (s *Store) Delete(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := os.Remove(s.Path(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除配置文件失败: %w", err)
	}
	return nil
}

// ---------- 校验与序列化 ----------

// validateClient 校验一份将要保存的配置。
//
// 做法是先把配置渲染成 TOML，再用 frp 自己的加载路径读回来校验：
// 校验的就是即将落盘的内容，与运行时行为完全一致；默认值补在解析出来的副本上，
// 因此不会把一堆 frp 的默认值写进用户的配置文件。
func validateClient(cfg *v1.ClientConfig) error {
	content, err := renderClientTOML(cfg)
	if err != nil {
		return err
	}

	var probe v1.ClientConfig
	if err := frpconfig.LoadConfigure(content, &probe, false); err != nil {
		return fmt.Errorf("FRP 配置无效: %w", err)
	}
	if err := probe.ClientCommonConfig.Complete(); err != nil {
		return fmt.Errorf("FRP 配置无效: %w", err)
	}

	proxies := make([]v1.ProxyConfigurer, 0, len(probe.Proxies))
	for _, p := range probe.Proxies {
		proxies = append(proxies, p.ProxyConfigurer)
	}
	visitors := make([]v1.VisitorConfigurer, 0, len(probe.Visitors))
	for _, v := range probe.Visitors {
		visitors = append(visitors, v.VisitorConfigurer)
	}
	proxies = frpconfig.CompleteProxyConfigurers(proxies)
	visitors = frpconfig.CompleteVisitorConfigurers(visitors)

	if _, err := validation.ValidateAllClientConfig(&probe.ClientCommonConfig, proxies, visitors, nil); err != nil {
		return fmt.Errorf("FRP 配置校验失败: %w", err)
	}
	return nil
}

// renderClientTOML 把 frp 的配置结构写成 TOML。
//
// frp 自己读取 TOML 的路径是 TOML → JSON → 结构体（见 pkg/config.LoadConfigure），
// 所以字段名由 json tag 决定；这里反过来走 JSON → TOML，保证写出的键名与 frp 期望的一致。
func renderClientTOML(cfg *v1.ClientConfig) ([]byte, error) {
	tree, err := configTree(cfg)
	if err != nil {
		return nil, err
	}

	// 结构体里的非指针子结构体没有 omitempty 效果，会序列化成空对象；
	// 去掉它们，配置文件才不会堆满 [transport] [healthCheck] 这类空段。
	pruneEmpty(tree)

	// frp 读配置时会用 Complete() 补全默认值。补全后的结构体直接写回去，
	// 等于把 frp 的默认值固化进用户文件：既吵，又会在这两者不同步时
	// （例如新版 frp 改了默认值）让旧默认值继续生效。
	defaults, err := defaultClientTree()
	if err != nil {
		return nil, err
	}
	pruneDefaults(tree, defaults)
	pruneEmpty(tree)

	content, err := toml.Marshal(tree)
	if err != nil {
		return nil, fmt.Errorf("生成 TOML 失败: %w", err)
	}
	return content, nil
}

// configTree 把配置结构转成可比较的通用树，数字还原成整数。
func configTree(cfg *v1.ClientConfig) (map[string]any, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("序列化 FRP 配置失败: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var tree map[string]any
	if err := decoder.Decode(&tree); err != nil {
		return nil, fmt.Errorf("序列化 FRP 配置失败: %w", err)
	}

	normalizeNumbers(tree)
	return tree, nil
}

// defaultClientTree 返回「一份空配置补全默认值后」的树，作为剔除默认值的基准。
func defaultClientTree() (map[string]any, error) {
	empty := &v1.ClientConfig{}
	if err := empty.ClientCommonConfig.Complete(); err != nil {
		return nil, err
	}
	tree, err := configTree(empty)
	if err != nil {
		return nil, err
	}
	pruneEmpty(tree)
	return tree, nil
}

// pruneDefaults 删除与 frp 默认值相同的字段。
//
// 只处理两边都存在、且逐层能对上的键；数组（proxies / visitors）整体保留，
// 因为默认树里没有它们，用户写的每一条代理都必须留在文件里。
func pruneDefaults(tree, defaults map[string]any) {
	for key, value := range tree {
		fallback, ok := defaults[key]
		if !ok {
			continue
		}

		child, isMap := value.(map[string]any)
		fallbackMap, fallbackIsMap := fallback.(map[string]any)
		if isMap && fallbackIsMap {
			pruneDefaults(child, fallbackMap)
			// 子项全部与默认值一致：整张表都可以省掉
			if isEmptyTable(child) {
				delete(tree, key)
			}
			continue
		}
		if reflect.DeepEqual(value, fallback) {
			delete(tree, key)
		}
	}
}

// normalizeNumbers 把 JSON 解出来的数字还原成整数或浮点数。
// 不还原的话端口会被写成 7000.0，虽然 frp 能读，但看起来像是被谁改坏了。
func normalizeNumbers(node any) {
	switch v := node.(type) {
	case map[string]any:
		for key, value := range v {
			if number, ok := value.(json.Number); ok {
				v[key] = toNumber(number)
				continue
			}
			normalizeNumbers(value)
		}
	case []any:
		for i, value := range v {
			if number, ok := value.(json.Number); ok {
				v[i] = toNumber(number)
				continue
			}
			normalizeNumbers(value)
		}
	}
}

func toNumber(number json.Number) any {
	if i, err := number.Int64(); err == nil {
		return i
	}
	if f, err := number.Float64(); err == nil {
		return f
	}
	return number.String()
}

// pruneEmpty 递归删除空表与空数组。
//
// 代理是数组元素，所以空表可能嵌在数组里，必须一路递归下去。
func pruneEmpty(node any) {
	switch v := node.(type) {
	case map[string]any:
		for key, value := range v {
			pruneEmpty(value)
			switch child := value.(type) {
			case nil:
				delete(v, key)
			case map[string]any:
				if isEmptyTable(child) {
					delete(v, key)
				}
			case []any:
				if len(child) == 0 {
					delete(v, key)
				}
			}
		}
	case []any:
		for _, item := range v {
			pruneEmpty(item)
		}
	}
}

// isEmptyValue 判断一个值是否等同于「没配置」。
//
// false 不算空：frp 里 tls.enable 这类开关的默认值是 true，
// 显式写下的 false 是有意义的取值，剪掉会改变行为。
func isEmptyValue(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case bool:
		return false
	case int64:
		return v == 0
	case float64:
		return v == 0
	case map[string]any:
		return isEmptyTable(v)
	case []any:
		return len(v) == 0
	default:
		return false
	}
}

func isEmptyTable(table map[string]any) bool {
	for _, value := range table {
		if !isEmptyValue(value) {
			return false
		}
	}
	return true
}

// ---------- frp 结构 ↔ 界面模型 ----------

func toTypedProxies(proxies []v1.ProxyConfigurer) []v1.TypedProxyConfig {
	result := make([]v1.TypedProxyConfig, 0, len(proxies))
	for _, p := range proxies {
		result = append(result, v1.TypedProxyConfig{
			Type:            p.GetBaseConfig().Type,
			ProxyConfigurer: p,
		})
	}
	return result
}

func toTypedVisitors(visitors []v1.VisitorConfigurer) []v1.TypedVisitorConfig {
	result := make([]v1.TypedVisitorConfig, 0, len(visitors))
	for _, v := range visitors {
		result = append(result, v1.TypedVisitorConfig{
			Type:              v.GetBaseConfig().Type,
			VisitorConfigurer: v,
		})
	}
	return result
}

// clientInfo 把 frp 配置转成界面模型（不含运行状态）。
func clientInfo(name string, cfg *v1.ClientConfig) ClientInfo {
	common := &cfg.ClientCommonConfig

	info := ClientInfo{
		Name:       name,
		Group:      common.Metadatas[metaKeyGroup],
		AutoStart:  clientAutoStart(common),
		ServerAddr: common.ServerAddr,
		ServerPort: common.ServerPort,
		AuthMethod: string(common.Auth.Method),
		AuthToken:  common.Auth.Token,
		TLSEnable:  common.Transport.TLS.Enable != nil && *common.Transport.TLS.Enable,
		LogLevel:   common.Log.Level,
	}

	for _, p := range cfg.Proxies {
		info.Proxies = append(info.Proxies, proxyInfo(common, p.ProxyConfigurer, p.Type))
	}
	for _, v := range cfg.Visitors {
		// visitor 是访问端，界面暂不支持编辑，但仍然列出来，
		// 否则用户会以为配置文件里的配置丢了。
		info.Proxies = append(info.Proxies, visitorInfo(common, v.VisitorConfigurer, v.Type))
	}
	return info
}

func proxyInfo(common *v1.ClientCommonConfig, p v1.ProxyConfigurer, proxyType string) ProxyInfo {
	base := p.GetBaseConfig()
	info := ProxyInfo{
		Name:     base.Name,
		Type:     proxyType,
		Enabled:  proxyEnabled(common.Start, base.Enabled, base.Name),
		Editable: isProxyType(proxyType) || isVisitorType(proxyType),
	}

	switch c := p.(type) {
	case *v1.TCPProxyConfig:
		info.LocalIP, info.LocalPort, info.RemotePort = c.LocalIP, c.LocalPort, c.RemotePort
	case *v1.UDPProxyConfig:
		info.LocalIP, info.LocalPort, info.RemotePort = c.LocalIP, c.LocalPort, c.RemotePort
	case *v1.HTTPProxyConfig:
		info.LocalIP, info.LocalPort = c.LocalIP, c.LocalPort
		info.CustomDomains, info.SubDomain = c.CustomDomains, c.SubDomain
	case *v1.HTTPSProxyConfig:
		info.LocalIP, info.LocalPort = c.LocalIP, c.LocalPort
		info.CustomDomains, info.SubDomain = c.CustomDomains, c.SubDomain
	case *v1.TCPMuxProxyConfig:
		info.LocalIP, info.LocalPort = c.LocalIP, c.LocalPort
		info.CustomDomains, info.SubDomain = c.CustomDomains, c.SubDomain
	}
	return info
}

// visitorInfo 描述一个访问端。它没有本地后端，因此只给出名称与类型。
func visitorInfo(common *v1.ClientCommonConfig, v v1.VisitorConfigurer, visitorType string) ProxyInfo {
	base := v.GetBaseConfig()
	return ProxyInfo{
		Name:     base.Name,
		Type:     visitorType,
		Enabled:  proxyEnabled(common.Start, base.Enabled, base.Name),
		Editable: false,
	}
}

// proxyEnabled 计算一条代理（或访问端）的有效启用状态。
//
// frp 有两处开关：自身的 enabled（nil 表示启用）与客户端级的 start 列表
// （非空时只有列表里的条目会启动）。两者都满足才算启用，
// 否则界面上会显示「已启用」但实际不生效。
func proxyEnabled(start []string, enabled *bool, name string) bool {
	if enabled != nil && !*enabled {
		return false
	}
	if len(start) > 0 {
		return contains(start, name)
	}
	return true
}

// setProxyEnabled 改写一条代理的启用状态。
//
// 优先改自身的 enabled；若配置里存在 start 列表则同步维护它，
// 因为这个列表在被导入的配置里更常见，且优先级更高。
func setProxyEnabled(start *[]string, enabled **bool, name string, value bool) {
	if value {
		*enabled = nil
	} else {
		disabled := false
		*enabled = &disabled
	}

	if len(*start) == 0 {
		return
	}
	if value {
		if !contains(*start, name) {
			*start = append(*start, name)
			sort.Strings(*start)
		}
		return
	}
	remaining := make([]string, 0, len(*start))
	for _, item := range *start {
		if item != name {
			remaining = append(remaining, item)
		}
	}
	*start = remaining
}

// applyClientPayload 把界面提交的字段写进配置，未提交的字段保持原样。
func applyClientPayload(cfg *v1.ClientConfig, payload ClientPayload) {
	common := &cfg.ClientCommonConfig

	common.ServerAddr = payload.ServerAddr
	common.ServerPort = payload.ServerPort

	if common.Auth.Method == "" {
		common.Auth.Method = AuthToken
	}
	if payload.AuthMethod != "" {
		common.Auth.Method = v1.AuthMethod(payload.AuthMethod)
	}
	common.Auth.Token = payload.AuthToken

	enable := payload.TLSEnable
	common.Transport.TLS.Enable = &enable

	common.Log.Level = payload.LogLevel

	// 服务器暂时不可达时保持重试，而不是让实例直接退出：
	// 启停由用户在界面上控制，进程自己退掉会让状态变得难以解释。
	loginFailExit := false
	common.LoginFailExit = &loginFailExit

	metaSet(&common.Metadatas, metaKeyGroup, payload.Group)
	if payload.AutoStart != nil && !*payload.AutoStart {
		metaSet(&common.Metadatas, metaKeyAutoStart, "false")
	} else {
		metaSet(&common.Metadatas, metaKeyAutoStart, "")
	}
}

// clientAutoStart 读取自动启动标记，缺省为 true（与隧道一致）。
func clientAutoStart(common *v1.ClientCommonConfig) bool {
	value, ok := common.Metadatas[metaKeyAutoStart]
	if !ok {
		return true
	}
	return value != "false"
}

func metaSet(metas *map[string]string, key, value string) {
	if value == "" {
		if *metas != nil {
			delete(*metas, key)
		}
		return
	}
	if *metas == nil {
		*metas = map[string]string{}
	}
	(*metas)[key] = value
}

// newProxy 按类型创建一个空代理。
func newProxy(payload ProxyPayload) (v1.ProxyConfigurer, error) {
	switch payload.Type {
	case TypeTCP:
		return &v1.TCPProxyConfig{}, nil
	case TypeUDP:
		return &v1.UDPProxyConfig{}, nil
	case TypeHTTP:
		return &v1.HTTPProxyConfig{}, nil
	case TypeHTTPS:
		return &v1.HTTPSProxyConfig{}, nil
	case TypeTCPMux:
		return &v1.TCPMuxProxyConfig{}, nil
	default:
		return nil, fmt.Errorf("暂不支持创建 %s 类型的代理", payload.Type)
	}
}

// validateProxyPayload 检查界面提交的代理配置。
//
// frp 自己的校验允许本地端口为 0（插件代理不需要本地端口），
// 但界面提供的这几种类型都必须有本地服务可转发，0 端口只会在运行时报错，
// 因此在这里提前拦下。
func validateProxyPayload(payload ProxyPayload) error {
	if strings.TrimSpace(payload.Name) == "" {
		return errors.New("代理名称不能为空")
	}
	if len([]rune(payload.Name)) > 64 {
		return errors.New("代理名称不能超过 64 个字符")
	}
	if !isProxyType(payload.Type) {
		return fmt.Errorf("暂不支持 %s 类型的代理", payload.Type)
	}
	if payload.LocalPort <= 0 || payload.LocalPort > 65535 {
		return errors.New("本地端口必须在 1-65535 之间")
	}
	if payload.RemotePort < 0 || payload.RemotePort > 65535 {
		return errors.New("远端端口必须在 0-65535 之间（0 表示由服务端分配）")
	}
	return nil
}

// applyProxyPayload 把界面提交的字段写进一条代理。
//
// 只设置该类型真正拥有的字段：frp 的校验器会因为「http 代理上出现 remotePort」
// 之类的字段组合而报错，按类型分别赋值可以从源头避免。
func applyProxyPayload(proxy v1.ProxyConfigurer, payload ProxyPayload) error {
	base := proxy.GetBaseConfig()
	base.Name = payload.Name
	base.Type = payload.Type

	switch c := proxy.(type) {
	case *v1.TCPProxyConfig:
		c.LocalIP, c.LocalPort, c.RemotePort = payload.LocalIP, payload.LocalPort, payload.RemotePort
	case *v1.UDPProxyConfig:
		c.LocalIP, c.LocalPort, c.RemotePort = payload.LocalIP, payload.LocalPort, payload.RemotePort
	case *v1.HTTPProxyConfig:
		c.LocalIP, c.LocalPort = payload.LocalIP, payload.LocalPort
		c.CustomDomains, c.SubDomain = payload.CustomDomains, payload.SubDomain
	case *v1.HTTPSProxyConfig:
		c.LocalIP, c.LocalPort = payload.LocalIP, payload.LocalPort
		c.CustomDomains, c.SubDomain = payload.CustomDomains, payload.SubDomain
	case *v1.TCPMuxProxyConfig:
		c.LocalIP, c.LocalPort = payload.LocalIP, payload.LocalPort
		c.CustomDomains, c.SubDomain = payload.CustomDomains, payload.SubDomain
	default:
		return fmt.Errorf("暂不支持编辑 %s 类型的代理", payload.Type)
	}
	return nil
}

// findProxy 按名字找一条普通代理。
func findProxy(cfg *v1.ClientConfig, name string) (*v1.TypedProxyConfig, int) {
	for i := range cfg.Proxies {
		if cfg.Proxies[i].GetBaseConfig().Name == name {
			return &cfg.Proxies[i], i
		}
	}
	return nil, -1
}

// findVisitor 按名字找一个访问端。
func findVisitor(cfg *v1.ClientConfig, name string) (*v1.TypedVisitorConfig, int) {
	for i := range cfg.Visitors {
		if cfg.Visitors[i].GetBaseConfig().Name == name {
			return &cfg.Visitors[i], i
		}
	}
	return nil, -1
}

// proxyNameTaken 判断代理名在普通代理与访问端之间是否已经被占用。
// frp 要求两者名字互不重复。
func proxyNameTaken(cfg *v1.ClientConfig, name string, exceptIndex int) bool {
	for i := range cfg.Proxies {
		if i != exceptIndex && cfg.Proxies[i].GetBaseConfig().Name == name {
			return true
		}
	}
	for i := range cfg.Visitors {
		if cfg.Visitors[i].GetBaseConfig().Name == name {
			return true
		}
	}
	return false
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

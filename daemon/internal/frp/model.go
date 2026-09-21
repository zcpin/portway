// Package frp 把 fatedier/frp 作为库嵌入，管理一组 frpc 客户端。
//
// 与 internal/tunnel 的定位对应：那里是 SSH 转发，这里是 FRP 代理。差别在配置形态——
// 每个客户端是一份 frp 原生的 TOML 文件（<配置目录>/frp/clients/<名字>.toml）：
// 我们不发明自己的格式，因此这些文件可以直接交给官方 frpc 使用，反向也成立（可直接导入现成配置）。
// 我们自己的元数据（分组、是否随引擎自动启动）写在 frp 原生支持的 metadatas 字段里，
// 不额外加私有段。
//
// 代理的启用/禁用用 frp 原生的 start 列表表达（列表为空 = 全部启用），
// 切换时通过热更新生效，不需要重启客户端。
package frp

// 写在配置文件 metadatas 里的元数据键。用 mgr 前缀避免与用户的 meta_* 冲突。
const (
	metaKeyGroup     = "mgrGroup"
	metaKeyAutoStart = "mgrAutoStart"
)

// 代理类型。阶段 1 的界面只提供 tcp / udp，其余类型在读取时原样保留。
const (
	TypeTCP    = "tcp"
	TypeUDP    = "udp"
	TypeTCPMux = "tcpmux"
	TypeHTTP   = "http"
	TypeHTTPS  = "https"
	TypeSTCP   = "stcp"
	TypeXTCP   = "xtcp"
	TypeSUDP   = "sudp"
)

// 认证方式。
const (
	AuthToken = "token"
	AuthOIDC  = "oidc"
)

// ClientInfo 是客户端配置与运行状态的合并视图，供界面直接渲染。
//
// 只包含界面用得到的字段：配置文件里 frp 支持的其它字段（OIDC、transport、
// 代理私有参数等）在读写时原样保留，不经过这里。
type ClientInfo struct {
	Name       string `json:"name"`
	Group      string `json:"group,omitempty"`
	AutoStart  bool   `json:"auto_start"`
	ServerAddr string `json:"server_addr"`
	ServerPort int    `json:"server_port"`
	AuthMethod string `json:"auth_method,omitempty"`
	// AuthToken 是明文令牌。它本来就以明文写在同一台机器的配置文件里，
	// 界面需要回显才能编辑其它字段，因此不做脱敏处理。
	AuthToken string `json:"auth_token,omitempty"`
	TLSEnable bool   `json:"tls_enable"`
	LogLevel  string `json:"log_level,omitempty"`

	Proxies []ProxyInfo `json:"proxies"`

	// 运行时状态
	Running   bool   `json:"running"`
	StartedAt string `json:"started_at,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// ProxyInfo 是一条代理的配置与运行状态。
type ProxyInfo struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Enabled       bool     `json:"enabled"`
	LocalIP       string   `json:"local_ip,omitempty"`
	LocalPort     int      `json:"local_port,omitempty"`
	RemotePort    int      `json:"remote_port,omitempty"`
	CustomDomains []string `json:"custom_domains,omitempty"`
	SubDomain     string   `json:"subdomain,omitempty"`
	// Editable 表示界面是否可以修改这条代理。阶段 1 只支持普通代理，
	// visitor（stcp / xtcp / sudp 的访问端）暂不支持编辑。
	Editable bool `json:"editable"`

	// 运行时状态，只对启用的代理有意义
	Phase      string `json:"phase,omitempty"`
	RemoteAddr string `json:"remote_addr,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

// ClientPayload 是新建或更新客户端时提交的配置。
type ClientPayload struct {
	Name       string `json:"name"`
	Group      string `json:"group,omitempty"`
	AutoStart  *bool  `json:"auto_start,omitempty"`
	ServerAddr string `json:"server_addr"`
	ServerPort int    `json:"server_port"`
	AuthMethod string `json:"auth_method,omitempty"`
	AuthToken  string `json:"auth_token,omitempty"`
	TLSEnable  bool   `json:"tls_enable,omitempty"`
	LogLevel   string `json:"log_level,omitempty"`
}

// ProxyPayload 是新建或更新代理时提交的配置。
//
// 字段按提交值生效：留空即清空对应配置，因此界面需要把未填写的输入框也一并提交。
type ProxyPayload struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Enabled       *bool    `json:"enabled,omitempty"`
	LocalIP       string   `json:"local_ip,omitempty"`
	LocalPort     int      `json:"local_port,omitempty"`
	RemotePort    int      `json:"remote_port,omitempty"`
	CustomDomains []string `json:"custom_domains,omitempty"`
	SubDomain     string   `json:"subdomain,omitempty"`
}

// isProxyType 判断是否是阶段 1 可以编辑的代理类型。
func isProxyType(t string) bool {
	switch t {
	case TypeTCP, TypeUDP, TypeTCPMux, TypeHTTP, TypeHTTPS:
		return true
	default:
		return false
	}
}

// isVisitorType 判断是否是访问端类型（stcp / xtcp / sudp 的 visitor 角色）。
func isVisitorType(t string) bool {
	switch t {
	case TypeSTCP, TypeXTCP, TypeSUDP:
		return true
	default:
		return false
	}
}

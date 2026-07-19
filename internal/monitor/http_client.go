package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPTimeout = 20 * time.Second
	defaultBodyLimit   = int64(1 << 20)
)

var permanentlyBlockedPrefixes = []netip.Prefix{
	// 这些网段不属于可安全访问的公网目标，即使显式允许私网也始终拒绝。
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// Resolver 允许测试或部署环境替换域名解析器。
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// HTTPOptions 控制所有适配器共享的安全 HTTP 行为。
type HTTPOptions struct {
	Timeout          time.Duration
	MaxResponseBytes int64
	Resolver         Resolver
}

type secureHTTPClient struct {
	timeout  time.Duration
	maxBody  int64
	resolver Resolver
}

type requestSession struct {
	owner        *secureHTTPClient
	client       *http.Client
	allowPrivate bool
}

func newSecureHTTPClient(options HTTPOptions) *secureHTTPClient {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	maxBody := options.MaxResponseBytes
	if maxBody <= 0 || maxBody > defaultBodyLimit {
		maxBody = defaultBodyLimit
	}
	resolver := options.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &secureHTTPClient{timeout: timeout, maxBody: maxBody, resolver: resolver}
}

func (client *secureHTTPClient) newSession(allowPrivate bool) *requestSession {
	jar, _ := cookiejar.New(nil)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// 禁用环境代理，确保目标连接始终经过本地 DNS 与 IP 校验，避免代理端重新解析造成绕过。
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: client.timeout, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("目标地址格式无效")
		}
		addresses, err := client.resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("目标域名解析失败")
		}
		// 直接拨号已校验的 IP，避免校验后再次按主机名解析产生 DNS 重绑定窗口。
		for _, address := range addresses {
			if err := validateResolvedIP(address.IP, allowPrivate); err != nil {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
		}
		return nil, fmt.Errorf("目标域名没有允许访问的地址")
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   client.timeout,
		Jar:       jar,
	}
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 {
			return nil
		}
		if len(via) >= 5 {
			return checkError(ErrorClassRemote, "处理重定向", "渠道重定向次数过多", 0, nil)
		}
		if canonicalAuthority(req.URL) != canonicalAuthority(via[0].URL) {
			return checkError(ErrorClassConfig, "处理重定向", "拒绝跨主机重定向", 0, nil)
		}
		if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return checkError(ErrorClassConfig, "处理重定向", "拒绝从 HTTPS 降级到 HTTP", 0, nil)
		}
		if err := client.validateURL(req.Context(), req.URL, allowPrivate); err != nil {
			return err
		}
		return nil
	}
	return &requestSession{owner: client, client: httpClient, allowPrivate: allowPrivate}
}

// ValidateTargetURL 验证地址协议、主机和解析结果是否符合默认 SSRF 策略。
func ValidateTargetURL(ctx context.Context, rawURL string, allowPrivate bool) error {
	return newSecureHTTPClient(HTTPOptions{}).validateRawURL(ctx, rawURL, allowPrivate)
}

func (client *secureHTTPClient) validateRawURL(ctx context.Context, rawURL string, allowPrivate bool) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return checkError(ErrorClassConfig, "校验渠道地址", "渠道地址格式无效", 0, nil)
	}
	return client.validateURL(ctx, parsed, allowPrivate)
}

func (client *secureHTTPClient) validateURL(ctx context.Context, parsed *url.URL, allowPrivate bool) error {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return checkError(ErrorClassConfig, "校验渠道地址", "渠道地址仅支持 HTTP 或 HTTPS", 0, nil)
	}
	if parsed.User != nil {
		return checkError(ErrorClassConfig, "校验渠道地址", "渠道地址不能包含用户名或密码", 0, nil)
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if isMetadataHostname(host) {
		return checkError(ErrorClassConfig, "校验渠道地址", "禁止访问云元数据地址", 0, nil)
	}
	addresses, err := client.resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return checkError(ErrorClassNetwork, "解析渠道地址", "渠道域名解析失败", 0, err)
	}
	for _, address := range addresses {
		if err := validateResolvedIP(address.IP, allowPrivate); err != nil {
			return err
		}
	}
	return nil
}

func validateResolvedIP(ip net.IP, allowPrivate bool) error {
	if ip == nil {
		return checkError(ErrorClassConfig, "校验渠道地址", "渠道地址解析结果无效", 0, nil)
	}
	if isMetadataIP(ip) {
		return checkError(ErrorClassConfig, "校验渠道地址", "禁止访问云元数据地址", 0, nil)
	}
	if ip.IsUnspecified() || ip.IsMulticast() {
		return checkError(ErrorClassConfig, "校验渠道地址", "禁止访问无效网络地址", 0, nil)
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return checkError(ErrorClassConfig, "校验渠道地址", "禁止访问链路本地或云凭据地址", 0, nil)
	}
	address, valid := netip.AddrFromSlice(ip)
	if !valid {
		return checkError(ErrorClassConfig, "校验渠道地址", "渠道地址解析结果无效", 0, nil)
	}
	address = address.Unmap()
	if isPermanentlyBlockedIP(address) {
		return checkError(ErrorClassConfig, "校验渠道地址", "禁止访问保留或不可路由网络地址", 0, nil)
	}
	if !allowPrivate && (!address.IsGlobalUnicast() || address.IsLoopback() || address.IsPrivate()) {
		return checkError(ErrorClassConfig, "校验渠道地址", "默认仅允许访问可路由公网单播地址", 0, nil)
	}
	return nil
}

func isPermanentlyBlockedIP(address netip.Addr) bool {
	for _, prefix := range permanentlyBlockedPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func isMetadataHostname(host string) bool {
	blocked := map[string]struct{}{
		"metadata":                 {},
		"metadata.google.internal": {},
		"metadata.goog":            {},
		"instance-data":            {},
	}
	_, exists := blocked[host]
	return exists
}

func isMetadataIP(ip net.IP) bool {
	blocked := []string{"169.254.169.254", "100.100.100.200", "fd00:ec2::254"}
	for _, raw := range blocked {
		if ip.Equal(net.ParseIP(raw)) {
			return true
		}
	}
	return false
}

func canonicalAuthority(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return strings.ToLower(parsed.Hostname()) + ":" + port
}

func joinTargetURL(baseURL, endpoint string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", checkError(ErrorClassConfig, "拼接渠道地址", "渠道地址格式无效", 0, nil)
	}
	reference, err := url.Parse(endpoint)
	if err != nil {
		return "", checkError(ErrorClassConfig, "拼接渠道地址", "渠道接口地址格式无效", 0, nil)
	}
	return base.ResolveReference(reference).String(), nil
}

func (session *requestSession) doJSON(ctx context.Context, method, rawURL string, headers http.Header, body []byte, output any) error {
	if err := session.owner.validateRawURL(ctx, rawURL, session.allowPrivate); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return checkError(ErrorClassConfig, "创建渠道请求", "无法创建渠道请求", 0, nil)
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	request.Header.Set("Accept", "application/json")
	if len(body) > 0 && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := session.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return checkError(ErrorClassNetwork, "请求渠道", "渠道请求已取消或超时", 0, ctx.Err())
		}
		var typed *CheckError
		if errors.As(err, &typed) {
			return typed
		}
		return checkError(ErrorClassNetwork, "请求渠道", "无法连接渠道", 0, err)
	}

	responseBody, readErr := readLimitedBody(response.Body, session.owner.maxBody)
	_ = response.Body.Close()
	if readErr != nil {
		return readErr
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return checkError(ErrorClassAuth, "验证渠道凭据", "渠道凭据无效或权限不足", response.StatusCode, nil)
	}
	if response.StatusCode >= 500 {
		return checkError(ErrorClassServer, "请求渠道", "渠道服务器暂时不可用", response.StatusCode, nil)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return checkError(ErrorClassRemote, "请求渠道", "渠道返回了非成功状态", response.StatusCode, nil)
	}
	if output == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return checkError(ErrorClassResponse, "解析渠道响应", "渠道返回的 JSON 无效", response.StatusCode, err)
	}
	return nil
}

func readLimitedBody(body io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(body, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, checkError(ErrorClassNetwork, "读取渠道响应", "无法读取渠道响应", 0, err)
	}
	if int64(len(data)) > limit {
		return nil, checkError(ErrorClassResponse, "读取渠道响应", "渠道响应超过 1 MB 限制", 0, nil)
	}
	return data, nil
}

func setBearer(headers http.Header, token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		headers.Set("Authorization", token)
		return
	}
	headers.Set("Authorization", "Bearer "+token)
}

func setBasic(headers http.Header, username, password string) {
	request := &http.Request{Header: headers}
	request.SetBasicAuth(username, password)
}

func statusCodeOf(err error) int {
	var typed *CheckError
	if errors.As(err, &typed) {
		return typed.StatusCode
	}
	return 0
}

func parseStatusCode(value any) int {
	parsed, err := parseDecimal(value)
	if err != nil {
		return 0
	}
	result, _ := strconv.Atoi(parsed.StringFixed(0))
	return result
}

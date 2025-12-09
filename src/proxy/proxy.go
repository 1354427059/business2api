package proxy

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/core"
	_ "github.com/xtls/xray-core/main/distro/all"
)

// tlsConfig 全局 TLS 配置，跳过证书验证
var tlsConfig = &tls.Config{InsecureSkipVerify: true}

// ProxyNode 代理节点
type ProxyNode struct {
	Raw       string // 原始链接
	Protocol  string // vmess, vless, ss, trojan, http, socks5
	Name      string
	Server    string
	Port      int
	UUID      string // vmess/vless
	AlterId   int    // vmess
	Security  string // vmess 加密方式
	Network   string // tcp, ws, grpc
	Path      string // ws path
	Host      string // ws host
	TLS       bool
	SNI       string
	Password  string // ss/trojan password
	Method    string // ss method
	Healthy   bool
	LastCheck time.Time
	LocalPort int
}

// XrayInstance xray 实例
type XrayInstance struct {
	server    *core.Instance
	localPort int
	node      *ProxyNode
	running   bool
	ctx       context.Context
	cancel    context.CancelFunc
}

// ProxyManager 代理管理器
type ProxyManager struct {
	mu             sync.RWMutex
	nodes          []*ProxyNode
	healthyNodes   []*ProxyNode
	currentIndex   int
	basePort       int
	instances      map[int]*XrayInstance
	subscribeURLs  []string
	proxyFiles     []string
	lastUpdate     time.Time
	updateInterval time.Duration
	checkInterval  time.Duration
	healthCheckURL string
}

var Manager = &ProxyManager{
	basePort:       10800,
	instances:      make(map[int]*XrayInstance),
	updateInterval: 30 * time.Minute,
	checkInterval:  5 * time.Minute,
	healthCheckURL: "https://www.google.com/generate_204",
}

func (pm *ProxyManager) SetXrayPath(path string) {
}

// AddSubscribeURL 添加订阅链接
func (pm *ProxyManager) AddSubscribeURL(url string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.subscribeURLs = append(pm.subscribeURLs, url)
}

// AddProxyFile 添加代理文件
func (pm *ProxyManager) AddProxyFile(path string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.proxyFiles = append(pm.proxyFiles, path)
}

// LoadAll 加载所有代理源
func (pm *ProxyManager) LoadAll() error {
	var allNodes []*ProxyNode

	// 从订阅加载
	for _, url := range pm.subscribeURLs {
		nodes, err := pm.loadFromURL(url)
		if err != nil {
			log.Printf("⚠️ 加载订阅失败 %s: %v", url, err)
			continue
		}
		allNodes = append(allNodes, nodes...)
	}

	// 从文件加载
	for _, file := range pm.proxyFiles {
		nodes, err := pm.loadFromFile(file)
		if err != nil {
			log.Printf("⚠️ 加载文件失败 %s: %v", file, err)
			continue
		}
		allNodes = append(allNodes, nodes...)
	}

	pm.mu.Lock()
	pm.nodes = allNodes
	pm.lastUpdate = time.Now()
	pm.mu.Unlock()

	log.Printf("✅ 共加载 %d 个代理节点", len(allNodes))
	return nil
}

// loadFromURL 从URL加载
func (pm *ProxyManager) loadFromURL(urlStr string) ([]*ProxyNode, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(urlStr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return pm.parseContent(string(body))
}

// loadFromFile 从文件加载
func (pm *ProxyManager) loadFromFile(path string) ([]*ProxyNode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return pm.parseContent(string(data))
}

func (pm *ProxyManager) parseContent(content string) ([]*ProxyNode, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(content))
	if err == nil {
		content = string(decoded)
	}

	var nodes []*ProxyNode
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		node := pm.parseLine(line)
		if node != nil {
			nodes = append(nodes, node)
		}
	}

	return nodes, nil
}

// parseLine 解析单行
func (pm *ProxyManager) parseLine(line string) *ProxyNode {
	if strings.HasPrefix(line, "vmess://") {
		return parseVmess(line)
	}
	if strings.HasPrefix(line, "vless://") {
		return parseVless(line)
	}
	if strings.HasPrefix(line, "ss://") {
		return parseSS(line)
	}
	if strings.HasPrefix(line, "trojan://") {
		return parseTrojan(line)
	}
	if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") || strings.HasPrefix(line, "socks5://") {
		return parseDirectProxy(line)
	}
	return nil
}

// parseVmess 解析 vmess 链接
func parseVmess(link string) *ProxyNode {
	// vmess://base64(json)
	data := strings.TrimPrefix(link, "vmess://")
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		decoded, _ = base64.RawStdEncoding.DecodeString(data)
	}
	if decoded == nil {
		return nil
	}

	var config map[string]interface{}
	if err := json.Unmarshal(decoded, &config); err != nil {
		return nil
	}

	node := &ProxyNode{
		Raw:      link,
		Protocol: "vmess",
	}

	if v, ok := config["ps"].(string); ok {
		node.Name = v
	}
	if v, ok := config["add"].(string); ok {
		node.Server = v
	}
	if v, ok := config["port"]; ok {
		switch p := v.(type) {
		case float64:
			node.Port = int(p)
		case string:
			node.Port, _ = strconv.Atoi(p)
		}
	}
	if v, ok := config["id"].(string); ok {
		node.UUID = v
	}
	if v, ok := config["aid"]; ok {
		switch a := v.(type) {
		case float64:
			node.AlterId = int(a)
		case string:
			node.AlterId, _ = strconv.Atoi(a)
		}
	}
	if v, ok := config["scy"].(string); ok {
		node.Security = v
	} else {
		node.Security = "auto"
	}
	if v, ok := config["net"].(string); ok {
		node.Network = v
	} else {
		node.Network = "tcp"
	}
	if v, ok := config["path"].(string); ok {
		node.Path = v
	}
	if v, ok := config["host"].(string); ok {
		node.Host = v
	}
	if v, ok := config["tls"].(string); ok && v == "tls" {
		node.TLS = true
	}
	if v, ok := config["sni"].(string); ok {
		node.SNI = v
	}

	if node.Server == "" || node.Port == 0 || node.UUID == "" {
		return nil
	}
	return node
}

// parseVless 解析 vless 链接
func parseVless(link string) *ProxyNode {
	// vless://uuid@server:port?params#name
	u, err := url.Parse(link)
	if err != nil {
		return nil
	}

	port, _ := strconv.Atoi(u.Port())
	node := &ProxyNode{
		Raw:      link,
		Protocol: "vless",
		UUID:     u.User.Username(),
		Server:   u.Hostname(),
		Port:     port,
		Name:     u.Fragment,
	}

	query := u.Query()
	node.Network = query.Get("type")
	if node.Network == "" {
		node.Network = "tcp"
	}
	node.Security = query.Get("security")
	if query.Get("security") == "tls" || query.Get("security") == "reality" {
		node.TLS = true
	}
	node.Path = query.Get("path")
	node.Host = query.Get("host")
	node.SNI = query.Get("sni")

	if node.Server == "" || node.Port == 0 || node.UUID == "" {
		return nil
	}
	return node
}

// parseSS 解析 ss 链接
func parseSS(link string) *ProxyNode {
	// ss://base64(method:password)@host:port#name
	// 或 ss://base64(method:password@host:port)#name
	link = strings.TrimPrefix(link, "ss://")

	var name string
	if idx := strings.Index(link, "#"); idx != -1 {
		name = link[idx+1:]
		link = link[:idx]
	}
	name, _ = url.QueryUnescape(name)

	node := &ProxyNode{
		Protocol: "shadowsocks",
		Name:     name,
	}

	if atIdx := strings.LastIndex(link, "@"); atIdx != -1 {
		// 新格式
		userInfo := link[:atIdx]
		hostPort := link[atIdx+1:]

		decoded, err := base64.URLEncoding.DecodeString(userInfo)
		if err != nil {
			decoded, _ = base64.StdEncoding.DecodeString(userInfo)
		}
		if decoded != nil {
			parts := strings.SplitN(string(decoded), ":", 2)
			if len(parts) == 2 {
				node.Method = parts[0]
				node.Password = parts[1]
			}
		}

		parts := strings.Split(hostPort, ":")
		if len(parts) == 2 {
			node.Server = parts[0]
			node.Port, _ = strconv.Atoi(parts[1])
		}
	} else {
		// 旧格式
		decoded, err := base64.URLEncoding.DecodeString(link)
		if err != nil {
			decoded, _ = base64.StdEncoding.DecodeString(link)
		}
		if decoded != nil {
			// method:password@host:port
			if atIdx := strings.LastIndex(string(decoded), "@"); atIdx != -1 {
				userInfo := string(decoded)[:atIdx]
				hostPort := string(decoded)[atIdx+1:]

				parts := strings.SplitN(userInfo, ":", 2)
				if len(parts) == 2 {
					node.Method = parts[0]
					node.Password = parts[1]
				}

				hpParts := strings.Split(hostPort, ":")
				if len(hpParts) == 2 {
					node.Server = hpParts[0]
					node.Port, _ = strconv.Atoi(hpParts[1])
				}
			}
		}
	}

	node.Raw = "ss://" + link
	if node.Server == "" || node.Port == 0 {
		return nil
	}
	return node
}

// parseTrojan 解析 trojan 链接
func parseTrojan(link string) *ProxyNode {
	// trojan://password@server:port?params#name
	u, err := url.Parse(link)
	if err != nil {
		return nil
	}

	port, _ := strconv.Atoi(u.Port())
	node := &ProxyNode{
		Raw:      link,
		Protocol: "trojan",
		Password: u.User.Username(),
		Server:   u.Hostname(),
		Port:     port,
		Name:     u.Fragment,
		TLS:      true, // trojan 默认 TLS
	}

	query := u.Query()
	node.SNI = query.Get("sni")
	if host := query.Get("host"); host != "" {
		node.Host = host
	}

	if node.Server == "" || node.Port == 0 || node.Password == "" {
		return nil
	}
	return node
}

// parseDirectProxy 解析直接代理
func parseDirectProxy(link string) *ProxyNode {
	u, err := url.Parse(link)
	if err != nil {
		return nil
	}

	port, _ := strconv.Atoi(u.Port())
	if port == 0 {
		if u.Scheme == "https" {
			port = 443
		} else {
			port = 80
		}
	}

	return &ProxyNode{
		Raw:       link,
		Protocol:  u.Scheme,
		Server:    u.Hostname(),
		Port:      port,
		LocalPort: port, // 直接代理使用原端口
		Healthy:   true,
	}
}

func (pm *ProxyManager) StartXray(node *ProxyNode) (string, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// 直接代理不需要 xray
	if node.Protocol == "http" || node.Protocol == "https" || node.Protocol == "socks5" {
		return node.Raw, nil
	}

	// 分配端口
	localPort := pm.allocatePort()
	if localPort == 0 {
		return "", fmt.Errorf("无可用端口")
	}

	// 生成 xray 配置
	xrayConfig := pm.buildXrayConfig(node, localPort)
	if xrayConfig == nil {
		return "", fmt.Errorf("生成配置失败")
	}

	// 启动内置 xray
	ctx, cancel := context.WithCancel(context.Background())
	server, err := core.New(xrayConfig)
	if err != nil {
		cancel()
		return "", fmt.Errorf("创建 xray 实例失败: %w", err)
	}

	if err := server.Start(); err != nil {
		cancel()
		return "", fmt.Errorf("启动 xray 失败: %w", err)
	}

	// 等待端口可用
	time.Sleep(300 * time.Millisecond)

	instance := &XrayInstance{
		server:    server,
		localPort: localPort,
		node:      node,
		running:   true,
		ctx:       ctx,
		cancel:    cancel,
	}
	pm.instances[localPort] = instance
	node.LocalPort = localPort
	return fmt.Sprintf("socks5://127.0.0.1:%d", localPort), nil
}
func (pm *ProxyManager) buildXrayConfig(node *ProxyNode, localPort int) *core.Config {
	jsonConfig := pm.generateXrayConfig(node, localPort)

	config, err := core.LoadConfig("json", strings.NewReader(jsonConfig))
	if err != nil {
		log.Printf("⚠️ 解析配置失败: %v", err)
		return nil
	}
	return config
}

// allocatePort 分配端口
func (pm *ProxyManager) allocatePort() int {
	for port := pm.basePort; port < pm.basePort+1000; port++ {
		if _, exists := pm.instances[port]; !exists {
			// 检查端口是否可用
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err == nil {
				ln.Close()
				return port
			}
		}
	}
	return 0
}

// generateXrayConfig 生成 xray 配置
func (pm *ProxyManager) generateXrayConfig(node *ProxyNode, localPort int) string {
	var outbound string
	// mux 多路复用配置
	muxConfig := `"mux": {"enabled": true, "concurrency": 8}`

	switch node.Protocol {
	case "vmess":
		outbound = fmt.Sprintf(`{
			"protocol": "vmess",
			"settings": {
				"vnext": [{
					"address": "%s",
					"port": %d,
					"users": [{
						"id": "%s",
						"alterId": %d,
						"security": "%s"
					}]
				}]
			},
			"streamSettings": %s,
			%s
		}`, node.Server, node.Port, node.UUID, node.AlterId, node.Security, pm.generateStreamSettings(node), muxConfig)

	case "vless":
		outbound = fmt.Sprintf(`{
			"protocol": "vless",
			"settings": {
				"vnext": [{
					"address": "%s",
					"port": %d,
					"users": [{
						"id": "%s",
						"encryption": "none"
					}]
				}]
			},
			"streamSettings": %s,
			%s
		}`, node.Server, node.Port, node.UUID, pm.generateStreamSettings(node), muxConfig)

	case "shadowsocks":
		outbound = fmt.Sprintf(`{
			"protocol": "shadowsocks",
			"settings": {
				"servers": [{
					"address": "%s",
					"port": %d,
					"method": "%s",
					"password": "%s"
				}]
			},
			%s
		}`, node.Server, node.Port, node.Method, node.Password, muxConfig)

	case "trojan":
		outbound = fmt.Sprintf(`{
			"protocol": "trojan",
			"settings": {
				"servers": [{
					"address": "%s",
					"port": %d,
					"password": "%s"
				}]
			},
			"streamSettings": %s,
			%s
		}`, node.Server, node.Port, node.Password, pm.generateStreamSettings(node), muxConfig)
	}

	return fmt.Sprintf(`{
		"log": {
			"loglevel": "none"
		},
		"inbounds": [{
			"port": %d,
			"listen": "127.0.0.1",
			"protocol": "socks",
			"settings": {
				"udp": true
			}
		}],
		"outbounds": [%s]
	}`, localPort, outbound)
}

// generateStreamSettings 生成传输设置
func (pm *ProxyManager) generateStreamSettings(node *ProxyNode) string {
	network := node.Network
	if network == "" {
		network = "tcp"
	}

	var settings string
	switch network {
	case "ws":
		settings = fmt.Sprintf(`"wsSettings": {"path": "%s", "headers": {"Host": "%s"}}`, node.Path, node.Host)
	case "grpc":
		settings = fmt.Sprintf(`"grpcSettings": {"serviceName": "%s"}`, node.Path)
	default:
		settings = ""
	}

	security := "none"
	tlsSettings := ""
	if node.TLS {
		security = "tls"
		sni := node.SNI
		if sni == "" {
			sni = node.Server
		}
		tlsSettings = fmt.Sprintf(`, "tlsSettings": {"serverName": "%s", "allowInsecure": true}`, sni)
	}

	if settings != "" {
		return fmt.Sprintf(`{"network": "%s", "security": "%s", %s%s}`, network, security, settings, tlsSettings)
	}
	return fmt.Sprintf(`{"network": "%s", "security": "%s"%s}`, network, security, tlsSettings)
}

// StopXray 停止 xray 实例
func (pm *ProxyManager) StopXray(localPort int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if instance, ok := pm.instances[localPort]; ok {
		if instance.server != nil {
			instance.server.Close()
		}
		if instance.cancel != nil {
			instance.cancel()
		}
		instance.running = false
		delete(pm.instances, localPort)
	}
}

// StopAll 停止所有实例
func (pm *ProxyManager) StopAll() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for port, instance := range pm.instances {
		if instance.server != nil {
			instance.server.Close()
		}
		if instance.cancel != nil {
			instance.cancel()
		}
		delete(pm.instances, port)
	}
	log.Printf("🛑 所有 xray 实例已停止")
}

// CheckHealth 检查节点健康状态
func (pm *ProxyManager) CheckHealth(node *ProxyNode) bool {
	proxyURL, err := pm.StartXray(node)
	if err != nil {
		return false
	}
	defer func() {
		if node.Protocol != "http" && node.Protocol != "https" && node.Protocol != "socks5" {
			pm.StopXray(node.LocalPort)
		}
	}()

	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
	}
	if proxyURL != "" {
		proxy, _ := url.Parse(proxyURL)
		transport.Proxy = http.ProxyURL(proxy)
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	resp, err := client.Get(pm.healthCheckURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == 204 || resp.StatusCode == 200
}

func (pm *ProxyManager) CheckAllHealth() {
	pm.mu.RLock()
	nodes := make([]*ProxyNode, len(pm.nodes))
	copy(nodes, pm.nodes)
	pm.mu.RUnlock()

	if len(nodes) == 0 {
		return
	}

	var healthy []*ProxyNode
	var checked int32
	var wg sync.WaitGroup
	var mu sync.Mutex

	total := len(nodes)
	log.Printf("🔍 开始检查 %d 个节点...", total)
	sem := make(chan struct{}, 64)

	for _, node := range nodes {
		wg.Add(1)
		go func(n *ProxyNode) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			n.Healthy = pm.CheckHealth(n)
			n.LastCheck = time.Now()

			current := int(atomic.AddInt32(&checked, 1))

			mu.Lock()
			if n.Healthy {
				healthy = append(healthy, n)
			}
			healthyCount := len(healthy)
			mu.Unlock()

			// 每 50 个或完成时输出进度
			if current%50 == 0 || current == total {
				log.Printf("🔍 进度: %d/%d, 健康: %d", current, total, healthyCount)
			}
		}(node)
	}

	wg.Wait()

	pm.mu.Lock()
	pm.healthyNodes = healthy
	pm.mu.Unlock()

	log.Printf("✅ 健康检查完成: %d/%d 节点可用", len(healthy), len(nodes))
}

// Next 获取下一个健康代理
func (pm *ProxyManager) Next() string {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if len(pm.healthyNodes) == 0 {
		// 如果没有健康节点，尝试使用所有节点
		if len(pm.nodes) == 0 {
			return ""
		}
		node := pm.nodes[pm.currentIndex%len(pm.nodes)]
		pm.currentIndex++

		// 尝试启动
		pm.mu.Unlock()
		proxy, err := pm.StartXray(node)
		pm.mu.Lock()
		if err != nil {
			log.Printf("⚠️ 启动代理失败: %v", err)
			return ""
		}
		return proxy
	}

	node := pm.healthyNodes[pm.currentIndex%len(pm.healthyNodes)]
	pm.currentIndex++

	// 启动 xray
	pm.mu.Unlock()
	proxy, err := pm.StartXray(node)
	pm.mu.Lock()
	if err != nil {
		log.Printf("⚠️ 启动代理失败: %v", err)
		return ""
	}
	return proxy
}

// Count 获取代理数量
func (pm *ProxyManager) Count() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if len(pm.healthyNodes) > 0 {
		return len(pm.healthyNodes)
	}
	return len(pm.nodes)
}

// HealthyCount 获取健康代理数量
func (pm *ProxyManager) HealthyCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.healthyNodes)
}

// TotalCount 获取总代理数量
func (pm *ProxyManager) TotalCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.nodes)
}

// StartAutoUpdate 启动自动更新和健康检查
func (pm *ProxyManager) StartAutoUpdate() {
	// 自动更新订阅
	go func() {
		for {
			time.Sleep(pm.updateInterval)
			if len(pm.subscribeURLs) > 0 || len(pm.proxyFiles) > 0 {
				if err := pm.LoadAll(); err != nil {
					log.Printf("⚠️ 自动更新代理失败: %v", err)
				}
			}
		}
	}()

	// 后台健康检查（启动时立即开始，不阻塞）
	go func() {
		// 延迟几秒后开始首次检查
		time.Sleep(3 * time.Second)
		log.Printf("🔍 开始后台健康检查...")
		pm.CheckAllHealth()

		// 定期检查
		for {
			time.Sleep(pm.checkInterval)
			pm.CheckAllHealth()
		}
	}()
}

// SetProxies 直接设置代理（兼容旧接口）
func (pm *ProxyManager) SetProxies(proxies []string) {
	var nodes []*ProxyNode
	for _, p := range proxies {
		if node := pm.parseLine(p); node != nil {
			nodes = append(nodes, node)
		}
	}
	pm.mu.Lock()
	pm.nodes = nodes
	pm.healthyNodes = nodes // 假设都健康
	pm.mu.Unlock()
	log.Printf("✅ 代理池已设置 %d 个代理", len(nodes))
}

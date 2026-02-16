package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"github.com/fsnotify/fsnotify"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"business2api/src/adminauth"
	"business2api/src/adminlogs"
	"business2api/src/flow"
	"business2api/src/logger"
	"business2api/src/pool"
	"business2api/src/proxy"
	"business2api/src/register"
	"business2api/src/utils"
)

// ==================== 配置结构 ====================
type PoolConfig struct {
	TargetCount            int      `json:"target_count"`              // 目标账号数量
	MinCount               int      `json:"min_count"`                 // 最小账号数，低于此值触发注册
	CheckIntervalMinutes   int      `json:"check_interval_minutes"`    // 检查间隔(分钟)
	EnableGoRegister       bool     `json:"enable_go_register"`        // 启用 Go 内置注册
	RegisterThreads        int      `json:"register_threads"`          // 注册线程数
	RegisterHeadless       bool     `json:"register_headless"`         // 无头模式
	MailChannelOrder       []string `json:"mail_channel_order"`        // 邮箱渠道优先级
	DuckMailBearer         string   `json:"duckmail_bearer"`           // DuckMail Bearer
	RefreshOnStartup       bool     `json:"refresh_on_startup"`        // 启动时刷新账号
	RefreshCooldownSec     int      `json:"refresh_cooldown_sec"`      // 刷新冷却时间(秒)
	UseCooldownSec         int      `json:"use_cooldown_sec"`          // 使用冷却时间(秒)
	MaxFailCount           int      `json:"max_fail_count"`            // 最大连续失败次数
	EnableBrowserRefresh   bool     `json:"enable_browser_refresh"`    // 启用浏览器刷新401账号
	BrowserRefreshHeadless bool     `json:"browser_refresh_headless"`  // 浏览器刷新无头模式
	BrowserRefreshMaxRetry int      `json:"browser_refresh_max_retry"` // 浏览器刷新最大重试次数(0=禁用)
	AutoDelete401          bool     `json:"auto_delete_401"`           // 401时自动删除账号
	ExternalRefreshMode    bool     `json:"external_refresh_mode"`     // 启用外部续期模式
	RegistrarBaseURL       string   `json:"registrar_base_url"`        // Python registrar 地址
}

// FlowConfig Flow 服务配置
type FlowConfigSection struct {
	Enable          bool     `json:"enable"`            // 是否启用 Flow
	Tokens          []string `json:"tokens"`            // Flow ST Tokens
	Proxy           string   `json:"proxy"`             // Flow 专用代理
	Timeout         int      `json:"timeout"`           // 超时时间
	PollInterval    int      `json:"poll_interval"`     // 轮询间隔
	MaxPollAttempts int      `json:"max_poll_attempts"` // 最大轮询次数
}

// ProxyConfig 代理配置
type ProxyConfig struct {
	Proxy          string   `json:"proxy"`            // 单个代理 (http/socks5)
	Subscribes     []string `json:"subscribes"`       // 订阅链接列表
	Files          []string `json:"files"`            // 代理文件列表
	HealthCheck    bool     `json:"health_check"`     // 是否启用健康检查
	CheckOnStartup bool     `json:"check_on_startup"` // 启动时检查
}

type AppConfig struct {
	APIKeys        []string              `json:"api_keys"`        // API 密钥列表
	ListenAddr     string                `json:"listen_addr"`     // 监听地址
	DataDir        string                `json:"data_dir"`        // 数据目录
	Pool           PoolConfig            `json:"pool"`            // 号池配置
	Proxy          string                `json:"proxy"`           // 代理 (兼容旧配置)
	ProxySubscribe string                `json:"proxy_subscribe"` // 代理订阅链接 (兼容旧配置)
	ProxyPool      ProxyConfig           `json:"proxy_pool"`      // 代理池配置
	DefaultConfig  string                `json:"default_config"`  // 默认 configId
	PoolServer     pool.PoolServerConfig `json:"pool_server"`     // 号池服务器配置
	Debug          bool                  `json:"debug"`           // 调试模式
	Flow           FlowConfigSection     `json:"flow"`            // Flow 配置
	Note           []string              `json:"note"`            // 备注信息（支持多行）
}

// PoolMode 号池模式
type PoolMode int

const (
	PoolModeLocal  PoolMode = iota // 本地模式
	PoolModeServer                 // 服务器模式（提供号池服务）
	PoolModeClient                 // 客户端模式（使用远程号池）
)

var (
	poolMode         PoolMode
	remotePoolClient *pool.RemotePoolClient
	flowClient       *flow.FlowClient
	flowHandler      *flow.GenerationHandler
	flowTokenPool    *flow.TokenPool
	panelAuthStore   *adminauth.Store
	panelSessions    *adminauth.SessionManager
	logStreamHandler gin.HandlerFunc
	panelAuthMu      sync.Mutex
	panelAuthDataDir string
)

// 配置热重载相关
var (
	configMu      sync.RWMutex           // 配置读写锁
	configWatcher *fsnotify.Watcher      // 配置文件监听器
	configPath    = "config/config.json" // 配置文件路径
)

// APIStats API 调用统计
type APIStats struct {
	mu              sync.RWMutex
	startTime       time.Time              // 服务启动时间
	totalRequests   int64                  // 总请求数
	successRequests int64                  // 成功请求数
	failedRequests  int64                  // 失败请求数
	inputTokens     int64                  // 输入 tokens
	outputTokens    int64                  // 输出 tokens
	imageGenerated  int64                  // 生成的图片数
	videoGenerated  int64                  // 生成的视频数
	requestTimes    []time.Time            // 最近请求时间（用于计算 RPM）
	modelStats      map[string]*ModelStats // 每个模型的统计
	hourlyStats     [24]HourlyStats        // 24小时统计
	lastHour        int                    // 上次记录的小时
}

// ModelStats 模型统计
type ModelStats struct {
	Requests     int64 `json:"requests"`
	Success      int64 `json:"success"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	Images       int64 `json:"images"`
}

// HourlyStats 小时统计
type HourlyStats struct {
	Hour         int   `json:"hour"`
	Requests     int64 `json:"requests"`
	Success      int64 `json:"success"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

var apiStats = &APIStats{
	startTime:    time.Now(),
	requestTimes: make([]time.Time, 0, 1000),
	modelStats:   make(map[string]*ModelStats),
	lastHour:     time.Now().Hour(),
}

// IPStats IP请求统计
type IPStats struct {
	mu         sync.RWMutex
	ipRequests map[string]*IPRequestInfo
}

// IPRequestInfo 单个IP的请求信息
type IPRequestInfo struct {
	IP           string           `json:"ip"`
	TotalCount   int64            `json:"total_count"`
	SuccessCount int64            `json:"success_count"`
	FailedCount  int64            `json:"failed_count"`
	InputTokens  int64            `json:"input_tokens"`
	OutputTokens int64            `json:"output_tokens"`
	ImagesCount  int64            `json:"images_count"`
	VideosCount  int64            `json:"videos_count"`
	FirstSeen    time.Time        `json:"first_seen"`
	LastSeen     time.Time        `json:"last_seen"`
	RequestTimes []time.Time      `json:"-"` // 用于计算RPM
	Models       map[string]int64 `json:"models"`
	UserAgents   map[string]int64 `json:"user_agents,omitempty"`
}

var ipStats = &IPStats{
	ipRequests: make(map[string]*IPRequestInfo),
}

// RecordIPRequest 记录IP请求（包含tokens、图片、视频统计）
func (s *IPStats) RecordIPRequest(ip, model, userAgent string, success bool, inputTokens, outputTokens, images, videos int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	info, exists := s.ipRequests[ip]
	if !exists {
		info = &IPRequestInfo{
			IP:           ip,
			FirstSeen:    now,
			Models:       make(map[string]int64),
			UserAgents:   make(map[string]int64),
			RequestTimes: make([]time.Time, 0, 100),
		}
		s.ipRequests[ip] = info
	}

	info.TotalCount++
	info.LastSeen = now
	info.InputTokens += inputTokens
	info.OutputTokens += outputTokens
	info.ImagesCount += images
	info.VideosCount += videos

	// 记录请求时间用于计算RPM（保留最近100条）
	info.RequestTimes = append(info.RequestTimes, now)
	if len(info.RequestTimes) > 100 {
		info.RequestTimes = info.RequestTimes[len(info.RequestTimes)-100:]
	}

	if success {
		info.SuccessCount++
	} else {
		info.FailedCount++
	}
	if model != "" {
		info.Models[model]++
	}
	if userAgent != "" && len(info.UserAgents) < 50 {
		info.UserAgents[userAgent]++
	}
}

// GetIPRPM 计算单个IP的RPM
func (info *IPRequestInfo) GetRPM() float64 {
	oneMinuteAgo := time.Now().Add(-time.Minute)
	count := 0
	for i := len(info.RequestTimes) - 1; i >= 0; i-- {
		if info.RequestTimes[i].After(oneMinuteAgo) {
			count++
		} else {
			break
		}
	}
	return float64(count)
}

func (s *IPStats) GetAllIPStats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type ipSortInfo struct {
		IP    string
		Count int64
	}
	sorted := make([]ipSortInfo, 0, len(s.ipRequests))
	for ip, info := range s.ipRequests {
		sorted = append(sorted, ipSortInfo{IP: ip, Count: info.TotalCount})
	}
	n := len(sorted)
	for i := 1; i < n; i++ {
		for j := i; j > 0 && sorted[j].Count > sorted[j-1].Count; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	var totalRequests, totalSuccess, totalFailed int64
	var totalInputTokens, totalOutputTokens int64
	var totalImages, totalVideos int64
	ips := make([]map[string]interface{}, 0, n)
	for i := 0; i < n; i++ {
		info := s.ipRequests[sorted[i].IP]
		rpm := info.GetRPM()
		totalRequests += info.TotalCount
		totalSuccess += info.SuccessCount
		totalFailed += info.FailedCount
		totalInputTokens += info.InputTokens
		totalOutputTokens += info.OutputTokens
		totalImages += info.ImagesCount
		totalVideos += info.VideosCount

		ips = append(ips, map[string]interface{}{
			"ip":            info.IP,
			"total_count":   info.TotalCount,
			"success_count": info.SuccessCount,
			"failed_count":  info.FailedCount,
			"success_rate":  fmt.Sprintf("%.1f%%", float64(info.SuccessCount)/float64(max(info.TotalCount, 1))*100),
			"input_tokens":  info.InputTokens,
			"output_tokens": info.OutputTokens,
			"total_tokens":  info.InputTokens + info.OutputTokens,
			"images":        info.ImagesCount,
			"videos":        info.VideosCount,
			"rpm":           rpm,
			"first_seen":    info.FirstSeen.Format(time.RFC3339),
			"last_seen":     info.LastSeen.Format(time.RFC3339),
			"models":        info.Models,
			"user_agents":   info.UserAgents,
		})
	}

	return map[string]interface{}{
		"server_time":         time.Now().Format(time.RFC3339),
		"unique_ips":          n,
		"total_requests":      totalRequests,
		"total_success":       totalSuccess,
		"total_failed":        totalFailed,
		"total_input_tokens":  totalInputTokens,
		"total_output_tokens": totalOutputTokens,
		"total_tokens":        totalInputTokens + totalOutputTokens,
		"total_images":        totalImages,
		"total_videos":        totalVideos,
		"ips":                 ips,
	}
}

// GetIPDetail 获取单个IP的详细信息
func (s *IPStats) GetIPDetail(ip string) *IPRequestInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ipRequests[ip]
}

// RecordRequest 记录请求
func (s *APIStats) RecordRequest(success bool, inputTokens, outputTokens, images, videos int64) {
	s.RecordRequestWithModel("", success, inputTokens, outputTokens, images, videos)
}

func (s *APIStats) RecordRequestWithModel(model string, success bool, inputTokens, outputTokens, images, videos int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalRequests++
	if success {
		s.successRequests++
	} else {
		s.failedRequests++
	}
	s.inputTokens += inputTokens
	s.outputTokens += outputTokens
	s.imageGenerated += images
	s.videoGenerated += videos

	// 记录请求时间（保留最近1000条）
	now := time.Now()
	s.requestTimes = append(s.requestTimes, now)
	if len(s.requestTimes) > 1000 {
		s.requestTimes = s.requestTimes[len(s.requestTimes)-1000:]
	}

	// 模型统计
	if model != "" {
		if s.modelStats[model] == nil {
			s.modelStats[model] = &ModelStats{}
		}
		ms := s.modelStats[model]
		ms.Requests++
		if success {
			ms.Success++
		}
		ms.InputTokens += inputTokens
		ms.OutputTokens += outputTokens
		ms.Images += images
	}

	// 小时统计
	currentHour := now.Hour()
	if currentHour != s.lastHour {
		// 新的小时，重置该小时统计
		s.hourlyStats[currentHour] = HourlyStats{Hour: currentHour}
		s.lastHour = currentHour
	}
	hs := &s.hourlyStats[currentHour]
	hs.Requests++
	if success {
		hs.Success++
	}
	hs.InputTokens += inputTokens
	hs.OutputTokens += outputTokens
}

func (s *APIStats) GetRPM() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	oneMinuteAgo := time.Now().Add(-time.Minute)
	count := 0
	for i := len(s.requestTimes) - 1; i >= 0; i-- {
		if s.requestTimes[i].After(oneMinuteAgo) {
			count++
		} else {
			break
		}
	}
	return float64(count)
}

// GetStats 获取统计数据
func (s *APIStats) GetStats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	uptime := time.Since(s.startTime)
	avgRPM := float64(0)
	if uptime.Minutes() > 0 {
		avgRPM = float64(s.totalRequests) / uptime.Minutes()
	}

	return map[string]interface{}{
		"uptime":           uptime.String(),
		"uptime_seconds":   int64(uptime.Seconds()),
		"total_requests":   s.totalRequests,
		"success_requests": s.successRequests,
		"failed_requests":  s.failedRequests,
		"success_rate":     fmt.Sprintf("%.2f%%", float64(s.successRequests)/float64(max(s.totalRequests, 1))*100),
		"input_tokens":     s.inputTokens,
		"output_tokens":    s.outputTokens,
		"total_tokens":     s.inputTokens + s.outputTokens,
		"images_generated": s.imageGenerated,
		"videos_generated": s.videoGenerated,
		"current_rpm":      s.GetRPM(),
		"average_rpm":      fmt.Sprintf("%.2f", avgRPM),
	}
}

// GetDetailedStats 获取详细统计数据
func (s *APIStats) GetDetailedStats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	uptime := time.Since(s.startTime)
	avgRPM := float64(0)
	if uptime.Minutes() > 0 {
		avgRPM = float64(s.totalRequests) / uptime.Minutes()
	}

	// 转换模型统计
	modelStatsMap := make(map[string]interface{})
	for model, ms := range s.modelStats {
		modelStatsMap[model] = map[string]interface{}{
			"requests":      ms.Requests,
			"success":       ms.Success,
			"success_rate":  fmt.Sprintf("%.2f%%", float64(ms.Success)/float64(max(ms.Requests, 1))*100),
			"input_tokens":  ms.InputTokens,
			"output_tokens": ms.OutputTokens,
			"total_tokens":  ms.InputTokens + ms.OutputTokens,
			"images":        ms.Images,
		}
	}

	// 转换小时统计
	hourlyStatsArr := make([]map[string]interface{}, 0, 24)
	for i := 0; i < 24; i++ {
		hs := s.hourlyStats[i]
		if hs.Requests > 0 {
			hourlyStatsArr = append(hourlyStatsArr, map[string]interface{}{
				"hour":          i,
				"requests":      hs.Requests,
				"success":       hs.Success,
				"input_tokens":  hs.InputTokens,
				"output_tokens": hs.OutputTokens,
			})
		}
	}

	return map[string]interface{}{
		"uptime":           uptime.String(),
		"uptime_seconds":   int64(uptime.Seconds()),
		"total_requests":   s.totalRequests,
		"success_requests": s.successRequests,
		"failed_requests":  s.failedRequests,
		"success_rate":     fmt.Sprintf("%.2f%%", float64(s.successRequests)/float64(max(s.totalRequests, 1))*100),
		"input_tokens":     s.inputTokens,
		"output_tokens":    s.outputTokens,
		"total_tokens":     s.inputTokens + s.outputTokens,
		"images_generated": s.imageGenerated,
		"videos_generated": s.videoGenerated,
		"current_rpm":      s.GetRPM(),
		"average_rpm":      fmt.Sprintf("%.2f", avgRPM),
		"models":           modelStatsMap,
		"hourly":           hourlyStatsArr,
	}
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func normalizeMailChannelOrder(order []string) []string {
	if len(order) == 0 {
		return []string{"chatgpt"}
	}

	seen := make(map[string]bool)
	var normalized []string
	for _, item := range order {
		ch := strings.ToLower(strings.TrimSpace(item))
		if ch != "duckmail" && ch != "chatgpt" {
			continue
		}
		if seen[ch] {
			continue
		}
		seen[ch] = true
		normalized = append(normalized, ch)
	}
	if len(normalized) == 0 {
		return []string{"chatgpt"}
	}
	return normalized
}

func getPoolBoolFieldFromJSON(data []byte, field string) (bool, bool) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return false, false
	}
	poolRaw, ok := root["pool"]
	if !ok {
		return false, false
	}
	var poolMap map[string]json.RawMessage
	if err := json.Unmarshal(poolRaw, &poolMap); err != nil {
		return false, false
	}
	valueRaw, ok := poolMap[field]
	if !ok {
		return false, false
	}
	var value bool
	if err := json.Unmarshal(valueRaw, &value); err != nil {
		return true, false
	}
	return true, value
}

var appConfig = AppConfig{
	ListenAddr: ":8000",
	DataDir:    "./data",
	Pool: PoolConfig{
		TargetCount:            50,
		MinCount:               10,
		CheckIntervalMinutes:   30,
		EnableGoRegister:       true,
		RegisterThreads:        1,
		RegisterHeadless:       false,
		MailChannelOrder:       []string{"chatgpt"},
		RefreshOnStartup:       true,
		RefreshCooldownSec:     240, // 4分钟
		UseCooldownSec:         15,  // 15秒
		MaxFailCount:           3,
		EnableBrowserRefresh:   true, // 默认启用浏览器刷新
		BrowserRefreshHeadless: false,
		BrowserRefreshMaxRetry: 1, // 浏览器刷新最多重试1次
		ExternalRefreshMode:    false,
		RegistrarBaseURL:       "http://127.0.0.1:8090",
	},
}

// GetAPIKeys 线程安全获取 API Keys
func GetAPIKeys() []string {
	configMu.RLock()
	defer configMu.RUnlock()
	keys := make([]string, len(appConfig.APIKeys))
	copy(keys, appConfig.APIKeys)
	return keys
}

// reloadConfig 重新加载配置文件（热重载）
func reloadConfig() error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("读取配置文件失败: %w", err)
	}

	var newConfig AppConfig
	if err := json.Unmarshal(data, &newConfig); err != nil {
		return fmt.Errorf("解析配置文件失败: %w", err)
	}

	configMu.Lock()
	oldAPIKeys := appConfig.APIKeys
	oldDebug := appConfig.Debug
	oldPoolConfig := appConfig.Pool
	hasEnableGoRegister, enableGoRegister := getPoolBoolFieldFromJSON(data, "enable_go_register")
	hasExternalRefreshMode, externalRefreshMode := getPoolBoolFieldFromJSON(data, "external_refresh_mode")

	// 更新可热重载的配置项
	appConfig.APIKeys = newConfig.APIKeys
	appConfig.Debug = newConfig.Debug
	appConfig.Note = newConfig.Note

	// 更新号池配置
	appConfig.Pool.RefreshCooldownSec = newConfig.Pool.RefreshCooldownSec
	appConfig.Pool.UseCooldownSec = newConfig.Pool.UseCooldownSec
	appConfig.Pool.MaxFailCount = newConfig.Pool.MaxFailCount
	appConfig.Pool.EnableBrowserRefresh = newConfig.Pool.EnableBrowserRefresh
	appConfig.Pool.BrowserRefreshHeadless = newConfig.Pool.BrowserRefreshHeadless
	appConfig.Pool.BrowserRefreshMaxRetry = newConfig.Pool.BrowserRefreshMaxRetry
	appConfig.Pool.AutoDelete401 = newConfig.Pool.AutoDelete401
	appConfig.Pool.EnableGoRegister = oldPoolConfig.EnableGoRegister
	if hasEnableGoRegister {
		appConfig.Pool.EnableGoRegister = enableGoRegister
	}
	appConfig.Pool.ExternalRefreshMode = oldPoolConfig.ExternalRefreshMode
	if hasExternalRefreshMode {
		appConfig.Pool.ExternalRefreshMode = externalRefreshMode
	}
	appConfig.Pool.MailChannelOrder = normalizeMailChannelOrder(newConfig.Pool.MailChannelOrder)
	appConfig.Pool.DuckMailBearer = strings.TrimSpace(newConfig.Pool.DuckMailBearer)
	if v := strings.TrimSpace(newConfig.Pool.RegistrarBaseURL); v != "" {
		appConfig.Pool.RegistrarBaseURL = v
	}
	newConfig.Pool.EnableGoRegister = appConfig.Pool.EnableGoRegister
	newConfig.Pool.ExternalRefreshMode = appConfig.Pool.ExternalRefreshMode
	newConfig.Pool.RegistrarBaseURL = appConfig.Pool.RegistrarBaseURL
	configMu.Unlock()

	// 应用变更
	applyConfigChanges(oldAPIKeys, oldDebug, oldPoolConfig, newConfig)

	return nil
}

// applyConfigChanges 应用配置变更
func applyConfigChanges(oldAPIKeys []string, oldDebug bool, oldPoolConfig PoolConfig, newConfig AppConfig) {
	// 日志模式变更
	if oldDebug != newConfig.Debug {
		logger.SetDebugMode(newConfig.Debug)
		logger.Info("🔄 调试模式: %v -> %v", oldDebug, newConfig.Debug)
	}

	// API Keys 变更
	if len(oldAPIKeys) != len(newConfig.APIKeys) {
		logger.Info("🔄 API Keys 数量: %d -> %d", len(oldAPIKeys), len(newConfig.APIKeys))
	}

	// 号池配置变更
	if oldPoolConfig.RefreshCooldownSec != newConfig.Pool.RefreshCooldownSec ||
		oldPoolConfig.UseCooldownSec != newConfig.Pool.UseCooldownSec {
		pool.SetCooldowns(newConfig.Pool.RefreshCooldownSec, newConfig.Pool.UseCooldownSec)
		logger.Info("🔄 冷却配置已更新: refresh=%ds, use=%ds",
			newConfig.Pool.RefreshCooldownSec, newConfig.Pool.UseCooldownSec)
	}

	if newConfig.Pool.MaxFailCount > 0 {
		pool.MaxFailCount = newConfig.Pool.MaxFailCount
	}

	pool.EnableBrowserRefresh = newConfig.Pool.EnableBrowserRefresh
	pool.BrowserRefreshHeadless = newConfig.Pool.BrowserRefreshHeadless
	if newConfig.Pool.BrowserRefreshMaxRetry >= 0 {
		pool.BrowserRefreshMaxRetry = newConfig.Pool.BrowserRefreshMaxRetry
	}
	pool.AutoDelete401 = newConfig.Pool.AutoDelete401
	pool.ExternalRefreshMode = newConfig.Pool.ExternalRefreshMode
	register.MailChannelOrder = normalizeMailChannelOrder(newConfig.Pool.MailChannelOrder)
	register.DuckMailBearer = strings.TrimSpace(newConfig.Pool.DuckMailBearer)
	register.EnableGoRegister = newConfig.Pool.EnableGoRegister

	logger.Info("✅ 配置热重载完成")
}

// startConfigWatcher 启动配置文件监听
func startConfigWatcher() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("创建配置监听器失败: %w", err)
	}
	configWatcher = watcher

	go configWatchLoop()

	// 监听配置目录
	configDir := filepath.Dir(configPath)
	if err := watcher.Add(configDir); err != nil {
		return fmt.Errorf("添加配置目录监听失败: %w", err)
	}

	logger.Info("🔄 配置文件热重载已启用: %s", configPath)
	return nil
}

// configWatchLoop 配置文件监听循环
func configWatchLoop() {
	var lastReload time.Time
	const debounceDelay = 500 * time.Millisecond

	for {
		select {
		case event, ok := <-configWatcher.Events:
			if !ok {
				return
			}
			// 只关注配置文件
			if filepath.Base(event.Name) != "config.json" {
				continue
			}
			// 只处理写入和创建事件
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			// 防抖：避免短时间内多次触发
			if time.Since(lastReload) < debounceDelay {
				continue
			}
			lastReload = time.Now()

			// 等待文件写入完成
			time.Sleep(100 * time.Millisecond)

			logger.Info("📝 检测到配置文件变更，正在重载...")
			if err := reloadConfig(); err != nil {
				logger.Error("❌ 配置重载失败: %v", err)
			}

		case err, ok := <-configWatcher.Errors:
			if !ok {
				return
			}
			logger.Error("❌ 配置监听错误: %v", err)
		}
	}
}

// stopConfigWatcher 停止配置文件监听
func stopConfigWatcher() {
	if configWatcher != nil {
		configWatcher.Close()
	}
}

var (
	DataDir       string
	Proxy         string
	ListenAddr    string
	DefaultConfig string
	JwtTTL        = 270 * time.Second
)

// mergeConfig 合并配置：loaded 中有值的字段覆盖 base 中的默认值
func mergeConfig(base, loaded *AppConfig) {
	// 基本字段
	if len(loaded.APIKeys) > 0 {
		base.APIKeys = loaded.APIKeys
	}
	if loaded.ListenAddr != "" {
		base.ListenAddr = loaded.ListenAddr
	}
	if loaded.DataDir != "" {
		base.DataDir = loaded.DataDir
	}
	if loaded.Proxy != "" {
		base.Proxy = loaded.Proxy
	}
	if loaded.DefaultConfig != "" {
		base.DefaultConfig = loaded.DefaultConfig
	}
	// Debug 是 bool，直接覆盖
	base.Debug = loaded.Debug

	// Pool 配置
	if loaded.Pool.TargetCount > 0 {
		base.Pool.TargetCount = loaded.Pool.TargetCount
	}
	if loaded.Pool.MinCount > 0 {
		base.Pool.MinCount = loaded.Pool.MinCount
	}
	if loaded.Pool.CheckIntervalMinutes > 0 {
		base.Pool.CheckIntervalMinutes = loaded.Pool.CheckIntervalMinutes
	}
	if loaded.Pool.RegisterThreads > 0 {
		base.Pool.RegisterThreads = loaded.Pool.RegisterThreads
	}
	if len(loaded.Pool.MailChannelOrder) > 0 {
		base.Pool.MailChannelOrder = normalizeMailChannelOrder(loaded.Pool.MailChannelOrder)
	}
	if strings.TrimSpace(loaded.Pool.DuckMailBearer) != "" {
		base.Pool.DuckMailBearer = strings.TrimSpace(loaded.Pool.DuckMailBearer)
	}
	if strings.TrimSpace(loaded.Pool.RegistrarBaseURL) != "" {
		base.Pool.RegistrarBaseURL = strings.TrimSpace(loaded.Pool.RegistrarBaseURL)
	}
	// bool 字段直接覆盖
	base.Pool.RegisterHeadless = loaded.Pool.RegisterHeadless
	base.Pool.RefreshOnStartup = loaded.Pool.RefreshOnStartup
	base.Pool.EnableBrowserRefresh = loaded.Pool.EnableBrowserRefresh
	base.Pool.BrowserRefreshHeadless = loaded.Pool.BrowserRefreshHeadless
	base.Pool.AutoDelete401 = loaded.Pool.AutoDelete401

	if loaded.Pool.RefreshCooldownSec > 0 {
		base.Pool.RefreshCooldownSec = loaded.Pool.RefreshCooldownSec
	}
	if loaded.Pool.UseCooldownSec > 0 {
		base.Pool.UseCooldownSec = loaded.Pool.UseCooldownSec
	}
	if loaded.Pool.MaxFailCount > 0 {
		base.Pool.MaxFailCount = loaded.Pool.MaxFailCount
	}
	if loaded.Pool.BrowserRefreshMaxRetry > 0 {
		base.Pool.BrowserRefreshMaxRetry = loaded.Pool.BrowserRefreshMaxRetry
	}

	// PoolServer 配置
	base.PoolServer = loaded.PoolServer

	// Flow 配置
	base.Flow = loaded.Flow

	// ProxyPool 配置
	if len(loaded.ProxyPool.Subscribes) > 0 {
		base.ProxyPool.Subscribes = loaded.ProxyPool.Subscribes
	}
	if len(loaded.ProxyPool.Files) > 0 {
		base.ProxyPool.Files = loaded.ProxyPool.Files
	}
	if loaded.ProxyPool.Proxy != "" {
		base.ProxyPool.Proxy = loaded.ProxyPool.Proxy
	}
	base.ProxyPool.HealthCheck = loaded.ProxyPool.HealthCheck
	base.ProxyPool.CheckOnStartup = loaded.ProxyPool.CheckOnStartup

	// Note
	if len(loaded.Note) > 0 {
		base.Note = loaded.Note
	}
}

// 保存默认配置到文件
func saveDefaultConfig(configPath string) error {
	// 确保目录存在
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(appConfig, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}

func loadAppConfig() {
	// 尝试加载配置文件
	configPath := "config/config.json"
	if data, err := os.ReadFile(configPath); err == nil {
		// 保留默认值，仅覆盖配置文件中存在的字段
		var loadedConfig AppConfig
		if err := json.Unmarshal(data, &loadedConfig); err != nil {
			logger.Warn("⚠️ 解析配置文件失败: %v，使用默认配置", err)
		} else {
			// 合并配置：配置文件中有的字段覆盖默认值，没有的保留默认值
			mergeConfig(&appConfig, &loadedConfig)
			if hasEnableGoRegister, enableGoRegister := getPoolBoolFieldFromJSON(data, "enable_go_register"); hasEnableGoRegister {
				appConfig.Pool.EnableGoRegister = enableGoRegister
			} else {
				appConfig.Pool.EnableGoRegister = true
			}
			if hasExternalRefreshMode, externalRefreshMode := getPoolBoolFieldFromJSON(data, "external_refresh_mode"); hasExternalRefreshMode {
				appConfig.Pool.ExternalRefreshMode = externalRefreshMode
			}
			logger.Info("✅ 加载配置文件: %s", configPath)
		}
	} else if os.IsNotExist(err) {
		// 配置文件不存在，创建默认配置
		logger.Warn("⚠️ 配置文件不存在，创建默认配置: %s", configPath)
		if err := saveDefaultConfig(configPath); err != nil {
			logger.Error("❌ 创建默认配置失败: %v", err)
		}
	}
	if v := os.Getenv("DATA_DIR"); v != "" {
		appConfig.DataDir = v
	}
	if v := os.Getenv("PROXY"); v != "" {
		appConfig.Proxy = v
	}
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		appConfig.ListenAddr = v
	}
	if v := os.Getenv("CONFIG_ID"); v != "" {
		appConfig.DefaultConfig = v
	}
	if v := os.Getenv("API_KEY"); v != "" {
		appConfig.APIKeys = append(appConfig.APIKeys, v)
	}

	// 设置全局变量
	DataDir = appConfig.DataDir
	Proxy = appConfig.Proxy
	ListenAddr = appConfig.ListenAddr
	DefaultConfig = appConfig.DefaultConfig

	// 应用调试模式
	logger.SetDebugMode(appConfig.Debug)

	// 应用号池配置
	pool.SetCooldowns(appConfig.Pool.RefreshCooldownSec, appConfig.Pool.UseCooldownSec)
	if appConfig.Pool.MaxFailCount > 0 {
		pool.MaxFailCount = appConfig.Pool.MaxFailCount
	}
	pool.EnableBrowserRefresh = appConfig.Pool.EnableBrowserRefresh
	pool.BrowserRefreshHeadless = appConfig.Pool.BrowserRefreshHeadless
	if appConfig.Pool.BrowserRefreshMaxRetry >= 0 {
		pool.BrowserRefreshMaxRetry = appConfig.Pool.BrowserRefreshMaxRetry
	}
	pool.AutoDelete401 = appConfig.Pool.AutoDelete401
	pool.ExternalRefreshMode = appConfig.Pool.ExternalRefreshMode
	// 服务端模式下，如果 expired_action 是 delete，则同步设置 AutoDelete401
	if appConfig.PoolServer.Enable && appConfig.PoolServer.Mode == "server" && appConfig.PoolServer.ExpiredAction == "delete" {
		pool.AutoDelete401 = true
		logger.Info("🗑️ 服务端模式 expired_action=delete，启用 AutoDelete401")
	}
	pool.DataDir = DataDir
	pool.DefaultConfig = DefaultConfig
	pool.Proxy = Proxy
	register.DataDir = DataDir
	register.TargetCount = appConfig.Pool.TargetCount
	register.MinCount = appConfig.Pool.MinCount
	register.CheckInterval = time.Duration(appConfig.Pool.CheckIntervalMinutes) * time.Minute
	register.Threads = appConfig.Pool.RegisterThreads
	register.Headless = appConfig.Pool.RegisterHeadless
	register.Proxy = Proxy
	register.EnableGoRegister = appConfig.Pool.EnableGoRegister
	register.MailChannelOrder = normalizeMailChannelOrder(appConfig.Pool.MailChannelOrder)
	register.DuckMailBearer = strings.TrimSpace(appConfig.Pool.DuckMailBearer)

	// 初始化代理池
	initProxyPool()

	if pool.EnableBrowserRefresh && pool.BrowserRefreshMaxRetry > 0 {
		logger.Info("🌐 浏览器刷新已启用 (headless=%v, 最大重试=%d)", pool.BrowserRefreshHeadless, pool.BrowserRefreshMaxRetry)
	} else if pool.EnableBrowserRefresh {
		logger.Info("🌐 浏览器刷新已禁用 (max_retry=0)")
		pool.EnableBrowserRefresh = false
	}

	// 初始化 Flow 客户端
	initFlowClient()
}

// initFlowClient 初始化 Flow 客户端
func initFlowClient() {
	if !appConfig.Flow.Enable {
		logger.Info("📹 Flow 服务已禁用")
		return
	}

	cfg := flow.FlowConfig{
		Proxy:           appConfig.Flow.Proxy,
		Timeout:         appConfig.Flow.Timeout,
		PollInterval:    appConfig.Flow.PollInterval,
		MaxPollAttempts: appConfig.Flow.MaxPollAttempts,
	}
	if cfg.Proxy == "" {
		cfg.Proxy = Proxy
	}

	flowClient = flow.NewFlowClient(cfg)

	// 初始化 Token 池
	flowTokenPool = flow.NewTokenPool(DataDir, flowClient)

	// 从 data/at 目录加载 Token
	loadedFromDir, err := flowTokenPool.LoadFromDir()
	if err != nil {
		logger.Warn("⚠️ 从 data/at 加载 Flow Token 失败: %v", err)
	}

	// 添加配置文件中的 Tokens（兼容旧配置）
	for i, st := range appConfig.Flow.Tokens {
		token := &flow.FlowToken{
			ID: fmt.Sprintf("flow_token_%d", i),
			ST: st,
		}
		flowClient.AddToken(token)
	}

	totalTokens := loadedFromDir + len(appConfig.Flow.Tokens)
	if totalTokens == 0 {
		logger.Info("📹 Flow 服务已启用但无可用 Token (请将 cookie 放入 data/at/ 目录)")
		flowHandler = flow.NewGenerationHandler(flowClient)
		return
	}

	// 启动 AT 刷新 worker (每 30 分钟刷新一次)
	flowTokenPool.StartRefreshWorker(30 * time.Minute)

	// 启动文件监听 (自动加载新增 Token)
	if err := flowTokenPool.StartWatcher(); err != nil {
		logger.Warn("⚠️ Flow 文件监听启动失败: %v", err)
	}

	flowHandler = flow.NewGenerationHandler(flowClient)
	logger.Info("📹 Flow 服务已启用，共 %d 个 Token (目录: %d, 配置: %d)", totalTokens, loadedFromDir, len(appConfig.Flow.Tokens))
}

func initProxyPool() {
	// 服务端模式不需要代理池
	if appConfig.PoolServer.Enable && appConfig.PoolServer.Mode == "server" {
		logger.Info("🖥️ 服务端模式，跳过代理初始化")
		return
	}

	// 初始化 sing-box（用于 hysteria2/tuic 等协议）
	proxy.InitSingbox()

	// 添加订阅链接（新配置）
	for _, sub := range appConfig.ProxyPool.Subscribes {
		proxy.Manager.AddSubscribeURL(sub)
	}
	// 兼容旧配置
	if appConfig.ProxySubscribe != "" {
		proxy.Manager.AddSubscribeURL(appConfig.ProxySubscribe)
	}

	// 添加代理文件
	for _, file := range appConfig.ProxyPool.Files {
		proxy.Manager.AddProxyFile(file)
	}
	if err := proxy.Manager.LoadAll(); err != nil {
		logger.Warn("⚠️ 加载代理失败: %v", err)
	}

	// 当有代理配置时，默认开启健康检查（除非明确关闭）
	hasProxyConfig := len(appConfig.ProxyPool.Subscribes) > 0 || len(appConfig.ProxyPool.Files) > 0 || appConfig.ProxySubscribe != ""
	shouldHealthCheck := hasProxyConfig || appConfig.ProxyPool.HealthCheck

	if shouldHealthCheck && appConfig.ProxyPool.CheckOnStartup {
		go func() {
			proxy.Manager.CheckAllHealth()
			// 健康检查完成后初始化实例池
			if proxy.Manager.HealthyCount() > 0 {
				poolSize := appConfig.Pool.RegisterThreads
				if poolSize <= 0 {
					poolSize = pool.DefaultProxyCount
				}
				if poolSize > 10 {
					poolSize = 10
				}
				proxy.Manager.SetMaxPoolSize(poolSize)
				if err := proxy.Manager.InitInstancePool(poolSize); err != nil {
					logger.Warn("⚠️ 初始化代理实例池失败: %v", err)
				} else {
					logger.Info("✅ 代理实例池初始化完成: %d 个实例", poolSize)
				}
			}
		}()
	} else if proxy.Manager.TotalCount() > 0 {
		// 不需要健康检查时直接标记就绪
		proxy.Manager.SetReady(true)
	}
	if proxy.Manager.TotalCount() == 0 {
		if appConfig.ProxyPool.Proxy != "" {
			proxy.Manager.SetProxies([]string{appConfig.ProxyPool.Proxy})
		} else if Proxy != "" {
			proxy.Manager.SetProxies([]string{Proxy})
		}
	}
	if proxy.Manager.TotalCount() == 0 || AutoSubscribeEnabled {
		logger.Info("🔄 启动自动订阅服务（每小时注册获取代理）...")
		proxy.Manager.StartAutoSubscribe()
	}

	if proxy.Manager.TotalCount() > 0 {
		proxy.Manager.StartAutoUpdate()
		logger.Info("✅ 代理池已初始化: %d 个节点, %d 个健康",
			proxy.Manager.TotalCount(), proxy.Manager.HealthyCount())
	}
	register.GetProxy = func() string {
		if proxy.Manager.Count() > 0 {
			return proxy.Manager.Next()
		}
		return Proxy
	}
	register.ReleaseProxy = func(proxyURL string) {
		proxy.Manager.ReleaseByURL(proxyURL)
	}
}

var BaseModels = []string{
	// Gemini 文本模型
	"gemini-2.5-flash",
	"gemini-2.5-pro",
	"gemini-3-pro-preview",
	"gemini-3-pro",
	// Gemini 图片生成
	"gemini-2.5-flash-image",
	"gemini-2.5-pro-image",
	"gemini-3-pro-preview-image",
	"gemini-3-pro-image",
	// Gemini 视频生成
	"gemini-2.5-flash-video",
	"gemini-2.5-pro-video",
	"gemini-3-pro-preview-video",
	"gemini-3-pro-video",
	// Gemini 搜索
	"gemini-2.5-flash-search",
	"gemini-2.5-pro-search",
	"gemini-3-pro-preview-search",
	"gemini-3-pro-search",

	"gemini-3-flash-preview",
	"gemini-3-flash-preview-image",
	"gemini-3-flash-preview-video",
	"gemini-3-flash-preview-search",

	"gemini-3-flash",
	"gemini-3-flash-image",
	"gemini-3-flash-video",
	"gemini-3-flash-search",

	"gemini-2.5-flash-preview-latest",
	"gemini-2.5-flash-preview-latest-image",
	"gemini-2.5-flash-preview-latest-video",
	"gemini-2.5-flash-preview-latest-search",
}
var FlowModels = []string{
	// Flow 图片生成模型
	"gemini-2.5-flash-image-landscape",
	"gemini-2.5-flash-image-portrait",
	"gemini-3.0-pro-image-landscape",
	"gemini-3.0-pro-image-portrait",
	"imagen-4.0-generate-preview-landscape",
	"imagen-4.0-generate-preview-portrait",
	// Flow 文生视频 (T2V)
	"veo_3_1_t2v_fast_portrait",
	"veo_3_1_t2v_fast_landscape",
	"veo_2_1_fast_d_15_t2v_portrait",
	"veo_2_1_fast_d_15_t2v_landscape",
	"veo_2_0_t2v_portrait",
	"veo_2_0_t2v_landscape",
	// Flow 图生视频 (I2V)
	"veo_3_1_i2v_s_fast_fl_portrait",
	"veo_3_1_i2v_s_fast_fl_landscape",
	"veo_2_1_fast_d_15_i2v_portrait",
	"veo_2_1_fast_d_15_i2v_landscape",
	"veo_2_0_i2v_portrait",
	"veo_2_0_i2v_landscape",
	// Flow 多图生成视频 (R2V)
	"veo_3_0_r2v_fast_portrait",
	"veo_3_0_r2v_fast_landscape",
}

func GetAvailableModels() []string {
	if flowHandler != nil {
		// Flow 已启用，返回全部模型
		return append(BaseModels, FlowModels...)
	}
	// Flow 未启用，只返回基础模型
	return BaseModels
}

// 模型名称映射到 Google API 的 modelId
var modelMapping = map[string]string{
	"gemini-2.5-flash":     "gemini-2.5-flash",
	"gemini-2.5-pro":       "gemini-2.5-pro",
	"gemini-3-pro-preview": "gemini-3-pro-preview",
	"gemini-3-pro":         "gemini-3-pro",
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getCommonHeaders(jwt, origAuth string) map[string]string {
	headers := map[string]string{
		"accept":             "*/*",
		"accept-encoding":    "gzip, deflate, br, zstd",
		"accept-language":    "zh-CN,zh;q=0.9,en;q=0.8",
		"authorization":      "Bearer " + jwt,
		"content-type":       "application/json",
		"origin":             "https://business.gemini.google",
		"referer":            "https://business.gemini.google/",
		"user-agent":         "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36",
		"x-server-timeout":   "1800",
		"sec-ch-ua":          `"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`,
		"sec-ch-ua-mobile":   "?0",
		"sec-ch-ua-platform": `"Windows"`,
		"sec-fetch-dest":     "empty",
		"sec-fetch-mode":     "cors",
		"sec-fetch-site":     "cross-site",
	}
	// 同时携带原始 authorization
	if origAuth != "" && !strings.HasPrefix(strings.ToLower(origAuth), "bearer fallback-csesidx-") {
		headers["x-original-authorization"] = origAuth
	}
	return headers
}

func createSession(jwt, configID, origAuth string) (string, error) {
	return createSessionWithRetry(jwt, configID, origAuth, 3)
}

// createSessionWithRetry 创建session带重试（处理400错误）
func createSessionWithRetry(jwt, configID, origAuth string, maxRetries int) (string, error) {
	var lastErr error

	for retry := 0; retry < maxRetries; retry++ {
		if retry > 0 {
			// 等待后重试
			waitTime := time.Duration(retry*500) * time.Millisecond
			time.Sleep(waitTime)
			logger.Info("🔄 createSession 重试 %d/%d", retry+1, maxRetries)
		}

		sessionName, err := createSessionOnce(jwt, configID, origAuth)
		if err == nil {
			return sessionName, nil
		}

		lastErr = err
		errMsg := err.Error()

		// 400错误可以重试
		if strings.Contains(errMsg, "400") {
			logger.Warn("⚠️ createSession 400 错误，尝试重试...")
			continue
		}

		// 401/403 不重试
		if strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403") {
			return "", err
		}

		// 其他错误继续重试
	}

	return "", lastErr
}

// createSessionOnce 单次创建session
func createSessionOnce(jwt, configID, origAuth string) (string, error) {
	body := map[string]interface{}{
		"configId":         configID,
		"additionalParams": map[string]string{"token": "-"},
		"createSessionRequest": map[string]interface{}{
			"session": map[string]string{"name": "", "displayName": ""},
		},
	}

	bodyBytes, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", "https://biz-discoveryengine.googleapis.com/v1alpha/locations/global/widgetCreateSession", bytes.NewReader(bodyBytes))

	for k, v := range getCommonHeaders(jwt, origAuth) {
		req.Header.Set(k, v)
	}

	resp, err := utils.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("createSession 请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := utils.ReadResponseBody(resp)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("createSession 失败: %d %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Session struct {
			Name string `json:"name"`
		} `json:"session"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("解析 session 响应失败: %w", err)
	}

	return result.Session.Name, nil
}
func uploadContextFile(jwt, configID, sessionName, mimeType, base64Content, origAuth string) (string, error) {
	ext := "jpg"
	if parts := strings.Split(mimeType, "/"); len(parts) == 2 {
		ext = parts[1]
	}
	fileName := fmt.Sprintf("upload_%d_%s.%s", time.Now().Unix(), uuid.New().String()[:6], ext)

	body := map[string]interface{}{
		"configId":         configID,
		"additionalParams": map[string]string{"token": "-"},
		"addContextFileRequest": map[string]interface{}{
			"name":         sessionName,
			"fileName":     fileName,
			"mimeType":     mimeType,
			"fileContents": base64Content,
		},
	}

	bodyBytes, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", "https://biz-discoveryengine.googleapis.com/v1alpha/locations/global/widgetAddContextFile", bytes.NewReader(bodyBytes))

	for k, v := range getCommonHeaders(jwt, origAuth) {
		req.Header.Set(k, v)
	}

	resp, err := utils.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("上传文件请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := utils.ReadResponseBody(resp)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("上传文件失败: %d %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		AddContextFileResponse struct {
			FileID string `json:"fileId"`
		} `json:"addContextFileResponse"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("解析上传响应失败: %w", err)
	}

	if result.AddContextFileResponse.FileID == "" {
		return "", fmt.Errorf("上传成功但 fileId 为空，响应: %s", string(respBody))
	}

	return result.AddContextFileResponse.FileID, nil
}
func uploadContextFileByURL(jwt, configID, sessionName, imageURL, origAuth string) (string, error) {
	body := map[string]interface{}{
		"configId":         configID,
		"additionalParams": map[string]string{"token": "-"},
		"addContextFileRequest": map[string]interface{}{
			"name":    sessionName,
			"fileUri": imageURL,
		},
	}

	bodyBytes, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", "https://biz-discoveryengine.googleapis.com/v1alpha/locations/global/widgetAddContextFile", bytes.NewReader(bodyBytes))

	for k, v := range getCommonHeaders(jwt, origAuth) {
		req.Header.Set(k, v)
	}

	resp, err := utils.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("上传文件请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := utils.ReadResponseBody(resp)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("URL上传文件失败: %d %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		AddContextFileResponse struct {
			FileID string `json:"fileId"`
		} `json:"addContextFileResponse"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("解析上传响应失败: %w", err)
	}

	if result.AddContextFileResponse.FileID == "" {
		return "", fmt.Errorf("URL上传成功但 fileId 为空，响应: %s", string(respBody))
	}

	return result.AddContextFileResponse.FileID, nil
}

type Message struct {
	Role       string      `json:"role"`
	Content    interface{} `json:"content"`                // string 或 []ContentPart
	Name       string      `json:"name,omitempty"`         // 函数名称（tool角色时）
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`   // 工具调用（assistant角色时）
	ToolCallID string      `json:"tool_call_id,omitempty"` // 工具调用ID（tool角色时）
}

type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL string `json:"url"`
}

// OpenAI格式的工具定义
type ToolDef struct {
	Type     string      `json:"type"` // "function"
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// 工具调用结果
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream"`
	Temperature float64   `json:"temperature"`
	TopP        float64   `json:"top_p"`
	Tools       []ToolDef `json:"tools,omitempty"`       // 工具定义
	ToolChoice  string    `json:"tool_choice,omitempty"` // "auto", "none", "required"
}

type ChatChoice struct {
	Index        int                    `json:"index"`
	Delta        map[string]interface{} `json:"delta,omitempty"`
	Message      map[string]interface{} `json:"message,omitempty"`
	FinishReason *string                `json:"finish_reason"`
	Logprobs     interface{}            `json:"logprobs"` // OpenAI兼容
}

type ChatChunk struct {
	ID                string       `json:"id"`
	Object            string       `json:"object"`
	Created           int64        `json:"created"`
	Model             string       `json:"model"`
	SystemFingerprint string       `json:"system_fingerprint,omitempty"`
	Choices           []ChatChoice `json:"choices"`
}

func createChunk(id string, created int64, model string, delta map[string]interface{}, finishReason *string) string {
	if delta == nil {
		delta = map[string]interface{}{}
	}
	chunk := ChatChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []ChatChoice{{
			Index:        0,
			Delta:        delta,
			FinishReason: finishReason,
			Logprobs:     nil,
		}},
	}
	data, _ := json.Marshal(chunk)
	return string(data)
}

func extractContentFromReply(replyMap map[string]interface{}, jwt, session, configID, origAuth string) (text string, imageData string, imageMime string, reasoning string, downloadErr error) {
	groundedContent, ok := replyMap["groundedContent"].(map[string]interface{})
	if !ok {
		return
	}
	content, ok := groundedContent["content"].(map[string]interface{})
	if !ok {
		return
	}
	if thought, ok := content["thought"].(bool); ok && thought {
		if t, ok := content["text"].(string); ok && t != "" {
			reasoning = t
		}
		return
	}
	if t, ok := content["text"].(string); ok && t != "" {
		text = t
	}
	if inlineData, ok := content["inlineData"].(map[string]interface{}); ok {
		if mime, ok := inlineData["mimeType"].(string); ok {
			imageMime = mime
		}
		if data, ok := inlineData["data"].(string); ok {
			imageData = data
		}
	}
	if file, ok := content["file"].(map[string]interface{}); ok {
		fileId, _ := file["fileId"].(string)
		mimeType, _ := file["mimeType"].(string)
		if fileId != "" {
			fileType := "文件"
			if strings.HasPrefix(mimeType, "image/") {
				fileType = "图片"
			} else if strings.HasPrefix(mimeType, "video/") {
				fileType = "视频"
			}
			data, err := downloadGeneratedFile(jwt, fileId, session, configID, origAuth)
			if err != nil {
				logger.Error("❌ 下载%s失败: %v", fileType, err)
				downloadErr = err // 返回错误供上层处理
			} else {
				imageData = data
				imageMime = mimeType
			}
		}
	}

	return
}

// ErrDownloadNeedsRetry 标识下载失败需要整体重试（换号重新生成）
var ErrDownloadNeedsRetry = fmt.Errorf("DOWNLOAD_NEEDS_RETRY")

func downloadGeneratedFile(jwt, fileId, session, configID, origAuth string) (string, error) {
	return downloadGeneratedFileWithRetry(jwt, fileId, session, configID, origAuth, 2)
}

func downloadGeneratedFileWithRetry(jwt, fileId, session, configID, origAuth string, maxRetries int) (string, error) {
	// 参数验证
	if jwt == "" {
		return "", fmt.Errorf("JWT 为空，无法下载文件")
	}
	if session == "" {
		return "", fmt.Errorf("session 为空，无法下载文件")
	}
	if configID == "" {
		return "", fmt.Errorf("configID 为空，无法下载文件")
	}
	var lastErr error
	var authFailCount int

	for retry := 0; retry < maxRetries; retry++ {
		result, err := downloadGeneratedFileOnce(jwt, fileId, session, configID, origAuth)
		if err == nil {
			return result, nil
		}

		lastErr = err
		errMsg := err.Error()

		// 检测认证失败（401/403）
		if strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403") ||
			strings.Contains(errMsg, "UNAUTHENTICATED") || strings.Contains(errMsg, "SESSION_COOKIE_INVALID") {
			authFailCount++
			logger.Warn("⚠️ 下载文件认证失败 (尝试 %d/%d): %v", retry+1, maxRetries, err)

			// 认证失败超过1次，返回特殊错误让上层重新发起整个请求
			if authFailCount >= 1 {
				logger.Info("🔄 下载认证失败，需要换号重新生成")
				return "", fmt.Errorf("%w: 401/403 认证失败", ErrDownloadNeedsRetry)
			}
			continue
		}

		// 其他错误，等待后重试
		logger.Error("❌ 下载文件失败 (尝试 %d/%d): %v", retry+1, maxRetries, err)
		time.Sleep(300 * time.Millisecond)
	}

	return "", fmt.Errorf("下载文件失败，已重试 %d 次: %w", maxRetries, lastErr)
}

// downloadGeneratedFileOnce 单次下载文件尝试
func downloadGeneratedFileOnce(jwt, fileId, session, configID, origAuth string) (string, error) {

	// 步骤1: 使用 widgetListSessionFileMetadata 获取文件下载 URL
	listBody := map[string]interface{}{
		"configId":         configID,
		"additionalParams": map[string]string{"token": "-"},
		"listSessionFileMetadataRequest": map[string]interface{}{
			"name":   session,
			"filter": "file_origin_type = AI_GENERATED",
		},
	}
	listBodyBytes, _ := json.Marshal(listBody)

	listReq, _ := http.NewRequest("POST", "https://biz-discoveryengine.googleapis.com/v1alpha/locations/global/widgetListSessionFileMetadata", bytes.NewReader(listBodyBytes))
	for k, v := range getCommonHeaders(jwt, origAuth) {
		listReq.Header.Set(k, v)
	}

	listResp, err := utils.HTTPClient.Do(listReq)
	if err != nil {
		return "", fmt.Errorf("获取文件元数据失败: %w", err)
	}
	defer listResp.Body.Close()

	listRespBody, _ := utils.ReadResponseBody(listResp)

	if listResp.StatusCode != 200 {
		return "", fmt.Errorf("获取文件元数据失败: HTTP %d: %s", listResp.StatusCode, string(listRespBody))
	}

	// 解析响应，查找匹配的 fileId
	var listResult struct {
		ListSessionFileMetadataResponse struct {
			FileMetadata []struct {
				FileID      string `json:"fileId"`
				Session     string `json:"session"` // 包含完整的 projects 路径
				DownloadURI string `json:"downloadUri"`
			} `json:"fileMetadata"`
		} `json:"listSessionFileMetadataResponse"`
	}
	if err := json.Unmarshal(listRespBody, &listResult); err != nil {
		return "", fmt.Errorf("解析文件元数据失败: %w", err)
	}

	// 查找匹配的文件，获取完整 session 路径
	var fullSession string
	for _, meta := range listResult.ListSessionFileMetadataResponse.FileMetadata {
		if meta.FileID == fileId {
			fullSession = meta.Session // 如: projects/372889301682/locations/global/collections/...
			break
		}
	}

	if fullSession == "" {
		return "", fmt.Errorf("未找到 fileId=%s 的文件信息", fileId)
	}

	downloadURL := fmt.Sprintf("https://biz-discoveryengine.googleapis.com/download/v1alpha/%s:downloadFile?fileId=%s&alt=media", fullSession, fileId)
	downloadReq, _ := http.NewRequest("GET", downloadURL, nil)
	for k, v := range getCommonHeaders(jwt, origAuth) {
		downloadReq.Header.Set(k, v)
	}

	downloadResp, err := utils.HTTPClient.Do(downloadReq)
	if err != nil {
		return "", fmt.Errorf("下载图片失败: %w", err)
	}
	defer downloadResp.Body.Close()

	imgBody, _ := utils.ReadResponseBody(downloadResp)

	if downloadResp.StatusCode != 200 {
		return "", fmt.Errorf("下载图片失败: HTTP %d: %s", downloadResp.StatusCode, string(imgBody))
	}

	// 响应是原始二进制图片数据，需要转为 base64
	return base64.StdEncoding.EncodeToString(imgBody), nil
}

// 将图片转换为 Markdown 格式的 data URI
func formatImageAsMarkdown(mimeType, base64Data string) string {
	return fmt.Sprintf("![image](data:%s;base64,%s)", mimeType, base64Data)
}

// 媒体信息（图片/视频）
type MediaInfo struct {
	MimeType  string
	Data      string // base64 数据
	URL       string // 原始 URL（如果有）
	IsURL     bool   // 是否使用 URL 直接上传
	MediaType string // "image" 或 "video"
}

// 别名，保持向后兼容
type ImageInfo = MediaInfo

// 解析消息内容，支持文本、图片和视频
func parseMessageContent(msg Message) (string, []MediaInfo) {
	var textContent string
	var medias []MediaInfo

	switch content := msg.Content.(type) {
	case string:
		textContent = content
	case []interface{}:
		for _, part := range content {
			partMap, ok := part.(map[string]interface{})
			if !ok {
				continue
			}

			partType, _ := partMap["type"].(string)
			switch partType {
			case "text":
				if text, ok := partMap["text"].(string); ok {
					textContent += text
				}
			case "image_url":
				if imgURL, ok := partMap["image_url"].(map[string]interface{}); ok {
					if urlStr, ok := imgURL["url"].(string); ok {
						media := parseMediaURL(urlStr, "image")
						if media != nil {
							medias = append(medias, *media)
						}
					}
				}
			case "video_url":
				// 支持视频 URL
				if videoURL, ok := partMap["video_url"].(map[string]interface{}); ok {
					if urlStr, ok := videoURL["url"].(string); ok {
						media := parseMediaURL(urlStr, "video")
						if media != nil {
							medias = append(medias, *media)
						}
					}
				}
			case "file":
				// 支持通用文件类型
				if fileData, ok := partMap["file"].(map[string]interface{}); ok {
					if urlStr, ok := fileData["url"].(string); ok {
						mediaType := "image" // 默认图片
						if mime, ok := fileData["mime_type"].(string); ok {
							if strings.HasPrefix(mime, "video/") {
								mediaType = "video"
							}
						}
						media := parseMediaURL(urlStr, mediaType)
						if media != nil {
							medias = append(medias, *media)
						}
					}
				}
			}
		}
	}

	return textContent, medias
}

// 解析媒体 URL（图片或视频）
func parseMediaURL(urlStr, defaultType string) *MediaInfo {
	// 处理 base64 数据
	if strings.HasPrefix(urlStr, "data:") {
		// data:image/jpeg;base64,/9j/4AAQ... 或 data:video/mp4;base64,...
		parts := strings.SplitN(urlStr, ",", 2)
		if len(parts) != 2 {
			return nil
		}

		base64Data := parts[1]
		var mediaType string
		var mimeType string

		// 检测媒体类型
		if strings.Contains(parts[0], "video/") {
			mediaType = "video"
			// 视频格式处理
			if strings.Contains(parts[0], "video/mp4") {
				mimeType = "video/mp4"
			} else if strings.Contains(parts[0], "video/webm") {
				mimeType = "video/webm"
			} else if strings.Contains(parts[0], "video/quicktime") || strings.Contains(parts[0], "video/mov") {
				// MOV 格式，尝试作为 mp4 上传
				mimeType = "video/mp4"
				logger.Debug("ℹ️ MOV 视频将作为 MP4 上传")
			} else if strings.Contains(parts[0], "video/avi") || strings.Contains(parts[0], "video/x-msvideo") {
				mimeType = "video/mp4"
				logger.Debug("ℹ️ AVI 视频将作为 MP4 上传")
			} else {
				// 其他视频格式默认作为 mp4
				mimeType = "video/mp4"
				logger.Debug("ℹ️ 未知视频格式 %s 将作为 MP4 上传", parts[0])
			}
		} else {
			mediaType = "image"
			// 图片格式处理
			if strings.Contains(parts[0], "image/png") {
				mimeType = "image/png"
			} else if strings.Contains(parts[0], "image/jpeg") {
				mimeType = "image/jpeg"
			} else {
				// 其他图片格式需要转换为 PNG
				converted, err := convertBase64ToPNG(base64Data)
				if err != nil {
					logger.Warn("⚠️ %s base64 转换失败: %v", parts[0], err)
					mimeType = "image/jpeg" // 回退
				} else {
					logger.Info("✅ %s base64 已转换为 PNG", parts[0])
					base64Data = converted
					mimeType = "image/png"
				}
			}
		}

		return &MediaInfo{
			MimeType:  mimeType,
			Data:      base64Data,
			IsURL:     false,
			MediaType: mediaType,
		}
	}

	// URL 媒体 - 优先尝试直接使用 URL 上传
	mediaType := defaultType
	lowerURL := strings.ToLower(urlStr)
	if strings.HasSuffix(lowerURL, ".mp4") || strings.HasSuffix(lowerURL, ".webm") ||
		strings.HasSuffix(lowerURL, ".mov") || strings.HasSuffix(lowerURL, ".avi") ||
		strings.HasSuffix(lowerURL, ".mkv") || strings.HasSuffix(lowerURL, ".m4v") {
		mediaType = "video"
	}

	return &MediaInfo{
		URL:       urlStr,
		IsURL:     true,
		MediaType: mediaType,
	}
}

func downloadImage(urlStr string) (string, string, error) {
	return downloadMedia(urlStr, "image")
}

// downloadMedia 下载媒体文件（图片或视频）
func downloadMedia(urlStr, mediaType string) (string, string, error) {
	resp, err := utils.HTTPClient.Get(urlStr)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	// 检查上游返回的状态码
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "", "", fmt.Errorf("UPSTREAM_%d: 上游返回状态码 %d 多媒体下载失败", resp.StatusCode, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("UPSTREAM_%d: 上游返回状态码 %d", resp.StatusCode, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}

	mimeType := resp.Header.Get("Content-Type")

	if mediaType == "video" || strings.HasPrefix(mimeType, "video/") {
		// 视频处理
		if mimeType == "" {
			mimeType = "video/mp4"
		}
		// 规范化视频 MIME 类型
		mimeType = normalizeVideoMimeType(mimeType)
		return base64.StdEncoding.EncodeToString(data), mimeType, nil
	}

	// 图片处理
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	needConvert := !strings.Contains(mimeType, "jpeg") && !strings.Contains(mimeType, "png")
	if needConvert {
		converted, err := convertToPNG(data)
		if err != nil {
			logger.Warn("⚠️ %s 转换失败: %v，尝试原格式", mimeType, err)
		} else {
			logger.Info("✅ %s 已转换为 PNG", mimeType)
			return base64.StdEncoding.EncodeToString(converted), "image/png", nil
		}
	}

	return base64.StdEncoding.EncodeToString(data), mimeType, nil
}

// normalizeVideoMimeType 规范化视频 MIME 类型
func normalizeVideoMimeType(mimeType string) string {
	switch {
	case strings.Contains(mimeType, "mp4"):
		return "video/mp4"
	case strings.Contains(mimeType, "webm"):
		return "video/webm"
	case strings.Contains(mimeType, "quicktime"), strings.Contains(mimeType, "mov"):
		logger.Debug("ℹ️ MOV 视频将作为 MP4 上传")
		return "video/mp4"
	case strings.Contains(mimeType, "avi"), strings.Contains(mimeType, "x-msvideo"):
		logger.Debug("ℹ️ AVI 视频将作为 MP4 上传")
		return "video/mp4"
	case strings.Contains(mimeType, "x-matroska"), strings.Contains(mimeType, "mkv"):
		logger.Debug("ℹ️ MKV 视频将作为 MP4 上传")
		return "video/mp4"
	case strings.Contains(mimeType, "3gpp"):
		return "video/3gpp"
	default:
		logger.Debug("ℹ️ 未知视频格式 %s 将作为 MP4 上传", mimeType)
		return "video/mp4"
	}
}

// convertToPNG 将图片转换为 PNG 格式
func convertToPNG(data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("解码图片失败: %w", err)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("编码 PNG 失败: %w", err)
	}

	return buf.Bytes(), nil
}

// convertBase64ToPNG 将 base64 图片转换为 PNG
func convertBase64ToPNG(base64Data string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return "", fmt.Errorf("解码 base64 失败: %w", err)
	}

	converted, err := convertToPNG(data)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(converted), nil
}

const maxRetries = 3

// convertMessagesToPrompt 将多轮对话转换为Gemini格式的prompt
// extractSystemPrompt 提取并返回系统提示词
func extractSystemPrompt(messages []Message) string {
	for _, msg := range messages {
		if msg.Role == "system" {
			text, _ := parseMessageContent(msg)
			return text
		}
	}
	return ""
}

// convertMessagesToPrompt 将多轮对话转换为带系统提示词的prompt
// 支持OpenAI/Claude/Gemini格式的messages
func convertMessagesToPrompt(messages []Message) string {
	var dialogParts []string
	var systemPrompt string

	for _, msg := range messages {
		text, _ := parseMessageContent(msg)
		if text == "" && msg.Role != "assistant" {
			continue
		}

		switch msg.Role {
		case "system":
			// 支持多个system消息拼接
			if systemPrompt != "" {
				systemPrompt += "\n" + text
			} else {
				systemPrompt = text
			}
		case "user", "human": // Claude使用human
			dialogParts = append(dialogParts, fmt.Sprintf("Human: %s", text))
		case "assistant":
			// 检查是否有工具调用
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					dialogParts = append(dialogParts, fmt.Sprintf("Assistant: [调用工具 %s(%s)]", tc.Function.Name, tc.Function.Arguments))
				}
			} else if text != "" {
				dialogParts = append(dialogParts, fmt.Sprintf("Assistant: %s", text))
			}
		case "tool", "tool_result": // Claude使用tool_result
			dialogParts = append(dialogParts, fmt.Sprintf("Tool Result [%s]: %s", msg.Name, text))
		}
	}

	// 组合最终prompt，系统提示词使用更强的格式
	var result strings.Builder
	if systemPrompt != "" {
		// 使用更明确的系统提示词格式，确保生效
		result.WriteString("<system>\n")
		result.WriteString(systemPrompt)
		result.WriteString("\n</system>\n\n")
	}
	if len(dialogParts) > 0 {
		result.WriteString(strings.Join(dialogParts, "\n\n"))
	}
	// 添加Assistant前缀引导回复
	result.WriteString("\n\nAssistant:")
	return result.String()
}

// ==================== Gemini API 兼容 ====================

// GeminiRequest Gemini generateContent API 请求格式
type GeminiRequest struct {
	Contents          []GeminiContent          `json:"contents"`
	SystemInstruction *GeminiContent           `json:"systemInstruction,omitempty"`
	GenerationConfig  map[string]interface{}   `json:"generationConfig,omitempty"`
	GeminiTools       []map[string]interface{} `json:"tools,omitempty"`
}

type GeminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GeminiPart `json:"parts"`
}

type GeminiPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *GeminiInlineData `json:"inlineData,omitempty"`
}

type GeminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// handleGeminiGenerate 处理Gemini generateContent API格式的请求
func handleGeminiGenerate(c *gin.Context) {
	action := c.Param("action")
	if action == "" {
		c.JSON(400, gin.H{"error": gin.H{"code": 400, "message": "Missing model action", "status": "INVALID_ARGUMENT"}})
		return
	}

	action = strings.TrimPrefix(action, "/")

	var model string
	var isStream bool
	if idx := strings.LastIndex(action, ":"); idx > 0 {
		model = action[:idx]
		actionType := action[idx+1:]
		isStream = actionType == "streamGenerateContent"
	} else {
		model = action
	}

	if model == "" {
		model = GetAvailableModels()[0]
	}

	var geminiReq GeminiRequest
	if err := c.ShouldBindJSON(&geminiReq); err != nil {
		c.JSON(400, gin.H{"error": gin.H{"code": 400, "message": err.Error(), "status": "INVALID_ARGUMENT"}})
		return
	}

	var messages []Message

	// 处理systemInstruction
	if geminiReq.SystemInstruction != nil && len(geminiReq.SystemInstruction.Parts) > 0 {
		var sysText string
		for _, part := range geminiReq.SystemInstruction.Parts {
			if part.Text != "" {
				sysText += part.Text
			}
		}
		if sysText != "" {
			messages = append(messages, Message{Role: "system", Content: sysText})
		}
	}

	// 处理contents
	for _, content := range geminiReq.Contents {
		role := content.Role
		if role == "model" {
			role = "assistant"
		}

		var textParts []string
		var contentParts []interface{}

		for _, part := range content.Parts {
			if part.Text != "" {
				textParts = append(textParts, part.Text)
			}
			if part.InlineData != nil {
				contentParts = append(contentParts, map[string]interface{}{
					"type": "image_url",
					"image_url": map[string]string{
						"url": fmt.Sprintf("data:%s;base64,%s", part.InlineData.MimeType, part.InlineData.Data),
					},
				})
			}
		}

		if len(contentParts) > 0 {
			if len(textParts) > 0 {
				contentParts = append([]interface{}{map[string]interface{}{"type": "text", "text": strings.Join(textParts, "\n")}}, contentParts...)
			}
			messages = append(messages, Message{Role: role, Content: contentParts})
		} else if len(textParts) > 0 {
			messages = append(messages, Message{Role: role, Content: strings.Join(textParts, "\n")})
		}
	}

	stream := isStream || c.Query("alt") == "sse"

	// 转换Gemini工具格式
	var tools []ToolDef
	for _, gt := range geminiReq.GeminiTools {
		if funcDecls, ok := gt["functionDeclarations"].([]interface{}); ok {
			for _, fd := range funcDecls {
				if funcMap, ok := fd.(map[string]interface{}); ok {
					name, _ := funcMap["name"].(string)
					desc, _ := funcMap["description"].(string)
					params, _ := funcMap["parameters"].(map[string]interface{})
					tools = append(tools, ToolDef{
						Type: "function",
						Function: FunctionDef{
							Name:        name,
							Description: desc,
							Parameters:  params,
						},
					})
				}
			}
		}
	}

	req := ChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   stream,
		Tools:    tools,
	}

	streamChat(c, req)
}

// ==================== Claude API 兼容 ====================

type ClaudeRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	System      string    `json:"system,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`
	Temperature float64   `json:"temperature,omitempty"`
	Tools       []ToolDef `json:"tools,omitempty"`
}

// handleClaudeMessages 处理Claude Messages API格式的请求
func handleClaudeMessages(c *gin.Context) {
	var claudeReq ClaudeRequest
	if err := c.ShouldBindJSON(&claudeReq); err != nil {
		c.JSON(400, gin.H{"type": "error", "error": gin.H{"type": "invalid_request_error", "message": err.Error()}})
		return
	}

	req := ChatRequest{
		Model:       claudeReq.Model,
		Messages:    claudeReq.Messages,
		Stream:      claudeReq.Stream,
		Temperature: claudeReq.Temperature,
		Tools:       claudeReq.Tools,
	}

	// 如果Claude格式有单独的system字段，插入到messages开头
	if claudeReq.System != "" {
		systemMsg := Message{Role: "system", Content: claudeReq.System}
		req.Messages = append([]Message{systemMsg}, req.Messages...)
	}

	if req.Model == "" {
		req.Model = GetAvailableModels()[0]
	}

	streamChat(c, req)
}

// buildToolsSpec 将OpenAI格式的工具定义转换为Gemini的toolsSpec
// 支持混合后缀同时启用多个功能，如 -image-search 同时启用图片生成和搜索
func buildToolsSpec(tools []ToolDef, isImageModel, isVideoModel, isSearchModel bool) map[string]interface{} {
	toolsSpec := make(map[string]interface{})

	// 检查是否指定了任何功能后缀
	hasAnySpec := isImageModel || isVideoModel || isSearchModel

	if !hasAnySpec {
		toolsSpec["webGroundingSpec"] = map[string]interface{}{}
		toolsSpec["toolRegistry"] = "default_tool_registry"
		toolsSpec["imageGenerationSpec"] = map[string]interface{}{}
		toolsSpec["videoGenerationSpec"] = map[string]interface{}{}
	} else {
		if isImageModel {
			toolsSpec["imageGenerationSpec"] = map[string]interface{}{}
		}
		if isVideoModel {
			toolsSpec["videoGenerationSpec"] = map[string]interface{}{}
		}
		if isSearchModel {
			toolsSpec["webGroundingSpec"] = map[string]interface{}{}
		}
	}
	_ = tools

	return toolsSpec
}

// extractToolCalls 从Gemini响应中提取工具调用
func extractToolCalls(dataList []map[string]interface{}) []ToolCall {
	var toolCalls []ToolCall

	for _, data := range dataList {
		streamResp, ok := data["streamAssistResponse"].(map[string]interface{})
		if !ok {
			continue
		}
		answer, ok := streamResp["answer"].(map[string]interface{})
		if !ok {
			continue
		}
		replies, ok := answer["replies"].([]interface{})
		if !ok {
			continue
		}

		for _, reply := range replies {
			replyMap, ok := reply.(map[string]interface{})
			if !ok {
				continue
			}
			groundedContent, ok := replyMap["groundedContent"].(map[string]interface{})
			if !ok {
				continue
			}
			content, ok := groundedContent["content"].(map[string]interface{})
			if !ok {
				continue
			}

			// 检查functionCall
			if fc, ok := content["functionCall"].(map[string]interface{}); ok {
				name, _ := fc["name"].(string)
				args, _ := fc["args"].(map[string]interface{})
				argsBytes, _ := json.Marshal(args)

				toolCalls = append(toolCalls, ToolCall{
					ID:   "call_" + uuid.New().String()[:8],
					Type: "function",
					Function: FunctionCall{
						Name:      name,
						Arguments: string(argsBytes),
					},
				})
			}
		}
	}

	return toolCalls
}

// needsConversationContext 检查是否需要对话上下文（多轮对话）
func needsConversationContext(messages []Message) bool {
	// 检查是否有多轮对话标志：存在assistant或tool消息
	for _, msg := range messages {
		if msg.Role == "assistant" || msg.Role == "tool" || msg.Role == "tool_result" {
			return true
		}
	}
	return false
}

// handleFlowRequest 处理 Flow 模型请求
func handleFlowRequest(c *gin.Context, req ChatRequest, chatID string, createdTime int64) {
	if flowHandler == nil {
		c.JSON(503, gin.H{"error": gin.H{
			"message": "Flow 服务未启用，请在配置文件中启用并添加 Token",
			"type":    "service_unavailable",
		}})
		return
	}

	// 解析消息内容和图片
	var prompt string
	var imageBytes [][]byte

	for _, msg := range req.Messages {
		if msg.Role == "user" || msg.Role == "human" {
			text, images := parseMessageContent(msg)
			if text != "" {
				prompt = text
			}
			// 提取图片数据
			for _, img := range images {
				if img.Data != "" {
					imgData, err := base64.StdEncoding.DecodeString(img.Data)
					if err == nil {
						imageBytes = append(imageBytes, imgData)
					}
				}
			}
		}
	}

	if prompt == "" {
		c.JSON(400, gin.H{"error": gin.H{
			"message": "Prompt cannot be empty",
			"type":    "invalid_request_error",
		}})
		return
	}

	flowReq := flow.GenerationRequest{
		Model:  req.Model,
		Prompt: prompt,
		Images: imageBytes,
		Stream: req.Stream,
	}

	if req.Stream {
		// 流式响应
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Status(200)

		flusher, ok := c.Writer.(http.Flusher)
		if !ok {
			c.JSON(500, gin.H{"error": "Streaming not supported"})
			return
		}

		result, _ := flowHandler.HandleGeneration(flowReq, func(chunk string) {
			c.Writer.WriteString(chunk)
			flusher.Flush()
		})

		// 发送 [DONE]
		c.Writer.WriteString("data: [DONE]\n\n")
		flusher.Flush()

		if result != nil && !result.Success && result.Error != "" {
			logger.Error("❌ [Flow] 生成失败: %s", result.Error)
		}
	} else {
		// 非流式响应
		result, err := flowHandler.HandleGeneration(flowReq, nil)
		if err != nil {
			c.JSON(500, gin.H{"error": gin.H{
				"message": err.Error(),
				"type":    "internal_error",
			}})
			return
		}

		if !result.Success {
			c.JSON(500, gin.H{"error": gin.H{
				"message": result.Error,
				"type":    "generation_failed",
			}})
			return
		}

		// 构建响应
		content := result.URL
		if result.Type == "image" {
			content = fmt.Sprintf("![Generated Image](%s)", result.URL)
		} else if result.Type == "video" {
			content = fmt.Sprintf("<video src='%s' controls></video>", result.URL)
		}

		c.JSON(200, gin.H{
			"id":      chatID,
			"object":  "chat.completion",
			"created": createdTime,
			"model":   req.Model,
			"choices": []gin.H{{
				"index": 0,
				"message": gin.H{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			}},
		})
	}
}

func streamChat(c *gin.Context, req ChatRequest) {
	chatID := "chatcmpl-" + uuid.New().String()
	createdTime := time.Now().Unix()
	clientIP := c.ClientIP()
	userAgent := c.GetHeader("User-Agent")

	// 统计变量
	var statsSuccess bool
	var statsInputTokens int64
	var statsOutputTokens int64
	var statsImages int64
	var statsVideos int64
	statsModel := req.Model
	defer func() {
		apiStats.RecordRequestWithModel(statsModel, statsSuccess, statsInputTokens, statsOutputTokens, statsImages, statsVideos)
		// 记录IP统计（包含tokens、图片、视频）
		ipStats.RecordIPRequest(clientIP, statsModel, userAgent, statsSuccess, statsInputTokens, statsOutputTokens, statsImages, statsVideos)
	}()

	// 入站日志
	logger.Info("📥 [%s] 请求: model=%s ", clientIP, req.Model)
	if flow.IsFlowModel(req.Model) {
		handleFlowRequest(c, req, chatID, createdTime)
		return
	}
	var textContent string
	var images []MediaInfo
	systemPrompt := extractSystemPrompt(req.Messages)
	if needsConversationContext(req.Messages) {
		// 多轮对话：拼接所有消息（包含system）
		textContent = convertMessagesToPrompt(req.Messages)
		// 只从最后一条用户消息提取图片
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" || req.Messages[i].Role == "human" {
				_, images = parseMessageContent(req.Messages[i])
				break
			}
		}
	} else {
		lastMsg := req.Messages[len(req.Messages)-1]
		userText, userImages := parseMessageContent(lastMsg)
		images = userImages
		if systemPrompt != "" {
			textContent = fmt.Sprintf("<system>\n%s\n</system>\n\nHuman: %s\n\nAssistant:", systemPrompt, userText)
		} else {
			textContent = userText
		}
	}
	var respBody []byte
	var lastErr error
	var lastErrStatusCode int // 保存最后一次错误的 HTTP 状态码
	var lastErrBody []byte    // 保存最后一次错误的响应体
	var usedAcc *pool.Account
	var usedJWT, usedOrigAuth, usedConfigID, usedSession string
	isLongRunning := !req.Stream && (strings.Contains(req.Model, "video") ||
		strings.Contains(req.Model, "imagen") ||
		strings.Contains(req.Model, "image"))

	var heartbeatDone chan struct{}
	if isLongRunning {
		heartbeatDone = make(chan struct{})
		c.Header("Content-Type", "application/json")
		c.Header("Transfer-Encoding", "chunked")
		c.Status(200)
		writer := c.Writer
		flusher, ok := writer.(http.Flusher)
		if ok {
			flusher.Flush() // 先发送头部
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
				}
			}()
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-heartbeatDone:
					return
				case <-ticker.C:
					if _, err := writer.Write([]byte(" ")); err != nil {
						return
					}
					if flusher, ok := writer.(http.Flusher); ok {
						flusher.Flush()
					}
				}
			}
		}()
	}
	defer func() {
		if heartbeatDone != nil {
			select {
			case <-heartbeatDone:
			default:
				close(heartbeatDone)
			}
		}
	}()

	// 估算输入 tokens（基于文本长度）
	statsInputTokens = int64(len(textContent)/4) + int64(len(images)*500) // 文本 + 图片估算

	// 流式请求：提前发送 SSE 头部，避免上游请求期间客户端等待超时
	var streamWriter http.ResponseWriter
	var streamFlusher http.Flusher
	var streamStarted bool
	if req.Stream {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		streamWriter = c.Writer
		streamFlusher, _ = streamWriter.(http.Flusher)
		chunk := createChunk(chatID, createdTime, req.Model, map[string]interface{}{"role": "assistant"}, nil)
		fmt.Fprintf(streamWriter, "data: %s\n\n", chunk)
		streamFlusher.Flush()
		streamStarted = true
	}

	for retry := 0; retry < maxRetries; retry++ {
		acc := pool.Pool.Next()
		if acc == nil {
			if streamStarted {
				// 流式请求已开始，发送 SSE 格式错误
				errChunk := createChunk(chatID, createdTime, req.Model, map[string]interface{}{"content": "[错误] 没有可用账号"}, nil)
				fmt.Fprintf(streamWriter, "data: %s\n\n", errChunk)
				finishReason := "stop"
				finalChunk := createChunk(chatID, createdTime, req.Model, nil, &finishReason)
				fmt.Fprintf(streamWriter, "data: %s\n\n", finalChunk)
				fmt.Fprintf(streamWriter, "data: [DONE]\n\n")
				streamFlusher.Flush()
			} else {
				c.JSON(500, gin.H{"error": "没有可用账号"})
			}
			return
		}
		usedAcc = acc
		logger.Info("📤 [%s] 使用账号: %s", clientIP, acc.Data.Email)

		if retry > 0 {
			logger.Info("🔄 第 %d 次重试，切换账号: %s", retry+1, acc.Data.Email)
		}

		jwt, configID, err := acc.GetJWT()
		if err != nil {
			logger.Error("❌ [%s] 获取 JWT 失败: %v", acc.Data.Email, err)
			lastErr = err
			continue
		}

		session, err := createSession(jwt, configID, acc.Data.Authorization)
		if err != nil {
			logger.Error("❌ [%s] 创建 Session 失败: %v", acc.Data.Email, err)
			// 401 错误标记账号需要刷新
			if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "UNAUTHENTICATED") {
				//		pool.Pool.MarkNeedsRefresh(acc)
			}
			lastErr = err
			continue
		}

		// 上传媒体文件并获取 fileIds
		var fileIds []string
		uploadFailed := false
		for _, media := range images {
			var fileId string
			var err error

			mediaTypeName := "图片"
			if media.MediaType == "video" {
				mediaTypeName = "视频"
			}

			if media.IsURL {
				// 优先尝试 URL 直接上传
				fileId, err = uploadContextFileByURL(jwt, configID, session, media.URL, acc.Data.Authorization)
				if err != nil {
					// URL 上传失败，回退到下载后上传
					mediaData, mimeType, dlErr := downloadMedia(media.URL, media.MediaType)
					if dlErr != nil {
						logger.Warn("⚠️ [%s] %s下载失败: %v", acc.Data.Email, mediaTypeName, dlErr)
						if strings.Contains(dlErr.Error(), "UPSTREAM_401") || strings.Contains(dlErr.Error(), "UPSTREAM_403") {
							c.JSON(500, gin.H{"error": gin.H{
								"message": dlErr.Error(),
								"type":    "upstream_error",
								"code":    "media_download_failed",
							}})
							return
						}
						uploadFailed = true
						break
					}
					fileId, err = uploadContextFile(jwt, configID, session, mimeType, mediaData, acc.Data.Authorization)
				}
			} else {
				fileId, err = uploadContextFile(jwt, configID, session, media.MimeType, media.Data, acc.Data.Authorization)
			}
			if err != nil {
				logger.Warn("⚠️ [%s] %s上传失败: %v", acc.Data.Email, mediaTypeName, err)
				uploadFailed = true
				break
			}
			fileIds = append(fileIds, fileId)
		}
		if uploadFailed {
			lastErr = fmt.Errorf("媒体上传失败")
			continue
		}
		// 构建 query parts（只包含文本）
		queryParts := []map[string]interface{}{}
		if textContent != "" {
			queryParts = append(queryParts, map[string]interface{}{"text": textContent})
		}
		// 确保 queryParts 不为空，避免 Google 返回空响应
		if len(queryParts) == 0 {
			queryParts = append(queryParts, map[string]interface{}{"text": " "})
		}
		isImageModel := strings.Contains(req.Model, "-image")
		isVideoModel := strings.Contains(req.Model, "-video")
		isSearchModel := strings.Contains(req.Model, "-search")
		actualModel := req.Model
		actualModel = strings.ReplaceAll(actualModel, "-image", "")
		actualModel = strings.ReplaceAll(actualModel, "-video", "")
		actualModel = strings.ReplaceAll(actualModel, "-search", "")

		// 构建 toolsSpec（支持自定义工具）
		toolsSpec := buildToolsSpec(req.Tools, isImageModel, isVideoModel, isSearchModel)

		body := map[string]interface{}{
			"configId":         configID,
			"additionalParams": map[string]string{"token": "-"},
			"streamAssistRequest": map[string]interface{}{
				"session":              session,
				"query":                map[string]interface{}{"parts": queryParts},
				"filter":               "",
				"fileIds":              fileIds,
				"answerGenerationMode": "NORMAL",
				"toolsSpec":            toolsSpec,
				"languageCode":         "zh-CN",
				"userMetadata":         map[string]string{"timeZone": "Asia/Shanghai"},
				"assistSkippingMode":   "REQUEST_ASSIST",
			},
		}

		// 设置模型 ID（去掉 -image 后缀）
		if targetModelID, ok := modelMapping[actualModel]; ok && targetModelID != "" {
			body["streamAssistRequest"].(map[string]interface{})["assistGenerationConfig"] = map[string]interface{}{
				"modelId": targetModelID,
			}
		}

		bodyBytes, _ := json.Marshal(body)
		httpReq, _ := http.NewRequest("POST", "https://biz-discoveryengine.googleapis.com/v1alpha/locations/global/widgetStreamAssist", bytes.NewReader(bodyBytes))

		for k, v := range getCommonHeaders(jwt, acc.Data.Authorization) {
			httpReq.Header.Set(k, v)
		}

		resp, err := utils.HTTPClient.Do(httpReq)
		if err != nil {
			logger.Error("❌ [%s] 请求失败: %v", acc.Data.Email, err)
			lastErr = err
			continue
		}

		if resp.StatusCode != 200 {
			body, _ := utils.ReadResponseBody(resp)
			resp.Body.Close()
			logger.Error("❌ [%s] Google 报错: %d %s (重试 %d/%d)", acc.Data.Email, resp.StatusCode, string(body), retry+1, maxRetries)
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
			lastErrStatusCode = resp.StatusCode
			lastErrBody = body
			// 401/403 无权限，标记需要刷新
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				logger.Warn("⚠️ [%s] %d 无权限，标记需要刷新", acc.Data.Email, resp.StatusCode)
				pool.Pool.MarkNeedsRefresh(acc)
			}
			// 429 限流，延长使用冷却时间（3倍冷却）
			if resp.StatusCode == 429 {
				cooldownTime := pool.UseCooldown * 3
				acc.Mu.Lock()
				acc.LastUsed = time.Now().Add(cooldownTime)
				acc.Mu.Unlock()
				logger.Info("⏳ [%s] 429 限流，账号进入延长冷却 %v", acc.Data.Email, cooldownTime)
				pool.Pool.MarkUsed(acc, false)
				time.Sleep(1 * time.Second) // 短暂等待后切换账号
				retry--                     // 不计入重试次数
				continue
			}
			if resp.StatusCode == 400 {
				logger.Warn("⚠️ [%s] 400 错误，换账号重试", acc.Data.Email)
				pool.Pool.MarkUsed(acc, false)
				time.Sleep(500 * time.Millisecond)
				continue
			}
			pool.Pool.MarkUsed(acc, false) // 标记失败
			continue
		}
		// 成功，读取响应
		respBody, _ = utils.ReadResponseBody(resp)
		resp.Body.Close()

		// Debug 模式输出上游响应
		if logger.IsDebug() {
			respSnippet := string(respBody)
			if len(respSnippet) > 2000 {
				respSnippet = respSnippet[:2000] + "..."
			}
			logger.Debug("[%s] 上游响应: %s", acc.Data.Email, respSnippet)
		}

		// 快速检查是否是认证错误响应
		if bytes.Contains(respBody, []byte("uToken")) && !bytes.Contains(respBody, []byte("streamAssistResponse")) {
			logger.Warn("[%s] 收到认证响应，标记需要刷新", acc.Data.Email)
			pool.Pool.MarkNeedsRefresh(acc)
			lastErr = fmt.Errorf("认证失败，需要刷新账号")
			continue
		}

		// 检查是否有实际内容（非空返回）
		hasText := bytes.Contains(respBody, []byte(`"text"`))
		hasFile := bytes.Contains(respBody, []byte(`"file"`))
		hasInlineData := bytes.Contains(respBody, []byte(`"inlineData"`))
		hasThought := bytes.Contains(respBody, []byte(`"thought"`))
		hasFunctionCall := bytes.Contains(respBody, []byte(`"functionCall"`))
		hasError := bytes.Contains(respBody, []byte(`"error"`)) || bytes.Contains(respBody, []byte(`"errorMessage"`))
		hasContent := hasText || hasFile || hasInlineData || hasFunctionCall

		// 检测是否有服务端错误信息
		if hasError && !hasContent {
			logger.Warn("[%s] 响应包含错误信息，重试 (%d/%d)", acc.Data.Email, retry+1, maxRetries)
			// 简单解析错误类型
			if bytes.Contains(respBody, []byte("RESOURCE_EXHAUSTED")) || bytes.Contains(respBody, []byte("quota")) {
				logger.Info("⏳ [%s] 检测到配额耗尽，标记冷却", acc.Data.Email)
				acc.SetCooldownMultiplier(5) // 5倍冷却
				pool.Pool.MarkUsed(acc, false)
			}
			lastErr = fmt.Errorf("上游返回错误响应")
			continue
		}

		// 响应完全为空或只有思考内容
		if !hasContent {
			if hasThought {
				logger.Warn("[%s] 响应只有思考内容，无实际输出，换号重试 (%d/%d)", acc.Data.Email, retry+1, maxRetries)
				lastErr = fmt.Errorf("空返回，只有思考内容")
				// 思考中的账号不标记失败，可能只是请求太慢
				time.Sleep(500 * time.Millisecond)
			} else {
				logger.Warn("[%s] 响应无有效内容 (text/file/inlineData/functionCall)，换号重试 (%d/%d)", acc.Data.Email, retry+1, maxRetries)
				lastErr = fmt.Errorf("空返回，无有效内容")
				pool.Pool.MarkUsed(acc, false)
			}
			continue
		}

		usedJWT = jwt
		usedOrigAuth = acc.Data.Authorization
		usedConfigID = configID
		usedSession = session // 保存创建的 session 作为回退
		usedAcc = acc
		lastErr = nil
		pool.Pool.MarkUsed(acc, true) // 标记成功
		break
	}

	if lastErr != nil {
		logger.Error("❌ 所有重试均失败: %v", lastErr)
		if streamStarted {
			// 流式请求已开始，发送 SSE 格式错误
			errMsg := fmt.Sprintf("[错误] %v", lastErr)
			errChunk := createChunk(chatID, createdTime, req.Model, map[string]interface{}{"content": errMsg}, nil)
			fmt.Fprintf(streamWriter, "data: %s\n\n", errChunk)
			finishReason := "stop"
			finalChunk := createChunk(chatID, createdTime, req.Model, nil, &finishReason)
			fmt.Fprintf(streamWriter, "data: %s\n\n", finalChunk)
			fmt.Fprintf(streamWriter, "data: [DONE]\n\n")
			streamFlusher.Flush()
		} else if lastErrStatusCode > 0 && len(lastErrBody) > 0 {
			// 如果有 HTTP 错误响应体，原样透传
			c.Data(lastErrStatusCode, "application/json", lastErrBody)
		} else {
			c.JSON(500, gin.H{"error": lastErr.Error()})
		}
		return
	}

	_ = usedAcc

	// 检查空响应
	if len(respBody) == 0 {
		logger.Error("❌ 响应为空")
		if streamStarted {
			errChunk := createChunk(chatID, createdTime, req.Model, map[string]interface{}{"content": "[错误] 上游返回空响应"}, nil)
			fmt.Fprintf(streamWriter, "data: %s\n\n", errChunk)
			finishReason := "stop"
			finalChunk := createChunk(chatID, createdTime, req.Model, nil, &finishReason)
			fmt.Fprintf(streamWriter, "data: %s\n\n", finalChunk)
			fmt.Fprintf(streamWriter, "data: [DONE]\n\n")
			streamFlusher.Flush()
		} else {
			c.JSON(500, gin.H{"error": "Empty response from Google"})
		}
		return
	}

	// 解析响应：支持多种格式
	var dataList []map[string]interface{}
	var parseErr error

	// 1. 尝试标准 JSON 数组
	if parseErr = json.Unmarshal(respBody, &dataList); parseErr != nil {
		logger.Warn("⚠️ JSON 数组解析失败: %v, 响应前100字符: %s", parseErr, string(respBody[:min(100, len(respBody))]))

		// 2. 尝试修复不完整的 JSON 数组
		dataList = utils.ParseIncompleteJSONArray(respBody)
		if dataList == nil {
			// 3. 尝试 NDJSON 格式
			logger.Warn("⚠️ 尝试 NDJSON 格式...")
			dataList = utils.ParseNDJSON(respBody)
		}

		if len(dataList) == 0 {
			// 输出完整响应用于调试
			respStr := string(respBody)
			if len(respStr) > 500 {
				logger.Error("❌ 所有解析方式均失败, 响应长度: %d, 前500字符: %s", len(respBody), respStr[:500])
				logger.Error("❌ 后200字符: %s", respStr[len(respStr)-200:])
			} else {
				logger.Error("❌ 所有解析方式均失败, 响应长度: %d, 完整响应: %s", len(respBody), respStr)
			}
			if streamStarted {
				errChunk := createChunk(chatID, createdTime, req.Model, map[string]interface{}{"content": "[错误] 响应解析失败"}, nil)
				fmt.Fprintf(streamWriter, "data: %s\n\n", errChunk)
				finishReason := "stop"
				finalChunk := createChunk(chatID, createdTime, req.Model, nil, &finishReason)
				fmt.Fprintf(streamWriter, "data: %s\n\n", finalChunk)
				fmt.Fprintf(streamWriter, "data: [DONE]\n\n")
				streamFlusher.Flush()
			} else {
				c.JSON(500, gin.H{"error": "JSON Parse Error"})
			}
			return
		}
		logger.Info("✅ 备用解析成功，共 %d 个对象", len(dataList))
	}

	// 检查是否有有效响应
	if len(dataList) > 0 {
		hasValidResponse := false
		hasFileContent := false
		for _, data := range dataList {
			if streamResp, ok := data["streamAssistResponse"].(map[string]interface{}); ok {
				hasValidResponse = true
				// 检查是否有文件内容
				if answer, ok := streamResp["answer"].(map[string]interface{}); ok {
					if replies, ok := answer["replies"].([]interface{}); ok {
						for _, reply := range replies {
							if replyMap, ok := reply.(map[string]interface{}); ok {
								if gc, ok := replyMap["groundedContent"].(map[string]interface{}); ok {
									if content, ok := gc["content"].(map[string]interface{}); ok {
										if _, ok := content["file"]; ok {
											hasFileContent = true
										}
									}
								}
							}
						}
					}
				}
			}
		}
		if !hasValidResponse {
			logger.Warn("⚠️ 响应中没有 streamAssistResponse，响应内容: %v", dataList[0])
		}
		logger.Debug("📊 响应统计: %d 个数据块, 有效响应=%v, 包含文件=%v", len(dataList), hasValidResponse, hasFileContent)
	}

	// 从响应中提取 session（用于下载图片）
	var respSession string
	for _, data := range dataList {
		if streamResp, ok := data["streamAssistResponse"].(map[string]interface{}); ok {
			if sessionInfo, ok := streamResp["sessionInfo"].(map[string]interface{}); ok {
				if s, ok := sessionInfo["session"].(string); ok && s != "" {
					respSession = s
					break
				}
			}
		}
	}

	// 如果响应中没有 session，使用请求时创建的 session 作为回退
	if respSession == "" {
		if usedSession != "" {
			logger.Warn("⚠️ 响应中未找到 session，使用请求时创建的 session: %s", usedSession)
			respSession = usedSession
		} else {
			logger.Warn("⚠️ 响应中未找到 session 且无回退 session，图片/视频下载可能失败")
		}
	} else {
	}

	// 待下载的文件信息
	type PendingFile struct {
		FileID   string
		MimeType string
	}

	if req.Stream {
		// 流式响应：文本/思考实时输出，图片最后处理
		// SSE 头部和 role chunk 已在请求前发送，复用 streamWriter/streamFlusher
		writer := streamWriter
		flusher := streamFlusher

		// 统计输出内容长度
		var outputLen int64

		// 收集待下载的文件和工具调用
		var pendingFiles []PendingFile
		hasToolCalls := false
		for _, data := range dataList {
			streamResp, ok := data["streamAssistResponse"].(map[string]interface{})
			if !ok {
				continue
			}
			answer, ok := streamResp["answer"].(map[string]interface{})
			if !ok {
				continue
			}
			replies, ok := answer["replies"].([]interface{})
			if !ok {
				continue
			}
			for _, reply := range replies {
				replyMap, ok := reply.(map[string]interface{})
				if !ok {
					continue
				}
				groundedContent, ok := replyMap["groundedContent"].(map[string]interface{})
				if !ok {
					continue
				}
				content, ok := groundedContent["content"].(map[string]interface{})
				if !ok {
					continue
				}
				// 检查是否是思考内容
				if thought, ok := content["thought"].(bool); ok && thought {
					if t, ok := content["text"].(string); ok && t != "" {
						chunk := createChunk(chatID, createdTime, req.Model, map[string]interface{}{"reasoning_content": t}, nil)
						fmt.Fprintf(writer, "data: %s\n\n", chunk)
						flusher.Flush()
						outputLen += int64(len(t))
					}
					continue
				}
				// 输出文本（实时）
				if t, ok := content["text"].(string); ok && t != "" {
					chunk := createChunk(chatID, createdTime, req.Model, map[string]interface{}{"content": t}, nil)
					fmt.Fprintf(writer, "data: %s\n\n", chunk)
					flusher.Flush()
					outputLen += int64(len(t))
				}

				// 处理 inlineData（直接有 base64 数据的图片）
				if inlineData, ok := content["inlineData"].(map[string]interface{}); ok {
					mime, _ := inlineData["mimeType"].(string)
					data, _ := inlineData["data"].(string)
					if mime != "" && data != "" {
						imgMarkdown := formatImageAsMarkdown(mime, data)
						chunk := createChunk(chatID, createdTime, req.Model, map[string]interface{}{"content": imgMarkdown}, nil)
						fmt.Fprintf(writer, "data: %s\n\n", chunk)
						flusher.Flush()
					}
				}

				// 收集需要下载的文件（图片/视频）
				if file, ok := content["file"].(map[string]interface{}); ok {
					fileId, _ := file["fileId"].(string)
					mimeType, _ := file["mimeType"].(string)
					if fileId != "" {
						pendingFiles = append(pendingFiles, PendingFile{FileID: fileId, MimeType: mimeType})
					}
				}
				if fc, ok := content["functionCall"].(map[string]interface{}); ok {
					hasToolCalls = true
					name, _ := fc["name"].(string)
					args, _ := fc["args"].(map[string]interface{})
					argsBytes, _ := json.Marshal(args)

					toolCall := ToolCall{
						ID:   "call_" + uuid.New().String()[:8],
						Type: "function",
						Function: FunctionCall{
							Name:      name,
							Arguments: string(argsBytes),
						},
					}
					chunk := createChunk(chatID, createdTime, req.Model, map[string]interface{}{
						"tool_calls": []map[string]interface{}{{
							"index": 0,
							"id":    toolCall.ID,
							"type":  "function",
							"function": map[string]interface{}{
								"name":      toolCall.Function.Name,
								"arguments": toolCall.Function.Arguments,
							},
						}},
					}, nil)
					fmt.Fprintf(writer, "data: %s\n\n", chunk)
					flusher.Flush()
				}
			}
		}
		if len(pendingFiles) > 0 {
			logger.Info("📥 开始下载 %d 个文件...", len(pendingFiles))
			type downloadResult struct {
				Index    int
				Data     string
				MimeType string
				Err      error
			}
			results := make(chan downloadResult, len(pendingFiles))
			var wg sync.WaitGroup
			for i, pf := range pendingFiles {
				wg.Add(1)
				go func(idx int, file PendingFile) {
					defer wg.Done()
					data, err := downloadGeneratedFile(usedJWT, file.FileID, respSession, usedConfigID, usedOrigAuth)
					results <- downloadResult{Index: idx, Data: data, MimeType: file.MimeType, Err: err}
				}(i, pf)
			}
			go func() {
				wg.Wait()
				close(results)
			}()
			downloaded := make([]downloadResult, len(pendingFiles))
			for r := range results {
				downloaded[r.Index] = r
			}

			// 按顺序输出
			successCount := 0
			var lastErr error
			needsRetry := false
			for i, r := range downloaded {
				if r.Err != nil {
					logger.Error("❌ 下载文件[%d]失败: %v", i, r.Err)
					lastErr = r.Err
					// 检测是否需要换号重试
					if errors.Is(r.Err, ErrDownloadNeedsRetry) {
						needsRetry = true
					}
					continue
				}
				imgMarkdown := formatImageAsMarkdown(r.MimeType, r.Data)
				chunk := createChunk(chatID, createdTime, req.Model, map[string]interface{}{"content": imgMarkdown}, nil)
				fmt.Fprintf(writer, "data: %s\n\n", chunk)
				flusher.Flush()
				successCount++
			}

			// 如果所有文件都下载失败
			if successCount == 0 && lastErr != nil {
				var errMsg string
				if needsRetry {
					// 401/403 认证失败，提示用户重试（下次会使用新账号）
					errMsg = "[提示] 文件下载认证失败，请重新发送请求（系统将自动切换账号）"
					pool.Pool.MarkNeedsRefresh(usedAcc) // 标记当前账号需要刷新
				} else {
					errMsg = fmt.Sprintf("生成的文件下载失败: %v", lastErr)
				}
				chunk := createChunk(chatID, createdTime, req.Model, map[string]interface{}{"content": errMsg}, nil)
				fmt.Fprintf(writer, "data: %s\n\n", chunk)
				flusher.Flush()
			}
		}

		// 发送结束
		finishReason := "stop"
		if hasToolCalls {
			finishReason = "tool_calls"
		}
		finalChunk := createChunk(chatID, createdTime, req.Model, nil, &finishReason)
		fmt.Fprintf(writer, "data: %s\n\n", finalChunk)
		fmt.Fprintf(writer, "data: [DONE]\n\n")
		flusher.Flush()

		// 更新统计（区分图片和视频）
		statsSuccess = true
		statsOutputTokens = outputLen / 4 // 估算输出 tokens
		for _, pf := range pendingFiles {
			if strings.HasPrefix(pf.MimeType, "video/") {
				statsVideos++
			} else {
				statsImages++
			}
		}
	} else {
		// 非流式响应
		var fullContent strings.Builder
		var fullReasoning strings.Builder
		replyCount := 0
		var fileCount int64
		var videoCount int64

		for _, data := range dataList {
			streamResp, ok := data["streamAssistResponse"].(map[string]interface{})
			if !ok {
				continue
			}
			answer, ok := streamResp["answer"].(map[string]interface{})
			if !ok {
				continue
			}
			replies, ok := answer["replies"].([]interface{})
			if !ok {
				continue
			}

			for _, reply := range replies {
				replyMap, ok := reply.(map[string]interface{})
				if !ok {
					continue
				}
				replyCount++
				if gc, ok := replyMap["groundedContent"].(map[string]interface{}); ok {
					if content, ok := gc["content"].(map[string]interface{}); ok {
						if file, ok := content["file"].(map[string]interface{}); ok {
							if mimeType, _ := file["mimeType"].(string); strings.HasPrefix(mimeType, "video/") {
								videoCount++
							} else {
								fileCount++
							}
						}
					}
				}

				text, imageData, imageMime, reasoning, dlErr := extractContentFromReply(replyMap, usedJWT, respSession, usedConfigID, usedOrigAuth)
				if reasoning != "" {
					fullReasoning.WriteString(reasoning)
				}
				if text != "" {
					fullContent.WriteString(text)
				}
				if imageData != "" && imageMime != "" {
					fullContent.WriteString(formatImageAsMarkdown(imageMime, imageData))
				}
				// 检测下载是否需要重试（401/403）
				if dlErr != nil && errors.Is(dlErr, ErrDownloadNeedsRetry) {
					pool.Pool.MarkNeedsRefresh(usedAcc)
					fullContent.WriteString("\n\n[提示] 文件下载认证失败，请重新发送请求（系统将自动切换账号）")
				}
			}
		}
		toolCalls := extractToolCalls(dataList)
		// 调试日志
		logger.Debug("📊 非流式响应统计: %d 个 reply, 图片=%d, 视频=%d, content长度=%d, reasoning长度=%d, 工具调用=%d",
			replyCount, fileCount, videoCount, fullContent.Len(), fullReasoning.Len(), len(toolCalls))

		// 构建响应消息
		message := gin.H{
			"role":    "assistant",
			"content": fullContent.String(),
		}
		if fullReasoning.Len() > 0 {
			message["reasoning_content"] = fullReasoning.String()
		}
		finishReason := "stop"
		if len(toolCalls) > 0 {
			message["tool_calls"] = toolCalls
			message["content"] = nil
			finishReason = "tool_calls"
		}

		// 构建最终响应（完全符合OpenAI格式）
		response := gin.H{
			"id":                 chatID,
			"object":             "chat.completion",
			"created":            createdTime,
			"model":              req.Model,
			"system_fingerprint": "fp_gemini_" + req.Model,
			"choices": []gin.H{{
				"index":         0,
				"message":       message,
				"logprobs":      nil,
				"finish_reason": finishReason,
			}},
			"usage": gin.H{
				"prompt_tokens":     0,
				"completion_tokens": 0,
				"total_tokens":      0,
			},
		}
		if isLongRunning && heartbeatDone != nil {
			close(heartbeatDone) // 停止心跳
			jsonBytes, _ := json.Marshal(response)
			c.Writer.Write(jsonBytes)
		} else {
			c.JSON(200, response)
		}

		// 更新统计
		statsSuccess = true
		statsOutputTokens = int64(fullContent.Len() / 4) // 粗略估算输出 tokens
		statsImages = fileCount
		statsVideos = videoCount
	}
}
func extractAPIKey(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	}
	return strings.TrimSpace(c.GetHeader("X-API-Key"))
}

func isValidAPIKey(apiKey string) bool {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return false
	}
	for _, key := range GetAPIKeys() {
		if key == apiKey {
			return true
		}
	}
	return false
}

func isSessionAuthorized(c *gin.Context) bool {
	if panelSessions == nil {
		return false
	}
	token, err := c.Cookie(adminauth.SessionCookieName)
	if err != nil || strings.TrimSpace(token) == "" {
		return false
	}
	session, ok := panelSessions.Validate(token)
	if !ok {
		return false
	}
	c.Set("panel_username", session.Username)
	c.Set("auth_type", "session")
	return true
}

func apiKeyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(GetAPIKeys()) == 0 {
			c.Next()
			return
		}

		apiKey := extractAPIKey(c)
		if apiKey == "" {
			c.JSON(401, gin.H{"error": "Missing API key"})
			c.Abort()
			return
		}
		if !isValidAPIKey(apiKey) {
			c.JSON(401, gin.H{"error": "Invalid API key"})
			c.Abort()
			return
		}
		c.Set("auth_type", "api_key")
		c.Set("api_key", apiKey)
		c.Next()
	}
}

func adminAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if isSessionAuthorized(c) {
			c.Next()
			return
		}
		if isValidAPIKey(extractAPIKey(c)) {
			c.Set("auth_type", "api_key")
			c.Next()
			return
		}
		c.JSON(401, gin.H{"error": "Unauthorized"})
		c.Abort()
	}
}

func requireSessionAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if isSessionAuthorized(c) {
			c.Next()
			return
		}
		c.JSON(401, gin.H{"error": "Unauthorized"})
		c.Abort()
	}
}

// runBrowserRefreshMode 有头浏览器刷新模式
func runBrowserRefreshMode(email string) {
	loadAppConfig()
	utils.InitHTTPClient(Proxy)

	// 强制有头模式
	pool.BrowserRefreshHeadless = false
	logger.Info("🌐 有头浏览器刷新模式")

	if err := pool.Pool.Load(DataDir); err != nil {
		log.Fatalf("❌ 加载账号失败: %v", err)
	}

	if pool.Pool.TotalCount() == 0 {
		log.Fatal("❌ 没有可用账号")
	}

	// 查找目标账号
	var targetAcc *pool.Account
	pool.Pool.WithLock(func(ready, pending []*pool.Account) {
		if email != "" {
			// 指定邮箱
			for _, acc := range ready {
				if acc.Data.Email == email {
					targetAcc = acc
					break
				}
			}
			if targetAcc == nil {
				for _, acc := range pending {
					if acc.Data.Email == email {
						targetAcc = acc
						break
					}
				}
			}
		} else {
			// 使用第一个账号
			if len(ready) > 0 {
				targetAcc = ready[0]
			} else if len(pending) > 0 {
				targetAcc = pending[0]
			}
		}
	})

	if targetAcc == nil {
		if email != "" {
			log.Fatalf("❌ 找不到账号: %s", email)
		}
		log.Fatal("❌ 没有可用账号")
	}
	result := register.RefreshCookieWithBrowser(targetAcc, false, Proxy)

	if result.Success {

		if len(result.NewCookies) > 0 {
		}
		if len(result.ResponseHeaders) > 0 {
		}

		// 更新账号数据
		targetAcc.Mu.Lock()
		targetAcc.Data.Cookies = result.SecureCookies
		if result.Authorization != "" {
			targetAcc.Data.Authorization = result.Authorization
		}
		if result.ConfigID != "" {
			targetAcc.ConfigID = result.ConfigID
			targetAcc.Data.ConfigID = result.ConfigID
		}
		if result.CSESIDX != "" {
			targetAcc.CSESIDX = result.CSESIDX
			targetAcc.Data.CSESIDX = result.CSESIDX
		}
		// 保存响应头
		if len(result.ResponseHeaders) > 0 {
			targetAcc.Data.ResponseHeaders = result.ResponseHeaders
		}
		targetAcc.Mu.Unlock()

		// 保存到文件
		if err := targetAcc.SaveToFile(); err != nil {
			logger.Warn("⚠️ 保存失败: %v", err)
		} else {
			logger.Info("💾 已保存到: %s", targetAcc.FilePath)
		}
	} else {
		logger.Error("❌ 刷新失败: %v", result.Error)
	}
}

var AutoSubscribeEnabled bool

func init() {
	// 设置环境变量禁用 quic-go 的警告
	os.Setenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING", "true")
	filterStdout()
}
func filterStdout() {
	// 创建 stdout 管道，用于统一过滤与日志聚合
	r, w, err := os.Pipe()
	if err != nil {
		return
	}
	origStdout := os.Stdout
	os.Stdout = w
	log.SetOutput(w)

	go func() {
		scanner := bufio.NewScanner(r)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			// 过滤特定噪音日志
			if strings.Contains(line, "REALITY localAddr:") ||
				strings.Contains(line, "DialTLSContext") ||
				strings.Contains(line, "sys_conn.go") ||
				strings.Contains(line, "failed to sufficiently increase receive buffer size") {
				continue
			}

			_, _ = origStdout.WriteString(line + "\n")
			logger.AppendRaw("business2api", line)
		}
	}()
}

func main() {
	log.SetFlags(log.Ltime | log.Lshortfile)

	var refreshEmail string
	var refreshMode bool

	// 解析命令行参数
	for i, arg := range os.Args[1:] {
		switch arg {
		case "--debug", "-d":
			register.RegisterDebug = true
			logger.Info("🔧 调试模式已启用，将保存截图到 data/screenshots/")
		case "--once":
			register.RegisterOnce = true
			logger.Info("🔧 单次运行模式")
		case "--auto":
			AutoSubscribeEnabled = true
		case "--refresh":
			refreshMode = true
			// 检查下一个参数是否是邮箱
			if i+2 < len(os.Args) && !strings.HasPrefix(os.Args[i+2], "-") {
				refreshEmail = os.Args[i+2]
			}
		case "--help", "-h":
			fmt.Println(`用法: ./business2api [选项]

选项:
  --debug, -d           调试模式，保存注册过程截图
  --auto                自动订阅模式，每小时注册获取代理
  --refresh [email]     有头浏览器刷新账号（不指定email则使用第一个账号）
  --help, -h            显示帮助`)
			os.Exit(0)
		}
	}

	// 刷新模式：直接执行浏览器刷新后退出
	if refreshMode {
		runBrowserRefreshMode(refreshEmail)
		return
	}

	loadAppConfig()
	utils.InitHTTPClient(Proxy)
	if appConfig.PoolServer.Enable {
		switch appConfig.PoolServer.Mode {
		case "client":
			runAsClient()
			return
		case "server":
			runAsServer()
			return
		}
	}

	// 本地模式
	runLocalMode()
}
func runAsClient() {
	logger.Info("🔌 启动客户端模式...")

	// 代理实例池由异步健康检查完成后初始化
	// 设置代理就绪检查回调
	pool.IsProxyReady = func() bool {
		return proxy.Manager.IsReady()
	}
	pool.WaitProxyReady = func(timeout time.Duration) bool {
		logger.Info("⏳ 等待代理就绪...")
		result := proxy.Manager.WaitReady(timeout)
		if result {
			logger.Info("✅ 代理已就绪")
		} else {
			logger.Warn("⚠️ 代理等待超时")
		}
		return result
	}

	pool.RunBrowserRegister = func(headless bool, proxyURL string, id int) *pool.BrowserRegisterResult {
		result := register.RunBrowserRegister(headless, proxyURL, id)
		return &pool.BrowserRegisterResult{
			Success:       result.Success,
			Email:         result.Email,
			FullName:      result.FullName,
			MailProvider:  result.MailProvider,
			MailPassword:  result.MailPassword,
			SecureCookies: result.Cookies,
			Authorization: result.Authorization,
			ConfigID:      result.ConfigID,
			CSESIDX:       result.CSESIDX,
			Error:         result.Error,
		}
	}
	pool.RefreshCookieWithBrowser = func(acc *pool.Account, headless bool, proxyURL string) *pool.BrowserRefreshResult {
		result := register.RefreshCookieWithBrowser(acc, headless, proxyURL)
		return &pool.BrowserRefreshResult{
			Success:         result.Success,
			SecureCookies:   result.SecureCookies,
			ConfigID:        result.ConfigID,
			CSESIDX:         result.CSESIDX,
			Authorization:   result.Authorization,
			ResponseHeaders: result.ResponseHeaders,
			Error:           result.Error,
		}
	}
	pool.ClientHeadless = appConfig.Pool.RegisterHeadless
	pool.ClientProxy = Proxy
	pool.GetClientProxy = func() string {
		if proxy.Manager.HealthyCount() > 0 {
			proxyURL := proxy.Manager.Next()
			if proxyURL != "" {
				return proxyURL
			}
		}
		return Proxy
	}
	pool.ReleaseProxy = func(proxyURL string) {
		proxy.Manager.ReleaseByURL(proxyURL)
		logger.Debug("释放代理: %s", proxyURL)
	}
	pool.GetHealthyCount = func() int {
		return proxy.Manager.HealthyCount()
	}
	go func() {
		proxy.Manager.CheckAllHealth()
		if proxy.Manager.HealthyCount() > 0 {
			poolSize := appConfig.Pool.RegisterThreads
			if poolSize <= 0 {
				poolSize = pool.DefaultProxyCount
			}
			if poolSize > 10 {
				poolSize = 10
			}
			proxy.Manager.SetMaxPoolSize(poolSize)
			proxy.Manager.InitInstancePool(poolSize)
		}
	}()
	client := pool.NewPoolClient(appConfig.PoolServer)
	if err := client.Start(); err != nil {
		log.Fatalf("❌ 客户端启动失败: %v", err)
	}
}

var poolServer *pool.PoolServer

func runAsServer() {
	logger.Info("🖥️ 启动服务器模式...")

	// 加载账号
	dataDir := appConfig.PoolServer.DataDir
	if dataDir == "" {
		dataDir = DataDir
	}
	if err := pool.Pool.Load(dataDir); err != nil {
		log.Fatalf("❌ 加载账号失败: %v", err)
	}

	// 启动配置文件热重载监听
	if err := startConfigWatcher(); err != nil {
		logger.Warn("⚠️ 配置热重载启动失败: %v", err)
	}

	poolServer = pool.NewPoolServer(pool.Pool, appConfig.PoolServer)
	poolServer.StartBackground() // 启动后台任务分发和心跳检测
	pool.Pool.StartPoolManager()
	runAPIServer()
}

// runAPIServer 启动 API 服务
func runAPIServer() {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	setupAPIRoutes(r)
	logger.Info("🚀 API 服务启动于 %s，账号: ready=%d, pending=%d", ListenAddr, pool.Pool.ReadyCount(), pool.Pool.PendingCount())
	if err := r.Run(ListenAddr); err != nil {
		log.Fatalf("❌ API 服务启动失败: %v", err)
	}
}

const (
	defaultRegistrarBaseURL = "http://127.0.0.1:8090"
	maxListPageSize         = 200
	defaultListPageSize     = 20
)

type adminAccountView struct {
	Email          string    `json:"email"`
	EmailMasked    string    `json:"email_masked"`
	Status         string    `json:"status"`
	IsValid        bool      `json:"is_valid"`
	InvalidReason  string    `json:"invalid_reason,omitempty"`
	FailCount      int       `json:"fail_count"`
	LastUsed       time.Time `json:"last_used,omitempty"`
	LastRefresh    time.Time `json:"last_refresh,omitempty"`
	DailyCount     int       `json:"daily_count"`
	DailyLimit     int       `json:"daily_limit"`
	DailyRemaining int       `json:"daily_remaining"`
	SuccessCount   int       `json:"success_count"`
	TotalCount     int       `json:"total_count"`
	JWTExpires     time.Time `json:"jwt_expires,omitempty"`
}

type adminPoolFileView struct {
	FileName          string    `json:"file_name"`
	EmailFromFilename string    `json:"email_from_filename"`
	ParseOK           bool      `json:"parse_ok"`
	ParseError        string    `json:"parse_error,omitempty"`
	ExistsInPool      bool      `json:"exists_in_pool"`
	PoolStatus        string    `json:"pool_status"`
	HasConfigID       bool      `json:"has_config_id"`
	HasCSESIDX        bool      `json:"has_csesidx"`
	SizeBytes         int64     `json:"size_bytes"`
	ModifiedAt        time.Time `json:"modified_at"`
}

type adminPoolFileRecord struct {
	view          adminPoolFileView
	filePath      string
	accountEmail  string
	invalidReason string
}

type adminDeleteCandidate struct {
	FileName     string    `json:"file_name"`
	Email        string    `json:"email"`
	Reason       string    `json:"reason"`
	SizeBytes    int64     `json:"size_bytes"`
	ModifiedAt   time.Time `json:"modified_at"`
	Status       string    `json:"status"`
	EmailMasked  string    `json:"email_masked"`
	ParseError   string    `json:"parse_error,omitempty"`
	ExistsInPool bool      `json:"exists_in_pool"`
	ScopeMatched bool      `json:"scope_matched"`
}

type adminImportResult struct {
	Total          int      `json:"total"`
	Success        int      `json:"success"`
	Failed         int      `json:"failed"`
	Skipped        int      `json:"skipped"`
	Errors         []string `json:"errors"`
	ImportedEmails []string `json:"imported_emails"`
}

type adminDeleteExecuteRequest struct {
	Files      []string `json:"files"`
	AutoBackup *bool    `json:"auto_backup"`
}

func getRegistrarBaseURL() string {
	configMu.RLock()
	defer configMu.RUnlock()
	if v := strings.TrimSpace(appConfig.Pool.RegistrarBaseURL); v != "" {
		return v
	}
	return defaultRegistrarBaseURL
}

func initPanelServices() error {
	panelAuthMu.Lock()
	defer panelAuthMu.Unlock()

	if panelAuthStore != nil && panelSessions != nil && logStreamHandler != nil && panelAuthDataDir == DataDir {
		return nil
	}

	store, err := adminauth.NewStore(DataDir)
	if err != nil {
		return fmt.Errorf("初始化管理员账号失败: %w", err)
	}
	panelAuthStore = store
	panelSessions = adminauth.NewSessionManager(adminauth.DefaultSessionTTL)
	logStreamHandler = adminlogs.NewStreamHandler(adminlogs.StreamHandlerConfig{
		GetRegistrarBaseURL: getRegistrarBaseURL,
		HTTPClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}).Handler()
	panelAuthDataDir = DataDir
	return nil
}

func setSessionCookie(c *gin.Context, token string, expiresAt time.Time) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(adminauth.SessionCookieName, token, maxAge, "/", "", false, true)
}

func clearSessionCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(adminauth.SessionCookieName, "", -1, "/", "", false, true)
}

func normalizeStateFilter(raw string) string {
	state := strings.ToLower(strings.TrimSpace(raw))
	switch state {
	case "", "all":
		return "all"
	case "active":
		return "active"
	case "invalid":
		return "invalid"
	default:
		return "all"
	}
}

func parseStatusFilter(raw string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, item := range strings.Split(raw, ",") {
		status := pool.NormalizeStatus(item)
		if status == "" {
			continue
		}
		result[status] = struct{}{}
	}
	return result
}

func containsStatus(filter map[string]struct{}, status string) bool {
	if len(filter) == 0 {
		return true
	}
	_, ok := filter[pool.NormalizeStatus(status)]
	return ok
}

func containsIgnoreCase(haystack, needle string) bool {
	if strings.TrimSpace(needle) == "" {
		return true
	}
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(strings.TrimSpace(needle)))
}

func matchState(status, state string) bool {
	switch state {
	case "active":
		return pool.IsActiveStatus(status)
	case "invalid":
		return !pool.IsActiveStatus(status)
	default:
		return true
	}
}

func parsePageParams(c *gin.Context) (int, int) {
	page := 1
	pageSize := defaultListPageSize
	if v := strings.TrimSpace(c.Query("page")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			page = parsed
		}
	}
	if v := strings.TrimSpace(c.Query("page_size")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			pageSize = parsed
		}
	}
	if pageSize > maxListPageSize {
		pageSize = maxListPageSize
	}
	return page, pageSize
}

func maskEmail(email string) string {
	email = strings.TrimSpace(email)
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		if len(email) <= 3 {
			return "***"
		}
		return email[:1] + "***" + email[len(email)-1:]
	}
	local := parts[0]
	domain := parts[1]
	if len(local) <= 2 {
		local = local[:1] + "***"
	} else {
		local = local[:1] + "***" + local[len(local)-1:]
	}
	return local + "@" + domain
}

func statusOrder(status string) int {
	switch pool.NormalizeStatus(status) {
	case "ready":
		return 1
	case "pending":
		return 2
	case "cooldown":
		return 3
	case "pending_external":
		return 4
	case "invalid":
		return 5
	default:
		return 6
	}
}

func getPoolAccountIndex() map[string]pool.AccountInfo {
	result := make(map[string]pool.AccountInfo)
	for _, info := range pool.Pool.ListAccounts() {
		email := strings.ToLower(strings.TrimSpace(info.Email))
		if email == "" {
			continue
		}
		result[email] = info
	}
	return result
}

func collectPoolFileRecords(dataDir string) ([]adminPoolFileRecord, error) {
	files, err := filepath.Glob(filepath.Join(dataDir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	accountIndex := getPoolAccountIndex()
	records := make([]adminPoolFileRecord, 0, len(files))
	for _, path := range files {
		baseName := filepath.Base(path)
		emailFromFilename := strings.TrimSuffix(baseName, filepath.Ext(baseName))
		record := adminPoolFileRecord{
			filePath:     path,
			accountEmail: emailFromFilename,
			view: adminPoolFileView{
				FileName:          baseName,
				EmailFromFilename: emailFromFilename,
				PoolStatus:        "invalid",
			},
		}

		stat, statErr := os.Stat(path)
		if statErr != nil {
			record.view.ParseError = fmt.Sprintf("读取文件元数据失败: %v", statErr)
			record.invalidReason = "stat_failed"
			records = append(records, record)
			continue
		}
		record.view.SizeBytes = stat.Size()
		record.view.ModifiedAt = stat.ModTime()

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			record.view.ParseError = fmt.Sprintf("读取文件失败: %v", readErr)
			record.invalidReason = "read_failed"
			records = append(records, record)
			continue
		}

		var accData pool.AccountData
		if err := json.Unmarshal(raw, &accData); err != nil {
			record.view.ParseError = fmt.Sprintf("JSON 解析失败: %v", err)
			record.invalidReason = "json_parse_error"
			records = append(records, record)
			continue
		}

		record.view.ParseOK = true
		accEmail := strings.TrimSpace(accData.Email)
		if accEmail != "" {
			record.accountEmail = accEmail
		}
		record.view.HasConfigID = strings.TrimSpace(accData.ConfigID) != ""
		record.view.HasCSESIDX = strings.TrimSpace(accData.CSESIDX) != ""

		valid, reason := pool.ValidateAccountData(&accData)
		if !valid {
			record.invalidReason = reason
			record.view.ParseError = reason
		}

		if info, ok := accountIndex[strings.ToLower(record.accountEmail)]; ok {
			record.view.ExistsInPool = true
			record.view.PoolStatus = pool.NormalizeStatus(info.Status)
		} else if valid {
			record.view.PoolStatus = "unknown"
		} else {
			record.view.PoolStatus = "invalid"
		}

		records = append(records, record)
	}

	sort.Slice(records, func(i, j int) bool {
		if statusOrder(records[i].view.PoolStatus) == statusOrder(records[j].view.PoolStatus) {
			return strings.ToLower(records[i].view.FileName) < strings.ToLower(records[j].view.FileName)
		}
		return statusOrder(records[i].view.PoolStatus) < statusOrder(records[j].view.PoolStatus)
	})
	return records, nil
}

func buildAdminAccountViews(dataDir string) ([]adminAccountView, error) {
	accountIndex := getPoolAccountIndex()
	fileRecords, err := collectPoolFileRecords(dataDir)
	if err != nil {
		return nil, err
	}

	views := make([]adminAccountView, 0, len(fileRecords)+len(accountIndex))
	seen := make(map[string]struct{})
	for _, rec := range fileRecords {
		email := strings.TrimSpace(rec.accountEmail)
		if email == "" {
			email = rec.view.EmailFromFilename
		}
		status := pool.NormalizeStatus(rec.view.PoolStatus)
		if status == "" {
			status = "invalid"
		}

		view := adminAccountView{
			Email:       email,
			EmailMasked: maskEmail(email),
			Status:      status,
			IsValid:     rec.view.ParseOK && rec.invalidReason == "" && pool.IsActiveStatus(status),
		}

		if rec.view.ParseError != "" {
			view.InvalidReason = rec.view.ParseError
		}
		if view.InvalidReason == "" && !pool.IsActiveStatus(status) {
			view.InvalidReason = "status_not_active"
		}

		if info, ok := accountIndex[strings.ToLower(email)]; ok {
			view.FailCount = info.FailCount
			view.LastUsed = info.LastUsed
			view.LastRefresh = info.LastRefresh
			view.DailyCount = info.DailyCount
			view.DailyLimit = info.DailyLimit
			view.DailyRemaining = info.DailyRemaining
			view.SuccessCount = info.SuccessCount
			view.TotalCount = info.TotalCount
			view.JWTExpires = info.JWTExpires
			view.Status = pool.NormalizeStatus(info.Status)
			view.IsValid = rec.invalidReason == "" && pool.IsActiveStatus(view.Status)
			if rec.invalidReason == "" && !pool.IsActiveStatus(view.Status) {
				view.InvalidReason = "status_not_active"
			}
			delete(accountIndex, strings.ToLower(email))
		}

		views = append(views, view)
		seen[strings.ToLower(email)] = struct{}{}
	}

	for _, info := range accountIndex {
		email := strings.TrimSpace(info.Email)
		if email == "" {
			continue
		}
		if _, ok := seen[strings.ToLower(email)]; ok {
			continue
		}
		status := pool.NormalizeStatus(info.Status)
		view := adminAccountView{
			Email:          email,
			EmailMasked:    maskEmail(email),
			Status:         status,
			IsValid:        pool.IsActiveStatus(status),
			FailCount:      info.FailCount,
			LastUsed:       info.LastUsed,
			LastRefresh:    info.LastRefresh,
			DailyCount:     info.DailyCount,
			DailyLimit:     info.DailyLimit,
			DailyRemaining: info.DailyRemaining,
			SuccessCount:   info.SuccessCount,
			TotalCount:     info.TotalCount,
			JWTExpires:     info.JWTExpires,
		}
		if !view.IsValid {
			view.InvalidReason = "status_not_active"
		}
		views = append(views, view)
	}

	sort.Slice(views, func(i, j int) bool {
		if statusOrder(views[i].Status) == statusOrder(views[j].Status) {
			return strings.ToLower(views[i].Email) < strings.ToLower(views[j].Email)
		}
		return statusOrder(views[i].Status) < statusOrder(views[j].Status)
	})
	return views, nil
}

func filterAccountViews(items []adminAccountView, state string, statusFilter map[string]struct{}, q string) []adminAccountView {
	filtered := make([]adminAccountView, 0, len(items))
	for _, item := range items {
		if !matchState(item.Status, state) {
			continue
		}
		if !containsStatus(statusFilter, item.Status) {
			continue
		}
		if !containsIgnoreCase(item.Email, q) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func filterPoolFileRecords(items []adminPoolFileRecord, state string, statusFilter map[string]struct{}, q string) []adminPoolFileRecord {
	filtered := make([]adminPoolFileRecord, 0, len(items))
	for _, item := range items {
		status := pool.NormalizeStatus(item.view.PoolStatus)
		if !matchState(status, state) {
			continue
		}
		if !containsStatus(statusFilter, status) {
			continue
		}
		if !containsIgnoreCase(item.view.FileName, q) && !containsIgnoreCase(item.accountEmail, q) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func paginatePoolFileViews(items []adminPoolFileView, page, pageSize int) ([]adminPoolFileView, int) {
	total := len(items)
	if total == 0 {
		return []adminPoolFileView{}, 0
	}
	start := (page - 1) * pageSize
	if start >= total {
		return []adminPoolFileView{}, total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return items[start:end], total
}

func isStructureInvalid(record adminPoolFileRecord) bool {
	if record.invalidReason == "" {
		return false
	}
	return record.invalidReason != "status_not_active"
}

func buildDeleteInvalidCandidates(records []adminPoolFileRecord, scope string) []adminDeleteCandidate {
	candidates := make([]adminDeleteCandidate, 0)
	for _, record := range records {
		reason := ""
		scopeMatched := false

		if isStructureInvalid(record) {
			reason = record.invalidReason
			scopeMatched = true
		} else if pool.NormalizeStatus(record.view.PoolStatus) == "invalid" {
			reason = "status_invalid"
			scopeMatched = true
		}

		if scope == "invalid_files_and_invalid_accounts" && !scopeMatched {
			continue
		}

		email := strings.TrimSpace(record.accountEmail)
		if email == "" {
			email = record.view.EmailFromFilename
		}

		candidates = append(candidates, adminDeleteCandidate{
			FileName:     record.view.FileName,
			Email:        email,
			Reason:       reason,
			SizeBytes:    record.view.SizeBytes,
			ModifiedAt:   record.view.ModifiedAt,
			Status:       record.view.PoolStatus,
			EmailMasked:  maskEmail(email),
			ParseError:   record.view.ParseError,
			ExistsInPool: record.view.ExistsInPool,
			ScopeMatched: scopeMatched,
		})
	}
	return candidates
}

func createBackupZip(dataDir string, filePaths []string) (string, error) {
	backupName := fmt.Sprintf("invalid-backup-%s.zip", time.Now().Format("20060102-150405"))
	backupPath := filepath.Join(dataDir, backupName)
	outFile, err := os.Create(backupPath)
	if err != nil {
		return "", err
	}
	zipWriter := zip.NewWriter(outFile)

	writeErr := func(err error) (string, error) {
		_ = zipWriter.Close()
		_ = outFile.Close()
		_ = os.Remove(backupPath)
		return "", err
	}

	for _, path := range filePaths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return writeErr(fmt.Errorf("读取备份文件失败 %s: %w", filepath.Base(path), err))
		}
		entry, err := zipWriter.Create(filepath.Base(path))
		if err != nil {
			return writeErr(fmt.Errorf("写入备份 ZIP 失败 %s: %w", filepath.Base(path), err))
		}
		if _, err := entry.Write(raw); err != nil {
			return writeErr(fmt.Errorf("写入备份内容失败 %s: %w", filepath.Base(path), err))
		}
	}

	if err := zipWriter.Close(); err != nil {
		return writeErr(fmt.Errorf("关闭备份 ZIP 失败: %w", err))
	}
	if err := outFile.Close(); err != nil {
		return "", fmt.Errorf("关闭备份文件失败: %w", err)
	}
	return backupPath, nil
}

func importSingleAccountPayload(name string, payload []byte, overwrite bool, result *adminImportResult) {
	result.Total++
	var accData pool.AccountData
	if err := json.Unmarshal(payload, &accData); err != nil {
		result.Failed++
		result.Errors = append(result.Errors, fmt.Sprintf("%s: JSON 解析失败: %v", name, err))
		return
	}

	accData.Email = strings.TrimSpace(accData.Email)
	cookies := accData.Cookies
	if len(cookies) == 0 && strings.TrimSpace(accData.CookieString) != "" {
		cookies = pool.ParseCookieString(accData.CookieString)
	}
	accData.Cookies = cookies

	if valid, reason := pool.ValidateAccountData(&accData); !valid {
		result.Failed++
		result.Errors = append(result.Errors, fmt.Sprintf("%s: 校验失败: %s", name, reason))
		return
	}

	filePath := filepath.Join(DataDir, fmt.Sprintf("%s.json", accData.Email))
	if !overwrite {
		if _, err := os.Stat(filePath); err == nil {
			result.Skipped++
			result.Errors = append(result.Errors, fmt.Sprintf("%s: 邮箱 %s 已存在，跳过", name, accData.Email))
			return
		}
	}

	req := &pool.AccountUploadRequest{
		Email:         accData.Email,
		FullName:      accData.FullName,
		MailProvider:  accData.MailProvider,
		MailPassword:  accData.MailPassword,
		Cookies:       cookies,
		CookieString:  accData.CookieString,
		Authorization: accData.Authorization,
		ConfigID:      accData.ConfigID,
		CSESIDX:       accData.CSESIDX,
		IsNew:         false,
	}
	if err := pool.ProcessAccountUpload(pool.Pool, DataDir, req); err != nil {
		result.Failed++
		result.Errors = append(result.Errors, fmt.Sprintf("%s: 写入失败: %v", name, err))
		return
	}
	result.Success++
	result.ImportedEmails = append(result.ImportedEmails, accData.Email)
}

func handlePoolFilesImport(c *gin.Context) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(400, gin.H{"error": "缺少上传文件字段 file"})
		return
	}

	overwrite := true
	if raw := strings.TrimSpace(c.PostForm("overwrite")); raw != "" {
		overwrite = strings.EqualFold(raw, "true") || raw == "1"
	}

	fileReader, err := fileHeader.Open()
	if err != nil {
		c.JSON(400, gin.H{"error": fmt.Sprintf("打开上传文件失败: %v", err)})
		return
	}
	defer fileReader.Close()

	payload, err := io.ReadAll(fileReader)
	if err != nil {
		c.JSON(400, gin.H{"error": fmt.Sprintf("读取上传文件失败: %v", err)})
		return
	}

	result := &adminImportResult{
		Errors:         make([]string, 0),
		ImportedEmails: make([]string, 0),
	}
	lowerName := strings.ToLower(fileHeader.Filename)

	switch {
	case strings.HasSuffix(lowerName, ".json"):
		importSingleAccountPayload(fileHeader.Filename, payload, overwrite, result)
	case strings.HasSuffix(lowerName, ".zip"):
		zipReader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
		if err != nil {
			c.JSON(400, gin.H{"error": fmt.Sprintf("ZIP 解析失败: %v", err)})
			return
		}

		foundJSON := false
		for _, file := range zipReader.File {
			if file.FileInfo().IsDir() {
				continue
			}
			if !strings.HasSuffix(strings.ToLower(file.Name), ".json") {
				continue
			}
			foundJSON = true
			entry, err := file.Open()
			if err != nil {
				result.Total++
				result.Failed++
				result.Errors = append(result.Errors, fmt.Sprintf("%s: 打开 ZIP 条目失败: %v", file.Name, err))
				continue
			}
			entryPayload, readErr := io.ReadAll(entry)
			_ = entry.Close()
			if readErr != nil {
				result.Total++
				result.Failed++
				result.Errors = append(result.Errors, fmt.Sprintf("%s: 读取 ZIP 条目失败: %v", file.Name, readErr))
				continue
			}
			importSingleAccountPayload(file.Name, entryPayload, overwrite, result)
		}
		if !foundJSON {
			c.JSON(400, gin.H{"error": "ZIP 内未找到 JSON 文件"})
			return
		}
	default:
		c.JSON(400, gin.H{"error": "仅支持 .zip 或 .json 文件"})
		return
	}

	_ = pool.Pool.Load(DataDir)

	c.JSON(200, gin.H{
		"total":           result.Total,
		"success":         result.Success,
		"failed":          result.Failed,
		"skipped":         result.Skipped,
		"errors":          result.Errors,
		"imported_emails": result.ImportedEmails,
		"overwrite":       overwrite,
	})
}

func handlePanelLogin(c *gin.Context) {
	if panelAuthStore == nil || panelSessions == nil {
		c.JSON(500, gin.H{"error": "panel auth unavailable"})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.Password = strings.TrimSpace(req.Password)
	if req.Username == "" || req.Password == "" {
		c.JSON(400, gin.H{"error": "用户名和密码不能为空"})
		return
	}

	if !panelAuthStore.Verify(req.Username, req.Password) {
		c.JSON(401, gin.H{"error": "用户名或密码错误"})
		return
	}

	session, err := panelSessions.Create(req.Username)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("创建会话失败: %v", err)})
		return
	}

	setSessionCookie(c, session.Token, session.ExpiresAt)
	c.JSON(200, gin.H{
		"success":    true,
		"username":   session.Username,
		"expires_at": session.ExpiresAt,
	})
}

func handlePanelLogout(c *gin.Context) {
	if panelSessions != nil {
		if token, err := c.Cookie(adminauth.SessionCookieName); err == nil && strings.TrimSpace(token) != "" {
			panelSessions.Delete(token)
		}
	}
	clearSessionCookie(c)
	c.JSON(200, gin.H{"success": true})
}

func handlePanelMe(c *gin.Context) {
	if !isSessionAuthorized(c) {
		c.JSON(200, gin.H{"authenticated": false})
		return
	}

	username, _ := c.Get("panel_username")
	token, _ := c.Cookie(adminauth.SessionCookieName)
	session, ok := panelSessions.Validate(token)
	if !ok {
		c.JSON(200, gin.H{"authenticated": false})
		return
	}
	c.JSON(200, gin.H{
		"authenticated": true,
		"username":      username,
		"expires_at":    session.ExpiresAt,
	})
}

func handlePanelChangePassword(c *gin.Context) {
	if panelAuthStore == nil || panelSessions == nil {
		c.JSON(500, gin.H{"error": "panel auth unavailable"})
		return
	}
	authType, _ := c.Get("auth_type")
	if authType != "session" {
		c.JSON(403, gin.H{"error": "仅允许登录会话修改密码"})
		return
	}

	var req struct {
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	req.NewPassword = strings.TrimSpace(req.NewPassword)
	if len(req.NewPassword) < adminauth.MinPasswordLength {
		c.JSON(400, gin.H{"error": fmt.Sprintf("新密码长度至少 %d 位", adminauth.MinPasswordLength)})
		return
	}

	updatedAt, err := panelAuthStore.ChangePassword(req.NewPassword)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	username := panelAuthStore.Username()
	panelSessions.DeleteByUsername(username)
	clearSessionCookie(c)
	c.JSON(200, gin.H{
		"success":             true,
		"username":            username,
		"password_updated_at": updatedAt,
	})
}

func handleLogsStream(c *gin.Context) {
	if logStreamHandler == nil {
		c.JSON(500, gin.H{"error": "log stream unavailable"})
		return
	}
	logStreamHandler(c)
}

func handleAdminPanel(c *gin.Context) {
	panelPath := filepath.Join("web", "admin", "index.html")
	if _, err := os.Stat(panelPath); err != nil {
		c.JSON(404, gin.H{"error": "admin panel not found"})
		return
	}
	c.File(panelPath)
}

func handleAdminPanelAsset(c *gin.Context) {
	assetPath := strings.TrimPrefix(c.Param("filepath"), "/")
	assetPath = filepath.Clean(assetPath)
	if assetPath == "." || strings.HasPrefix(assetPath, "..") {
		c.JSON(400, gin.H{"error": "invalid asset path"})
		return
	}
	fullPath := filepath.Join("web", "admin", assetPath)
	base := filepath.Clean(filepath.Join("web", "admin"))
	cleanFullPath := filepath.Clean(fullPath)
	if !strings.HasPrefix(cleanFullPath, base+string(os.PathSeparator)) && cleanFullPath != base {
		c.JSON(400, gin.H{"error": "invalid asset path"})
		return
	}
	if _, err := os.Stat(cleanFullPath); err != nil {
		c.JSON(404, gin.H{"error": "asset not found"})
		return
	}
	c.File(cleanFullPath)
}

func handleAdminAccounts(c *gin.Context) {
	state := normalizeStateFilter(c.Query("state"))
	statusFilter := parseStatusFilter(c.Query("status"))
	q := strings.TrimSpace(c.Query("q"))

	accounts, err := buildAdminAccountViews(DataDir)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("读取账号列表失败: %v", err)})
		return
	}
	filtered := filterAccountViews(accounts, state, statusFilter, q)
	c.JSON(200, gin.H{
		"items":  filtered,
		"total":  len(filtered),
		"state":  state,
		"status": c.Query("status"),
		"q":      q,
	})
}

func handleAdminPoolFiles(c *gin.Context) {
	state := normalizeStateFilter(c.Query("state"))
	statusFilter := parseStatusFilter(c.Query("status"))
	q := strings.TrimSpace(c.Query("q"))
	page, pageSize := parsePageParams(c)

	records, err := collectPoolFileRecords(DataDir)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("读取号池文件失败: %v", err)})
		return
	}
	filteredRecords := filterPoolFileRecords(records, state, statusFilter, q)
	items := make([]adminPoolFileView, 0, len(filteredRecords))
	for _, rec := range filteredRecords {
		items = append(items, rec.view)
	}
	pageItems, total := paginatePoolFileViews(items, page, pageSize)

	c.JSON(200, gin.H{
		"items":      pageItems,
		"total":      total,
		"page":       page,
		"page_size":  pageSize,
		"state":      state,
		"status":     c.Query("status"),
		"q":          q,
		"total_page": (total + pageSize - 1) / pageSize,
	})
}

func handleAdminPoolFilesExport(c *gin.Context) {
	state := normalizeStateFilter(c.Query("state"))
	statusFilter := parseStatusFilter(c.Query("status"))
	q := strings.TrimSpace(c.Query("q"))

	records, err := collectPoolFileRecords(DataDir)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("读取号池文件失败: %v", err)})
		return
	}
	filtered := filterPoolFileRecords(records, state, statusFilter, q)

	buffer := bytes.NewBuffer(nil)
	zipWriter := zip.NewWriter(buffer)

	manifestFiles := make([]adminPoolFileView, 0, len(filtered))
	exportErrors := make([]string, 0)
	for _, record := range filtered {
		manifestFiles = append(manifestFiles, record.view)
		raw, readErr := os.ReadFile(record.filePath)
		if readErr != nil {
			exportErrors = append(exportErrors, fmt.Sprintf("%s: %v", record.view.FileName, readErr))
			continue
		}
		entry, err := zipWriter.Create(record.view.FileName)
		if err != nil {
			exportErrors = append(exportErrors, fmt.Sprintf("%s: %v", record.view.FileName, err))
			continue
		}
		if _, err := entry.Write(raw); err != nil {
			exportErrors = append(exportErrors, fmt.Sprintf("%s: %v", record.view.FileName, err))
			continue
		}
	}

	manifest := map[string]interface{}{
		"generated_at":   time.Now().UTC().Format(time.RFC3339),
		"state":          state,
		"status":         c.Query("status"),
		"q":              q,
		"total":          len(filtered),
		"exported_count": len(filtered) - len(exportErrors),
		"errors":         exportErrors,
		"files":          manifestFiles,
	}
	manifestRaw, _ := json.MarshalIndent(manifest, "", "  ")
	manifestEntry, err := zipWriter.Create("manifest.json")
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("创建 manifest 失败: %v", err)})
		return
	}
	if _, err := manifestEntry.Write(manifestRaw); err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("写入 manifest 失败: %v", err)})
		return
	}
	if err := zipWriter.Close(); err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("导出 ZIP 失败: %v", err)})
		return
	}

	filename := fmt.Sprintf("pool-export-%s.zip", time.Now().Format("20060102-150405"))
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Header("X-Export-Count", strconv.Itoa(len(filtered)))
	c.Header("X-Export-Errors", strconv.Itoa(len(exportErrors)))
	c.Data(200, "application/zip", buffer.Bytes())
}

func handleDeleteInvalidPreview(c *gin.Context) {
	scope := strings.TrimSpace(c.DefaultQuery("scope", "invalid_files_and_invalid_accounts"))
	if scope == "" {
		scope = "invalid_files_and_invalid_accounts"
	}

	records, err := collectPoolFileRecords(DataDir)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("读取号池文件失败: %v", err)})
		return
	}
	candidates := buildDeleteInvalidCandidates(records, scope)

	structureInvalid := 0
	statusInvalid := 0
	for _, c := range candidates {
		if c.Reason == "status_invalid" {
			statusInvalid++
		} else {
			structureInvalid++
		}
	}

	c.JSON(200, gin.H{
		"scope":      scope,
		"candidates": candidates,
		"total":      len(candidates),
		"summary": gin.H{
			"structure_invalid": structureInvalid,
			"status_invalid":    statusInvalid,
		},
	})
}

func handleDeleteInvalidExecute(c *gin.Context) {
	var req adminDeleteExecuteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if len(req.Files) == 0 {
		c.JSON(400, gin.H{"error": "files 不能为空"})
		return
	}
	autoBackup := true
	if req.AutoBackup != nil {
		autoBackup = *req.AutoBackup
	}

	records, err := collectPoolFileRecords(DataDir)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("读取号池文件失败: %v", err)})
		return
	}
	allowed := make(map[string]adminPoolFileRecord)
	for _, record := range buildDeleteInvalidCandidates(records, "invalid_files_and_invalid_accounts") {
		allowed[record.FileName] = adminPoolFileRecord{
			filePath: filepath.Join(DataDir, record.FileName),
		}
	}

	uniqueFiles := make([]string, 0, len(req.Files))
	seen := make(map[string]struct{})
	for _, file := range req.Files {
		name := filepath.Base(strings.TrimSpace(file))
		if name == "" || name != strings.TrimSpace(file) {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		if _, ok := allowed[name]; !ok {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		uniqueFiles = append(uniqueFiles, name)
	}
	if len(uniqueFiles) == 0 {
		c.JSON(400, gin.H{"error": "未提供可删除文件（需来自 preview 结果）"})
		return
	}

	pathsToDelete := make([]string, 0, len(uniqueFiles))
	for _, name := range uniqueFiles {
		pathsToDelete = append(pathsToDelete, filepath.Join(DataDir, name))
	}

	backupFile := ""
	if autoBackup {
		if created, err := createBackupZip(DataDir, pathsToDelete); err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("创建备份失败: %v", err)})
			return
		} else {
			backupFile = created
		}
	}

	deletedFiles := make([]string, 0, len(pathsToDelete))
	failed := make([]string, 0)
	for i, path := range pathsToDelete {
		if err := os.Remove(path); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", uniqueFiles[i], err))
			continue
		}
		deletedFiles = append(deletedFiles, uniqueFiles[i])
	}

	_ = pool.Pool.Load(DataDir)

	resp := gin.H{
		"deleted_count": len(deletedFiles),
		"deleted_files": deletedFiles,
		"failed":        failed,
		"stats":         pool.Pool.Stats(),
	}
	if backupFile != "" {
		resp["backup_file"] = backupFile
	}
	c.JSON(200, resp)
}

func handleRegistrarTriggerRegister(c *gin.Context) {
	var req struct {
		Count int `json:"count"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if req.Count < 1 {
		req.Count = 1
	}
	if req.Count > 20 {
		req.Count = 20
	}

	baseURL := strings.TrimRight(getRegistrarBaseURL(), "/")
	targetURL := fmt.Sprintf("%s/trigger/register?count=%d", baseURL, req.Count)

	httpReq, err := http.NewRequest(http.MethodPost, targetURL, nil)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("构建 registrar 请求失败: %v", err)})
		return
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		c.JSON(502, gin.H{
			"error":         fmt.Sprintf("调用 registrar 失败: %v", err),
			"registrar_url": targetURL,
			"source":        "python_registrar",
		})
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.JSON(resp.StatusCode, gin.H{
			"error":            "registrar 返回错误",
			"registrar_url":    targetURL,
			"registrar_status": resp.StatusCode,
			"registrar_body":   string(body),
			"source":           "python_registrar",
		})
		return
	}

	var passthrough interface{}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &passthrough)
	}

	response := gin.H{
		"accepted":      true,
		"count":         req.Count,
		"source":        "python_registrar",
		"registrar_url": targetURL,
	}
	if passthrough != nil {
		response["registrar_response"] = passthrough
	}
	c.JSON(200, response)
}

func setupAPIRoutes(r *gin.Engine) {
	if err := initPanelServices(); err != nil {
		panic(err)
	}

	// 请求日志中间件
	r.Use(func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		method := c.Request.Method
		clientIP := c.ClientIP()

		c.Next()

		latency := time.Since(start)
		statusCode := c.Writer.Status()

		if statusCode >= 400 {
			logger.Error("❌ %s %s %s %d %v", clientIP, method, path, statusCode, latency)
		} else {
			logger.Info("✅ %s %s %s %d %v", clientIP, method, path, statusCode, latency)
		}
	})

	r.GET("/", func(c *gin.Context) {
		stats := apiStats.GetStats()
		response := gin.H{
			"status":  "running",
			"service": "business2api",
			"version": "2.1.6",
			"mode":    map[PoolMode]string{PoolModeLocal: "local", PoolModeServer: "server", PoolModeClient: "client"}[poolMode],
			// 统计数据
			"uptime":           stats["uptime"],
			"total_requests":   stats["total_requests"],
			"success_requests": stats["success_requests"],
			"failed_requests":  stats["failed_requests"],
			"success_rate":     stats["success_rate"],
			"input_tokens":     stats["input_tokens"],
			"output_tokens":    stats["output_tokens"],
			"total_tokens":     stats["total_tokens"],
			"images_generated": stats["images_generated"],
			"videos_generated": stats["videos_generated"],
			"current_rpm":      stats["current_rpm"],
			"average_rpm":      stats["average_rpm"],
			"pool": gin.H{
				"ready":   pool.Pool.ReadyCount(),
				"pending": pool.Pool.PendingCount(),
				"total":   pool.Pool.TotalCount(),
			},
			// Flow 状态
			"flow_enabled": flowHandler != nil,
		}
		// 添加备注信息
		if len(appConfig.Note) > 0 {
			response["note"] = appConfig.Note
		}
		// 服务端模式：添加客户端信息
		if poolServer != nil {
			response["clients"] = gin.H{
				"count":         poolServer.GetClientCount(),
				"total_threads": poolServer.GetTotalThreads(),
				"list":          poolServer.GetClientsInfo(),
			}
		}
		c.JSON(200, response)
	})

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status":  "ok",
			"time":    time.Now().UTC().Format(time.RFC3339),
			"ready":   pool.Pool.ReadyCount(),
			"pending": pool.Pool.PendingCount(),
			"mode":    map[PoolMode]string{PoolModeLocal: "local", PoolModeServer: "server", PoolModeClient: "client"}[poolMode],
		})
	})

	// 管理面板静态资源（页面本身不鉴权，具体管理接口仍由 API Key 保护）
	r.GET("/admin/panel", handleAdminPanel)
	r.GET("/admin/panel/assets/*filepath", handleAdminPanelAsset)
	panelAuthGroup := r.Group("/admin/panel")
	panelAuthGroup.POST("/login", handlePanelLogin)
	panelAuthGroup.POST("/logout", handlePanelLogout)
	panelAuthGroup.GET("/me", handlePanelMe)
	panelAuthGroup.POST("/change-password", requireSessionAuth(), handlePanelChangePassword)

	// WebSocket 端点（服务端模式下用于客户端连接）
	r.GET("/ws", func(c *gin.Context) {
		if poolServer == nil {
			c.JSON(503, gin.H{"error": "WebSocket 服务未启用，仅在服务端模式下可用"})
			return
		}
		poolServer.HandleWS(c.Writer, c.Request)
	})

	// Pool 内部端点（客户端上传账号等，使用 X-Pool-Secret 鉴权）
	poolGroup := r.Group("/pool")
	poolGroup.Use(func(c *gin.Context) {
		if poolServer == nil {
			c.JSON(503, gin.H{"error": "Pool 服务未启用"})
			c.Abort()
			return
		}
		secret := appConfig.PoolServer.Secret
		if secret != "" && c.GetHeader("X-Pool-Secret") != secret {
			c.JSON(401, gin.H{"error": "Unauthorized"})
			c.Abort()
			return
		}
		c.Next()
	})
	poolGroup.POST("/upload-account", func(c *gin.Context) {
		poolServer.HandleUploadAccount(c.Writer, c.Request)
	})

	apiGroup := r.Group("/")
	apiGroup.Use(apiKeyAuth())

	// Gemini 风格模型列表 /v1beta/models
	apiGroup.GET("/v1beta/models", func(c *gin.Context) {
		var models []gin.H
		for _, m := range GetAvailableModels() {
			models = append(models, gin.H{
				"name":                       "models/" + m,
				"version":                    "001",
				"displayName":                m,
				"description":                "Gemini model: " + m,
				"inputTokenLimit":            1048576,
				"outputTokenLimit":           8192,
				"supportedGenerationMethods": []string{"generateContent", "countTokens"},
				"temperature":                1.0,
				"topP":                       0.95,
				"topK":                       64,
			})
		}
		c.JSON(200, gin.H{"models": models})
	})

	// OpenAI 风格模型列表
	apiGroup.GET("/v1/models", func(c *gin.Context) {
		now := time.Now().Unix()
		var models []gin.H
		for _, m := range GetAvailableModels() {
			models = append(models, gin.H{
				"id":         m,
				"object":     "model",
				"created":    now,
				"owned_by":   "google",
				"permission": []interface{}{},
			})
		}
		c.JSON(200, gin.H{"object": "list", "data": models})
	})

	apiGroup.POST("/v1/chat/completions", func(c *gin.Context) {
		var req ChatRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if req.Model == "" {
			req.Model = GetAvailableModels()[0]
		}
		streamChat(c, req)
	})

	apiGroup.POST("/v1/messages", handleClaudeMessages)

	// Gemini 单模型详情 GET /v1beta/models/{model}
	apiGroup.GET("/v1beta/models/:model", func(c *gin.Context) {
		modelName := c.Param("model")
		// 移除 "models/" 前缀（如果有）
		modelName = strings.TrimPrefix(modelName, "models/")

		// 检查模型是否存在
		found := false
		for _, m := range GetAvailableModels() {
			if m == modelName {
				found = true
				break
			}
		}
		if !found {
			c.JSON(404, gin.H{"error": gin.H{
				"code":    404,
				"message": "Model not found: " + modelName,
				"status":  "NOT_FOUND",
			}})
			return
		}

		c.JSON(200, gin.H{
			"name":                       "models/" + modelName,
			"version":                    "001",
			"displayName":                modelName,
			"description":                "Gemini model: " + modelName,
			"inputTokenLimit":            1048576,
			"outputTokenLimit":           8192,
			"supportedGenerationMethods": []string{"generateContent", "countTokens"},
			"temperature":                1.0,
			"topP":                       0.95,
			"topK":                       64,
		})
	})

	// Gemini generateContent/streamGenerateContent
	apiGroup.POST("/v1beta/models/*action", handleGeminiGenerate)
	apiGroup.POST("/v1/models/*action", handleGeminiGenerate)

	admin := r.Group("/admin")
	admin.Use(adminAuth())
	admin.POST("/register", func(c *gin.Context) {
		if poolMode == PoolModeLocal && !register.EnableGoRegister {
			c.JSON(400, gin.H{"error": "Go 注册已禁用，请使用 Python registrar 接管"})
			return
		}
		var req struct {
			Count int `json:"count"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Count <= 0 {
			req.Count = appConfig.Pool.TargetCount - pool.Pool.TotalCount()
		}
		if req.Count <= 0 {
			c.JSON(200, gin.H{"message": "账号数量已足够", "count": pool.Pool.TotalCount()})
			return
		}
		if poolMode == PoolModeServer {
			// 服务端模式：注册任务会通过 WS 分发给客户端
			c.JSON(200, gin.H{"message": "注册任务已加入队列，将通过 WS 分发给客户端", "target": req.Count})
			return
		}
		if err := register.StartRegister(req.Count); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "注册已启动", "target": req.Count})
	})

	admin.POST("/registrar/upload-account", func(c *gin.Context) {
		var req pool.AccountUploadRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"success": false, "error": err.Error()})
			return
		}

		if err := pool.ProcessAccountUpload(pool.Pool, DataDir, &req); err != nil {
			statusCode := 500
			if errors.Is(err, pool.ErrInvalidAccountUpload) {
				statusCode = 400
			}
			c.JSON(statusCode, gin.H{"success": false, "error": err.Error()})
			return
		}

		c.JSON(200, gin.H{
			"success": true,
			"message": fmt.Sprintf("账号 %s 已入池", req.Email),
		})
	})

	admin.GET("/registrar/refresh-tasks", func(c *gin.Context) {
		limit := 20
		if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		if limit > 200 {
			limit = 200
		}

		tasks := pool.Pool.ExternalRefreshTasks(limit)
		c.JSON(200, gin.H{
			"tasks": tasks,
			"count": len(tasks),
		})
	})

	admin.POST("/refresh", func(c *gin.Context) {
		pool.Pool.Load(DataDir)
		c.JSON(200, gin.H{
			"message": "刷新完成",
			"ready":   pool.Pool.ReadyCount(),
			"pending": pool.Pool.PendingCount(),
		})
	})

	admin.GET("/accounts", handleAdminAccounts)
	admin.GET("/pool-files", handleAdminPoolFiles)
	admin.GET("/pool-files/export", handleAdminPoolFilesExport)
	admin.POST("/pool-files/import", handlePoolFilesImport)
	admin.POST("/pool-files/delete-invalid/preview", handleDeleteInvalidPreview)
	admin.POST("/pool-files/delete-invalid/execute", handleDeleteInvalidExecute)
	admin.POST("/registrar/trigger-register", handleRegistrarTriggerRegister)
	admin.GET("/logs/stream", handleLogsStream)

	admin.GET("/status", func(c *gin.Context) {
		stats := pool.Pool.Stats()
		stats["target"] = appConfig.Pool.TargetCount
		stats["min"] = appConfig.Pool.MinCount
		stats["is_registering"] = atomic.LoadInt32(&register.IsRegistering) == 1
		stats["register_stats"] = register.Stats.Get()
		stats["mode"] = map[PoolMode]string{PoolModeLocal: "local", PoolModeServer: "server", PoolModeClient: "client"}[poolMode]
		c.JSON(200, stats)
	})

	// 详细API统计
	admin.GET("/stats", func(c *gin.Context) {
		detailed := apiStats.GetDetailedStats()
		detailed["pool"] = pool.Pool.Stats()
		detailed["proxy_pool"] = proxy.Manager.PoolStats()
		c.JSON(200, detailed)
	})
	admin.GET("/ip", func(c *gin.Context) {
		c.JSON(200, ipStats.GetAllIPStats())
	})

	admin.POST("/force-refresh", func(c *gin.Context) {
		count := pool.Pool.ForceRefreshAll()
		c.JSON(200, gin.H{
			"message": "已触发强制刷新",
			"count":   count,
		})
	})
	admin.POST("/reload-config", func(c *gin.Context) {
		if err := reloadConfig(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		configMu.RLock()
		c.JSON(200, gin.H{
			"message":  "配置已重载",
			"api_keys": len(appConfig.APIKeys),
			"debug":    appConfig.Debug,
			"pool_config": gin.H{
				"refresh_cooldown_sec":      appConfig.Pool.RefreshCooldownSec,
				"use_cooldown_sec":          appConfig.Pool.UseCooldownSec,
				"max_fail_count":            appConfig.Pool.MaxFailCount,
				"enable_browser_refresh":    appConfig.Pool.EnableBrowserRefresh,
				"browser_refresh_headless":  appConfig.Pool.BrowserRefreshHeadless,
				"browser_refresh_max_retry": appConfig.Pool.BrowserRefreshMaxRetry,
				"auto_delete_401":           appConfig.Pool.AutoDelete401,
				"registrar_base_url":        appConfig.Pool.RegistrarBaseURL,
			},
		})
		configMu.RUnlock()
	})

	admin.POST("/config/cooldown", func(c *gin.Context) {
		var req struct {
			RefreshCooldownSec int `json:"refresh_cooldown_sec"`
			UseCooldownSec     int `json:"use_cooldown_sec"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		pool.SetCooldowns(req.RefreshCooldownSec, req.UseCooldownSec)
		c.JSON(200, gin.H{
			"message":              "冷却配置已更新",
			"refresh_cooldown_sec": int(pool.RefreshCooldown.Seconds()),
			"use_cooldown_sec":     int(pool.UseCooldown.Seconds()),
		})
	})

	admin.POST("/browser-refresh", func(c *gin.Context) {
		var req struct {
			Email string `json:"email"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if req.Email == "" {
			c.JSON(400, gin.H{"error": "需要提供 email"})
			return
		}

		var targetAcc *pool.Account
		pool.Pool.WithLock(func(ready, pending []*pool.Account) {
			for _, acc := range ready {
				if acc.Data.Email == req.Email {
					targetAcc = acc
					break
				}
			}
			if targetAcc == nil {
				for _, acc := range pending {
					if acc.Data.Email == req.Email {
						targetAcc = acc
						break
					}
				}
			}
		})

		if targetAcc == nil {
			c.JSON(404, gin.H{"error": "账号未找到", "email": req.Email})
			return
		}

		go func() {
			logger.Info("🔄 手动触发浏览器刷新: %s", req.Email)
			result := register.RefreshCookieWithBrowser(targetAcc, pool.BrowserRefreshHeadless, Proxy)
			if result.Success {
				targetAcc.Mu.Lock()
				// 更新完整信息
				targetAcc.Data.Cookies = result.SecureCookies
				if result.Authorization != "" {
					targetAcc.Data.Authorization = result.Authorization
				}
				if result.CSESIDX != "" {
					targetAcc.CSESIDX = result.CSESIDX
					targetAcc.Data.CSESIDX = result.CSESIDX
				}
				if result.ConfigID != "" {
					targetAcc.ConfigID = result.ConfigID
					targetAcc.Data.ConfigID = result.ConfigID
				}
				targetAcc.Data.Timestamp = time.Now().Format(time.RFC3339)
				targetAcc.FailCount = 0
				targetAcc.Mu.Unlock()

				if err := targetAcc.SaveToFile(); err != nil {
					logger.Error("❌ [%s] 保存刷新后的数据失败: %v", req.Email, err)
				} else {
					logger.Info("✅ [%s] 刷新数据已保存到文件", req.Email)
				}
				pool.Pool.MarkNeedsRefresh(targetAcc)
				logger.Info("✅ 手动浏览器刷新成功: %s", req.Email)
			} else {
				logger.Error("❌ 手动浏览器刷新失败: %s - %v", req.Email, result.Error)
			}
		}()

		c.JSON(200, gin.H{
			"message": "浏览器刷新已触发",
			"email":   req.Email,
		})
	})

	// Flow Token 管理
	admin.GET("/flow/status", func(c *gin.Context) {
		if flowTokenPool == nil {
			c.JSON(200, gin.H{
				"enabled": false,
				"message": "Flow 服务未启用",
			})
			return
		}
		stats := flowTokenPool.Stats()
		stats["enabled"] = flowHandler != nil
		c.JSON(200, stats)
	})

	admin.POST("/flow/add-token", func(c *gin.Context) {
		if flowTokenPool == nil {
			c.JSON(503, gin.H{"error": "Flow 服务未启用"})
			return
		}
		var req struct {
			Cookie string `json:"cookie"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if req.Cookie == "" {
			c.JSON(400, gin.H{"error": "需要提供 cookie"})
			return
		}
		tokenID, err := flowTokenPool.AddFromCookie(req.Cookie)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{
			"message":  "Token 添加成功",
			"token_id": tokenID,
			"total":    flowTokenPool.Count(),
		})
	})

	admin.POST("/flow/remove-token", func(c *gin.Context) {
		if flowTokenPool == nil {
			c.JSON(503, gin.H{"error": "Flow 服务未启用"})
			return
		}
		var req struct {
			TokenID string `json:"token_id"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if err := flowTokenPool.RemoveToken(req.TokenID); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{
			"message": "Token 已移除",
			"total":   flowTokenPool.Count(),
		})
	})

	admin.POST("/flow/reload", func(c *gin.Context) {
		if flowTokenPool == nil {
			c.JSON(503, gin.H{"error": "Flow 服务未启用"})
			return
		}
		loaded, err := flowTokenPool.LoadFromDir()
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{
			"message": "已重新加载",
			"loaded":  loaded,
			"total":   flowTokenPool.Count(),
		})
	})

	admin.POST("/config/browser-refresh", func(c *gin.Context) {
		var req struct {
			Enable   *bool `json:"enable"`
			Headless *bool `json:"headless"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if req.Enable != nil {
			pool.EnableBrowserRefresh = *req.Enable
		}
		if req.Headless != nil {
			pool.BrowserRefreshHeadless = *req.Headless
		}
		c.JSON(200, gin.H{
			"message":  "浏览器刷新配置已更新",
			"enable":   pool.EnableBrowserRefresh,
			"headless": pool.BrowserRefreshHeadless,
		})
	})
}

func runLocalMode() {
	// 本地模式：正常启动
	if err := pool.Pool.Load(DataDir); err != nil {
		log.Fatalf("❌ 加载账号失败: %v", err)
	}

	// 启动配置文件热重载监听
	if err := startConfigWatcher(); err != nil {
		logger.Warn("⚠️ 配置热重载启动失败: %v", err)
	}

	// 代理实例池由异步健康检查完成后初始化

	// 检查 CONFIG_ID
	if DefaultConfig != "" {
		logger.Info("✅ 使用默认 configId: %s", DefaultConfig)
	}

	// 检查 API Key 配置
	if len(GetAPIKeys()) == 0 {
		logger.Warn("⚠️ 未配置 API Key，API 将无鉴权运行")
	}

	// 启动号池管理
	if appConfig.Pool.RefreshOnStartup {
		pool.Pool.StartPoolManager()
	}
	if pool.Pool.TotalCount() == 0 && register.EnableGoRegister {
		needCount := appConfig.Pool.TargetCount
		logger.Info("📝 无账号，启动注册 %d 个...", needCount)
		register.StartRegister(needCount)
	} else if pool.Pool.TotalCount() == 0 {
		logger.Info("🧩 Go 注册已禁用，等待外部 registrar 注入账号")
	}
	if appConfig.Pool.CheckIntervalMinutes > 0 && register.EnableGoRegister {
		go register.PoolMaintainer()
	}

	// 启动 API 服务
	runAPIServer()
}

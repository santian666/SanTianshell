package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const version = "0.0.22"

type PasswordResolver func(hostID, alias string) string

type Service struct {
	dataDir               string
	externalCredentials   bool
	vpsMu                 sync.RWMutex
	sshConnectorMu        sync.RWMutex
	sshConnector          SSHConnector
	interactiveConnector  SSHConnector
	passwordResolver      PasswordResolver
	countryMu             sync.Mutex
	countryCache          map[string]string
	ipInfoCache           map[string]IPInfo
	nodeConfigMu          sync.RWMutex
	nodeConfigAliasLocks  sync.Map
	nodeCacheMu           sync.Mutex
	nodeCacheSignature    string
	nodeCache             []NodeSummary
	middleSettingsMu        sync.Mutex
	autoDeleteRunMu         sync.Mutex
	autoDeleteSchedulerMu   sync.Mutex
	autoDeleteSchedulerOnce sync.Once
	lastAutoDeleteDate      string
	dbMu                    sync.Mutex
	persistentDB            bool
	sharedDB                *sql.DB
	schemaReady             bool
}

func (s *Service) EnablePersistentDB() {
	if s == nil {
		return
	}
	s.dbMu.Lock()
	s.persistentDB = true
	s.dbMu.Unlock()
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.dbMu.Lock()
	db := s.sharedDB
	s.sharedDB = nil
	s.schemaReady = false
	s.dbMu.Unlock()
	if db != nil {
		return db.Close()
	}
	return nil
}

func (s *Service) lockNodeConfigAliases(aliases ...string) func() {
	keys := make([]string, 0, len(aliases))
	seen := make(map[string]bool, len(aliases))
	for _, alias := range aliases {
		key := strings.ToLower(strings.TrimSpace(alias))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	sort.Strings(keys)
	locks := make([]*sync.Mutex, 0, len(keys))
	for _, key := range keys {
		value, _ := s.nodeConfigAliasLocks.LoadOrStore(key, &sync.Mutex{})
		lock := value.(*sync.Mutex)
		lock.Lock()
		locks = append(locks, lock)
	}
	return func() {
		for index := len(locks) - 1; index >= 0; index-- {
			locks[index].Unlock()
		}
	}
}

type AppInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	DataDir string `json:"dataDir"`
}

type Overview struct {
	AppInfo AppInfo      `json:"appInfo"`
	VPSList []VPS        `json:"vpsList"`
	Stats   OverviewStat `json:"stats"`
}

type OverviewStat struct {
	VPSCount    int `json:"vpsCount"`
	NodeCount   int `json:"nodeCount"`
	BackupCount int `json:"backupCount"`
}

type VPS struct {
	ID                   string      `json:"id"`
	HostID               string      `json:"hostId,omitempty"`
	OriginalAlias        string      `json:"original_alias,omitempty"`
	Address              string      `json:"address"`
	Port                 int         `json:"port"`
	Username             string      `json:"username"`
	Password             string      `json:"password,omitempty"`
	KeyPath              string      `json:"key_path"`
	Domain               string      `json:"domain"`
	MiddleDomain         string      `json:"middle_domain"`
	Alias                string      `json:"alias"`
	ProxyHost            string      `json:"proxy_host"`
	ProxyPort            interface{} `json:"proxy_port"`
	ProxyUsername        string      `json:"proxy_username"`
	ProxyPassword        string      `json:"proxy_password,omitempty"`
	GlobalMiddleProxy    interface{} `json:"global_middle_proxy"`
	MiddleProxyCountries interface{} `json:"middle_proxy_countries"`
	NodeCount            int         `json:"nodeCount"`
	StartPort            int         `json:"startPort"`
	CountryCode          string      `json:"countryCode"`
	SingboxStatus        string      `json:"singboxStatus"`
}

type NodeSummary struct {
	VPSAlias     string `json:"vpsAlias"`
	FileName     string `json:"fileName"`
	Tag          string `json:"tag"`
	GroupName    string `json:"groupName"`
	Type         string `json:"type"`
	Host         string `json:"host"`
	ListenPort   int    `json:"listenPort"`
	Outbound     string `json:"outbound"`
	OutboundIP   string `json:"outboundIp"`
	OutboundPort int    `json:"outboundPort"`
	ServerName   string `json:"serverName"`
	UUID         string `json:"uuid"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	Flow         string `json:"flow"`
	Reality      bool   `json:"reality"`
	PublicKey    string `json:"publicKey"`
	ShortID      string `json:"shortId"`
	Fingerprint  string `json:"fingerprint"`
	CreatedAt    string `json:"createdAt"`
	SortAt       string `json:"sortAt"`
}

type BackupFile struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	SourceAlias string `json:"sourceAlias"`
	Size        int64  `json:"size"`
	Modified    string `json:"modified"`
}

type CheckResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Latency int64  `json:"latency"`
}

type VPSRuntimeState struct {
	Alias         string `json:"alias"`
	Status        string `json:"status"`
	NodeCount     int    `json:"nodeCount"`
	StartPort     int    `json:"startPort"`
	Synced        bool   `json:"synced"`
	ConfigDiff    bool   `json:"configDiff,omitempty"`
	CoreVersion   string `json:"coreVersion,omitempty"`
	Uptime        string `json:"uptime,omitempty"`
	ErrorCategory string `json:"errorCategory,omitempty"`
	Message       string `json:"message"`
}

type IPInfo struct {
	Country  string `json:"country"`
	ISP      string `json:"isp"`
	IP       string `json:"ip"`
	Resolved bool   `json:"resolved"`
}

func NewService() *Service {
	return &Service{dataDir: resolveDataDir(), countryCache: map[string]string{}, ipInfoCache: map[string]IPInfo{}}
}

func NewServiceWithDataDir(dataDir string) *Service {
	return &Service{dataDir: dataDir, countryCache: map[string]string{}, ipInfoCache: map[string]IPInfo{}}
}

func NewServiceWithConnector(dataDir string, connector SSHConnector) *Service {
	return &Service{dataDir: dataDir, sshConnector: connector, countryCache: map[string]string{}, ipInfoCache: map[string]IPInfo{}}
}

func NewServiceWithConnectors(dataDir string, connector, interactiveConnector SSHConnector) *Service {
	return NewServiceWithConnectorsAndResolver(dataDir, connector, interactiveConnector, nil)
}

func NewServiceWithConnectorsAndResolver(dataDir string, connector, interactiveConnector SSHConnector, resolver PasswordResolver) *Service {
	return &Service{dataDir: dataDir, externalCredentials: true, sshConnector: connector, interactiveConnector: interactiveConnector, passwordResolver: resolver, countryCache: map[string]string{}, ipInfoCache: map[string]IPInfo{}}
}

func (s *Service) SetPasswordResolver(resolver PasswordResolver) {
	if s == nil {
		return
	}
	s.sshConnectorMu.Lock()
	defer s.sshConnectorMu.Unlock()
	s.passwordResolver = resolver
}

func (s *Service) resolvePassword(vps VPS) string {
	if s != nil {
		s.sshConnectorMu.RLock()
		resolver := s.passwordResolver
		s.sshConnectorMu.RUnlock()
		if resolver != nil {
			if pass := resolver(strings.TrimSpace(vps.HostID), firstNonEmpty(strings.TrimSpace(vps.Alias), strings.TrimSpace(vps.ID))); pass != "" {
				return pass
			}
		}
	}
	return vps.Password
}

func (s *Service) GetAppInfo() AppInfo {
	return AppInfo{Name: "SanTianShell 节点模式", Version: version, DataDir: s.dataDir}
}

func (s *Service) GetOverview() (*Overview, error) {
	vpsList, err := s.loadVPSList()
	if err != nil {
		return nil, err
	}
	nodes, err := s.loadAllNodes()
	if err != nil {
		return nil, err
	}
	backups, err := s.GetBackups("")
	if err != nil {
		return nil, err
	}

	counts := map[string]int{}
	startPorts := map[string]int{}
	for _, node := range nodes {
		counts[node.VPSAlias]++
		if node.ListenPort > startPorts[node.VPSAlias] {
			startPorts[node.VPSAlias] = node.ListenPort
		}
	}
	for i := range vpsList {
		alias := vpsList[i].Alias
		if alias == "" {
			alias = vpsList[i].ID
		}
		vpsList[i].NodeCount = counts[alias]
		vpsList[i].StartPort = nextStartPort(startPorts[alias], 20000)
		if strings.TrimSpace(vpsList[i].CountryCode) == "" {
			vpsList[i].CountryCode = "--"
		}
		vpsList[i].SingboxStatus = "unknown"
	}

	return &Overview{
		AppInfo: s.GetAppInfo(),
		VPSList: vpsList,
		Stats: OverviewStat{
			VPSCount:    len(vpsList),
			NodeCount:   len(nodes),
			BackupCount: len(backups),
		},
	}, nil
}

func (s *Service) RefreshVPSCountries() ([]VPS, error) {
	s.vpsMu.Lock()
	defer s.vpsMu.Unlock()
	list, err := s.loadVPSListUnlocked()
	if err != nil {
		return nil, err
	}
	changed := s.clearExternalCredentials(list)
	for i := range list {
		current := strings.TrimSpace(list[i].CountryCode)
		if current != "" && current != "--" {
			continue
		}
		code := s.lookupCountryCode(list[i].Address)
		if code == "" {
			code = "--"
		}
		if code != current {
			list[i].CountryCode = code
			changed = true
		}
	}
	if changed {
		if err := writeJSON(s.sshPath(), list); err != nil {
			return nil, err
		}
	}
	return list, nil
}

func (s *Service) SaveVPS(vps VPS) error {
	if s.externalCredentials {
		vps.Password = ""
		vps.ProxyPassword = ""
	}
	oldAlias := strings.TrimSpace(vps.OriginalAlias)
	vps.Alias = strings.TrimSpace(vps.Alias)
	if vps.Alias == "" {
		vps.Alias = strings.TrimSpace(vps.ID)
	}
	if vps.Alias == "" || strings.TrimSpace(vps.Address) == "" {
		return errors.New("别名和地址不能为空")
	}
	if vps.Port == 0 {
		vps.Port = 22
	}
	vps.CountryCode = strings.ToUpper(strings.TrimSpace(vps.CountryCode))
	if vps.CountryCode != "" && len(vps.CountryCode) != 2 {
		return errors.New("国家代码必须为空或两位字母，例如 US")
	}
	for _, ch := range vps.CountryCode {
		if ch < 'A' || ch > 'Z' {
			return errors.New("国家代码必须为空或两位字母，例如 US")
		}
	}
	if vps.ID == "" {
		vps.ID = vps.Alias
	}
	vps.ID = vps.Alias
	vps.OriginalAlias = ""
	unlockNodeConfigs := s.lockNodeConfigAliases(oldAlias, vps.Alias)
	defer unlockNodeConfigs()
	s.vpsMu.Lock()
	defer s.vpsMu.Unlock()
	list, err := s.loadVPSListUnlocked()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	replaced := false
	renamedFrom := ""
	for i := range list {
		currentAlias := firstNonEmpty(list[i].Alias, list[i].ID)
		if oldAlias != "" && (strings.EqualFold(currentAlias, oldAlias) || strings.EqualFold(list[i].ID, oldAlias)) {
			renamedFrom = currentAlias
			list[i] = vps
			replaced = true
			break
		}
		if strings.EqualFold(currentAlias, vps.Alias) || strings.EqualFold(list[i].ID, vps.ID) {
			list[i] = vps
			replaced = true
			break
		}
	}
	if !replaced {
		for i := range list {
			if sameVPSIdentity(list[i], vps) {
				renamedFrom = firstNonEmpty(list[i].Alias, list[i].ID)
				list[i] = vps
				replaced = true
				break
			}
		}
	}
	if !replaced {
		for _, item := range list {
			if strings.EqualFold(firstNonEmpty(item.Alias, item.ID), vps.Alias) || strings.EqualFold(item.ID, vps.ID) {
				return errors.New("VPS 别名已存在")
			}
		}
		list = append(list, vps)
	}
	if renamedFrom != "" && !strings.EqualFold(renamedFrom, vps.Alias) {
		if err := s.renameNodeConfigAlias(renamedFrom, vps.Alias); err != nil {
			return err
		}
	}
	s.clearExternalCredentials(list)
	return writeJSON(s.sshPath(), list)
}

func sameVPSIdentity(a VPS, b VPS) bool {
	return strings.EqualFold(strings.TrimSpace(a.Address), strings.TrimSpace(b.Address)) &&
		asInt(a.Port) == asInt(b.Port) &&
		strings.EqualFold(strings.TrimSpace(a.Username), strings.TrimSpace(b.Username))
}

func (s *Service) DeleteVPS(alias string) error {
	return s.forgetVPSLocalData("", alias, true)
}

// ForgetHostLocalData 仅清理本机保存的 VPS 绑定和节点展示，不连接或修改远端服务器。
func (s *Service) ForgetHostLocalData(hostID string, alias string) error {
	return s.forgetVPSLocalData(hostID, alias, false)
}

// ReconcileHostLocalData 根据当前仍存在的主机清理本地 VPS 绑定和节点展示，不访问远端服务器。
func (s *Service) ReconcileHostLocalData(hostIDs []string, aliases []string) error {
	s.vpsMu.Lock()
	defer s.vpsMu.Unlock()
	s.nodeConfigMu.Lock()
	defer s.nodeConfigMu.Unlock()

	activeHostIDs := make(map[string]bool, len(hostIDs))
	for _, hostID := range hostIDs {
		if hostID = strings.ToLower(strings.TrimSpace(hostID)); hostID != "" {
			activeHostIDs[hostID] = true
		}
	}
	activeAliases := make(map[string]bool, len(aliases))
	for _, alias := range aliases {
		if alias = strings.ToLower(strings.TrimSpace(alias)); alias != "" {
			activeAliases[alias] = true
		}
	}

	list, err := s.loadVPSListUnlocked()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next := make([]VPS, 0, len(list))
	retainedAliases := make(map[string]bool, len(list))
	for _, item := range list {
		hostID := strings.ToLower(strings.TrimSpace(item.HostID))
		alias := strings.ToLower(strings.TrimSpace(firstNonEmpty(item.Alias, item.ID)))
		if (hostID != "" && activeHostIDs[hostID]) || (alias != "" && activeAliases[alias]) {
			next = append(next, item)
			if alias != "" {
				retainedAliases[alias] = true
			}
		}
	}
	if len(next) != len(list) {
		s.clearExternalCredentials(next)
		if err := writeJSON(s.sshPath(), next); err != nil {
			return err
		}
	}

	configPath := filepath.Join(s.dataDir, "sbconfig.json")
	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	raw := map[string]any{}
	if len(strings.TrimSpace(string(data))) == 0 {
		return errors.New("读取 sbconfig.json 失败: 文件为空")
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("读取 sbconfig.json 失败: %w", err)
	}
	changed := false
	for key := range raw {
		alias := strings.ToLower(strings.TrimSpace(key))
		if !activeAliases[alias] && !retainedAliases[alias] {
			delete(raw, key)
			changed = true
		}
	}
	if changed {
		if err := writeJSON(configPath, raw); err != nil {
			return err
		}
		s.invalidateNodeCache()
	}
	return nil
}

func (s *Service) forgetVPSLocalData(hostID string, alias string, requireVPS bool) error {
	s.vpsMu.Lock()
	defer s.vpsMu.Unlock()
	s.nodeConfigMu.Lock()
	defer s.nodeConfigMu.Unlock()

	hostID = strings.TrimSpace(hostID)
	alias = strings.TrimSpace(alias)
	if hostID == "" && alias == "" {
		return errors.New("别名不能为空")
	}
	list, err := s.loadVPSListUnlocked()
	if err != nil {
		return err
	}
	next := make([]VPS, 0, len(list))
	removedAliases := map[string]bool{}
	for _, item := range list {
		matchesHost := hostID != "" && strings.EqualFold(strings.TrimSpace(item.HostID), hostID)
		matchesAlias := alias != "" && (strings.EqualFold(item.Alias, alias) || strings.EqualFold(item.ID, alias))
		if matchesHost || matchesAlias {
			removedAlias := firstNonEmpty(strings.TrimSpace(item.Alias), strings.TrimSpace(item.ID))
			if removedAlias != "" {
				removedAliases[strings.ToLower(removedAlias)] = true
			}
			continue
		}
		next = append(next, item)
	}
	if requireVPS && len(next) == len(list) {
		return errors.New("未找到 VPS")
	}
	if alias != "" {
		removedAliases[strings.ToLower(alias)] = true
	}
	if len(next) != len(list) {
		s.clearExternalCredentials(next)
		if err := writeJSON(s.sshPath(), next); err != nil {
			return err
		}
	}

	configPath := filepath.Join(s.dataDir, "sbconfig.json")
	raw := map[string]any{}
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return errors.New("读取 sbconfig.json 失败: 文件为空")
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("读取 sbconfig.json 失败: %w", err)
	}
	changed := false
	for key := range raw {
		if removedAliases[strings.ToLower(strings.TrimSpace(key))] {
			delete(raw, key)
			changed = true
		}
	}
	if changed {
		if err := writeJSON(configPath, raw); err != nil {
			return err
		}
		s.invalidateNodeCache()
	}
	return nil
}

func (s *Service) GetNodes(alias string) ([]NodeSummary, error) {
	nodes, err := s.loadAllNodes()
	if err != nil {
		return nil, err
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return nodes, nil
	}
	filtered := make([]NodeSummary, 0)
	for _, node := range nodes {
		if strings.EqualFold(node.VPSAlias, alias) {
			filtered = append(filtered, node)
		}
	}
	return filtered, nil
}

func (s *Service) GetBackups(alias string) ([]BackupFile, error) {
	baseDir := filepath.Join(s.dataDir, "bk")
	alias = strings.TrimSpace(alias)
	files := make([]BackupFile, 0)
	seenPaths := make(map[string]bool)

	// 1. 若指定了具体 VPS 别名，优先读取该 VPS 的专属隔离目录
	if alias != "" {
		subDir := filepath.Join(baseDir, safeVPSDirName(alias))
		if entries, err := os.ReadDir(subDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				physicalName := entry.Name()
				displayName, sourceAlias := backupDisplayInfo(physicalName)
				if sourceAlias == "" {
					sourceAlias = alias
				}
				info, err := entry.Info()
				if err != nil {
					continue
				}
				filePath := filepath.Join(subDir, physicalName)
				seenPaths[filePath] = true
				files = append(files, BackupFile{
					Name:        displayName,
					Path:        filePath,
					SourceAlias: sourceAlias,
					Size:        info.Size(),
					Modified:    info.ModTime().Format("2006-01-02 15:04:05"),
				})
			}
		}
	}

	// 2. 读取基础目录（兼容未迁移的旧单层备份，以及支持 alias == "" 时扫描全量子目录）
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) && len(files) == 0 {
			return []BackupFile{}, nil
		}
		if os.IsNotExist(err) {
			sort.Slice(files, func(i, j int) bool { return files[i].Modified > files[j].Modified })
			return files, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			// 当 alias 为空（全量概览），遍历所有子目录中的备份
			if alias == "" {
				subDir := filepath.Join(baseDir, entry.Name())
				subEntries, err := os.ReadDir(subDir)
				if err != nil {
					continue
				}
				for _, subEntry := range subEntries {
					if subEntry.IsDir() {
						continue
					}
					physicalName := subEntry.Name()
					filePath := filepath.Join(subDir, physicalName)
					if seenPaths[filePath] {
						continue
					}
					seenPaths[filePath] = true
					displayName, sourceAlias := backupDisplayInfo(physicalName)
					if sourceAlias == "" {
						if decoded, unErr := url.QueryUnescape(entry.Name()); unErr == nil {
							sourceAlias = decoded
						} else {
							sourceAlias = entry.Name()
						}
					}
					info, err := subEntry.Info()
					if err != nil {
						continue
					}
					files = append(files, BackupFile{
						Name:        displayName,
						Path:        filePath,
						SourceAlias: sourceAlias,
						Size:        info.Size(),
						Modified:    info.ModTime().Format("2006-01-02 15:04:05"),
					})
				}
			}
			continue
		}

		// 兼容老版本直接放在 bk 根目录下的历史文件
		physicalName := entry.Name()
		filePath := filepath.Join(baseDir, physicalName)
		if seenPaths[filePath] {
			continue
		}
		displayName, sourceAlias := backupDisplayInfo(physicalName)
		if alias != "" && !strings.EqualFold(sourceAlias, alias) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		seenPaths[filePath] = true
		files = append(files, BackupFile{
			Name:        displayName,
			Path:        filePath,
			SourceAlias: sourceAlias,
			Size:        info.Size(),
			Modified:    info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Modified > files[j].Modified
	})
	return files, nil
}

func (s *Service) ReadBackup(path string) (string, error) {
	path, err := s.resolveBackupPath(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Service) resolveBackupPath(path string) (string, error) {
	backupDir, err := filepath.Abs(filepath.Join(s.dataDir, "bk"))
	if err != nil {
		return "", errors.New("备份目录无效")
	}
	path, err = filepath.Abs(filepath.Clean(strings.TrimSpace(path)))
	if err != nil {
		return "", errors.New("备份路径无效")
	}
	rel, err := filepath.Rel(backupDir, path)
	if err != nil || rel == "." || strings.EqualFold(rel, "..") || strings.HasPrefix(strings.ToLower(rel), ".."+string(filepath.Separator)) {
		return "", errors.New("备份路径非法")
	}
	return path, nil
}

func (s *Service) TestTCP(address string, port int) CheckResult {
	address = strings.TrimSpace(address)
	if address == "" || port <= 0 {
		return CheckResult{Success: false, Message: "地址或端口无效"}
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(address, strconv.Itoa(port)), 5*time.Second)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return CheckResult{Success: false, Message: err.Error(), Latency: latency}
	}
	_ = conn.Close()
	return CheckResult{Success: true, Message: "连接正常", Latency: latency}
}

func (s *Service) loadVPSList() ([]VPS, error) {
	if s.externalCredentials {
		s.vpsMu.Lock()
		defer s.vpsMu.Unlock()
		list, err := s.loadVPSListUnlocked()
		if err != nil {
			return nil, err
		}
		if s.clearExternalCredentials(list) {
			if err := writeJSON(s.sshPath(), list); err != nil {
				return nil, fmt.Errorf("清理 ssh.json 中的旧明文凭证失败: %w", err)
			}
		}
		return list, nil
	}
	s.vpsMu.RLock()
	defer s.vpsMu.RUnlock()
	return s.loadVPSListUnlocked()
}

func (s *Service) clearExternalCredentials(list []VPS) bool {
	if !s.externalCredentials {
		return false
	}
	changed := false
	for i := range list {
		if list[i].Password != "" {
			list[i].Password = ""
			changed = true
		}
		if list[i].ProxyPassword != "" {
			list[i].ProxyPassword = ""
			changed = true
		}
	}
	return changed
}

func (s *Service) loadVPSListUnlocked() ([]VPS, error) {
	data, err := os.ReadFile(s.sshPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []VPS{}, nil
		}
		return nil, err
	}
	var list []VPS
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("读取 ssh.json 失败: %w", err)
	}
	sort.Slice(list, func(i, j int) bool {
		return strings.ToLower(list[i].Alias) < strings.ToLower(list[j].Alias)
	})
	return list, nil
}

func (s *Service) getVPS(alias string) (VPS, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return VPS{}, errors.New("VPS 别名不能为空")
	}
	list, err := s.loadVPSList()
	if err != nil {
		return VPS{}, err
	}
	for _, item := range list {
		if strings.EqualFold(item.Alias, alias) || strings.EqualFold(item.ID, alias) {
			if item.Port == 0 {
				item.Port = 22
			}
			if item.Username == "" {
				item.Username = "root"
			}
			return item, nil
		}
	}
	return VPS{}, errors.New("未找到 VPS")
}

func (s *Service) loadAllNodes() ([]NodeSummary, error) {
	path := filepath.Join(s.dataDir, "sbconfig.json")
	s.nodeConfigMu.RLock()
	defer s.nodeConfigMu.RUnlock()

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.invalidateNodeCache()
			return []NodeSummary{}, nil
		}
		return nil, err
	}
	signature := fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
	s.nodeCacheMu.Lock()
	defer s.nodeCacheMu.Unlock()
	if signature == s.nodeCacheSignature {
		return append([]NodeSummary(nil), s.nodeCache...), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]struct {
		Nodes map[string]json.RawMessage `json:"nodes_configs"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("读取 sbconfig.json 失败: %w", err)
	}
	nodes := make([]NodeSummary, 0)
	for alias, group := range raw {
		for fileName, nodeRaw := range group.Nodes {
			nodes = append(nodes, parseNode(alias, fileName, nodeRaw))
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].SortAt != nodes[j].SortAt {
			return nodes[i].SortAt > nodes[j].SortAt
		}
		if nodes[i].ListenPort != nodes[j].ListenPort {
			return nodes[i].ListenPort > nodes[j].ListenPort
		}
		return nodes[i].VPSAlias < nodes[j].VPSAlias
	})
	s.nodeCacheSignature = signature
	s.nodeCache = append(s.nodeCache[:0], nodes...)
	return append([]NodeSummary(nil), nodes...), nil
}

func (s *Service) invalidateNodeCache() {
	s.nodeCacheMu.Lock()
	s.nodeCacheSignature = ""
	s.nodeCache = nil
	s.nodeCacheMu.Unlock()
}

func (s *Service) sshPath() string {
	return filepath.Join(s.dataDir, "ssh.json")
}

func (s *Service) lookupCountryCode(address string) string {
	info := s.lookupIPInfo(address)
	if info.Country == "" {
		return "--"
	}
	return info.Country
}

func (s *Service) lookupIPInfo(address string) IPInfo {
	host := strings.TrimSpace(address)
	if host == "" {
		return IPInfo{Country: "--", ISP: "-", Resolved: false}
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	ip := net.ParseIP(host)
	if ip == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err == nil && len(addrs) > 0 {
			ip = addrs[0].IP
		}
	}
	if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() {
		return IPInfo{Country: "--", ISP: "-", Resolved: false}
	}
	key := ip.String()
	s.countryMu.Lock()
	if s.ipInfoCache == nil {
		s.ipInfoCache = make(map[string]IPInfo)
	}
	if s.countryCache == nil {
		s.countryCache = make(map[string]string)
	}
	if info, ok := s.ipInfoCache[key]; ok {
		s.countryMu.Unlock()
		return info
	}
	if code, ok := s.countryCache[key]; ok {
		s.countryMu.Unlock()
		return IPInfo{Country: code, ISP: "-", IP: key, Resolved: true}
	}
	s.countryMu.Unlock()

	info := s.queryIPInfo(key)
	if info.Country == "" {
		info = IPInfo{Country: "--", ISP: "-", IP: key, Resolved: true}
	}

	s.countryMu.Lock()
	if len(s.ipInfoCache) >= 1024 {
		removed := 0
		for cachedKey := range s.ipInfoCache {
			delete(s.ipInfoCache, cachedKey)
			delete(s.countryCache, cachedKey)
			removed++
			if removed >= 256 {
				break
			}
		}
	}
	if info.Country != "--" {
		s.countryCache[key] = info.Country
	}
	s.ipInfoCache[key] = info
	s.countryMu.Unlock()
	return info
}

func (s *Service) queryIPInfo(key string) IPInfo {
	client := http.Client{Timeout: 4 * time.Second}
	if info, ok := queryIPWhoIs(client, key); ok {
		return info
	}
	if info, ok := queryIPInfoIO(client, key); ok {
		return info
	}
	return IPInfo{Country: "--", ISP: "-", IP: key, Resolved: true}
}

func readLimitedHTTPBody(body io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(body, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("响应体超过大小限制")
	}
	return data, nil
}

func queryIPWhoIs(client http.Client, key string) (IPInfo, bool) {
	resp, err := client.Get("https://ipwho.is/" + key)
	if err != nil {
		return IPInfo{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return IPInfo{}, false
	}
	data, err := readLimitedHTTPBody(resp.Body, 1<<20)
	if err != nil {
		return IPInfo{}, false
	}
	var payload struct {
		Success     bool   `json:"success"`
		CountryCode string `json:"country_code"`
		IP          string `json:"ip"`
		Connection  struct {
			ISP string `json:"isp"`
			Org string `json:"org"`
		} `json:"connection"`
	}
	if json.Unmarshal(data, &payload) != nil || !payload.Success || payload.CountryCode == "" {
		return IPInfo{}, false
	}
	return IPInfo{Country: strings.ToUpper(payload.CountryCode), ISP: firstNonEmpty(payload.Connection.ISP, payload.Connection.Org, "-"), IP: firstNonEmpty(payload.IP, key), Resolved: true}, true
}

func queryIPInfoIO(client http.Client, key string) (IPInfo, bool) {
	resp, err := client.Get("https://ipinfo.io/" + key + "/json")
	if err != nil {
		return IPInfo{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return IPInfo{}, false
	}
	data, err := readLimitedHTTPBody(resp.Body, 1<<20)
	if err != nil {
		return IPInfo{}, false
	}
	var payload struct {
		Country string `json:"country"`
		Org     string `json:"org"`
		IP      string `json:"ip"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.Country == "" {
		return IPInfo{}, false
	}
	return IPInfo{Country: strings.ToUpper(payload.Country), ISP: firstNonEmpty(payload.Org, "-"), IP: firstNonEmpty(payload.IP, key), Resolved: true}, true
}

func parseNode(alias, fileName string, data json.RawMessage) NodeSummary {
	var obj map[string]interface{}
	_ = json.Unmarshal(data, &obj)
	node := NodeSummary{VPSAlias: alias, FileName: fileName}

	if inbounds, ok := obj["inbounds"].([]interface{}); ok && len(inbounds) > 0 {
		if inbound, ok := inbounds[0].(map[string]interface{}); ok {
			node.Tag = asString(inbound["tag"])
			node.Type = asString(inbound["type"])
			node.Host = asString(inbound["listen"])
			if node.Host == "::" || node.Host == "0.0.0.0" {
				node.Host = ""
			}
			node.ListenPort = asInt(inbound["listen_port"])
			if tls, ok := inbound["tls"].(map[string]interface{}); ok {
				node.ServerName = asString(tls["server_name"])
			}
			if users, ok := inbound["users"].([]interface{}); ok && len(users) > 0 {
				if user, ok := users[0].(map[string]interface{}); ok {
					node.UUID = asString(user["uuid"])
					if node.UUID == "" {
						node.UUID = asString(user["password"])
					}
					node.Username = asString(user["username"])
					node.Password = asString(user["password"])
					node.Flow = asString(user["flow"])
				}
			}
		}
	}
	if share, ok := obj["share"].(map[string]interface{}); ok {
		node.PublicKey = asString(share["reality_public_key"])
		node.ShortID = asString(share["reality_short_id"])
		node.Fingerprint = asString(share["reality_fingerprint"])
	}
	if metadata, ok := obj["metadata"].(map[string]interface{}); ok {
		node.CreatedAt = asString(metadata["created_at"])
		node.SortAt = firstNonEmpty(asString(metadata["imported_at"]), node.CreatedAt)
	}
	if node.SortAt == "" {
		node.SortAt = node.CreatedAt
	}
	node.Reality = node.Type == "vless" && node.PublicKey != ""
	if outbounds, ok := obj["outbounds"].([]interface{}); ok && len(outbounds) > 0 {
		if outbound, ok := outbounds[0].(map[string]interface{}); ok {
			node.Outbound = asString(outbound["type"])
			node.OutboundIP = asString(outbound["server"])
			node.OutboundPort = asInt(outbound["server_port"])
		}
	}
	if node.Tag == "" {
		node.Tag = strings.TrimSuffix(fileName, filepath.Ext(fileName))
	}
	node.GroupName = extractGroupName(obj)
	return node
}

func resolveDataDir() string {
	wd, _ := os.Getwd()
	exe, _ := os.Executable()
	exeDir := filepath.Dir(exe)
	bases := []string{
		wd,
		filepath.Dir(wd),
		exeDir,
		filepath.Dir(exeDir),
		filepath.Dir(filepath.Dir(exeDir)),
	}
	for _, base := range bases {
		candidate := filepath.Join(base, "data", "web", "nsbsingbox")
		if _, err := os.Stat(filepath.Join(candidate, "ssh.json")); err == nil {
			abs, _ := filepath.Abs(candidate)
			return abs
		}
	}
	abs, _ := filepath.Abs(filepath.Join(wd, "data", "web", "nsbsingbox"))
	_ = os.MkdirAll(abs, 0755)
	return abs
}

func writeJSON(path string, value interface{}) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeBytesAtomic(path, data, 0600)
}

func writeBytesAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			backupPath := path + ".bak"
			_ = os.Remove(backupPath)
			if backupErr := os.Rename(path, backupPath); backupErr == nil {
				if renameErr := os.Rename(tempPath, path); renameErr == nil {
					_ = os.Remove(backupPath)
					return nil
				} else {
					_ = os.Rename(backupPath, path)
					return renameErr
				}
			}
		}
		return err
	}
	return nil
}

func asString(value interface{}) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

func asInt(value interface{}) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	default:
		return 0
	}
}

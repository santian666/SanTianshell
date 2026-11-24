package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"santianshell/internal/quickconnect"
	"santianshell/internal/rdp"
	"santianshell/internal/remote"

	runtime2 "github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) SaveHost(h remote.Host) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	if strings.TrimSpace(h.Name) == "" || strings.TrimSpace(h.Address) == "" || strings.TrimSpace(h.Username) == "" {
		return Settings{}, errors.New("名称、地址和用户名不能为空")
	}
	h.Group = strings.TrimSpace(h.Group)
	if err := normalizeSOCKS5(&h); err != nil {
		return Settings{}, err
	}
	h.MFACode = ""
	h.Temporary = false
	if h.Port == 0 {
		if strings.EqualFold(h.Protocol, "rdp") {
			h.Port = 3389
		} else {
			h.Port = 22
		}
	}
	if h.Protocol == "" {
		h.Protocol = "ssh"
	}
	if h.AuthType == "" {
		h.AuthType = "password"
	}
	if h.Timeout <= 0 {
		h.Timeout = 12
	}
	if h.KeepAlive <= 0 {
		h.KeepAlive = 30
	}
	if h.Encoding == "" {
		h.Encoding = "UTF-8"
	}
	if err := normalizeRDPHostOptions(&h); err != nil {
		return Settings{}, err
	}
	if err := normalizeSSHHostOptions(&h); err != nil {
		return Settings{}, err
	}
	modifiedExisting := false
	connectionChanged := false
	a.mu.Lock()
	candidate := cloneSettings(a.settings)
	if h.Group != "" {
		groupExists := false
		for _, group := range candidate.Groups {
			if strings.EqualFold(strings.TrimSpace(group), h.Group) {
				groupExists = true
				break
			}
		}
		if !groupExists {
			candidate.Groups = append(candidate.Groups, h.Group)
		}
	}
	if temporary, exists := a.temporaryHosts[h.ID]; exists && h.HostKey == "" &&
		temporary.Address == h.Address && equalSSHPort(temporary.Port, h.Port) {
		h.HostKey = temporary.HostKey
	}
	if h.ID == "" {
		h.ID = strconv.FormatInt(time.Now().UnixNano(), 36)
		candidate.Hosts = append(candidate.Hosts, h)
	} else {
		found := false
		for i := range candidate.Hosts {
			if candidate.Hosts[i].ID == h.ID {
				previous := candidate.Hosts[i]
				previousRevision := aiHostRevision(a.resolveStoredCredential(previous))
				h.Country = previous.Country
				h.CountryManual = previous.CountryManual
				h.LastUsed = previous.LastUsed
				h.SystemVersion = previous.SystemVersion
				h.Starred = previous.Starred
				h.Deleted = previous.Deleted
				if strings.TrimSpace(h.Tags) == "" {
					h.Tags = previous.Tags
				}
				if h.Address == previous.Address && equalSSHPort(h.Port, previous.Port) {
					h.HostKey = previous.HostKey
				} else {
					h.HostKey = ""
				}
				if h.VPSAlias == "" {
					h.VPSAlias = previous.VPSAlias
				}
				connectionChanged = previousRevision != aiHostRevision(a.resolveStoredCredential(h))
				candidate.Hosts[i] = h
				modifiedExisting = true
				found = true
				break
			}
		}
		if !found {
			candidate.Hosts = append(candidate.Hosts, h)
		}
	}
	if err := a.validateSSHJumpChainLocked(candidate.Hosts, h); err != nil {
		a.mu.Unlock()
		return Settings{}, err
	}
	if err := a.commitSettingsLocked(candidate); err != nil {
		a.mu.Unlock()
		return Settings{}, err
	}
	delete(a.temporaryHosts, h.ID)
	var detached hostRuntimeDetach
	if connectionChanged {
		detached = a.detachHostRuntimeLocked(h.ID)
	}
	settings := settingsForClient(a.settings)
	a.mu.Unlock()
	if modifiedExisting && a.aiAgent != nil {
		a.aiAgent.Close()
	}
	if connectionChanged {
		a.closeDetachedHostRuntime(h.ID, detached, "主机连接信息已修改，请重新连接", "修改主机连接信息后关闭 RDP 会话失败")
		a.invalidateSSHJumpDependents(h.ID)
	}
	return settings, nil
}

func normalizeSSHHostOptions(h *remote.Host) error {
	if h == nil {
		return nil
	}
	if strings.EqualFold(h.Protocol, "rdp") {
		h.SSHJumpHostID = ""
		return nil
	}
	h.SSHJumpHostID = strings.TrimSpace(h.SSHJumpHostID)
	if h.SSHJumpHostID != "" && h.ProxyEnabled {
		return errors.New("SSH 跳板不能与目标主机 SOCKS5 代理同时启用")
	}
	if h.ID != "" && h.SSHJumpHostID == h.ID {
		return errors.New("SSH 主机不能将自身设置为 SSH 跳板")
	}
	return nil
}

func validateSingleSSHJumpChain(hostsByID map[string]remote.Host, start remote.Host) error {
	if strings.EqualFold(start.Protocol, "rdp") {
		return nil
	}
	jumpID := strings.TrimSpace(start.SSHJumpHostID)
	if jumpID == "" {
		return nil
	}
	seen := make(map[string]struct{})
	if id := strings.TrimSpace(start.ID); id != "" {
		seen[id] = struct{}{}
	}
	for depth := 0; jumpID != ""; depth++ {
		if depth >= maxSSHJumpDepth {
			return fmt.Errorf("SSH 跳板链深度不能超过 %d 级", maxSSHJumpDepth)
		}
		if _, visited := seen[jumpID]; visited {
			return errors.New("SSH 跳板链存在循环引用")
		}
		seen[jumpID] = struct{}{}
		hop, ok := hostsByID[jumpID]
		if !ok || hop.Deleted {
			return errors.New("所选 SSH 跳板主机不存在或已删除")
		}
		if !isSSHHostProtocol(hop.Protocol) {
			return errors.New("SSH 跳板主机必须是 SSH 协议")
		}
		jumpID = strings.TrimSpace(hop.SSHJumpHostID)
	}
	return nil
}

func hostJumpChainUsesID(hostsByID map[string]remote.Host, start remote.Host, upstreamID string) bool {
	if upstreamID == "" || strings.EqualFold(start.Protocol, "rdp") {
		return false
	}
	seen := make(map[string]struct{})
	jumpID := strings.TrimSpace(start.SSHJumpHostID)
	for depth := 0; jumpID != "" && depth <= maxSSHJumpDepth+1; depth++ {
		if jumpID == upstreamID {
			return true
		}
		if _, visited := seen[jumpID]; visited {
			return false
		}
		seen[jumpID] = struct{}{}
		hop, ok := hostsByID[jumpID]
		if !ok {
			return false
		}
		jumpID = strings.TrimSpace(hop.SSHJumpHostID)
	}
	return false
}

func (a *App) validateSSHJumpChainLocked(hosts []remote.Host, h remote.Host) error {
	hostsByID := make(map[string]remote.Host, len(hosts)+1)
	for _, item := range hosts {
		if item.ID != "" {
			hostsByID[item.ID] = item
		}
	}
	if err := validateSingleSSHJumpChain(hostsByID, h); err != nil {
		return err
	}
	if targetID := strings.TrimSpace(h.ID); targetID != "" {
		for _, item := range hosts {
			if item.Deleted || item.ID == "" || item.ID == targetID || !hostJumpChainUsesID(hostsByID, item, targetID) {
				continue
			}
			if err := validateSingleSSHJumpChain(hostsByID, item); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) invalidateSSHJumpDependents(upstreamID string) {
	upstreamID = strings.TrimSpace(upstreamID)
	if upstreamID == "" {
		return
	}
	a.mu.RLock()
	hostsByID := make(map[string]remote.Host, len(a.settings.Hosts))
	for _, item := range a.settings.Hosts {
		if item.ID != "" {
			hostsByID[item.ID] = item
		}
	}
	var dependents []string
	for _, item := range a.settings.Hosts {
		if item.ID != "" && item.ID != upstreamID && hostJumpChainUsesID(hostsByID, item, upstreamID) {
			dependents = append(dependents, item.ID)
		}
	}
	a.mu.RUnlock()
	for _, id := range dependents {
		a.connections.Invalidate(id)
	}
}

func normalizeSOCKS5(h *remote.Host) error {
	if !h.ProxyEnabled {
		return nil
	}
	h.ProxyHost = strings.TrimSpace(h.ProxyHost)
	if h.ProxyHost == "" {
		h.ProxyHost = "127.0.0.1"
	}
	if h.ProxyPort == 0 {
		h.ProxyPort = 20808
	}
	if h.ProxyPort < 1 || h.ProxyPort > 65535 {
		return errors.New("SOCKS5 代理端口必须在 1-65535 之间")
	}
	return nil
}

func (a *App) DetectHostCountry(id string) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	a.mu.RLock()
	var host remote.Host
	for _, item := range a.settings.Hosts {
		if item.ID == id {
			host = item
			break
		}
	}
	a.mu.RUnlock()
	if host.ID == "" {
		return Settings{}, errors.New("主机不存在")
	}
	if host.CountryManual || host.Country != "" {
		a.mu.RLock()
		defer a.mu.RUnlock()
		return settingsForClient(a.settings), nil
	}
	ip := net.ParseIP(strings.TrimSpace(host.Address))
	if ip == nil {
		addresses, err := net.LookupIP(host.Address)
		if err != nil {
			return Settings{}, errors.New("无法解析主机地址")
		}
		for _, address := range addresses {
			if address.IsGlobalUnicast() && !address.IsPrivate() {
				ip = address
				break
			}
		}
	}
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return Settings{}, errors.New("内网地址无法识别国家")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ipwho.is/"+ip.String()+"?fields=success,country_code", nil)
	if err != nil {
		return Settings{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Settings{}, errors.New("国家识别服务暂不可用")
	}
	defer resp.Body.Close()
	var result struct {
		Success     bool   `json:"success"`
		CountryCode string `json:"country_code"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result) != nil || !result.Success {
		return Settings{}, errors.New("国家识别失败")
	}
	country := strings.ToUpper(strings.TrimSpace(result.CountryCode))
	if country == "GB" {
		country = "UK"
	}
	if len(country) != 2 {
		return Settings{}, errors.New("国家识别结果无效")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	candidate := cloneSettings(a.settings)
	for i := range candidate.Hosts {
		if candidate.Hosts[i].ID == id && !candidate.Hosts[i].CountryManual {
			candidate.Hosts[i].Country = country
			if err := a.commitSettingsLocked(candidate); err != nil {
				return Settings{}, err
			}
			return settingsForClient(a.settings), nil
		}
	}
	return settingsForClient(a.settings), nil
}

func (a *App) SetHostCountry(id, country string) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	country = strings.ToUpper(strings.TrimSpace(country))
	if country == "GB" {
		country = "UK"
	}
	if country != "" && (len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z') {
		return Settings{}, errors.New("国家缩写必须是两个英文字母，例如 UK、US、CN")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	candidate := cloneSettings(a.settings)
	for i := range candidate.Hosts {
		if candidate.Hosts[i].ID == id {
			candidate.Hosts[i].Country = country
			candidate.Hosts[i].CountryManual = country != ""
			if err := a.commitSettingsLocked(candidate); err != nil {
				return Settings{}, err
			}
			return settingsForClient(a.settings), nil
		}
	}
	return Settings{}, errors.New("主机不存在")
}

func (a *App) SetHostStarred(id string, starred bool) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	candidate := cloneSettings(a.settings)
	for i := range candidate.Hosts {
		if candidate.Hosts[i].ID == id {
			candidate.Hosts[i].Starred = starred
			if err := a.commitSettingsLocked(candidate); err != nil {
				return Settings{}, err
			}
			return settingsForClient(a.settings), nil
		}
	}
	return Settings{}, errors.New("主机不存在")
}

func (a *App) TestHostConfig(h remote.Host) (remote.Result, error) {
	if err := a.requireUnlocked(); err != nil {
		return remote.Result{}, err
	}
	if strings.TrimSpace(h.Address) == "" || strings.TrimSpace(h.Username) == "" {
		return remote.Result{}, errors.New("地址和用户名不能为空")
	}
	if err := normalizeSOCKS5(&h); err != nil {
		return remote.Result{}, err
	}
	if strings.EqualFold(h.Protocol, "rdp") {
		if err := normalizeRDPHostOptions(&h); err != nil {
			return remote.Result{}, err
		}
		latency, err := testRDPHostConfig(h, a.startRDPJumpForward, remote.Latency)
		if err != nil {
			return remote.Result{}, err
		}
		return remote.Result{Output: fmt.Sprintf("RDP 端口可连接，延迟 %d ms", latency)}, nil
	}
	if err := normalizeSSHHostOptions(&h); err != nil {
		return remote.Result{}, err
	}
	persistHostKey := false
	a.mu.RLock()
	if err := a.validateSSHJumpChainLocked(a.settings.Hosts, h); err != nil {
		a.mu.RUnlock()
		return remote.Result{}, err
	}
	for _, stored := range a.settings.Hosts {
		if h.ID == "" || stored.ID != h.ID {
			continue
		}
		storedPort, testPort := stored.Port, h.Port
		if storedPort == 0 {
			storedPort = 22
		}
		if testPort == 0 {
			testPort = 22
		}
		if stored.Address == h.Address && storedPort == testPort {
			h.HostKey = stored.HostKey
			persistHostKey = true
		}
		break
	}
	h = a.resolveStoredCredential(h)
	a.mu.RUnlock()
	h = a.wireSSHCallbacks(h, persistHostKey)
	result, err := remote.Run(h, "printf '连接成功：'; hostname; printf '\n系统：'; uname -sr")
	if err != nil {
		slog.Error("SSH 测试连接失败", "主机ID", h.ID, "地址", h.Address, "错误", err)
	}
	return result, err
}


func (a *App) DeleteHost(id string) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	a.mu.Lock()
	candidate := cloneSettings(a.settings)
	var deletedHost remote.Host
	for i := range candidate.Hosts {
		if candidate.Hosts[i].ID == id {
			candidate.Hosts[i].Deleted = true
			deletedHost = candidate.Hosts[i]
			break
		}
	}
	if err := a.commitSettingsLocked(candidate); err != nil {
		a.mu.Unlock()
		return Settings{}, err
	}
	detached := a.detachHostRuntimeLocked(id)
	settings := settingsForClient(a.settings)
	nodeDataCleanup := a.nodeDataCleanup
	a.mu.Unlock()
	if nodeDataCleanup != nil && deletedHost.ID != "" {
		if err := nodeDataCleanup(deletedHost.ID, deletedHost.VPSAlias); err != nil {
			slog.Error("删除主机时清理本地节点展示失败", "主机ID", id, "VPS别名", deletedHost.VPSAlias, "错误", err)
		}
	}
	if a.aiAgent != nil {
		a.aiAgent.Close()
	}
	a.closeDetachedHostRuntime(id, detached, "主机已删除，SSH 连接已断开", "删除主机时关闭 RDP 会话失败")
	a.invalidateSSHJumpDependents(id)
	return settings, nil
}

func (a *App) DeleteHosts(ids []string) (Settings, error) {
	seen := make(map[string]bool, len(ids))
	var settings Settings
	deleted := 0
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		var err error
		settings, err = a.DeleteHost(id)
		if err != nil {
			return Settings{}, err
		}
		deleted++
	}
	if deleted == 0 {
		return Settings{}, errors.New("请先勾选要删除的主机")
	}
	return settings, nil
}

func (a *App) activeNodeHosts() ([]string, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	hostIDs := make([]string, 0, len(a.settings.Hosts))
	aliases := make([]string, 0, len(a.settings.Hosts)*2)
	for _, host := range a.settings.Hosts {
		protocol := strings.ToLower(strings.TrimSpace(host.Protocol))
		if host.Deleted || host.Temporary || (protocol != "" && protocol != "ssh") {
			continue
		}
		hostIDs = append(hostIDs, host.ID)
		aliases = append(aliases, host.VPSAlias, host.Name)
	}
	return hostIDs, aliases
}

func (a *App) RestoreHost(id string) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	candidate := cloneSettings(a.settings)
	for i := range candidate.Hosts {
		if candidate.Hosts[i].ID == id {
			candidate.Hosts[i].Deleted = false
			break
		}
	}
	if err := a.commitSettingsLocked(candidate); err != nil {
		return Settings{}, err
	}
	return settingsForClient(a.settings), nil
}

func (a *App) PermanentDeleteHost(id string) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	a.mu.Lock()
	candidate := cloneSettings(a.settings)
	index := -1
	var deletedHost remote.Host
	for i := range candidate.Hosts {
		if candidate.Hosts[i].ID == id {
			index = i
			deletedHost = candidate.Hosts[i]
			break
		}
	}
	if index < 0 {
		a.mu.Unlock()
		return Settings{}, errors.New("未找到主机")
	}
	if !deletedHost.Deleted {
		a.mu.Unlock()
		return Settings{}, errors.New("请先将主机移入已删除")
	}
	candidate.Hosts = slices.Delete(candidate.Hosts, index, index+1)
	if err := a.commitSettingsLocked(candidate); err != nil {
		a.mu.Unlock()
		return Settings{}, err
	}
	detached := a.detachHostRuntimeLocked(id)
	settings := settingsForClient(a.settings)
	nodeDataCleanup := a.nodeDataCleanup
	a.mu.Unlock()

	if nodeDataCleanup != nil {
		if err := nodeDataCleanup(deletedHost.ID, deletedHost.VPSAlias); err != nil {
			slog.Error("永久删除主机时清理本地节点展示失败", "主机ID", id, "VPS别名", deletedHost.VPSAlias, "错误", err)
		}
	}
	a.closeDetachedHostRuntime(id, detached, "主机已永久删除，SSH 连接已断开", "永久删除主机时关闭 RDP 会话失败")
	a.invalidateSSHJumpDependents(id)
	return settings, nil
}

func (a *App) PermanentDeleteHosts(ids []string) (Settings, error) {
	seen := make(map[string]bool, len(ids))
	var settings Settings
	deleted := 0
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		var err error
		settings, err = a.PermanentDeleteHost(id)
		if err != nil {
			return Settings{}, err
		}
		deleted++
	}
	if deleted == 0 {
		return Settings{}, errors.New("请先勾选要永久删除的主机")
	}
	return settings, nil
}

func (a *App) MarkHostUsed(id string) {
	if a.requireUnlocked() != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	candidate := cloneSettings(a.settings)
	for i := range candidate.Hosts {
		if candidate.Hosts[i].ID == id {
			candidate.Hosts[i].LastUsed = time.Now().Format(time.RFC3339)
			_ = a.commitSettingsLocked(candidate)
			return
		}
	}
}

func (a *App) ExportHosts(ids []string) (bool, error) {
	if err := a.requireUnlocked(); err != nil {
		return false, err
	}
	a.mu.RLock()
	protected := a.settings.ProtectExport
	a.mu.RUnlock()
	if protected {
		return false, errors.New("导出主机需要密保验证")
	}
	return a.exportHosts(ids)
}
func (a *App) ExportHostsWithPassword(password string, ids []string) (bool, error) {
	if err := a.requireUnlocked(); err != nil {
		return false, err
	}
	a.mu.Lock()
	protected := a.settings.ProtectExport
	if protected {
		if a.settings.SecurityHash == "" {
			a.mu.Unlock()
			return false, errors.New("密保配置异常，请重新设置密保")
		}
		verified := a.verifySecurityPasswordLocked(password, false)
		if !verified.Success {
			a.mu.Unlock()
			return false, errors.New(verified.Message)
		}
	}
	a.mu.Unlock()
	return a.exportHosts(ids)
}
func (a *App) exportHosts(ids []string) (bool, error) {
	if len(ids) == 0 {
		return false, errors.New("请至少选择一台主机")
	}
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		selected[id] = true
	}
	a.mu.RLock()
	hosts := make([]remote.Host, 0, len(ids))
	for _, host := range a.settings.Hosts {
		if selected[host.ID] {
			host.MFACode = ""
			hosts = append(hosts, host)
		}
	}
	defaultDirectory := a.settings.DownloadDirectory
	a.mu.RUnlock()
	if len(hosts) == 0 {
		return false, errors.New("选择的主机不存在")
	}
	ctx, e := a.runtimeContext()
	if e != nil {
		return false, e
	}
	if info, statErr := os.Stat(defaultDirectory); statErr != nil || !info.IsDir() {
		defaultDirectory = defaultDownloadDirectory()
	}
	directory, e := runtime2.OpenDirectoryDialog(ctx, runtime2.OpenDialogOptions{Title: "选择主机配置导出目录", DefaultDirectory: defaultDirectory})
	if e != nil || directory == "" {
		return false, e
	}
	if e = writeHostExportFiles(directory, hosts); e != nil {
		return false, e
	}
	return true, nil
}

func writeHostExportFiles(directory string, hosts []remote.Host) error {
	usedNames := make(map[string]bool, len(hosts))
	type exportFile struct {
		path string
		raw  []byte
	}
	files := make([]exportFile, 0, len(hosts))
	for _, host := range hosts {
		host.MFACode = ""
		host.RDPGatewayPassword = ""
		baseName := hostExportBaseName(host)
		filePath, pathErr := availableHostExportPath(directory, baseName, usedNames)
		if pathErr != nil {
			return pathErr
		}
		raw, marshalErr := json.MarshalIndent(host, "", "  ")
		if marshalErr != nil {
			return fmt.Errorf("导出 %s 失败: %w", baseName, marshalErr)
		}
		files = append(files, exportFile{path: filePath, raw: append(raw, '\n')})
	}
	created := make([]string, 0, len(files))
	for _, file := range files {
		out, openErr := os.OpenFile(file.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if openErr != nil {
			for _, createdPath := range created {
				_ = os.Remove(createdPath)
			}
			return fmt.Errorf("创建导出文件 %s 失败: %w", filepath.Base(file.path), openErr)
		}
		_, writeErr := out.Write(file.raw)
		closeErr := out.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(file.path)
			for _, createdPath := range created {
				_ = os.Remove(createdPath)
			}
			if writeErr != nil {
				return fmt.Errorf("写入导出文件 %s 失败: %w", filepath.Base(file.path), writeErr)
			}
			return fmt.Errorf("关闭导出文件 %s 失败: %w", filepath.Base(file.path), closeErr)
		}
		created = append(created, file.path)
	}
	return nil
}

func hostExportBaseName(host remote.Host) string {
	name := strings.TrimSpace(host.Name)
	if name == "" {
		name = strings.TrimSpace(host.VPSAlias)
	}
	if name == "" {
		name = strings.TrimSpace(host.Address)
	}
	name = strings.Map(func(value rune) rune {
		if value < 32 || strings.ContainsRune(`\\/:*?"<>|`, value) {
			return '-'
		}
		return value
	}, name)
	name = strings.TrimRight(strings.TrimSpace(name), ". ")
	if runes := []rune(name); len(runes) > 120 {
		name = strings.TrimRight(string(runes[:120]), ". ")
	}
	if name == "" {
		name = "host"
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" ||
		(len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
		name = "_" + name
	}
	return name
}

func availableHostExportPath(directory, baseName string, usedNames map[string]bool) (string, error) {
	for suffix := 1; ; suffix++ {
		name := baseName + ".json"
		if suffix > 1 {
			name = fmt.Sprintf("%s-%d.json", baseName, suffix)
		}
		key := strings.ToLower(name)
		if usedNames[key] {
			continue
		}
		filePath := filepath.Join(directory, name)
		_, err := os.Stat(filePath)
		if err == nil {
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("检查导出文件 %s 失败: %w", name, err)
		}
		usedNames[key] = true
		return filePath, nil
	}
}
func (a *App) ImportHosts() (HostImportResult, error) {
	if err := a.requireUnlocked(); err != nil {
		return HostImportResult{}, err
	}
	ctx, e := a.runtimeContext()
	if e != nil {
		return HostImportResult{}, e
	}
	paths, e := runtime2.OpenMultipleFilesDialog(ctx, runtime2.OpenDialogOptions{Title: "导入 SanTianShell / FinalShell 主机配置", Filters: []runtime2.FileFilter{{DisplayName: "主机配置 JSON", Pattern: "*.json"}}})
	if e != nil || len(paths) == 0 {
		settings, _ := a.GetSettings()
		return HostImportResult{Settings: settingsForClient(settings)}, e
	}
	return a.importHostsFromPaths(paths, false)
}

func (a *App) ImportHostsFromFolder() (HostImportResult, error) {
	if err := a.requireUnlocked(); err != nil {
		return HostImportResult{}, err
	}
	ctx, err := a.runtimeContext()
	if err != nil {
		return HostImportResult{}, err
	}
	directory, err := runtime2.OpenDirectoryDialog(ctx, runtime2.OpenDialogOptions{Title: "选择主机配置文件夹"})
	if err != nil || strings.TrimSpace(directory) == "" {
		settings, _ := a.GetSettings()
		return HostImportResult{Settings: settingsForClient(settings)}, err
	}
	paths, err := collectJSONFiles(directory)
	if err != nil {
		return HostImportResult{}, err
	}
	if len(paths) == 0 {
		return HostImportResult{}, errors.New("所选文件夹中没有 JSON 文件")
	}
	return a.importHostsFromPaths(paths, true)
}

const (
	maxHostImportDirDepth = 16
	maxHostImportFiles    = 500
	maxHostImportFileSize = 4 << 20
)

func collectJSONFiles(directory string) ([]string, error) {
	paths := make([]string, 0)
	if err := collectJSONFilesBounded(directory, 0, &paths); err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func collectJSONFilesBounded(directory string, depth int, paths *[]string) error {
	if depth > maxHostImportDirDepth {
		return fmt.Errorf("导入文件夹层级过深（超过 %d 层）", maxHostImportDirDepth)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("读取文件夹失败: %w", err)
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		if entry.IsDir() {
			if err := collectJSONFilesBounded(path, depth+1, paths); err != nil {
				return err
			}
			continue
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			if len(*paths) >= maxHostImportFiles {
				return fmt.Errorf("一次最多只能从文件夹导入 %d 个 JSON 文件", maxHostImportFiles)
			}
			*paths = append(*paths, path)
		}
	}
	return nil
}

func readBoundedImportFile(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info, err := file.Stat(); err == nil && info.Size() > maxBytes {
		return nil, fmt.Errorf("配置文件 %s 超过 %d MB 大小限制", filepath.Base(path), maxBytes>>20)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("配置文件 %s 超过 %d MB 大小限制", filepath.Base(path), maxBytes>>20)
	}
	return data, nil
}

func (a *App) importHostsFromPaths(paths []string, skipUnsupported bool) (HostImportResult, error) {
	var hosts []remote.Host
	finalShellCount := 0
	unsupportedFiles := 0
	for _, path := range paths {
		raw, readErr := readBoundedImportFile(path, maxHostImportFileSize)
		if readErr != nil {
			if skipUnsupported {
				unsupportedFiles++
				continue
			}
			return HostImportResult{}, readErr
		}
		if remote.IsFinalShellConfig(raw) {
			items, parseErr := remote.ParseFinalShellHosts(raw)
			if parseErr != nil {
				if skipUnsupported {
					unsupportedFiles++
					continue
				}
				return HostImportResult{}, fmt.Errorf("导入 %s 失败: %w", filepath.Base(path), parseErr)
			}
			finalShellCount += len(items)
			hosts = append(hosts, items...)
			continue
		}
		var items []remote.Host
		if json.Unmarshal(raw, &items) != nil {
			var item remote.Host
			if objectErr := json.Unmarshal(raw, &item); objectErr != nil || strings.TrimSpace(item.Address) == "" {
				if skipUnsupported {
					unsupportedFiles++
					continue
				}
				return HostImportResult{}, fmt.Errorf("%s 不是支持的 SanTianShell 或 FinalShell 配置", filepath.Base(path))
			}
			items = []remote.Host{item}
		}
		hosts = append(hosts, items...)
	}
	if len(hosts) == 0 {
		if unsupportedFiles > 0 {
			return HostImportResult{}, fmt.Errorf("文件夹中没有可导入的主机，已忽略 %d 个非主机 JSON 文件", unsupportedFiles)
		}
		return HostImportResult{}, errors.New("配置中没有可导入的主机")
	}
	for _, host := range hosts {
		if host.RDPGatewayPassword != "" {
			return HostImportResult{}, fmt.Errorf("导入主机 %s 失败: RD Gateway 密码不能保存在主机配置中", host.Name)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	candidate := cloneSettings(a.settings)
	existing := make(map[string]bool, len(candidate.Hosts))
	ids := make(map[string]bool, len(candidate.Hosts))
	for _, host := range candidate.Hosts {
		key := strings.ToLower(fmt.Sprintf("%s|%s|%d|%s", host.Protocol, host.Address, host.Port, host.Username))
		existing[key] = true
		ids[host.ID] = true
	}
	imported, skipped := 0, 0
	for i := range hosts {
		hosts[i].MFACode = ""
		hosts[i].Address = strings.TrimSpace(hosts[i].Address)
		hosts[i].Protocol = strings.ToLower(strings.TrimSpace(hosts[i].Protocol))
		if hosts[i].Protocol != "rdp" {
			hosts[i].Protocol = "ssh"
		}
		if hosts[i].Port == 0 {
			if hosts[i].Protocol == "rdp" {
				hosts[i].Port = 3389
			} else {
				hosts[i].Port = 22
			}
		}
		hosts[i].Username = strings.TrimSpace(hosts[i].Username)
		if hosts[i].Username == "" {
			if hosts[i].Protocol == "rdp" {
				hosts[i].Username = "administrator"
			} else {
				hosts[i].Username = "root"
			}
		}
		if optionErr := normalizeRDPHostOptions(&hosts[i]); optionErr != nil {
			return HostImportResult{}, fmt.Errorf("导入主机 %s 失败: %w", hosts[i].Name, optionErr)
		}
		if optionErr := normalizeSSHHostOptions(&hosts[i]); optionErr != nil {
			return HostImportResult{}, fmt.Errorf("导入主机 %s 失败: %w", hosts[i].Name, optionErr)
		}
		if hosts[i].Address == "" || hosts[i].Port < 1 || hosts[i].Port > 65535 {
			skipped++
			continue
		}
		key := strings.ToLower(fmt.Sprintf("%s|%s|%d|%s", hosts[i].Protocol, hosts[i].Address, hosts[i].Port, hosts[i].Username))
		if existing[key] {
			skipped++
			continue
		}
		if hosts[i].ID == "" || ids[hosts[i].ID] {
			hosts[i].ID = strconv.FormatInt(time.Now().UnixNano()+int64(i), 36)
		}
		ids[hosts[i].ID] = true
		existing[key] = true
		candidate.Hosts = append(candidate.Hosts, hosts[i])
		imported++
	}
	if finalShellCount > 0 && !slices.Contains(candidate.Groups, "FinalShell") {
		candidate.Groups = append(candidate.Groups, "FinalShell")
	}
	if e := a.commitSettingsLocked(candidate); e != nil {
		return HostImportResult{}, e
	}
	return HostImportResult{Settings: settingsForClient(a.settings), Imported: imported, Skipped: skipped, IgnoredFiles: unsupportedFiles, FinalShell: finalShellCount}, nil
}

func (a *App) GetOpenSSHConfigHosts() ([]quickconnect.SSHConfigHost, error) {
	if err := a.requireUnlocked(); err != nil {
		return nil, err
	}
	return quickconnect.LoadOpenSSHConfig()
}

func (a *App) CreateQuickHost(targetText, password string) (remote.Host, error) {
	if err := a.requireUnlocked(); err != nil {
		return remote.Host{}, err
	}
	target, err := quickconnect.Parse(targetText)
	if err != nil {
		return remote.Host{}, err
	}
	return a.createTemporaryQuickHost(target, target.Host, password), nil
}

func (a *App) ParseQuickConnectCandidates(input string) ([]quickconnect.Candidate, error) {
	if err := a.requireUnlocked(); err != nil {
		return nil, err
	}
	return quickconnect.ParseCandidates(input)
}

func (a *App) CreateQuickHostCandidate(input, candidateID string) (remote.Host, error) {
	if err := a.requireUnlocked(); err != nil {
		return remote.Host{}, err
	}
	candidate, err := quickconnect.SelectCandidate(input, strings.TrimSpace(candidateID))
	if err != nil {
		return remote.Host{}, err
	}
	target := quickconnect.Target{
		Scheme: candidate.Scheme,
		User:   candidate.User,
		Host:   candidate.Host,
		Port:   candidate.Port,
	}
	host := a.createTemporaryQuickHost(target, candidate.Name, candidate.Password)
	host.Password = ""
	return host, nil
}

func (a *App) createTemporaryQuickHost(target quickconnect.Target, name, password string) remote.Host {
	if strings.TrimSpace(name) == "" {
		name = target.Host
	}
	host := remote.Host{
		Name: name, Address: target.Host, Port: target.Port, Username: target.User,
		Protocol: target.Scheme, Timeout: 12, KeepAlive: 30, Encoding: "UTF-8", Temporary: true,
	}
	if host.Protocol == quickconnect.SchemeRDP {
		if host.Username == "" {
			host.Username = "administrator"
		}
		host.AuthType = "password"
		host.Password = password
		host.RDPMapDrives = true
		host.RDPResolution = "1280x1024"
		host.RDPDisplayMode = string(rdp.DisplayModeWindow)
		clipboard := true
		host.RDPRedirectClipboard = &clipboard
	} else {
		if host.Username == "" {
			host.Username = "root"
		}
		if password == "" {
			host.AuthType = "agent"
		} else {
			host.AuthType = "password"
			host.Password = password
		}
	}
	return a.registerTemporaryHost(host)
}

func (a *App) CreateQuickHostFromSSHConfig(alias string) (remote.Host, error) {
	if err := a.requireUnlocked(); err != nil {
		return remote.Host{}, err
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return remote.Host{}, errors.New("请选择 OpenSSH 配置主机")
	}
	hosts, err := quickconnect.LoadOpenSSHConfig()
	if err != nil {
		return remote.Host{}, err
	}
	for _, item := range hosts {
		if item.Alias != alias {
			continue
		}
		host := remote.Host{
			Name: item.Alias, Address: item.HostName, Port: item.Port, Username: item.User,
			Protocol: "ssh", Timeout: 12, KeepAlive: 30, Encoding: "UTF-8", Temporary: true,
		}
		if host.Address == "" {
			host.Address = item.Alias
		}
		if host.Port == 0 {
			host.Port = 22
		}
		if host.Username == "" {
			host.Username = "root"
		}
		if item.IdentityFile != "" {
			host.AuthType = "key"
			host.KeyPath = expandSSHIdentityPath(item.IdentityFile)
		} else {
			host.AuthType = "agent"
		}
		return a.registerTemporaryHost(host), nil
	}
	return remote.Host{}, errors.New("OpenSSH 配置主机不存在或已经变化")
}

func expandSSHIdentityPath(value string) string {
	value = os.ExpandEnv(strings.TrimSpace(value))
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			if value == "~" {
				return home
			}
			return filepath.Join(home, value[2:])
		}
	}
	return value
}

func (a *App) registerTemporaryHost(host remote.Host) remote.Host {
	a.mu.Lock()
	defer a.mu.Unlock()
	base := time.Now().UnixNano()
	for suffix := int64(0); ; suffix++ {
		host.ID = "quick-" + strconv.FormatInt(base+suffix, 36)
		if _, exists := a.temporaryHosts[host.ID]; !exists {
			break
		}
	}
	a.temporaryHosts[host.ID] = host
	return host
}

func (a *App) SaveConnectedTemporaryHost(id string) (Settings, error) {
	id = strings.TrimSpace(id)
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.unlocked {
		return Settings{}, errors.New("软件已锁定，请先输入密保")
	}
	host, exists := a.temporaryHosts[id]
	connected := a.shells[id] != nil
	password, hasTransientPassword := a.transientPasswords[id]
	if !exists {
		return Settings{}, errors.New("临时主机不存在或已经保存")
	}
	if !strings.EqualFold(host.Protocol, "ssh") {
		return Settings{}, errors.New("连接成功后自动保存目前仅支持 SSH")
	}
	if !connected {
		return Settings{}, errors.New("SSH 尚未连接成功，未保存主机")
	}
	if hasTransientPassword && ordinaryPasswordHost(host) {
		host.Password = password
		host.AuthType = "password"
	}
	if strings.TrimSpace(host.Name) == "" || strings.TrimSpace(host.Address) == "" || strings.TrimSpace(host.Username) == "" {
		return Settings{}, errors.New("名称、地址和用户名不能为空")
	}
	if err := normalizeSOCKS5(&host); err != nil {
		return Settings{}, err
	}
	host.MFACode = ""
	host.Temporary = false
	if host.Port == 0 {
		host.Port = 22
	}
	if host.AuthType == "" {
		host.AuthType = "password"
	}
	if host.Timeout <= 0 {
		host.Timeout = 12
	}
	if host.KeepAlive <= 0 {
		host.KeepAlive = 30
	}
	if host.Encoding == "" {
		host.Encoding = "UTF-8"
	}
	host.LastUsed = time.Now().Format(time.RFC3339)

	newSettings := cloneSettings(a.settings)
	savedIndex := -1
	for i := range newSettings.Hosts {
		if newSettings.Hosts[i].ID != id {
			continue
		}
		previous := newSettings.Hosts[i]
		host.Country = previous.Country
		host.CountryManual = previous.CountryManual
		host.LastUsed = previous.LastUsed
		host.SystemVersion = previous.SystemVersion
		host.Deleted = previous.Deleted
		if strings.TrimSpace(host.Tags) == "" {
			host.Tags = previous.Tags
		}
		if host.Address == previous.Address && equalSSHPort(host.Port, previous.Port) {
			host.HostKey = previous.HostKey
		} else {
			host.HostKey = ""
		}
		if host.VPSAlias == "" {
			host.VPSAlias = previous.VPSAlias
		}
		newSettings.Hosts[i] = host
		savedIndex = i
		break
	}
	if savedIndex < 0 {
		newSettings.Hosts = append(newSettings.Hosts, host)
		savedIndex = len(newSettings.Hosts) - 1
	}
	if err := a.commitSettingsLocked(newSettings); err != nil {
		return Settings{}, err
	}

	delete(a.temporaryHosts, id)
	delete(a.transientPasswords, id)
	if a.shells[id] != nil {
		if a.shellHostRevisions == nil {
			a.shellHostRevisions = make(map[string]string)
		}
		a.shellHostRevisions[id] = aiHostRevision(a.resolveStoredCredential(a.settings.Hosts[savedIndex]))
	}
	return settingsForClient(a.settings), nil
}

type hostRuntimeDetach struct {
	shell       *remote.Shell
	clientEpoch int
	rdpSessions []*rdp.Session
}

func (a *App) detachHostRuntimeLocked(id string) hostRuntimeDetach {
	if a.shellEpochs == nil {
		a.shellEpochs = make(map[string]uint64)
	}
	a.shellEpochs[id]++
	detached := hostRuntimeDetach{
		shell:       a.shells[id],
		clientEpoch: a.shellClientEpochs[id],
	}
	delete(a.shells, id)
	delete(a.shellClientEpochs, id)
	delete(a.shellHostRevisions, id)
	delete(a.transientPasswords, id)
	delete(a.hostOSReleases, id)
	for sessionID, hostID := range a.rdpSessionHosts {
		if hostID != id && a.rdpSessionJumps[sessionID] != id {
			continue
		}
		if session := a.rdpSessions[sessionID]; session != nil {
			detached.rdpSessions = append(detached.rdpSessions, session)
			if a.rdpSessionClosing == nil {
				a.rdpSessionClosing = make(map[string]struct{})
			}
			a.rdpSessionClosing[sessionID] = struct{}{}
		}
		delete(a.rdpSessions, sessionID)
		delete(a.rdpSessionHosts, sessionID)
		delete(a.rdpSessionJumps, sessionID)
	}
	return detached
}

func (a *App) closeDetachedHostRuntime(id string, detached hostRuntimeDetach, terminalMessage, rdpContext string) {
	if _, err := a.disableSSHSessionLog(id); err != nil {
		slog.Warn("关闭 SSH 会话日志失败", "主机ID", id, "错误", err)
	}
	if detached.shell != nil {
		detached.shell.Close()
		if a.ctx != nil && terminalMessage != "" {
			runtime2.EventsEmit(a.ctx, "terminal-closed", map[string]any{"id": id, "epoch": detached.clientEpoch, "message": terminalMessage})
		}
	}
	for _, session := range detached.rdpSessions {
		if closeErr := session.Close(); closeErr != nil {
			slog.Error(rdpContext, "主机ID", id, "会话ID", session.ID, "错误", closeErr)
			a.mu.Lock()
			if a.rdpSessions[session.ID] == nil {
				delete(a.rdpSessionClosing, session.ID)
			}
			a.mu.Unlock()
		}
	}
	a.stopPortForwardsForHost(id)
	if a.connections != nil {
		a.connections.Invalidate(id)
	}
}

func (a *App) ReleaseTemporaryHost(id string) {
	if a.requireUnlocked() != nil {
		return
	}
	a.mu.Lock()
	if _, isTemporary := a.temporaryHosts[id]; !isTemporary {
		a.mu.Unlock()
		return
	}
	delete(a.temporaryHosts, id)
	detached := a.detachHostRuntimeLocked(id)
	a.mu.Unlock()
	a.closeDetachedHostRuntime(id, detached, "", "释放临时主机时关闭 RDP 会话失败")
}

func (a *App) TestHost(id string) (remote.Result, error) {
	if err := a.requireUnlocked(); err != nil {
		return remote.Result{}, err
	}
	result, err := a.RunCommand(id, "printf '连接成功：'; hostname; printf '\n系统：'; uname -sr")
	if err == nil && result.ExitCode == 0 {
		a.mu.Lock()
		candidate := cloneSettings(a.settings)
		updated := false
		for i := range candidate.Hosts {
			if candidate.Hosts[i].ID == id {
				candidate.Hosts[i].LastUsed = time.Now().Format(time.RFC3339)
				if parts := strings.SplitN(result.Output, "系统：", 2); len(parts) == 2 {
					candidate.Hosts[i].SystemVersion = strings.TrimSpace(parts[1])
				}
				updated = true
				break
			}
		}
		if updated {
			if saveErr := a.commitSettingsLocked(candidate); saveErr != nil {
				slog.Warn("保存主机测试结果失败", "主机ID", id, "错误", saveErr)
			}
		}
		a.mu.Unlock()
	}
	return result, err
}

func (a *App) RunCommand(id, command string) (remote.Result, error) {
	if err := a.requireUnlocked(); err != nil {
		return remote.Result{}, err
	}
	if strings.TrimSpace(command) == "" {
		return remote.Result{}, errors.New("命令不能为空")
	}
	a.mu.RLock()
	shell := a.shells[id]
	a.mu.RUnlock()
	if shell != nil {
		return shell.Run(command)
	}
	h, e := a.host(id)
	if e != nil {
		return remote.Result{}, e
	}
	client, release, e := a.connections.AcquireContext(a.lifecycleContext(), id, h)
	if e != nil {
		return remote.Result{}, e
	}
	defer release()
	return remote.RunWithClient(client, command)
}

package core

import (
	"archive/zip"
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
)

var backupFileNamePattern = regexp.MustCompile(`^(.*)_(\d{8}_\d{6}_all\.json)$`)

type AddNodeRequest struct {
	VPSAlias         string          `json:"vpsAlias"`
	GroupName        string          `json:"groupName"`
	InboundProtocol  string          `json:"inboundProtocol"`
	OutboundProtocol string          `json:"outboundProtocol"`
	StartPort        int             `json:"startPort"`
	Count            int             `json:"count"`
	TLSEnabled       bool            `json:"tlsEnabled"`
	RealityEnabled   bool            `json:"realityEnabled"`
	RealitySNI       string          `json:"realitySni"`
	OutboundLines    string          `json:"outboundLines"`
	OutboundDetails  map[string]any  `json:"outboundDetails"`
	RawConfigs       json.RawMessage `json:"rawConfigs"`
}

type AddNodeGroupRequest struct {
	GroupName     string `json:"groupName"`
	OutboundLines string `json:"outboundLines"`
	Count         int    `json:"count"`
}

type BatchAddNodeRequest struct {
	VPSAlias         string                `json:"vpsAlias"`
	InboundProtocol  string                `json:"inboundProtocol"`
	OutboundProtocol string                `json:"outboundProtocol"`
	StartPort        int                   `json:"startPort"`
	TLSEnabled       bool                  `json:"tlsEnabled"`
	RealityEnabled   bool                  `json:"realityEnabled"`
	RealitySNI       string                `json:"realitySni"`
	Groups           []AddNodeGroupRequest `json:"groups"`
}

type MultiIPNodeRequest struct {
	VPSAlias        string   `json:"vpsAlias"`
	GroupName       string   `json:"groupName"`
	InboundProtocol string   `json:"inboundProtocol"`
	StartPort       int      `json:"startPort"`
	IncludeIPv6     bool     `json:"includeIpv6,omitempty"`
	RealitySNI      string   `json:"realitySni"`
	IPs             []string `json:"ips"`
}

type VPSIPList struct {
	Alias            string   `json:"alias"`
	IPs              []string `json:"ips"`
	PrivateIPs       []string `json:"private_ips,omitempty"`
	Count            int      `json:"count"`
	TotalCount       int      `json:"total_count"`
	CreatedIPCount   int      `json:"created_ip_count"`
	DefaultStartPort int      `json:"default_start_port"`
}

type ModifyOutboundChange struct {
	FileName    string `json:"fileName"`
	Tag         string `json:"tag"`
	Port        int    `json:"port"`
	OldOutbound string `json:"oldOutbound"`
	NewOutbound string `json:"newOutbound"`
}

type ModifyOutboundPreview struct {
	Success   bool                   `json:"success"`
	Message   string                 `json:"message"`
	Changes   []ModifyOutboundChange `json:"changes"`
	Unmatched []string               `json:"unmatched,omitempty"`
}

type BackupDiffPreview struct {
	Success        bool     `json:"success"`
	Message        string   `json:"message"`
	SourceAlias    string   `json:"sourceAlias"`
	TargetAlias    string   `json:"targetAlias"`
	BackupCount    int      `json:"backupCount"`
	CurrentCount   int      `json:"currentCount"`
	Added          []string `json:"added"`
	Removed        []string `json:"removed"`
	Modified       []string `json:"modified"`
	BindIPWarnings []string `json:"bindIpWarnings,omitempty"`
}

type NodeDefaults struct {
	DefaultStartPort int   `json:"default_start_port"`
	UsedPorts        []int `json:"used_ports"`
	UsedPortCount    int   `json:"used_port_count"`
}

type ExportFile struct {
	FileName string `json:"fileName"`
	MimeType string `json:"mimeType"`
	Base64   string `json:"base64"`
}

type ExportBytes struct {
	FileName string
	MimeType string
	Data     []byte
}

type excelNodeItem struct {
	filename string
	config   map[string]any
	port     int
}

type QRCodeResult struct {
	Link  string `json:"link"`
	Image string `json:"image"`
}

type OperationResult struct {
	Success bool     `json:"success"`
	Message string   `json:"message"`
	Count   int      `json:"count"`
	Items   []string `json:"items"`
}

// allocateSequentialPorts 优先按当前最大端口顺延分配；仅当尾部顺序空间不足（超过 65535）时，
// 才回绕从 defaultStart（默认 20000）起顺查空闲端口（优先连续段，碎片化时按升序复用空闲端口）。
func allocateSequentialPorts(configs map[string]map[string]any, requestedStart int, count int, defaultStart int) ([]int, error) {
	if count <= 0 {
		return nil, errors.New("节点数量必须大于 0")
	}
	if defaultStart <= 0 {
		defaultStart = 20000
	}
	if requestedStart > 0 {
		if requestedStart > 65535 || requestedStart+count-1 > 65535 {
			return nil, errors.New("端口范围超出限制")
		}
		if conflicts := usedPortsInRange(configs, requestedStart, count); len(conflicts) > 0 {
			return nil, fmt.Errorf("端口已被占用: %v", conflicts)
		}
		ports := make([]int, count)
		for i := 0; i < count; i++ {
			ports[i] = requestedStart + i
		}
		return ports, nil
	}

	used := map[int]bool{}
	maxPort := 0
	for _, cfg := range configs {
		if p := extractListenPort(cfg); p > 0 {
			used[p] = true
			if p > maxPort {
				maxPort = p
			}
		}
	}

	// 1. 优先从最大已用端口之后顺延分配
	if candidate := nextStartPort(maxPort, defaultStart); candidate > 0 && candidate+count-1 <= 65535 {
		ports := make([]int, count)
		for i := 0; i < count; i++ {
			ports[i] = candidate + i
		}
		return ports, nil
	}

	// 2. 尾部已满，回绕到最初（defaultStart）优先找连续空闲段
	for port := defaultStart; port <= 65535-count+1; port++ {
		ok := true
		for i := 0; i < count; i++ {
			if used[port+i] {
				ok = false
				break
			}
		}
		if ok {
			ports := make([]int, count)
			for i := 0; i < count; i++ {
				ports[i] = port + i
			}
			return ports, nil
		}
	}

	// 3. 若无连续空闲段，则从 defaultStart 起按升序回收零散空闲端口
	ports := make([]int, 0, count)
	for port := defaultStart; port <= 65535 && len(ports) < count; port++ {
		if !used[port] {
			ports = append(ports, port)
		}
	}
	if len(ports) < count {
		return nil, errors.New("端口范围超出限制，无足够空闲端口")
	}
	return ports, nil
}

func deduplicateOutbounds(outbounds []map[string]any) []map[string]any {
	if len(outbounds) <= 1 {
		return outbounds
	}
	seen := make(map[string]bool, len(outbounds))
	unique := make([]map[string]any, 0, len(outbounds))
	for _, ob := range outbounds {
		key, _ := json.Marshal(ob)
		ks := string(key)
		if seen[ks] {
			continue
		}
		seen[ks] = true
		unique = append(unique, ob)
	}
	return unique
}

func (s *Service) AddNodes(req AddNodeRequest) (OperationResult, error) {
	req.VPSAlias = strings.TrimSpace(req.VPSAlias)
	req.GroupName = strings.TrimSpace(req.GroupName)
	if req.VPSAlias == "" {
		return OperationResult{Success: false, Message: "VPS 不能为空"}, nil
	}
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.InboundProtocol == "" {
		req.InboundProtocol = "vless"
	}
	if req.OutboundProtocol == "" {
		req.OutboundProtocol = "socks"
	}
	vps, err := s.getVPS(req.VPSAlias)
	if err != nil {
		return OperationResult{Success: false, Message: err.Error()}, nil
	}
	unlock := s.lockNodeConfigAliases(req.VPSAlias)
	defer unlock()
	fallbackName := defaultNodeNameBase(req.VPSAlias, vps)
	configs, err := s.loadNodeConfigMap(req.VPSAlias)
	if err != nil {
		return OperationResult{}, err
	}
	assignedPorts, err := allocateSequentialPorts(configs, req.StartPort, req.Count, 20000)
	if err != nil {
		return OperationResult{Success: false, Message: err.Error()}, nil
	}
	outbounds := deduplicateOutbounds(parseOutboundLines(req.OutboundLines, req.OutboundProtocol))
	if len(outbounds) == 0 && req.OutboundDetails != nil {
		outbounds = append(outbounds, req.OutboundDetails)
	}
	if len(outbounds) == 0 {
		outbounds = append(outbounds, defaultOutbound(req.OutboundProtocol))
	}

	created := []string{}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < req.Count; i++ {
		outbound := cloneMap(outbounds[i%len(outbounds)])
		password := randomUUID()
		port := assignedPorts[i]
		groupName := resolvedNodeGroupName(req.GroupName, outbound, fallbackName)
		config := generateConfig(groupName, req.InboundProtocol, port, password, req.OutboundProtocol, outbound, req.TLSEnabled, req.RealityEnabled, req.RealitySNI)
		tag := extractNodeTag(config)
		fileName := strings.ToLower(safeFileName(tag)) + ".json"
		if _, exists := configs[fileName]; exists {
			return OperationResult{Success: false, Message: fmt.Sprintf("节点名称已存在: %s", tag)}, nil
		}
		setNodeMetadata(config, createdAt)
		explicitGroup := strings.TrimSpace(req.GroupName)
		if explicitGroup == "" {
			explicitGroup = extractGroupFromTag(tag, createdAt)
		}
		setNodeGroupMetadata(config, explicitGroup)
		configs[fileName] = config
		created = append(created, fileName)
	}
	if err := s.saveNodeConfigMap(req.VPSAlias, configs); err != nil {
		return OperationResult{}, err
	}
	return OperationResult{Success: true, Message: "节点已添加", Count: len(created), Items: created}, nil
}

func (s *Service) BatchAddNodes(req BatchAddNodeRequest) (OperationResult, error) {
	req.VPSAlias = strings.TrimSpace(req.VPSAlias)
	if req.VPSAlias == "" {
		return OperationResult{Success: false, Message: "VPS 不能为空"}, nil
	}
	if req.InboundProtocol == "" {
		req.InboundProtocol = "vless"
	}
	if req.OutboundProtocol == "" {
		req.OutboundProtocol = "socks"
	}
	vps, err := s.getVPS(req.VPSAlias)
	if err != nil {
		return OperationResult{Success: false, Message: err.Error()}, nil
	}
	unlock := s.lockNodeConfigAliases(req.VPSAlias)
	defer unlock()
	fallbackName := defaultNodeNameBase(req.VPSAlias, vps)
	configs, err := s.loadNodeConfigMap(req.VPSAlias)
	if err != nil {
		return OperationResult{}, err
	}
	totalCount := 0
	for i := range req.Groups {
		req.Groups[i].GroupName = strings.TrimSpace(req.Groups[i].GroupName)
		if req.Groups[i].Count <= 0 {
			req.Groups[i].Count = 1
		}
		totalCount += req.Groups[i].Count
	}
	if totalCount <= 0 {
		return OperationResult{Success: false, Message: "没有需要添加的节点"}, nil
	}
	assignedPorts, err := allocateSequentialPorts(configs, req.StartPort, totalCount, 20000)
	if err != nil {
		return OperationResult{Success: false, Message: err.Error()}, nil
	}
	created := []string{}
	portIndex := 0
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	pending := map[string]bool{}
	for _, group := range req.Groups {
		outbounds := deduplicateOutbounds(parseOutboundLines(group.OutboundLines, req.OutboundProtocol))
		if len(outbounds) == 0 {
			outbounds = append(outbounds, defaultOutbound(req.OutboundProtocol))
		}
		for i := 0; i < group.Count; i++ {
			outbound := cloneMap(outbounds[i%len(outbounds)])
			groupName := resolvedNodeGroupName(group.GroupName, outbound, fallbackName)
			config := generateConfig(groupName, req.InboundProtocol, assignedPorts[portIndex], randomUUID(), req.OutboundProtocol, outbound, req.TLSEnabled, req.RealityEnabled, req.RealitySNI)
			tag := extractNodeTag(config)
			fileName := strings.ToLower(safeFileName(tag)) + ".json"
			if _, exists := configs[fileName]; exists || pending[fileName] {
				return OperationResult{Success: false, Message: fmt.Sprintf("节点名称已存在: %s", tag)}, nil
			}
			pending[fileName] = true
			setNodeMetadata(config, createdAt)
			explicitGroup := strings.TrimSpace(group.GroupName)
			if explicitGroup == "" {
				explicitGroup = extractGroupFromTag(tag, createdAt)
			}
			setNodeGroupMetadata(config, explicitGroup)
			configs[fileName] = config
			created = append(created, fileName)
			portIndex++
		}
	}
	if err := s.saveNodeConfigMap(req.VPSAlias, configs); err != nil {
		return OperationResult{}, err
	}
	return OperationResult{Success: true, Message: "节点已添加", Count: len(created), Items: created}, nil
}

func (s *Service) DetectVPSIPs(alias string) (VPSIPList, error) {
	ips, err := s.detectVPSIPsWithOptions(alias, true)
	if err != nil {
		return VPSIPList{}, err
	}
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return VPSIPList{}, err
	}
	available := uncreatedMultiIPs(ips, configs)
	privateIPs := []string{}
	for _, ipStr := range available {
		if parsed := net.ParseIP(ipStr); parsed != nil && parsed.IsPrivate() {
			privateIPs = append(privateIPs, ipStr)
		}
	}
	createdCount := len(cleanMultiIPList(ips)) - len(available)
	if createdCount < 0 {
		createdCount = 0
	}
	start := firstFreePort(configs, len(available), 20000)
	if start <= 0 {
		start = nextStartPort(maxListenPort(configs), 20000)
	}
	return VPSIPList{Alias: alias, IPs: available, PrivateIPs: privateIPs, Count: len(available), TotalCount: len(cleanMultiIPList(ips)), CreatedIPCount: createdCount, DefaultStartPort: start}, nil
}

func (s *Service) AddMultiIPNodes(req MultiIPNodeRequest) (OperationResult, error) {
	req.VPSAlias = strings.TrimSpace(req.VPSAlias)
	req.GroupName = strings.TrimSpace(req.GroupName)
	if req.VPSAlias == "" {
		return OperationResult{Success: false, Message: "VPS 不能为空"}, nil
	}
	req.InboundProtocol = strings.TrimSpace(req.InboundProtocol)
	if req.InboundProtocol == "" {
		req.InboundProtocol = "vless"
	}
	realityEnabled := req.InboundProtocol == "vless-reality"
	inboundProtocol := req.InboundProtocol
	if realityEnabled {
		inboundProtocol = "vless"
	}
	ips := cleanMultiIPList(req.IPs)
	if len(ips) == 0 {
		detectedIPs, err := s.detectVPSIPsWithOptions(req.VPSAlias, req.IncludeIPv6)
		if err != nil {
			return OperationResult{}, err
		}
		ips = cleanMultiIPList(detectedIPs)
	}
	if len(ips) == 0 {
		return OperationResult{Success: false, Message: "未检测到可用公网 IP"}, nil
	}
	unlock := s.lockNodeConfigAliases(req.VPSAlias)
	defer unlock()
	configs, err := s.loadNodeConfigMap(req.VPSAlias)
	if err != nil {
		return OperationResult{}, err
	}
	ips = uncreatedMultiIPs(ips, configs)
	if len(ips) == 0 {
		return OperationResult{Success: false, Message: "没有检测到未创建协议的 IP"}, nil
	}
	explicitStartPort := req.StartPort > 0
	if req.StartPort <= 0 {
		req.StartPort = firstFreePort(configs, len(ips), 20000)
	}
	if conflicts := usedPortsInRange(configs, req.StartPort, len(ips)); len(conflicts) > 0 {
		if explicitStartPort {
			suggested := firstFreePort(configs, len(ips), 20000)
			return OperationResult{Success: false, Message: fmt.Sprintf("起始端口 %d 存在冲突端口 %v，建议改用起始端口 %d", req.StartPort, conflicts, suggested)}, nil
		}
		req.StartPort = firstFreePort(configs, len(ips), 20000)
	}
	if req.StartPort <= 0 || req.StartPort > 65535 || req.StartPort+len(ips)-1 > 65535 {
		return OperationResult{Success: false, Message: "端口范围超出限制"}, nil
	}
	if conflicts := usedPortsInRange(configs, req.StartPort, len(ips)); len(conflicts) > 0 {
		return OperationResult{Success: false, Message: fmt.Sprintf("端口已被占用: %v", conflicts)}, nil
	}
	pending := make(map[string]bool, len(ips))
	for _, ip := range ips {
		tag := multiIPNodeTag(req.GroupName, ip, req.InboundProtocol)
		fileName := strings.ToLower(safeFileName(tag)) + ".json"
		if _, exists := configs[fileName]; exists || pending[fileName] {
			return OperationResult{Success: false, Message: fmt.Sprintf("节点名称已存在: %s", tag)}, nil
		}
		pending[fileName] = true
	}
	created := make([]string, 0, len(ips))
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	for i, ip := range ips {
		port := req.StartPort + i
		tag := multiIPNodeTag(req.GroupName, ip, req.InboundProtocol)
		config := generateConfigWithTag(tag, inboundProtocol, ip, port, randomUUID(), "direct", directOutboundForIP(ip), true, realityEnabled, req.RealitySNI)
		fileName := strings.ToLower(safeFileName(tag)) + ".json"
		setNodeMetadata(config, createdAt)
		setNodeGroupMetadata(config, req.GroupName)
		configs[fileName] = config
		created = append(created, fileName)
	}
	if err := s.saveNodeConfigMap(req.VPSAlias, configs); err != nil {
		return OperationResult{}, err
	}
	return OperationResult{Success: true, Message: fmt.Sprintf("多IP节点已添加：%d 个", len(created)), Count: len(created), Items: created}, nil
}

func (s *Service) DeleteNode(alias string, filename string) (OperationResult, error) {
	unlock := s.lockNodeConfigAliases(alias)
	defer unlock()
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return OperationResult{}, err
	}
	if _, ok := configs[filename]; !ok {
		return OperationResult{Success: false, Message: "节点不存在"}, nil
	}
	if err := s.createDeletedRecord(alias, filename, configs[filename]); err != nil {
		return OperationResult{Success: false, Message: "保存删除记录失败"}, fmt.Errorf("保存删除记录失败: %w", err)
	}
	delete(configs, filename)
	if err := s.saveNodeConfigMap(alias, configs); err != nil {
		return OperationResult{}, err
	}
	return OperationResult{Success: true, Message: "节点已删除", Count: 1, Items: []string{filename}}, nil
}

func (s *Service) GetNodeDefaults(alias string, count int) (NodeDefaults, error) {
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return NodeDefaults{}, err
	}
	if count <= 0 {
		count = 1
	}
	ports := usedPortsFromConfigs(configs)
	start := firstFreePort(configs, count, 20000)
	if start <= 0 {
		start = 20000
	}
	return NodeDefaults{DefaultStartPort: start, UsedPorts: ports, UsedPortCount: len(ports)}, nil
}

func (s *Service) BatchDeleteNodes(alias string, filenames []string) (OperationResult, error) {
	unlock := s.lockNodeConfigAliases(alias)
	defer unlock()
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return OperationResult{}, err
	}
	deleted := []string{}
	deletedConfigs := map[string]map[string]any{}
	for _, filename := range filenames {
		if config, ok := configs[filename]; ok {
			deletedConfigs[filename] = config
			delete(configs, filename)
			deleted = append(deleted, filename)
		}
	}
	if err := s.createDeletedRecords(alias, deletedConfigs); err != nil {
		return OperationResult{}, err
	}
	if err := s.saveNodeConfigMap(alias, configs); err != nil {
		return OperationResult{}, err
	}
	return OperationResult{Success: true, Message: "批量删除完成", Count: len(deleted), Items: deleted}, nil
}

func (s *Service) ForceDeleteNodes(alias string, filenames []string) (OperationResult, error) {
	return s.BatchDeleteNodes(alias, filenames)
}

func (s *Service) DeleteNodeGroup(alias string, group string) (OperationResult, error) {
	unlock := s.lockNodeConfigAliases(alias)
	defer unlock()
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return OperationResult{}, err
	}
	targetGroup := resolveTargetGroupName(configs, group)
	deleted := []string{}
	deletedConfigs := map[string]map[string]any{}
	for filename, config := range configs {
		if nodeMatchesGroup(config, targetGroup) {
			deletedConfigs[filename] = config
			delete(configs, filename)
			deleted = append(deleted, filename)
		}
	}
	if len(deleted) == 0 {
		return OperationResult{Success: false, Message: fmt.Sprintf("未找到节点组 %s 的节点", group)}, nil
	}
	if err := s.createDeletedRecords(alias, deletedConfigs); err != nil {
		return OperationResult{}, err
	}
	if err := s.saveNodeConfigMap(alias, configs); err != nil {
		return OperationResult{}, err
	}
	return OperationResult{Success: true, Message: fmt.Sprintf("节点组 %s 已删除，共 %d 个节点", targetGroup, len(deleted)), Count: len(deleted), Items: deleted}, nil
}

type outboundMappingEntry struct {
	MatchIP  string
	Outbound map[string]any
}

func parseOutboundMappingLines(text string, protocol string) []outboundMappingEntry {
	entries := []outboundMappingEntry{}
	for _, rawLine := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		matchIP := ""
		outboundText := line
		if idx := strings.Index(line, "=>"); idx > 0 {
			matchIP = normalizeServerAddress(strings.TrimSpace(line[:idx]))
			outboundText = strings.TrimSpace(line[idx+2:])
		}
		parsed := parseOutboundLines(outboundText, protocol)
		for _, ob := range parsed {
			targetMatch := matchIP
			if targetMatch == "" {
				targetMatch = normalizeServerAddress(asString(ob["server"]))
			}
			entries = append(entries, outboundMappingEntry{MatchIP: targetMatch, Outbound: ob})
		}
	}
	return entries
}

func formatOutboundBrief(outbound map[string]any) string {
	if outbound == nil {
		return "-"
	}
	server := asString(outbound["server"])
	port := asInt(outbound["server_port"])
	user := asString(outbound["username"])
	if server == "" {
		return asString(outbound["type"])
	}
	if user != "" {
		return fmt.Sprintf("%s:%d (%s)", server, port, user)
	}
	return fmt.Sprintf("%s:%d", server, port)
}

func (s *Service) PreviewModifyNodeOutbounds(alias string, filenames []string, outboundLines string, outboundProtocol string) (ModifyOutboundPreview, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return ModifyOutboundPreview{Success: false, Message: "VPS 不能为空"}, nil
	}
	entries := parseOutboundMappingLines(outboundLines, outboundProtocol)
	if len(entries) == 0 {
		return ModifyOutboundPreview{Success: false, Message: "落地代理为空或格式错误"}, nil
	}
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return ModifyOutboundPreview{}, err
	}
	if len(configs) == 0 {
		return ModifyOutboundPreview{Success: false, Message: "该 VPS 暂无可修改的节点"}, nil
	}
	target := map[string]bool{}
	for _, fn := range filenames {
		fn = strings.TrimSpace(fn)
		if fn != "" {
			target[fn] = true
		}
	}
	changes := []ModifyOutboundChange{}
	unmatched := []string{}
	sortedFilenames := make([]string, 0, len(configs))
	for fn := range configs {
		if len(target) > 0 && !target[fn] {
			continue
		}
		sortedFilenames = append(sortedFilenames, fn)
	}
	sort.Slice(sortedFilenames, func(i, j int) bool {
		pi, pj := extractListenPort(configs[sortedFilenames[i]]), extractListenPort(configs[sortedFilenames[j]])
		if pi != pj {
			return pi < pj
		}
		return sortedFilenames[i] < sortedFilenames[j]
	})
	if len(target) > 0 {
		for i, fn := range sortedFilenames {
			cfg := configs[fn]
			idx := findEditableOutboundIndex(cfg)
			var oldOb map[string]any
			if outs, ok := cfg["outbounds"].([]any); ok && idx >= 0 && idx < len(outs) {
				oldOb, _ = outs[idx].(map[string]any)
			}
			newOb := entries[i%len(entries)].Outbound
			changes = append(changes, ModifyOutboundChange{
				FileName: fn, Tag: extractNodeTag(cfg), Port: extractListenPort(cfg),
				OldOutbound: formatOutboundBrief(oldOb), NewOutbound: formatOutboundBrief(newOb),
			})
		}
	} else {
		for _, entry := range entries {
			if entry.MatchIP == "" {
				continue
			}
			matched := 0
			for _, fn := range sortedFilenames {
				cfg := configs[fn]
				idx := findMatchingOutboundIndex(cfg, entry.MatchIP)
				if idx < 0 {
					continue
				}
				var oldOb map[string]any
				if outs, ok := cfg["outbounds"].([]any); ok && idx < len(outs) {
					oldOb, _ = outs[idx].(map[string]any)
				}
				changes = append(changes, ModifyOutboundChange{
					FileName: fn, Tag: extractNodeTag(cfg), Port: extractListenPort(cfg),
					OldOutbound: formatOutboundBrief(oldOb), NewOutbound: formatOutboundBrief(entry.Outbound),
				})
				matched++
			}
			if matched == 0 {
				unmatched = append(unmatched, entry.MatchIP)
			}
		}
	}
	if len(changes) == 0 {
		return ModifyOutboundPreview{Success: false, Message: "未匹配到可修改的节点", Unmatched: unmatched}, nil
	}
	msg := fmt.Sprintf("预览完成：预计修改 %d 个节点", len(changes))
	if len(unmatched) > 0 {
		msg += fmt.Sprintf("，未匹配 %d 个目标 IP（%s）", len(unmatched), strings.Join(unmatched, ", "))
	}
	return ModifyOutboundPreview{Success: true, Message: msg, Changes: changes, Unmatched: unmatched}, nil
}

func (s *Service) ModifyNodeOutbounds(alias string, filenames []string, outboundLines string, outboundProtocol string) (OperationResult, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return OperationResult{Success: false, Message: "VPS 不能为空"}, nil
	}
	entries := parseOutboundMappingLines(outboundLines, outboundProtocol)
	if len(entries) == 0 {
		return OperationResult{Success: false, Message: "落地代理为空或格式错误"}, nil
	}
	unlock := s.lockNodeConfigAliases(alias)
	defer unlock()
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return OperationResult{}, err
	}
	if len(configs) == 0 {
		return OperationResult{Success: false, Message: "该 VPS 暂无可修改的节点"}, nil
	}
	_, _ = s.BackupNow(alias)

	target := map[string]bool{}
	for _, filename := range filenames {
		filename = strings.TrimSpace(filename)
		if filename != "" {
			target[filename] = true
		}
	}

	changed := []string{}

	if len(target) > 0 {
		targetFilenames := []string{}
		for filename := range target {
			if _, exists := configs[filename]; exists {
				targetFilenames = append(targetFilenames, filename)
			}
		}
		if len(targetFilenames) == 0 {
			return OperationResult{Success: false, Message: "未找到选中的节点配置"}, nil
		}
		sort.Slice(targetFilenames, func(i, j int) bool {
			portI := extractListenPort(configs[targetFilenames[i]])
			portJ := extractListenPort(configs[targetFilenames[j]])
			if portI != portJ {
				return portI < portJ
			}
			return targetFilenames[i] < targetFilenames[j]
		})
		for i, filename := range targetFilenames {
			config := configs[filename]
			outbound := cloneMap(entries[i%len(entries)].Outbound)
			index := findEditableOutboundIndex(config)
			if index >= 0 {
				applyOutboundAt(config, index, outbound)
			} else {
				applyOutbound(config, outbound)
			}
			configs[filename] = config
			changed = append(changed, filename)
		}
		if err := s.saveNodeConfigMap(alias, configs); err != nil {
			return OperationResult{}, err
		}
		var message string
		if len(entries) == len(changed) {
			message = fmt.Sprintf("提交 %d 条落地已成功更新至 %d 个选中节点", len(entries), len(changed))
		} else {
			message = fmt.Sprintf("提交 %d 条落地已循环应用至 %d 个选中节点", len(entries), len(changed))
		}
		return OperationResult{Success: true, Message: message, Count: len(changed), Items: changed}, nil
	}

	// len(target) == 0: 支持同 IP 精确匹配或 "旧IP => 新落地" 映射替换
	failed := []string{}
	changedSet := map[string]bool{}
	matchedOutboundCount := 0

	sortedFilenames := make([]string, 0, len(configs))
	for filename := range configs {
		sortedFilenames = append(sortedFilenames, filename)
	}
	sort.Slice(sortedFilenames, func(i, j int) bool {
		portI := extractListenPort(configs[sortedFilenames[i]])
		portJ := extractListenPort(configs[sortedFilenames[j]])
		if portI != portJ {
			return portI < portJ
		}
		return sortedFilenames[i] < sortedFilenames[j]
	})

	for _, entry := range entries {
		targetServer := entry.MatchIP
		if targetServer == "" {
			continue
		}
		matchedThisOutbound := 0
		for _, filename := range sortedFilenames {
			config := configs[filename]
			idx := findMatchingOutboundIndex(config, targetServer)
			if idx < 0 {
				continue
			}
			applyOutboundAt(config, idx, cloneMap(entry.Outbound))
			configs[filename] = config
			if !changedSet[filename] {
				changedSet[filename] = true
				changed = append(changed, filename)
			}
			matchedThisOutbound++
		}
		if matchedThisOutbound == 0 {
			failed = append(failed, targetServer)
		} else {
			matchedOutboundCount++
		}
	}

	if len(changed) == 0 {
		if len(failed) > 0 {
			return OperationResult{Success: false, Message: fmt.Sprintf("未匹配到 IP 为 %s 的落地节点，未进行任何修改", strings.Join(failed, ", "))}, nil
		}
		return OperationResult{Success: false, Message: "未匹配到任何相同 IP 的落地节点，未进行任何修改"}, nil
	}

	if err := s.saveNodeConfigMap(alias, configs); err != nil {
		return OperationResult{}, err
	}

	inputCount := len(entries)
	var message string
	if matchedOutboundCount == inputCount {
		if len(changed) == matchedOutboundCount {
			message = fmt.Sprintf("提交 %d 条落地已全部匹配成功（共更新 %d 个节点）", inputCount, len(changed))
		} else {
			message = fmt.Sprintf("提交 %d 条落地已全部匹配成功（共更新 %d 个节点，部分节点共享相同落地 IP）", inputCount, len(changed))
		}
	} else {
		message = fmt.Sprintf("提交 %d 条落地：成功匹配 %d 条（共更新 %d 个节点）", inputCount, matchedOutboundCount, len(changed))
		if len(failed) > 0 {
			message += fmt.Sprintf("，未匹配 %d 条 (%s)", len(failed), strings.Join(failed, ", "))
		}
	}
	return OperationResult{Success: true, Message: message, Count: len(changed), Items: append(changed, failed...)}, nil
}

func (s *Service) BackupNow(alias string) (BackupFile, error) {
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return BackupFile{}, err
	}
	data, err := buildPortableAllJSON(configs)
	if err != nil {
		return BackupFile{}, err
	}
	return s.saveBackupSnapshot(alias, data)
}

// BackupAllJSON 根据本地完整节点配置生成标准 sing-box all.json，并保存到内部备份目录。
// 不连接或修改远程 VPS，生成的文件可直接通过备份列表执行还原部署。
func (s *Service) BackupAllJSON(alias string) (BackupFile, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return BackupFile{}, errors.New("VPS 不能为空")
	}
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return BackupFile{}, err
	}
	data, err := buildPortableAllJSON(configs)
	if err != nil {
		return BackupFile{}, err
	}
	return s.saveBackupSnapshot(alias, data)
}

// buildPortableAllJSON 是备份、还原 JSON 和在线查看共用的唯一序列化入口。
// 输出始终是标准 sing-box all.json，去除本地 share/metadata，并使用固定缩进和结尾换行，
// 从而保证“立即备份”和“保存还原 JSON”生成的文件内容完全一致。
func buildPortableAllJSON(configs map[string]map[string]any) ([]byte, error) {
	if len(configs) == 0 {
		return nil, errors.New("当前 VPS 没有本地节点配置")
	}
	if err := validateNodeConfigMap(configs); err != nil {
		return nil, fmt.Errorf("本地节点配置无法生成可还原 all.json: %w", err)
	}

	// buildAllJSON 已按节点文件名排序并移除本地辅助字段；重新解码后格式化，
	// 让所有调用方共享完全相同的 JSON 字节表示，而不是一处紧凑、一处缩进。
	var value any
	if err := json.Unmarshal(buildAllJSON(configs), &value); err != nil {
		return nil, fmt.Errorf("生成 all.json 失败: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("格式化 all.json 失败: %w", err)
	}
	return append(data, '\n'), nil
}

func safeVPSDirName(alias string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return "default"
	}
	encoded := url.QueryEscape(alias)
	re := regexp.MustCompile(`[\\/:*?"<>|]+`)
	safe := re.ReplaceAllString(encoded, "_")
	if safe == "" {
		return "default"
	}
	return safe
}

func (s *Service) saveBackupSnapshot(alias string, data []byte) (BackupFile, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return BackupFile{}, errors.New("VPS 不能为空")
	}
	if len(data) == 0 {
		return BackupFile{}, errors.New("没有可保存的备份内容")
	}
	dir := filepath.Join(s.dataDir, "bk", safeVPSDirName(alias))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return BackupFile{}, err
	}

	encodedAlias := url.QueryEscape(alias)
	var path, displayName string
	var file *os.File
	for attempt := 0; attempt < 1000; attempt++ {
		stamp := time.Now().Add(time.Duration(attempt) * time.Second).Format("20060102_150405")
		physicalName := fmt.Sprintf("~%s_%s_all.json", encodedAlias, stamp)
		candidate := filepath.Join(dir, physicalName)
		created, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return BackupFile{}, err
		}
		path = candidate
		displayName = fmt.Sprintf("%s_%s_all.json", alias, stamp)
		file = created
		break
	}
	if file == nil {
		return BackupFile{}, errors.New("备份文件名生成失败，请稍后重试")
	}
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(path)
	}
	if _, err := file.Write(data); err != nil {
		cleanup()
		return BackupFile{}, err
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return BackupFile{}, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return BackupFile{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		_ = os.Remove(path)
		return BackupFile{}, fmt.Errorf("读取备份文件信息失败: %w", err)
	}
	pruneOldBackups(dir, 30)
	return BackupFile{Name: displayName, Path: path, SourceAlias: alias, Size: info.Size(), Modified: info.ModTime().Format("2006-01-02 15:04:05")}, nil
}

func pruneOldBackups(dir string, maxKeep int) {
	if maxKeep <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= maxKeep {
		return
	}
	type fileItem struct {
		path    string
		modTime time.Time
	}
	list := make([]fileItem, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), "_all.json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		list = append(list, fileItem{path: filepath.Join(dir, entry.Name()), modTime: info.ModTime()})
	}
	if len(list) <= maxKeep {
		return
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].modTime.After(list[j].modTime)
	})
	for _, old := range list[maxKeep:] {
		_ = os.Remove(old.path)
	}
}

func backupDisplayInfo(physicalName string) (string, string) {
	matched := backupFileNamePattern.FindStringSubmatch(physicalName)
	if len(matched) != 3 {
		return physicalName, ""
	}
	alias := matched[1]
	if strings.HasPrefix(alias, "~") {
		if decoded, err := url.QueryUnescape(strings.TrimPrefix(alias, "~")); err == nil {
			alias = decoded
		}
	}
	return fmt.Sprintf("%s_%s", alias, matched[2]), alias
}

func (s *Service) ReadNodeJSON(alias string, filename string) (string, error) {
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return "", err
	}
	config, ok := configs[filename]
	if !ok {
		return "", errors.New("节点不存在")
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Service) ReadAllJSON(alias string) (string, error) {
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return "", err
	}
	data, err := buildPortableAllJSON(configs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Service) SaveNodeJSONAndDeploy(alias string, filename string, text string) CommandResult {
	unlock := s.lockNodeConfigAliases(alias)
	defer unlock()
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	oldConfig, ok := configs[filename]
	if !ok {
		return CommandResult{Success: false, ExitCode: -1, Message: "节点不存在"}
	}
	oldConfigs := cloneNodeConfigMap(configs)
	var config map[string]any
	if err := json.Unmarshal([]byte(text), &config); err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: "JSON 格式错误: " + err.Error()}
	}
	if _, ok := config["metadata"]; !ok {
		if oldMeta, ok := oldConfig["metadata"].(map[string]any); ok && len(oldMeta) > 0 {
			config["metadata"] = cloneMap(oldMeta)
		}
	}
	if _, ok := config["share"]; !ok {
		if oldShare, ok := oldConfig["share"].(map[string]any); ok && len(oldShare) > 0 {
			config["share"] = cloneMap(oldShare)
		}
	}
	configs[filename] = config
	if err := validateNodeConfigMap(configs); err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	return s.saveAndDeployNodeConfigs(alias, configs, oldConfigs, "节点 JSON 已保存并发布生效", "发布失败")
}

func (s *Service) SaveAllJSONAndDeploy(alias string, text string) CommandResult {
	unlock := s.lockNodeConfigAliases(alias)
	defer unlock()
	oldConfigs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	configs, err := parseNodeConfigText(text)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	restoreLocalShareMetadata(configs, oldConfigs)
	if err := validateNodeConfigMap(configs); err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	message := fmt.Sprintf("JSON 已保存并发布生效，节点数：%d", len(configs))
	return s.saveAndDeployNodeConfigs(alias, configs, oldConfigs, message, "发布失败")
}

func (s *Service) ImportAllJSONAndDeploy(alias string, text string) CommandResult {
	unlock := s.lockNodeConfigAliases(alias)
	defer unlock()
	oldConfigs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	configs, err := parseNodeConfigText(text)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	restoreLocalShareMetadata(configs, oldConfigs)
	markImportedMetadata(configs, time.Now().UTC().Format(time.RFC3339Nano))
	if err := validateNodeConfigMap(configs); err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	message := fmt.Sprintf("备份已导入并发布生效，节点数：%d", len(configs))
	return s.saveAndDeployNodeConfigs(alias, configs, oldConfigs, message, "导入发布失败")
}

// RestoreBackup 安全还原备份到指定 VPS。
// 具备强拦截硬屏障：如果备份文件的来源服务器与当前 targetAlias 不一致，将直接硬拦截并拒绝还原，防止误将其他服务器配置覆盖当前服务器。
func (s *Service) RestoreBackup(targetAlias string, backupPath string) CommandResult {
	targetAlias = strings.TrimSpace(targetAlias)
	if targetAlias == "" {
		return CommandResult{Success: false, ExitCode: -1, Message: "目标 VPS 不能为空"}
	}
	resolvedPath, err := s.resolveBackupPath(backupPath)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: "备份路径无效: " + err.Error()}
	}
	content, err := s.ReadBackup(resolvedPath)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: "读取备份内容失败: " + err.Error()}
	}

	// 提取来源服务器别名并进行硬拦截校验
	fileName := filepath.Base(resolvedPath)
	_, sourceAlias := backupDisplayInfo(fileName)
	if sourceAlias == "" {
		parentDir := filepath.Base(filepath.Dir(resolvedPath))
		if parentDir != "bk" && parentDir != "." && parentDir != "" {
			if decoded, unErr := url.QueryUnescape(parentDir); unErr == nil {
				sourceAlias = decoded
			} else {
				sourceAlias = parentDir
			}
		}
	}

	if sourceAlias != "" && !strings.EqualFold(strings.TrimSpace(sourceAlias), targetAlias) {
		return CommandResult{
			Success:  false,
			ExitCode: -1,
			Message:  fmt.Sprintf("安全拦截：该备份来源为 [%s]，与当前操作的服务器 [%s] 不一致！已阻止覆盖还原。如需跨服务器复制，请使用“迁移部署”。", sourceAlias, targetAlias),
		}
	}
	_, _ = s.BackupNow(targetAlias)
	return s.ImportAllJSONAndDeploy(targetAlias, content)
}

// PreviewBackupDiff 对比备份文件与目标 VPS 当前本地配置的差异，并检测跨机迁移时的 bind_address 风险。
func (s *Service) PreviewBackupDiff(targetAlias string, backupPath string) (BackupDiffPreview, error) {
	targetAlias = strings.TrimSpace(targetAlias)
	resolvedPath, err := s.resolveBackupPath(backupPath)
	if err != nil {
		return BackupDiffPreview{Success: false, Message: "备份路径无效: " + err.Error()}, nil
	}
	content, err := s.ReadBackup(resolvedPath)
	if err != nil {
		return BackupDiffPreview{Success: false, Message: "读取备份失败: " + err.Error()}, nil
	}
	backupConfigs, err := parseNodeConfigText(content)
	if err != nil {
		return BackupDiffPreview{Success: false, Message: "备份内容解析失败: " + err.Error()}, nil
	}
	currentConfigs, _ := s.loadNodeConfigMap(targetAlias)
	_, sourceAlias := backupDisplayInfo(filepath.Base(resolvedPath))
	preview := BackupDiffPreview{
		Success:      true,
		SourceAlias:  sourceAlias,
		TargetAlias:  targetAlias,
		BackupCount:  len(backupConfigs),
		CurrentCount: len(currentConfigs),
		Added:        []string{},
		Removed:      []string{},
		Modified:     []string{},
	}
	for fn, bCfg := range backupConfigs {
		cCfg, exists := currentConfigs[fn]
		if !exists {
			preview.Added = append(preview.Added, extractNodeTag(bCfg))
		} else {
			bRaw, _ := json.Marshal(buildAllJSON(map[string]map[string]any{fn: bCfg}))
			cRaw, _ := json.Marshal(buildAllJSON(map[string]map[string]any{fn: cCfg}))
			if !bytes.Equal(bRaw, cRaw) {
				preview.Modified = append(preview.Modified, extractNodeTag(bCfg))
			}
		}
		if sourceAlias != "" && targetAlias != "" && !strings.EqualFold(sourceAlias, targetAlias) {
			if outs, ok := bCfg["outbounds"].([]any); ok {
				for _, rawOut := range outs {
					if ob, ok := rawOut.(map[string]any); ok {
						if bindIP := asString(ob["inet4_bind_address"]); bindIP != "" {
							preview.BindIPWarnings = append(preview.BindIPWarnings, fmt.Sprintf("%s 绑定了源机 IP %s", extractNodeTag(bCfg), bindIP))
						}
					}
				}
			}
		}
	}
	for fn, cCfg := range currentConfigs {
		if _, exists := backupConfigs[fn]; !exists {
			preview.Removed = append(preview.Removed, extractNodeTag(cCfg))
		}
	}
	sort.Strings(preview.Added)
	sort.Strings(preview.Removed)
	sort.Strings(preview.Modified)
	preview.Message = fmt.Sprintf("备份含 %d 个节点（当前 %d 个）：新增 %d、变更 %d、将移除 %d",
		preview.BackupCount, preview.CurrentCount, len(preview.Added), len(preview.Modified), len(preview.Removed))
	if len(preview.BindIPWarnings) > 0 {
		preview.Message += fmt.Sprintf("；注意：检测到 %d 个绑定了源机 IP 的多IP节点", len(preview.BindIPWarnings))
	}
	return preview, nil
}

// MigrateBackup 明确将一个备份文件跨服务器迁移/克隆部署到 targetAlias。
func (s *Service) MigrateBackup(targetAlias string, backupPath string) CommandResult {
	targetAlias = strings.TrimSpace(targetAlias)
	if targetAlias == "" {
		return CommandResult{Success: false, ExitCode: -1, Message: "目标 VPS 不能为空"}
	}
	resolvedPath, err := s.resolveBackupPath(backupPath)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: "备份路径无效: " + err.Error()}
	}
	content, err := s.ReadBackup(resolvedPath)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: "读取备份内容失败: " + err.Error()}
	}
	_, _ = s.BackupNow(targetAlias)
	return s.ImportAllJSONAndDeploy(targetAlias, content)
}

func (s *Service) saveAndDeployNodeConfigs(alias string, configs, oldConfigs map[string]map[string]any, successMessage, failureMessage string) CommandResult {
	if err := s.saveNodeConfigMap(alias, configs); err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	result := s.UploadAllJSON(alias)
	if result.Success {
		result.Message = successMessage
		return result
	}
	if err := s.saveNodeConfigMap(alias, oldConfigs); err != nil {
		result.Message = failureMessage + "，本地回滚失败: " + err.Error()
		return result
	}
	rollback := s.UploadAllJSON(alias)
	result.Message = failureMessage + "，已恢复本地旧配置"
	if rollback.Success {
		result.Message += "，远程已回滚"
	} else {
		result.Message += "，远程回滚失败: " + rollback.Message
	}
	return result
}

func cloneNodeConfigMap(configs map[string]map[string]any) map[string]map[string]any {
	cloned := make(map[string]map[string]any, len(configs))
	for filename, config := range configs {
		cloned[filename] = config
	}
	return cloned
}

func (s *Service) RollbackLocalNodes(alias string, filenames []string) (OperationResult, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return OperationResult{Success: false, Message: "VPS 别名不能为空"}, nil
	}
	if len(filenames) == 0 {
		return OperationResult{Success: true, Message: "无须回滚"}, nil
	}
	unlock := s.lockNodeConfigAliases(alias)
	defer unlock()
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return OperationResult{}, err
	}
	changed := false
	for _, fn := range filenames {
		fn = strings.ToLower(strings.TrimSpace(fn))
		if _, ok := configs[fn]; ok {
			delete(configs, fn)
			changed = true
		}
	}
	if changed {
		if err := s.saveNodeConfigMap(alias, configs); err != nil {
			return OperationResult{}, err
		}
	}
	return OperationResult{Success: true, Message: "本地未生效节点已清除"}, nil
}

func (s *Service) clearLocalNodesForVPS(alias string) (int, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return 0, errors.New("VPS 别名不能为空")
	}
	unlock := s.lockNodeConfigAliases(alias)
	defer unlock()
	existing, err := s.loadNodeConfigMap(alias)
	if err != nil || len(existing) == 0 {
		return 0, nil
	}
	count := len(existing)
	if err := s.saveNodeConfigMap(alias, map[string]map[string]any{}); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Service) reconcileLocalNodesWithRemote(alias string, remoteConfigs map[string]map[string]any) (int, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return 0, errors.New("VPS 别名不能为空")
	}
	unlock := s.lockNodeConfigAliases(alias)
	defer unlock()
	existing, err := s.loadNodeConfigMap(alias)
	if err != nil {
		existing = map[string]map[string]any{}
	}
	deletedCount := 0
	for fn := range existing {
		if _, ok := remoteConfigs[fn]; !ok {
			deletedCount++
		}
	}
	restoreLocalShareMetadata(remoteConfigs, existing)
	if err := s.saveNodeConfigMap(alias, remoteConfigs); err != nil {
		return 0, err
	}
	return deletedCount, nil
}

func (s *Service) DeleteBackup(path string) error {
	path, err := s.resolveBackupPath(path)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

func (s *Service) ExportGroupExcel(alias string, group string) (ExportFile, error) {
	alias = strings.TrimSpace(alias)
	group = strings.TrimSpace(group)
	if alias == "" || group == "" {
		return ExportFile{}, errors.New("VPS 和节点组不能为空")
	}
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return ExportFile{}, err
	}
	targetGroup := resolveTargetGroupName(configs, group)
	items := []excelNodeItem{}
	for filename, config := range configs {
		if !nodeMatchesGroup(config, targetGroup) {
			continue
		}
		items = append(items, excelNodeItem{filename: filename, config: config, port: extractListenPort(config)})
	}
	return s.exportExcelItems(alias, targetGroup, items)
}

func (s *Service) ExportNodeExcel(alias string, filename string, mode string) (ExportFile, error) {
	file, err := s.ExportNodeExcelBytes(alias, filename, mode)
	if err != nil {
		return ExportFile{}, err
	}
	return ExportFile{FileName: file.FileName, MimeType: file.MimeType, Base64: base64.StdEncoding.EncodeToString(file.Data)}, nil
}

func (s *Service) ExportNodeExcelBytes(alias string, filename string, mode string) (ExportBytes, error) {
	alias, exportName, items, err := s.nodeExcelSelection(alias, filename, mode)
	if err != nil {
		return ExportBytes{}, err
	}
	return s.buildExcelItems(alias, exportName, items)
}

func (s *Service) ExportNodeExcelFileName(alias string, filename string, mode string) (string, error) {
	alias, exportName, items, err := s.nodeExcelSelection(alias, filename, mode)
	if err != nil {
		return "", err
	}
	return excelExportFileName(alias, exportName, len(items)), nil
}

func (s *Service) nodeExcelSelection(alias string, filename string, mode string) (string, string, []excelNodeItem, error) {
	alias = strings.TrimSpace(alias)
	filename = strings.TrimSpace(filename)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "single"
	}
	if alias == "" || filename == "" {
		return "", "", nil, errors.New("VPS 和节点不能为空")
	}
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return "", "", nil, err
	}
	target, ok := configs[filename]
	if !ok {
		return "", "", nil, errors.New("节点不存在")
	}
	items := []excelNodeItem{{filename: filename, config: target, port: extractListenPort(target)}}
	exportName := extractNodeTag(target)
	if mode == "batch" {
		createdAt := metadataCreatedAt(target)
		if createdAt == "" {
			return "", "", nil, errors.New("该节点缺少批次信息，无法批次导出")
		}
		items = []excelNodeItem{}
		for name, config := range configs {
			if metadataCreatedAt(config) != createdAt {
				continue
			}
			items = append(items, excelNodeItem{filename: name, config: config, port: extractListenPort(config)})
		}
		exportName = "批次-" + createdAt
	}
	return alias, exportName, items, nil
}

func (s *Service) exportExcelItems(alias string, exportName string, items []excelNodeItem) (ExportFile, error) {
	file, err := s.buildExcelItems(alias, exportName, items)
	if err != nil {
		return ExportFile{}, err
	}
	return ExportFile{FileName: file.FileName, MimeType: file.MimeType, Base64: base64.StdEncoding.EncodeToString(file.Data)}, nil
}

func (s *Service) buildExcelItems(alias string, exportName string, items []excelNodeItem) (ExportBytes, error) {
	vps, _ := s.getVPS(alias)
	rows := [][]string{{"网络编号", "网络链接", "socks", "二维码", "初次到期日期"}}
	sort.Slice(items, func(i, j int) bool { return items[i].port < items[j].port })
	qrImages := []xlsxImage{}
	for _, item := range items {
		node := parseNode(alias, item.filename, mustJSON(item.config))
		link := nodeShareLink(node, vps)
		socksText := firstSocksOutboundText(item.config)
		if strings.EqualFold(node.Type, "socks") {
			socksText = socksInboundText(node, vps)
			link = ""
		}
		qrText := firstNonEmpty(link, socksText)
		if qrText != "" {
			qrImages = append(qrImages, xlsxImage{Row: len(rows), Col: 3, Name: fmt.Sprintf("qr%d.png", len(qrImages)+1), Text: qrText})
		}
		rows = append(rows, []string{
			extractNodeTag(item.config),
			link,
			socksText,
			"扫码",
			calculateExpireDate(extractNodeTag(item.config), metadataCreatedAt(item.config)),
		})
	}
	if len(rows) == 1 {
		return ExportBytes{}, errors.New("未找到可导出的节点数据")
	}
	data, err := buildSimpleXLSX(rows, qrImages)
	if err != nil {
		return ExportBytes{}, err
	}
	fileName := excelExportFileName(alias, exportName, len(rows)-1)
	return ExportBytes{FileName: fileName, MimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Data: data}, nil
}

func excelExportFileName(alias string, exportName string, count int) string {
	return safeFileName(fmt.Sprintf("%s_%s_%d个.xlsx", alias, exportName, count))
}

type SelectedNodeExportTarget struct {
	VPSAlias string `json:"vpsAlias"`
	FileName string `json:"fileName"`
}

type ExportSelectedNodesRequest struct {
	Nodes []SelectedNodeExportTarget `json:"nodes"`
}

func (s *Service) ExportSelectedNodesExcelBytes(req ExportSelectedNodesRequest) (ExportBytes, error) {
	if len(req.Nodes) == 0 {
		return ExportBytes{}, errors.New("请选择需要导出的节点")
	}

	byVPS := make(map[string][]string)
	aliasOrder := []string{}
	for _, target := range req.Nodes {
		alias := strings.TrimSpace(target.VPSAlias)
		fn := strings.TrimSpace(target.FileName)
		if alias == "" || fn == "" {
			continue
		}
		if _, ok := byVPS[alias]; !ok {
			aliasOrder = append(aliasOrder, alias)
		}
		byVPS[alias] = append(byVPS[alias], fn)
	}
	if len(byVPS) == 0 {
		return ExportBytes{}, errors.New("未找到有效的节点选择")
	}

	type selectedItem struct {
		alias    string
		filename string
		config   map[string]any
		port     int
	}
	var allItems []selectedItem

	for _, alias := range aliasOrder {
		fileNames := byVPS[alias]
		configs, err := s.loadNodeConfigMap(alias)
		if err != nil {
			continue
		}
		for _, fn := range fileNames {
			if cfg, ok := configs[fn]; ok {
				allItems = append(allItems, selectedItem{
					alias:    alias,
					filename: fn,
					config:   cfg,
					port:     extractListenPort(cfg),
				})
			}
		}
	}

	if len(allItems) == 0 {
		return ExportBytes{}, errors.New("所选节点配置不存在或已被删除")
	}

	// Sort items by VPS alias, then by port
	sort.SliceStable(allItems, func(i, j int) bool {
		if allItems[i].alias != allItems[j].alias {
			return allItems[i].alias < allItems[j].alias
		}
		return allItems[i].port < allItems[j].port
	})

	rows := [][]string{{"网络编号", "网络链接", "socks", "二维码", "初次到期日期"}}
	qrImages := []xlsxImage{}

	for _, item := range allItems {
		vps, _ := s.getVPS(item.alias)
		node := parseNode(item.alias, item.filename, mustJSON(item.config))
		link := nodeShareLink(node, vps)
		socksText := firstSocksOutboundText(item.config)
		if strings.EqualFold(node.Type, "socks") {
			socksText = socksInboundText(node, vps)
			link = ""
		}
		qrText := firstNonEmpty(link, socksText)
		if qrText != "" {
			qrImages = append(qrImages, xlsxImage{Row: len(rows), Col: 3, Name: fmt.Sprintf("qr%d.png", len(qrImages)+1), Text: qrText})
		}
		tag := extractNodeTag(item.config)
		rows = append(rows, []string{
			tag,
			link,
			socksText,
			"扫码",
			calculateExpireDate(tag, metadataCreatedAt(item.config)),
		})
	}

	if len(rows) == 1 {
		return ExportBytes{}, errors.New("未找到可导出的节点数据")
	}

	data, err := buildSimpleXLSX(rows, qrImages)
	if err != nil {
		return ExportBytes{}, err
	}

	exportName := "批量勾选节点"
	if len(aliasOrder) == 1 {
		exportName = fmt.Sprintf("%s_批量勾选", aliasOrder[0])
	}
	fileName := safeFileName(fmt.Sprintf("%s_%d个.xlsx", exportName, len(allItems)))
	return ExportBytes{
		FileName: fileName,
		MimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		Data:     data,
	}, nil
}

func (s *Service) ExportSelectedNodesExcel(req ExportSelectedNodesRequest) (ExportFile, error) {
	file, err := s.ExportSelectedNodesExcelBytes(req)
	if err != nil {
		return ExportFile{}, err
	}
	return ExportFile{
		FileName: file.FileName,
		MimeType: file.MimeType,
		Base64:   base64.StdEncoding.EncodeToString(file.Data),
	}, nil
}

func (s *Service) GetNodeQRCode(alias string, filename string) (QRCodeResult, error) {
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return QRCodeResult{}, err
	}
	config, ok := configs[filename]
	if !ok {
		return QRCodeResult{}, errors.New("节点不存在")
	}
	vps, _ := s.getVPS(alias)
	node := parseNode(alias, filename, mustJSON(config))
	link := nodeShareLink(node, vps)
	if link == "" {
		return QRCodeResult{}, errors.New("节点链接生成失败")
	}
	png, err := qrcode.Encode(link, qrcode.Medium, 240)
	if err != nil {
		return QRCodeResult{}, err
	}
	return QRCodeResult{Link: link, Image: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}, nil
}

func (s *Service) loadNodeConfigMap(alias string) (map[string]map[string]any, error) {
	s.nodeConfigMu.RLock()
	defer s.nodeConfigMu.RUnlock()

	data, err := os.ReadFile(filepath.Join(s.dataDir, "sbconfig.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]map[string]any{}, nil
		}
		return nil, err
	}
	var raw map[string]struct {
		Nodes map[string]map[string]any `json:"nodes_configs"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw[alias].Nodes == nil {
		return map[string]map[string]any{}, nil
	}
	return raw[alias].Nodes, nil
}

func (s *Service) saveNodeConfigMap(alias string, configs map[string]map[string]any) error {
	s.nodeConfigMu.Lock()
	defer s.nodeConfigMu.Unlock()

	path := filepath.Join(s.dataDir, "sbconfig.json")
	raw := map[string]any{}
	data, err := os.ReadFile(path)
	if err == nil {
		if len(bytes.TrimSpace(data)) == 0 {
			return errors.New("读取 sbconfig.json 失败: 文件为空")
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("读取 sbconfig.json 失败: %w", err)
		}
		if raw == nil {
			return errors.New("读取 sbconfig.json 失败: 根对象无效")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	current, _ := raw[alias].(map[string]any)
	if current == nil {
		current = map[string]any{}
	}
	current["nodes_configs"] = configs
	current["metadata"] = map[string]any{
		"server_actual_file_count":       len(configs),
		"all_json_reported_file_count":   len(configs),
		"last_successful_sync_timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	}
	raw[alias] = current
	if err := writeJSON(path, raw); err != nil {
		return err
	}
	s.invalidateNodeCache()
	return nil
}

func (s *Service) renameNodeConfigAlias(oldAlias string, newAlias string) error {
	oldAlias = strings.TrimSpace(oldAlias)
	newAlias = strings.TrimSpace(newAlias)
	if oldAlias == "" || newAlias == "" || strings.EqualFold(oldAlias, newAlias) {
		return nil
	}
	s.nodeConfigMu.Lock()
	defer s.nodeConfigMu.Unlock()

	path := filepath.Join(s.dataDir, "sbconfig.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	raw := map[string]any{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
	}
	oldKey := ""
	for key := range raw {
		if strings.EqualFold(key, oldAlias) {
			oldKey = key
			break
		}
	}
	if oldKey == "" {
		return nil
	}
	oldValue := raw[oldKey]
	newKey := ""
	for key := range raw {
		if strings.EqualFold(key, newAlias) {
			newKey = key
			break
		}
	}
	if newKey != "" && newKey != oldKey {
		merged, err := mergeNodeConfigGroups(raw[newKey], oldValue)
		if err != nil {
			return err
		}
		raw[newKey] = merged
		delete(raw, oldKey)
		if err := writeJSON(path, raw); err != nil {
			return err
		}
		s.invalidateNodeCache()
		return nil
	}
	raw[newAlias] = oldValue
	delete(raw, oldKey)
	if err := writeJSON(path, raw); err != nil {
		return err
	}
	s.invalidateNodeCache()
	return nil
}

func mergeNodeConfigGroups(target any, source any) (map[string]any, error) {
	targetMap, _ := target.(map[string]any)
	if targetMap == nil {
		targetMap = map[string]any{}
	}
	sourceMap, _ := source.(map[string]any)
	if sourceMap == nil {
		return targetMap, nil
	}
	targetNodes, _ := targetMap["nodes_configs"].(map[string]any)
	if targetNodes == nil {
		targetNodes = map[string]any{}
	}
	sourceNodes, _ := sourceMap["nodes_configs"].(map[string]any)
	for name, config := range sourceNodes {
		if _, exists := targetNodes[name]; exists {
			return nil, fmt.Errorf("节点文件名冲突，无法合并: %s", name)
		}
		targetNodes[name] = config
	}
	targetMap["nodes_configs"] = targetNodes
	targetMap["metadata"] = map[string]any{
		"server_actual_file_count":       len(targetNodes),
		"all_json_reported_file_count":   len(targetNodes),
		"last_successful_sync_timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	}
	return targetMap, nil
}

func buildAllJSON(configs map[string]map[string]any) []byte {
	all := map[string]any{
		"log":       map[string]any{"disabled": false, "level": "warn", "output": "/var/log/sing-box/sing-box.log", "timestamp": true},
		"inbounds":  []any{},
		"outbounds": []any{},
		"route": map[string]any{
			"rules": []any{},
		},
	}
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		config := runtimeConfig(configs[name])
		all["inbounds"] = appendSlice(all["inbounds"], config["inbounds"])
		all["outbounds"] = appendSlice(all["outbounds"], config["outbounds"])
		if route, ok := config["route"].(map[string]any); ok {
			all["route"].(map[string]any)["rules"] = appendSlice(all["route"].(map[string]any)["rules"], route["rules"])
		}
	}
	data, _ := json.Marshal(all)
	return data
}

func runtimeConfig(config map[string]any) map[string]any {
	clone := cloneMap(config)
	delete(clone, "share")
	delete(clone, "metadata")
	return clone
}

func splitAllJSON(content []byte) (map[string]map[string]any, error) {
	var all map[string]any
	if err := json.Unmarshal(content, &all); err != nil {
		return nil, err
	}
	inbounds, _ := all["inbounds"].([]any)
	outbounds, _ := all["outbounds"].([]any)
	route, _ := all["route"].(map[string]any)
	rules, _ := route["rules"].([]any)

	outboundByTag := map[string]any{}
	for _, item := range outbounds {
		if outbound, ok := item.(map[string]any); ok {
			tag := asString(outbound["tag"])
			if tag != "" {
				outboundByTag[tag] = outbound
			}
		}
	}
	routeByInbound := map[string]string{}
	for _, item := range rules {
		rule, ok := item.(map[string]any)
		if !ok {
			continue
		}
		outTag := asString(rule["outbound"])
		if inboundList, ok := rule["inbound"].([]any); ok {
			for _, inbound := range inboundList {
				routeByInbound[asString(inbound)] = outTag
			}
		}
	}

	configs := map[string]map[string]any{}
	for _, item := range inbounds {
		inbound, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tag := asString(inbound["tag"])
		if tag == "" {
			continue
		}
		outTag := routeByInbound[tag]
		node := map[string]any{
			"log":      map[string]any{"disabled": true, "level": "info", "timestamp": true},
			"inbounds": []any{inbound},
			"route":    map[string]any{"rules": []any{map[string]any{"inbound": []any{tag}, "outbound": outTag}}},
		}
		if outbound, ok := outboundByTag[outTag]; ok {
			node["outbounds"] = []any{outbound}
		} else {
			node["outbounds"] = []any{map[string]any{"tag": outTag, "type": "direct"}}
		}
		configs[strings.ToLower(safeFileName(tag))+".json"] = node
	}
	return configs, nil
}

func parseNodeConfigText(text string) (map[string]map[string]any, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("JSON 内容不能为空")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &root); err != nil {
		return nil, fmt.Errorf("JSON 格式错误: %w", err)
	}
	if _, ok := root["inbounds"]; ok {
		configs, err := splitAllJSON([]byte(text))
		if err != nil {
			return nil, fmt.Errorf("all.json 解析失败: %w", err)
		}
		return configs, nil
	}
	if nodesRaw, ok := root["nodes_configs"]; ok {
		var wrapped map[string]map[string]any
		if err := json.Unmarshal(nodesRaw, &wrapped); err != nil {
			return nil, fmt.Errorf("nodes_configs 解析失败: %w", err)
		}
		return wrapped, nil
	}
	var configs map[string]map[string]any
	if err := json.Unmarshal([]byte(text), &configs); err != nil {
		return nil, errors.New("备份文件格式不支持")
	}
	return configs, nil
}

func validateNodeConfigMap(configs map[string]map[string]any) error {
	if len(configs) == 0 {
		return errors.New("节点配置不能为空")
	}
	ports := map[int]string{}
	tags := map[string]string{}
	for filename, config := range configs {
		if strings.TrimSpace(filename) == "" {
			return errors.New("节点文件名不能为空")
		}
		if config == nil {
			return fmt.Errorf("节点 %s 配置不能为空", filename)
		}
		tag := extractNodeTag(config)
		if tag == "" {
			return fmt.Errorf("节点 %s 缺少 inbound tag", filename)
		}
		if previous := tags[tag]; previous != "" {
			return fmt.Errorf("节点标签重复: %s (%s / %s)", tag, previous, filename)
		}
		tags[tag] = filename
		port := extractListenPort(config)
		if port <= 0 || port > 65535 {
			return fmt.Errorf("节点 %s 入站端口无效: %d", filename, port)
		}
		if previous := ports[port]; previous != "" {
			return fmt.Errorf("入站端口重复: %d (%s / %s)", port, previous, filename)
		}
		ports[port] = filename
	}
	return nil
}

func restoreLocalShareMetadata(configs map[string]map[string]any, existing map[string]map[string]any) {
	shareByTag := map[string]map[string]any{}
	metadataByTag := map[string]map[string]any{}
	for _, config := range existing {
		tag := extractNodeTag(config)
		if tag == "" {
			continue
		}
		if share, ok := config["share"].(map[string]any); ok && len(share) > 0 {
			shareByTag[tag] = cloneMap(share)
		}
		if metadata, ok := config["metadata"].(map[string]any); ok && len(metadata) > 0 {
			metadataByTag[tag] = cloneMap(metadata)
		}
	}
	for filename, config := range configs {
		// 1. 恢复与保留节点本地元数据（metadata：created_at, group_name, imported_at 等）
		var oldMetadata map[string]any
		if old, ok := existing[filename]; ok {
			if metadata, ok := old["metadata"].(map[string]any); ok && len(metadata) > 0 {
				oldMetadata = cloneMap(metadata)
			}
		}
		if oldMetadata == nil {
			oldMetadata = metadataByTag[extractNodeTag(config)]
		}

		curMetadata, hasCurMeta := config["metadata"].(map[string]any)
		if oldMetadata != nil && len(oldMetadata) > 0 {
			if hasCurMeta && len(curMetadata) > 0 {
				// 以 oldMetadata 为基准保留已有历史元数据，合并非空新字段
				merged := cloneMap(oldMetadata)
				for k, v := range curMetadata {
					if str, isStr := v.(string); isStr {
						if strings.TrimSpace(str) != "" {
							merged[k] = v
						}
					} else if v != nil {
						merged[k] = v
					}
				}
				config["metadata"] = merged
			} else {
				config["metadata"] = cloneMap(oldMetadata)
			}
		} else if !hasCurMeta || len(curMetadata) == 0 {
			// 若为远端同步发现的全新节点，赋予当前时间作为创建时间兜底，确保有确定的排序依据
			config["metadata"] = map[string]any{
				"created_at": time.Now().UTC().Format(time.RFC3339Nano),
			}
		} else {
			if asString(curMetadata["created_at"]) == "" {
				curMetadata["created_at"] = time.Now().UTC().Format(time.RFC3339Nano)
				config["metadata"] = curMetadata
			}
		}

		// 2. 恢复与保留 Reality 分享元数据（share）
		if share, ok := config["share"].(map[string]any); ok && len(share) > 0 {
			continue
		}
		var oldShare map[string]any
		if old, ok := existing[filename]; ok {
			if share, ok := old["share"].(map[string]any); ok && len(share) > 0 {
				oldShare = cloneMap(share)
			}
		}
		if oldShare == nil {
			oldShare = shareByTag[extractNodeTag(config)]
		}
		if share := realityShareFromConfig(config, oldShare); len(share) > 0 {
			config["share"] = share
		}
	}
}

func realityShareFromConfig(config map[string]any, fallback map[string]any) map[string]any {
	inbounds, _ := config["inbounds"].([]any)
	if len(inbounds) == 0 {
		return nil
	}
	inbound, _ := inbounds[0].(map[string]any)
	if !strings.EqualFold(asString(inbound["type"]), "vless") {
		return nil
	}
	tls, _ := inbound["tls"].(map[string]any)
	reality, _ := tls["reality"].(map[string]any)
	privateKey := asString(reality["private_key"])
	if privateKey == "" {
		return nil
	}
	publicKey := asString(fallback["reality_public_key"])
	if derived := realityPublicKeyFromPrivate(privateKey); derived != "" {
		publicKey = derived
	}
	shortID := firstNonEmpty(firstRealityShortID(reality["short_id"]), asString(fallback["reality_short_id"]))
	if publicKey == "" || shortID == "" {
		return nil
	}
	return map[string]any{
		"reality_public_key":  publicKey,
		"reality_short_id":    shortID,
		"reality_fingerprint": firstNonEmpty(asString(fallback["reality_fingerprint"]), "firefox"),
	}
}

func firstRealityShortID(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		if len(v) > 0 {
			return asString(v[0])
		}
	}
	return ""
}

func generateConfig(tag, inboundProtocol string, port int, password string, outboundProtocol string, outbound map[string]any, tlsEnabled bool, realityEnabled bool, realitySNI string) map[string]any {
	return generateConfigWithTag(tag, inboundProtocol, "::", port, password, outboundProtocol, outbound, tlsEnabled, realityEnabled, realitySNI)
}

func setNodeMetadata(config map[string]any, createdAt string) {
	metadata, _ := config["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	if asString(metadata["created_at"]) == "" {
		metadata["created_at"] = createdAt
	}
	config["metadata"] = metadata
}

func setNodeGroupMetadata(config map[string]any, groupName string) {
	groupName = strings.TrimSpace(groupName)
	if groupName == "" {
		return
	}
	metadata, _ := config["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["group_name"] = groupName
	config["metadata"] = metadata
}

func markImportedMetadata(configs map[string]map[string]any, importedAt string) {
	for _, config := range configs {
		metadata, _ := config["metadata"].(map[string]any)
		if metadata == nil {
			metadata = map[string]any{}
		}
		if asString(metadata["created_at"]) == "" {
			metadata["created_at"] = importedAt
		}
		metadata["imported_at"] = importedAt
		config["metadata"] = metadata
	}
}

func resolvedNodeGroupName(prefix string, outbound map[string]any, fallback string) string {
	prefix = strings.TrimSpace(prefix)
	server := strings.TrimSpace(asString(outbound["server"]))
	if server == "" {
		server = strings.TrimSpace(fallback)
	}
	if server == "" {
		if prefix != "" {
			return prefix
		}
		return "node"
	}
	if prefix == "" {
		return server
	}
	return prefix + "-" + server
}

func defaultNodeNameBase(alias string, vps VPS) string {
	for _, value := range []string{vps.Address, vps.Domain, alias} {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return "node"
}

func multiIPNodeTag(prefix string, ip string, protocol string) string {
	prefix = strings.TrimSpace(prefix)
	ip = strings.TrimSpace(ip)
	if prefix == "" {
		protocol = strings.TrimSpace(protocol)
		if protocol == "" {
			return ip
		}
		return ip + "-" + protocol
	}
	return prefix + "-" + ip
}

func generateConfigWithTag(tag, inboundProtocol string, listenHost string, port int, password string, outboundProtocol string, outbound map[string]any, tlsEnabled bool, realityEnabled bool, realitySNI string) map[string]any {
	outTag := tag + "-out"
	if listenHost == "" {
		listenHost = "::"
	}
	inbound := map[string]any{"tag": tag, "type": inboundProtocol, "listen": listenHost, "listen_port": port}
	config := map[string]any{
		"log":       map[string]any{"disabled": true, "level": "info", "timestamp": true},
		"inbounds":  []any{inbound},
		"outbounds": []any{},
		"route":     map[string]any{"rules": []any{map[string]any{"inbound": []any{tag}, "outbound": outTag}}},
	}
	switch inboundProtocol {
	case "trojan":
		inbound["users"] = []any{map[string]any{"password": password}}
	case "vmess", "vless":
		user := map[string]any{"uuid": password}
		if inboundProtocol == "vless" && realityEnabled {
			user["flow"] = "xtls-rprx-vision"
		}
		inbound["users"] = []any{user}
	case "anytls":
		inbound["users"] = []any{map[string]any{"name": tag, "password": password}}
	case "socks":
		inbound["users"] = []any{map[string]any{"username": randomLetters(8), "password": randomLetters(8)}}
	}
	if realityEnabled && inboundProtocol == "vless" {
		if realitySNI == "" {
			realitySNI = "stock.adobe.com"
		}
		privateKey, publicKey := realityKeyPair()
		shortID := randomHex(8)
		inbound["tls"] = map[string]any{"enabled": true, "server_name": realitySNI, "reality": map[string]any{"enabled": true, "handshake": map[string]any{"server": realitySNI, "server_port": 443}, "private_key": privateKey, "short_id": []any{shortID}}}
		config["share"] = map[string]any{"reality_public_key": publicKey, "reality_short_id": shortID, "reality_fingerprint": "firefox"}
	} else if tlsEnabled && inboundProtocol != "vmess" && inboundProtocol != "socks" {
		tls := map[string]any{"enabled": true, "certificate_path": "/etc/sing-box/bin/tls.cer", "key_path": "/etc/sing-box/bin/tls.key"}
		if inboundProtocol == "anytls" {
			tls["server_name"] = "bing.com"
		}
		inbound["tls"] = tls
	}
	outbound["tag"] = outTag
	if outbound["type"] == nil {
		outbound["type"] = outboundProtocol
	}
	config["outbounds"] = []any{outbound}
	return config
}

func parseOutboundLines(text string, protocol string) []map[string]any {
	result := []map[string]any{}
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if outbound, err := outboundFromShareLink(line); err == nil {
			result = append(result, normalizeOutboundForProtocol(outbound, protocol))
			continue
		}
		parts := strings.FieldsFunc(line, func(r rune) bool { return r == ':' || r == '|' || r == ',' || r == '\t' })
		if len(parts) < 2 {
			continue
		}
		item := outboundFromParts(protocol, parts)
		result = append(result, item)
	}
	return result
}

func defaultOutbound(protocol string) map[string]any {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	switch protocol {
	case "block":
		return map[string]any{"type": "block"}
	case "dns":
		return map[string]any{"type": "dns"}
	default:
		return map[string]any{"type": "direct"}
	}
}

func directOutboundForIP(ip string) map[string]any {
	outbound := map[string]any{"type": "direct"}
	if parsed := net.ParseIP(strings.TrimSpace(ip)); parsed != nil {
		if parsed.To4() != nil {
			outbound["inet4_bind_address"] = strings.TrimSpace(ip)
		} else {
			outbound["inet6_bind_address"] = strings.TrimSpace(ip)
		}
	}
	return outbound
}

func outboundFromParts(protocol string, parts []string) map[string]any {
	if protocol == "" {
		protocol = "socks"
	}
	item := map[string]any{"type": protocol, "server": parts[0], "server_port": asInt(parts[1])}
	switch protocol {
	case "socks", "http":
		if len(parts) > 2 && parts[2] != "" {
			item["username"] = parts[2]
		}
		if len(parts) > 3 && parts[3] != "" {
			item["password"] = parts[3]
		}
		if protocol == "socks" {
			item["network"] = "tcp"
		}
	case "shadowsocks":
		if len(parts) > 2 && parts[2] != "" {
			item["method"] = parts[2]
		} else {
			item["method"] = "2022-blake3-aes-128-gcm"
		}
		if len(parts) > 3 && parts[3] != "" {
			item["password"] = parts[3]
		}
	case "vmess", "vless":
		if len(parts) > 2 && parts[2] != "" {
			item["uuid"] = parts[2]
		}
		if protocol == "vmess" {
			item["security"] = "auto"
		}
	case "trojan", "anytls":
		if len(parts) > 2 && parts[2] != "" {
			item["password"] = parts[2]
		}
		if protocol == "anytls" || (len(parts) > 3 && parts[3] != "") {
			tls := map[string]any{"enabled": true}
			if len(parts) > 3 && parts[3] != "" {
				tls["server_name"] = parts[3]
			} else {
				tls["server_name"] = parts[0]
			}
			item["tls"] = tls
		}
	}
	return item
}

func normalizeOutboundForProtocol(outbound map[string]any, protocol string) map[string]any {
	if protocol == "" {
		return outbound
	}
	if strings.EqualFold(asString(outbound["type"]), protocol) {
		return outbound
	}
	outbound["type"] = protocol
	return outbound
}

func applyOutbound(config map[string]any, outbound map[string]any) {
	tag := extractNodeTag(config) + "-out"
	outbound["tag"] = tag
	outbounds, _ := config["outbounds"].([]any)
	if len(outbounds) == 0 {
		config["outbounds"] = []any{outbound}
	} else {
		outbounds[0] = outbound
		config["outbounds"] = outbounds
	}
	syncRouteOutboundTag(config, tag)
}

func findEditableOutboundIndex(config map[string]any) int {
	outbounds, _ := config["outbounds"].([]any)
	for index, raw := range outbounds {
		outbound, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		outType := strings.ToLower(asString(outbound["type"]))
		if outType == "direct" || outType == "block" || outType == "dns" {
			continue
		}
		if asString(outbound["server"]) != "" || asString(outbound["server_port"]) != "" || asString(outbound["port"]) != "" {
			return index
		}
	}
	return -1
}

func findMatchingOutboundIndex(config map[string]any, targetServer string) int {
	targetServer = normalizeServerAddress(targetServer)
	if targetServer == "" {
		return -1
	}
	outbounds, _ := config["outbounds"].([]any)
	for index, raw := range outbounds {
		outbound, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		outType := strings.ToLower(asString(outbound["type"]))
		if outType == "direct" || outType == "block" || outType == "dns" {
			continue
		}
		server := normalizeServerAddress(asString(outbound["server"]))
		if server != "" && strings.EqualFold(server, targetServer) {
			return index
		}
	}
	return -1
}

func normalizeServerAddress(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	return strings.ToLower(strings.TrimSpace(s))
}

func outboundAt(config map[string]any, index int) map[string]any {
	outbounds, _ := config["outbounds"].([]any)
	if index < 0 || index >= len(outbounds) {
		return map[string]any{}
	}
	outbound, _ := outbounds[index].(map[string]any)
	return outbound
}

func applyOutboundAt(config map[string]any, index int, input map[string]any) {
	outbounds, _ := config["outbounds"].([]any)
	if index < 0 || index >= len(outbounds) {
		applyOutbound(config, input)
		return
	}
	tag := extractNodeTag(config) + "-out"
	if current, ok := outbounds[index].(map[string]any); ok {
		if currentTag := asString(current["tag"]); currentTag != "" {
			tag = currentTag
		}
	}
	updated := cloneMap(input)
	updated["tag"] = tag
	outbounds[index] = updated
	config["outbounds"] = outbounds
	syncRouteOutboundTag(config, tag)
}

func syncRouteOutboundTag(config map[string]any, outTag string) {
	if outTag == "" {
		return
	}
	if route, ok := config["route"].(map[string]any); ok {
		if rules, ok := route["rules"].([]any); ok {
			for _, r := range rules {
				if rule, ok := r.(map[string]any); ok {
					rule["outbound"] = outTag
				}
			}
		}
	}
}

func firstFreePort(configs map[string]map[string]any, count int, start int) int {
	used := map[int]bool{}
	maxPort := 0
	for _, config := range configs {
		port := extractListenPort(config)
		if port > 0 {
			used[port] = true
			if port > maxPort {
				maxPort = port
			}
		}
	}
	candidate := nextStartPort(maxPort, start)
	if candidate > 0 && candidate+count-1 <= 65535 {
		return candidate
	}
	for port := start; port <= 65535-count+1; port++ {
		ok := true
		for i := 0; i < count; i++ {
			if used[port+i] {
				ok = false
				break
			}
		}
		if ok {
			return port
		}
	}
	return start
}

func nextStartPort(maxPort int, defaultStart int) int {
	if defaultStart <= 0 {
		defaultStart = 20000
	}
	if maxPort <= 0 {
		return defaultStart
	}
	next := maxPort + 1
	if next < defaultStart {
		return defaultStart
	}
	if next > 65535 {
		return 0
	}
	return next
}

func usedPortsFromConfigs(configs map[string]map[string]any) []int {
	ports := []int{}
	for _, config := range configs {
		port := extractListenPort(config)
		if port >= 1 && port <= 65535 {
			ports = append(ports, port)
		}
	}
	sort.Ints(ports)
	return ports
}

func maxListenPort(configs map[string]map[string]any) int {
	maxPort := 0
	for _, config := range configs {
		if port := extractListenPort(config); port > maxPort {
			maxPort = port
		}
	}
	return maxPort
}

func usedPortsInRange(configs map[string]map[string]any, start int, count int) []int {
	used := map[int]bool{}
	for _, port := range usedPortsFromConfigs(configs) {
		used[port] = true
	}
	conflicts := []int{}
	for port := start; port < start+count; port++ {
		if used[port] {
			conflicts = append(conflicts, port)
		}
	}
	return conflicts
}

func cleanMultiIPList(ips []string) []string {
	result := make([]string, 0, len(ips))
	seen := make(map[string]bool, len(ips))
	for _, item := range ips {
		ip := strings.TrimSpace(item)
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		result = append(result, ip)
	}
	return result
}

func uncreatedMultiIPs(ips []string, configs map[string]map[string]any) []string {
	clean := cleanMultiIPList(ips)
	created := createdMultiIPSet(configs)
	result := make([]string, 0, len(clean))
	for _, ip := range clean {
		if !created[ip] {
			result = append(result, ip)
		}
	}
	return result
}

func createdMultiIPSet(configs map[string]map[string]any) map[string]bool {
	result := map[string]bool{}
	for _, config := range configs {
		for _, ip := range nodeBoundIPs(config) {
			result[ip] = true
		}
	}
	return result
}

func nodeBoundIPs(config map[string]any) []string {
	seen := map[string]bool{}
	result := []string{}
	add := func(value any) {
		ip := strings.TrimSpace(asString(value))
		parsed := net.ParseIP(ip)
		if parsed == nil {
			return
		}
		ip = parsed.String()
		if ip == "" || seen[ip] {
			return
		}
		seen[ip] = true
		result = append(result, ip)
	}
	if inbounds, ok := config["inbounds"].([]any); ok {
		for _, item := range inbounds {
			if inbound, ok := item.(map[string]any); ok {
				add(inbound["listen"])
			}
		}
	}
	if outbounds, ok := config["outbounds"].([]any); ok {
		for _, item := range outbounds {
			if outbound, ok := item.(map[string]any); ok {
				add(outbound["inet4_bind_address"])
				add(outbound["inet6_bind_address"])
			}
		}
	}
	tag := extractNodeTag(config)
	if isPlainIPTag(tag) {
		add(tag)
	} else if parts := strings.Split(tag, "-"); len(parts) > 0 {
		add(parts[len(parts)-1])
	}
	return result
}

func extractNodeTag(config map[string]any) string {
	if inbounds, ok := config["inbounds"].([]any); ok && len(inbounds) > 0 {
		if inbound, ok := inbounds[0].(map[string]any); ok {
			return asString(inbound["tag"])
		}
	}
	return ""
}

func extractListenPort(config map[string]any) int {
	if inbounds, ok := config["inbounds"].([]any); ok && len(inbounds) > 0 {
		if inbound, ok := inbounds[0].(map[string]any); ok {
			return asInt(inbound["listen_port"])
		}
	}
	return 0
}

var (
	tagIPSuffixRegex    = regexp.MustCompile(`^(.*?)[-_](\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})(:\d+)?$`)
	tagNumSuffixRegex   = regexp.MustCompile(`^(.*?)(?:--|[-_])\d+$`)
	tagProtoSuffixRegex = regexp.MustCompile(`^(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})-[a-zA-Z0-9]+$`)
)

func extractGroupFromTag(tag string, createdAt string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return ""
	}
	if isPlainIPTag(tag) {
		if createdAt != "" {
			return createdAt
		}
		return tag
	}
	if m := tagIPSuffixRegex.FindStringSubmatch(tag); len(m) > 1 && strings.TrimSpace(m[1]) != "" {
		return strings.TrimSpace(m[1])
	}
	if m := tagNumSuffixRegex.FindStringSubmatch(tag); len(m) > 1 && strings.TrimSpace(m[1]) != "" {
		return strings.TrimSpace(m[1])
	}
	if m := tagProtoSuffixRegex.FindStringSubmatch(tag); len(m) > 1 {
		if createdAt != "" {
			return createdAt
		}
		return strings.TrimSpace(m[1])
	}
	return tag
}

func extractGroupName(config map[string]any) string {
	if groupName := metadataGroupName(config); groupName != "" {
		return groupName
	}
	tag := extractNodeTag(config)
	return extractGroupFromTag(tag, metadataCreatedAt(config))
}

func resolveTargetGroupName(configs map[string]map[string]any, group string) string {
	group = strings.TrimSpace(group)
	if group == "" {
		return ""
	}
	for _, config := range configs {
		if strings.EqualFold(extractGroupName(config), group) {
			return group
		}
	}
	for _, config := range configs {
		if strings.EqualFold(metadataGroupName(config), group) {
			return group
		}
	}
	for filename, config := range configs {
		tag := extractNodeTag(config)
		if strings.EqualFold(tag, group) || strings.EqualFold(filename, group) || strings.EqualFold(strings.TrimSuffix(filename, ".json"), group) {
			if g := extractGroupName(config); g != "" {
				return g
			}
		}
	}
	return group
}

func nodeMatchesGroup(config map[string]any, group string) bool {
	group = strings.TrimSpace(group)
	if group == "" {
		return false
	}
	if strings.EqualFold(extractGroupName(config), group) {
		return true
	}
	if strings.EqualFold(metadataGroupName(config), group) {
		return true
	}
	if created := metadataCreatedAt(config); created != "" && strings.EqualFold(created, group) {
		return true
	}
	return strings.EqualFold(extractNodeTag(config), group)
}

func isPlainIPTag(tag string) bool {
	parsed := net.ParseIP(strings.TrimSpace(tag))
	return parsed != nil && parsed.To4() != nil
}

func metadataCreatedAt(config map[string]any) string {
	metadata, _ := config["metadata"].(map[string]any)
	return asString(metadata["created_at"])
}

func metadataGroupName(config map[string]any) string {
	metadata, _ := config["metadata"].(map[string]any)
	return strings.TrimSpace(asString(metadata["group_name"]))
}

func safeFileName(value string) string {
	re := regexp.MustCompile(`[\\/:*?"<>|\s]+`)
	return strings.Trim(re.ReplaceAllString(value, "-"), "-")
}

func appendSlice(dst any, src any) []any {
	result, _ := dst.([]any)
	if items, ok := src.([]any); ok {
		return append(result, items...)
	}
	return result
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func nodeShareLink(node NodeSummary, vps VPS) string {
	host := nodePublicHost(node, vps)
	if host == "" || node.ListenPort <= 0 {
		return ""
	}
	tag := url.QueryEscape(firstNonEmpty(node.Tag, node.FileName))
	protocol := strings.ToLower(firstNonEmpty(node.Type, "vless"))
	switch protocol {
	case "anytls":
		values := url.Values{}
		values.Set("security", "tls")
		values.Set("sni", firstNonEmpty(node.ServerName, "bing.com"))
		values.Set("insecure", "1")
		values.Set("allowInsecure", "1")
		return fmt.Sprintf("anytls://%s@%s:%d?%s#%s", node.UUID, host, node.ListenPort, values.Encode(), tag)
	case "trojan":
		values := url.Values{}
		values.Set("security", "tls")
		values.Set("sni", firstNonEmpty(node.ServerName, host))
		values.Set("insecure", "1")
		values.Set("allowInsecure", "1")
		return fmt.Sprintf("trojan://%s@%s:%d?%s#%s", node.UUID, host, node.ListenPort, values.Encode(), tag)
	case "vmess":
		payload := map[string]string{
			"v":    "2",
			"ps":   firstNonEmpty(node.Tag, node.FileName),
			"add":  host,
			"port": strconv.Itoa(node.ListenPort),
			"id":   node.UUID,
			"aid":  "0",
			"net":  "tcp",
			"type": "none",
			"host": "",
			"path": "",
			"tls":  "",
		}
		data, _ := json.Marshal(payload)
		return "vmess://" + base64.StdEncoding.EncodeToString(data)
	case "socks":
		return socksInboundText(node, vps)
	case "vless":
		if node.Reality {
			values := url.Values{}
			values.Set("encryption", "none")
			values.Set("security", "reality")
			values.Set("sni", firstNonEmpty(node.ServerName, "stock.adobe.com"))
			values.Set("fp", firstNonEmpty(node.Fingerprint, "firefox"))
			values.Set("pbk", node.PublicKey)
			values.Set("sid", node.ShortID)
			values.Set("type", "tcp")
			if node.Flow != "" {
				values.Set("flow", node.Flow)
			}
			return fmt.Sprintf("vless://%s@%s:%d?%s#%s", node.UUID, host, node.ListenPort, values.Encode(), tag)
		}
		values := url.Values{}
		values.Set("encryption", "none")
		values.Set("security", "tls")
		values.Set("sni", firstNonEmpty(node.ServerName, host))
		values.Set("type", "tcp")
		values.Set("insecure", "1")
		values.Set("allowInsecure", "1")
		return fmt.Sprintf("vless://%s@%s:%d?%s#%s", node.UUID, host, node.ListenPort, values.Encode(), tag)
	default:
		return fmt.Sprintf("vless://%s@%s:%d#%s", node.UUID, host, node.ListenPort, tag)
	}
}

func socksInboundText(node NodeSummary, vps VPS) string {
	host := nodePublicHost(node, vps)
	if host == "" || node.ListenPort <= 0 {
		return ""
	}
	return strings.Join([]string{
		host,
		strconv.Itoa(node.ListenPort),
		node.Username,
		node.Password,
	}, ":")
}

func nodePublicHost(node NodeSummary, vps VPS) string {
	host := strings.TrimSpace(node.Host)
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = strings.TrimSpace(vps.Domain)
		if host == "" {
			host = strings.TrimSpace(vps.Address)
		}
	}
	return host
}

func firstSocksOutboundText(config map[string]any) string {
	outbounds, _ := config["outbounds"].([]any)
	for _, raw := range outbounds {
		outbound, ok := raw.(map[string]any)
		if !ok || strings.ToLower(asString(outbound["type"])) != "socks" {
			continue
		}
		return strings.Join([]string{
			asString(outbound["server"]),
			asString(outbound["server_port"]),
			asString(outbound["username"]),
			asString(outbound["password"]),
		}, ":")
	}
	return ""
}

func calculateExpireDate(tag string, createdAt string) string {
	return calculateExpireDateAt(tag, createdAt, time.Now())
}

func calculateExpireDateAt(tag string, createdAt string, now time.Time) string {
	base := now
	if created, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(createdAt)); err == nil {
		base = created.In(now.Location())
	}
	// IP 地址中的 12.22 等片段不是日期。只有非 IP 名称开头的明确日期标记才参与计算。
	match := regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})(?:[-_]|$)`).FindStringSubmatch(strings.TrimSpace(tag))
	if !isPlainIPTag(tag) && len(match) == 3 {
		month := asInt(match[1])
		day := asInt(match[2])
		if parsed := time.Date(base.Year(), time.Month(month), day, 0, 0, 0, 0, base.Location()); parsed.Month() == time.Month(month) && parsed.Day() == day {
			base = parsed
		}
	}
	return base.AddDate(0, 0, 30).Format("2006-01-02")
}

type xlsxImage struct {
	Row  int
	Col  int
	Name string
	Data []byte
	Text string
}

func buildSimpleXLSX(rows [][]string, images []xlsxImage) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hasImages := len(images) > 0
	files := map[string]string{
		"[Content_Types].xml":        contentTypesXML(hasImages),
		"_rels/.rels":                `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/workbook.xml":            `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="节点" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/worksheets/sheet1.xml":   simpleSheetXML(rows, hasImages),
	}
	if hasImages {
		files["xl/worksheets/_rels/sheet1.xml.rels"] = `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/drawing" Target="../drawings/drawing1.xml"/></Relationships>`
		files["xl/drawings/drawing1.xml"] = drawingXML(images)
		files["xl/drawings/_rels/drawing1.xml.rels"] = drawingRelsXML(images)
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			return nil, err
		}
	}
	for _, img := range images {
		if img.Name == "" {
			continue
		}
		data := img.Data
		if len(data) == 0 && strings.TrimSpace(img.Text) != "" {
			png, err := qrcode.Encode(img.Text, qrcode.Low, 140)
			if err != nil {
				return nil, err
			}
			data = png
		}
		if len(data) == 0 {
			continue
		}
		w, err := zw.Create("xl/media/" + img.Name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func contentTypesXML(hasImages bool) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/>`)
	if hasImages {
		b.WriteString(`<Default Extension="png" ContentType="image/png"/><Override PartName="/xl/drawings/drawing1.xml" ContentType="application/vnd.openxmlformats-officedocument.drawing+xml"/>`)
	}
	b.WriteString(`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`)
	return b.String()
}

func simpleSheetXML(rows [][]string, hasDrawing bool) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">`)
	if maxRowColumns(rows) >= 7 {
		b.WriteString(`<cols><col min="1" max="1" width="18" customWidth="1"/><col min="2" max="3" width="72" customWidth="1"/><col min="4" max="4" width="34" customWidth="1"/><col min="5" max="6" width="18" customWidth="1"/><col min="7" max="7" width="16" customWidth="1"/></cols><sheetData>`)
	} else {
		b.WriteString(`<cols><col min="1" max="1" width="18" customWidth="1"/><col min="2" max="2" width="72" customWidth="1"/><col min="3" max="3" width="34" customWidth="1"/><col min="4" max="4" width="18" customWidth="1"/><col min="5" max="5" width="16" customWidth="1"/></cols><sheetData>`)
	}
	for r, row := range rows {
		if r == 0 {
			b.WriteString(fmt.Sprintf(`<row r="%d">`, r+1))
		} else {
			b.WriteString(fmt.Sprintf(`<row r="%d" ht="95" customHeight="1">`, r+1))
		}
		for c, value := range row {
			cell := fmt.Sprintf("%s%d", excelColumnName(c), r+1)
			b.WriteString(fmt.Sprintf(`<c r="%s" t="inlineStr"><is><t>%s</t></is></c>`, cell, html.EscapeString(sanitizeXMLText(value))))
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData>`)
	if hasDrawing {
		b.WriteString(`<drawing r:id="rId1"/>`)
	}
	b.WriteString(`</worksheet>`)
	return b.String()
}

func excelColumnName(index int) string {
	if index < 0 {
		return "A"
	}
	var parts []byte
	for n := index; n >= 0; n = n/26 - 1 {
		parts = append(parts, byte('A'+n%26))
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return string(parts)
}

func sanitizeXMLText(value string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			return r
		case r >= 0x20 && r <= 0xD7FF:
			return r
		case r >= 0xE000 && r <= 0xFFFD:
			return r
		case r >= 0x10000 && r <= 0x10FFFF:
			return r
		default:
			return -1
		}
	}, value)
}

func maxRowColumns(rows [][]string) int {
	maxCols := 0
	for _, row := range rows {
		if len(row) > maxCols {
			maxCols = len(row)
		}
	}
	return maxCols
}

func drawingXML(images []xlsxImage) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><xdr:wsDr xmlns:xdr="http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">`)
	for i, img := range images {
		id := i + 1
		b.WriteString(fmt.Sprintf(`<xdr:oneCellAnchor><xdr:from><xdr:col>%d</xdr:col><xdr:colOff>90000</xdr:colOff><xdr:row>%d</xdr:row><xdr:rowOff>90000</xdr:rowOff></xdr:from><xdr:ext cx="1000000" cy="1000000"/><xdr:pic><xdr:nvPicPr><xdr:cNvPr id="%d" name="二维码%d"/><xdr:cNvPicPr/></xdr:nvPicPr><xdr:blipFill><a:blip r:embed="rId%d"/><a:stretch><a:fillRect/></a:stretch></xdr:blipFill><xdr:spPr><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></xdr:spPr></xdr:pic><xdr:clientData/></xdr:oneCellAnchor>`, img.Col, img.Row, id, id, id))
	}
	b.WriteString(`</xdr:wsDr>`)
	return b.String()
}

func drawingRelsXML(images []xlsxImage) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i, img := range images {
		b.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/%s"/>`, i+1, html.EscapeString(img.Name)))
	}
	b.WriteString(`</Relationships>`)
	return b.String()
}

func cloneMap(input map[string]any) map[string]any {
	output := map[string]any{}
	for k, v := range input {
		output[k] = v
	}
	return output
}

func randomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func randomHex(size int) string {
	b := make([]byte, size)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomLetters(size int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	if size <= 0 {
		size = 8
	}
	b := make([]byte, size)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}

func realityKeyPair() (string, string) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", ""
	}
	privateKey := base64.RawURLEncoding.EncodeToString(key.Bytes())
	publicKey := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	return privateKey, publicKey
}

func realityPublicKeyFromPrivate(privateKey string) string {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(privateKey))
	if err != nil {
		return ""
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
}

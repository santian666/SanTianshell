package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type MiddleAccelerationRequest struct {
	RelayURL      string `json:"relayUrl"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	EntryID       string `json:"entryId"`
	MiddleDomain  string `json:"middleDomain"`
	InputText     string `json:"inputText"`
	MaxRetry      int    `json:"maxRetry"`
	Concurrency   int    `json:"concurrency"`
	ReuseExisting bool   `json:"reuseExisting"`
}

type MiddleAccelerationItem struct {
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	TargetHost string `json:"targetHost"`
	TargetPort int    `json:"targetPort"`
	ListenPort int    `json:"listenPort"`
	Output     string `json:"output"`
	Success    bool   `json:"success"`
	Message    string `json:"message"`
}

type MiddleAccelerationResult struct {
	Success bool                     `json:"success"`
	Message string                   `json:"message"`
	Output  string                   `json:"output"`
	Logs    []string                 `json:"logs"`
	Items   []MiddleAccelerationItem `json:"items"`
}

type middleNodeLink struct {
	Raw      string
	Protocol string
	Name     string
	Host     string
	Port     int
	Suffix   string
	VMess    map[string]interface{}
}

type yixunResponse struct {
	Code   int             `json:"code"`
	Msg    string          `json:"msg"`
	Data   json.RawMessage `json:"data"`
	Count  int             `json:"count"`
	Total  int             `json:"total"`
	Logout bool            `json:"logout"`
}

type yixunRule struct {
	ID            int                    `json:"id"`
	Name          string                 `json:"name"`
	DisplayName   string                 `json:"display_name"`
	ListenPort    int                    `json:"listen_port"`
	Status        string                 `json:"status"`
	DeviceGroupIn interface{}            `json:"device_group_in"`
	Config        interface{}            `json:"config"`
	Raw           map[string]interface{} `json:"-"`
}

func (s *Service) RunMiddleAcceleration(req MiddleAccelerationRequest) (MiddleAccelerationResult, error) {
	return s.RunMiddleAccelerationWithLogger(req, nil)
}

func (s *Service) RunMiddleAccelerationWithLogger(req MiddleAccelerationRequest, emit func(string)) (MiddleAccelerationResult, error) {
	result := MiddleAccelerationResult{Logs: []string{}}
	var logMu sync.Mutex
	logf := func(format string, args ...interface{}) {
		line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, args...)
		logMu.Lock()
		result.Logs = append(result.Logs, line)
		logMu.Unlock()
		if emit != nil {
			emit(line)
		}
	}

	req.RelayURL = strings.TrimSpace(req.RelayURL)
	req.Username = strings.TrimSpace(req.Username)
	req.EntryID = strings.TrimSpace(req.EntryID)
	req.MiddleDomain = strings.TrimSpace(req.MiddleDomain)
	if req.MaxRetry <= 0 {
		req.MaxRetry = 5
	}
	if req.Concurrency <= 0 {
		req.Concurrency = 10
	}
	if req.Concurrency > 50 {
		req.Concurrency = 50
	}
	if req.RelayURL == "" || req.Username == "" || req.Password == "" || req.EntryID == "" || req.MiddleDomain == "" {
		return result, errors.New("中转网址、账号、密码、入口ID、中转域名不能为空")
	}
	baseURL, err := normalizeYixunBaseURL(req.RelayURL)
	if err != nil {
		return result, err
	}
	entryID, err := strconv.Atoi(req.EntryID)
	if err != nil {
		return result, errors.New("入口ID必须是数字")
	}

	nodes, parseLogs := parseMiddleNodeLinks(req.InputText)
	logMu.Lock()
	result.Logs = append(result.Logs, parseLogs...)
	logMu.Unlock()
	if emit != nil {
		for _, line := range parseLogs {
			emit(line)
		}
	}
	if len(nodes) == 0 {
		return result, errors.New("没有识别到可加速的协议链接")
	}
	logf("识别到 %d 条协议链接，开始登录后台", len(nodes))

	client := &yixunClient{baseURL: baseURL, client: &http.Client{Timeout: 25 * time.Second}}
	if err := client.login(req.Username, req.Password); err != nil {
		logf("登录失败：%v", err)
		result.Message = err.Error()
		return result, nil
	}
	logf("后台登录成功")
	if err := client.ensureEntry(entryID); err != nil {
		logf("入口检查失败：%v", err)
		result.Message = err.Error()
		return result, nil
	}
	logf("入口ID %d 已确认", entryID)

	var (
		reusedRules = make(map[int]yixunRule)
		toCreate    []int
	)
	if req.ReuseExisting {
		list, err := client.forwardList()
		if err == nil && len(list) > 0 {
			for index, node := range nodes {
				var best yixunRule
				found := false
				for _, rule := range list {
					if ruleMatches(entryID, node, rule) && ruleIsNormal(rule) {
						if !found || rule.ID > best.ID {
							best = rule
							found = true
						}
					}
				}
				if found {
					reusedRules[index] = best
					logf("%s 匹配到已有正常规则 (ID %d, 端口 %d)，自动复用", node.Name, best.ID, best.ListenPort)
				} else {
					toCreate = append(toCreate, index)
				}
			}
		} else {
			for index := range nodes {
				toCreate = append(toCreate, index)
			}
		}
	} else {
		for index := range nodes {
			toCreate = append(toCreate, index)
		}
	}

	normalRules := make([]yixunRule, len(nodes))
	ruleErrors := make([]error, len(nodes))
	for index, rule := range reusedRules {
		normalRules[index] = rule
		ruleErrors[index] = nil
	}

	if len(toCreate) > 0 {
		const batchChunkSize = 25
		totalToCreate := len(toCreate)
		totalBatches := (totalToCreate + batchChunkSize - 1) / batchChunkSize

		for batchIdx := 0; batchIdx < totalBatches; batchIdx++ {
			start := batchIdx * batchChunkSize
			end := start + batchChunkSize
			if end > totalToCreate {
				end = totalToCreate
			}
			chunkIndices := toCreate[start:end]

			var ruleLines []string
			chunkNodes := make([]middleNodeLink, len(chunkIndices))
			for i, index := range chunkIndices {
				node := nodes[index]
				chunkNodes[i] = node
				cleanName := sanitizeYixunRuleName(node.Name)
				if cleanName == "" {
					cleanName = fmt.Sprintf("%s:%d", node.Host, node.Port)
				}
				ruleLines = append(ruleLines, fmt.Sprintf("%s##%s#%d", cleanName, node.Host, node.Port))
			}
			content := strings.Join(ruleLines, "\n")

			if totalBatches > 1 {
				logf("第 %d/%d 批：提交 %d 条转发规则 (共 %d 条待建)", batchIdx+1, totalBatches, len(chunkIndices), totalToCreate)
			} else {
				logf("需要新建 %d 条转发规则，开始批量导入", totalToCreate)
			}

			if err := client.batchCreate(entryID, content); err != nil {
				logf("第 %d 批导入失败：%v", batchIdx+1, err)
				for _, index := range chunkIndices {
					ruleErrors[index] = fmt.Errorf("批量导入失败：%w", err)
				}
				if totalBatches == 1 {
					result.Message = err.Error()
					return result, nil
				}
				continue
			}

			if totalBatches > 1 {
				logf("第 %d/%d 批：导入已提交，开始确认规则状态", batchIdx+1, totalBatches)
			} else {
				logf("批量导入已提交，开始批量获取规则状态")
			}

			createdNormalRules, createdRuleErrors := client.waitNormalRules(entryID, chunkNodes, req.MaxRetry, req.Concurrency, logf)
			for i, index := range chunkIndices {
				normalRules[index] = createdNormalRules[i]
				ruleErrors[index] = createdRuleErrors[i]
			}

			if batchIdx < totalBatches-1 {
				time.Sleep(300 * time.Millisecond)
			}
		}
	} else {
		logf("全部 %d 条规则均已复用已有正常规则，跳过新建", len(nodes))
	}
	items := make([]MiddleAccelerationItem, len(nodes))
	for index, node := range nodes {
		item := MiddleAccelerationItem{Name: node.Name, Protocol: node.Protocol, TargetHost: node.Host, TargetPort: node.Port}
		if ruleErrors[index] != nil {
			item.Message = ruleErrors[index].Error()
			logf("%s 失败：%v", node.Name, ruleErrors[index])
			items[index] = item
			continue
		}
		item.ListenPort = normalRules[index].ListenPort
		output, err := replaceMiddleNodeEndpoint(node, req.MiddleDomain, normalRules[index].ListenPort)
		if err != nil {
			item.Message = err.Error()
			logf("%s 链接替换失败：%v", node.Name, err)
			items[index] = item
			continue
		}
		item.Output = output
		item.Success = true
		item.Message = "正常"
		items[index] = item
	}

	outputs := make([]string, 0, len(items))
	for _, item := range items {
		result.Items = append(result.Items, item)
		if item.Success && item.Output != "" {
			outputs = append(outputs, item.Output)
		}
	}
	result.Output = strings.Join(outputs, "\n")
	successCount := len(outputs)
	result.Success = successCount == len(nodes)
	result.Message = fmt.Sprintf("完成：成功 %d 条，失败 %d 条", successCount, len(nodes)-successCount)
	logf("%s", result.Message)
	return result, nil
}

func normalizeYixunBaseURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return "", errors.New("中转网址格式错误")
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func parseMiddleNodeLinks(text string) ([]middleNodeLink, []string) {
	var nodes []middleNodeLink
	var logs []string
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		node, err := parseMiddleNodeLink(line)
		if err != nil {
			logs = append(logs, fmt.Sprintf("%s 第 %d 行跳过：%v", time.Now().Format("15:04:05"), i+1, err))
			continue
		}
		nodes = append(nodes, node)
	}
	return nodes, logs
}

func parseMiddleNodeLink(line string) (middleNodeLink, error) {
	lower := strings.ToLower(line)
	if strings.HasPrefix(lower, "vmess://") {
		return parseMiddleVMess(line)
	}
	if !strings.Contains(line, "://") {
		return parseMiddlePlainProxy(line)
	}
	u, err := url.Parse(line)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return middleNodeLink{}, errors.New("不是有效协议链接")
	}
	port, _ := strconv.Atoi(u.Port())
	if port <= 0 {
		return middleNodeLink{}, errors.New("协议链接缺少端口")
	}
	name := u.Fragment
	if decoded, err := url.QueryUnescape(u.EscapedFragment()); err == nil && decoded != "" {
		name = decoded
	}
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("%s:%d", u.Hostname(), port)
	}
	return middleNodeLink{Raw: line, Protocol: strings.ToLower(u.Scheme), Name: name, Host: u.Hostname(), Port: port}, nil
}

func parseMiddlePlainProxy(line string) (middleNodeLink, error) {
	value := strings.TrimSpace(line)
	host := ""
	rest := ""
	if strings.HasPrefix(value, "[") {
		end := strings.Index(value, "]")
		if end <= 1 || len(value) <= end+2 || value[end+1] != ':' {
			return middleNodeLink{}, errors.New("代理地址格式应为 [IPv6]:端口[:账号][:密码]")
		}
		host = value[1:end]
		rest = value[end+2:]
	} else {
		parts := strings.SplitN(value, ":", 2)
		if len(parts) != 2 {
			return middleNodeLink{}, errors.New("代理地址格式应为 IP:端口[:账号][:密码]")
		}
		host = strings.TrimSpace(parts[0])
		rest = parts[1]
	}
	parts := strings.SplitN(rest, ":", 3)
	port, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if host == "" || err != nil || port <= 0 || port > 65535 {
		return middleNodeLink{}, errors.New("代理地址缺少有效主机或端口")
	}
	suffix := ""
	if len(parts) > 1 {
		suffix = ":" + strings.Join(parts[1:], ":")
	}
	return middleNodeLink{
		Raw:      line,
		Protocol: "plain",
		Name:     net.JoinHostPort(host, strconv.Itoa(port)),
		Host:     host,
		Port:     port,
		Suffix:   suffix,
	}, nil
}

func parseMiddleVMess(line string) (middleNodeLink, error) {
	raw := strings.TrimPrefix(line, "vmess://")
	raw = strings.TrimPrefix(raw, "VMess://")
	payload, err := decodeBase64Text(raw)
	if err != nil {
		return middleNodeLink{}, errors.New("vmess 解码失败")
	}
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		return middleNodeLink{}, errors.New("vmess JSON 无效")
	}
	host := strings.TrimSpace(fmt.Sprint(data["add"]))
	port, _ := strconv.Atoi(strings.TrimSpace(fmt.Sprint(data["port"])))
	if host == "" || port <= 0 {
		return middleNodeLink{}, errors.New("vmess 缺少地址或端口")
	}
	name := strings.TrimSpace(fmt.Sprint(data["ps"]))
	if name == "" {
		name = fmt.Sprintf("%s:%d", host, port)
	}
	return middleNodeLink{Raw: line, Protocol: "vmess", Name: name, Host: host, Port: port, VMess: data}, nil
}

func decodeBase64Text(value string) (string, error) {
	value = strings.ReplaceAll(value, "-", "+")
	value = strings.ReplaceAll(value, "_", "/")
	if mod := len(value) % 4; mod != 0 {
		value += strings.Repeat("=", 4-mod)
	}
	data, err := base64.StdEncoding.DecodeString(value)
	return string(data), err
}

func replaceMiddleNodeEndpoint(node middleNodeLink, host string, port int) (string, error) {
	if node.Protocol == "plain" {
		return net.JoinHostPort(host, strconv.Itoa(port)) + node.Suffix, nil
	}
	if node.Protocol == "vmess" {
		payload := make(map[string]interface{}, len(node.VMess))
		for k, v := range node.VMess {
			payload[k] = v
		}
		payload["add"] = host
		payload["port"] = strconv.Itoa(port)
		data, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		return "vmess://" + base64.StdEncoding.EncodeToString(data), nil
	}
	u, err := url.Parse(node.Raw)
	if err != nil {
		return "", err
	}
	u.Host = net.JoinHostPort(host, strconv.Itoa(port))
	return u.String(), nil
}

type yixunClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func (c *yixunClient) login(username, password string) error {
	var resp yixunResponse
	err := c.do("POST", "/api/v1/auth/login", map[string]string{
		"username": username,
		"password": password,
	}, &resp)
	if err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("登录失败：%s", resp.Msg)
	}
	var token string
	if err := json.Unmarshal(resp.Data, &token); err != nil || token == "" {
		return errors.New("登录成功但未返回 token")
	}
	c.token = token
	return nil
}

func (c *yixunClient) ensureEntry(entryID int) error {
	var resp yixunResponse
	if err := c.do("GET", "/api/v1/user/devicegroup", nil, &resp); err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("读取入口失败：%s", resp.Msg)
	}
	var groups []map[string]interface{}
	_ = json.Unmarshal(resp.Data, &groups)
	for _, group := range groups {
		id, _ := intFromAny(group["id"])
		groupType := strings.TrimSpace(fmt.Sprint(group["type"]))
		if id == entryID && groupType == "DeviceGroupType_Inbound" {
			return nil
		}
	}
	return fmt.Errorf("未找到入口ID %d", entryID)
}

func (c *yixunClient) batchCreate(entryID int, content string) error {
	body := map[string]interface{}{
		"device_group_in": entryID,
		"content":         content,
		"config":          nil,
	}
	var resp yixunResponse
	if err := c.do("POST", "/api/v1/user/forward/batch_create", body, &resp); err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("批量导入失败：%s", resp.Msg)
	}
	return nil
}

func (c *yixunClient) waitNormalRules(entryID int, nodes []middleNodeLink, maxRetry int, concurrency int, logf func(string, ...interface{})) ([]yixunRule, []error) {
	rules := make([]yixunRule, len(nodes))
	errs := make([]error, len(nodes))
	pending := make(map[int]struct{}, len(nodes))
	for i := range nodes {
		pending[i] = struct{}{}
	}
	for attempt := 1; attempt <= maxRetry && len(pending) > 0; attempt++ {
		if delay := middleStatusRetryDelay(attempt); delay > 0 {
			time.Sleep(delay)
		}
		list, err := c.forwardList()
		if err != nil {
			for index := range pending {
				errs[index] = err
			}
			logf("第 %d 次批量读取规则失败：%v", attempt, err)
			continue
		}
		matched := matchLatestRules(entryID, nodes, list)
		resetRules := make([]yixunRule, 0)
		resetNames := make(map[int]string)
		for index := range pending {
			node := nodes[index]
			rule, ok := matched[index]
			if !ok {
				errs[index] = fmt.Errorf("未找到规则：%s", node.Name)
				logf("%s 第 %d 次状态：未找到规则", node.Name, attempt)
				continue
			}
			rules[index] = rule
			status := strings.TrimSpace(rule.Status)
			if ruleIsNormal(rule) {
				errs[index] = nil
				delete(pending, index)
				logf("%s 正常，新端口 %d", node.Name, rule.ListenPort)
				continue
			}
			errs[index] = fmt.Errorf("状态未正常，最后状态：%s，端口：%d", emptyString(status, "未知"), rule.ListenPort)
			logf("%s 第 %d 次状态：%s，端口：%d", node.Name, attempt, emptyString(status, "未知"), rule.ListenPort)
			if ruleNeedsPortReset(rule) && rule.ID > 0 {
				resetRules = append(resetRules, rule)
				resetNames[rule.ID] = node.Name
			}
		}
		c.resetListenPorts(resetRules, resetNames, concurrency, logf)
	}
	for index := range pending {
		if errs[index] == nil {
			last := rules[index]
			errs[index] = fmt.Errorf("状态未正常，最后状态：%s，端口：%d", emptyString(last.Status, "未知"), last.ListenPort)
		}
	}
	return rules, errs
}

func middleStatusRetryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 0
	case 2:
		return 2 * time.Second
	case 3:
		return 5 * time.Second
	default:
		return 10 * time.Second
	}
}

func matchLatestRules(entryID int, nodes []middleNodeLink, list []yixunRule) map[int]yixunRule {
	matched := make(map[int]yixunRule, len(nodes))
	for _, rule := range list {
		for index, node := range nodes {
			if !ruleMatches(entryID, node, rule) {
				continue
			}
			if old, ok := matched[index]; !ok || rule.ID > old.ID {
				matched[index] = rule
			}
		}
	}
	return matched
}

func ruleIsNormal(rule yixunRule) bool {
	status := strings.TrimSpace(rule.Status)
	return rule.ListenPort > 0 && (status == "" || status == "ForwardRuleStatus_Normal" || status == "正常")
}

func ruleNeedsPortReset(rule yixunRule) bool {
	status := strings.TrimSpace(rule.Status)
	return rule.ListenPort <= 0 || status == "ForwardRuleStatus_Failed" || status == "ForwardRuleStatus_Disabled" || status == "同步失败" || status == "不可用"
}

func (c *yixunClient) resetListenPorts(rules []yixunRule, names map[int]string, concurrency int, logf func(string, ...interface{})) {
	if len(rules) == 0 {
		return
	}
	if concurrency <= 0 {
		concurrency = 5
	}
	if concurrency > 20 {
		concurrency = 20
	}
	seen := map[int]struct{}{}
	unique := make([]yixunRule, 0, len(rules))
	for _, rule := range rules {
		if _, ok := seen[rule.ID]; ok {
			continue
		}
		seen[rule.ID] = struct{}{}
		unique = append(unique, rule)
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, rule := range unique {
		rule := rule
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			name := names[rule.ID]
			if err := c.resetListenPort(rule); err != nil {
				logf("%s 重置监听端口失败：%v", name, err)
				return
			}
			logf("%s 已清空监听端口，等待重新分配", name)
		}()
	}
	wg.Wait()
}

func (c *yixunClient) forwardList() ([]yixunRule, error) {
	const pageSize = 1000
	var allRules []yixunRule
	seenIDs := make(map[int]struct{})

	for page := 1; page <= 50; page++ {
		var resp yixunResponse
		path := fmt.Sprintf("/api/v1/user/forward?page=%d&size=%d", page, pageSize)
		if err := c.do("GET", path, nil, &resp); err != nil {
			if page == 1 {
				return nil, err
			}
			break
		}
		if resp.Code != 0 {
			if page == 1 {
				return nil, fmt.Errorf("读取规则列表失败：%s", resp.Msg)
			}
			break
		}
		rules := parseYixunRules(resp.Data)
		if len(rules) == 0 {
			break
		}

		newCount := 0
		for _, r := range rules {
			if r.ID > 0 {
				if _, exists := seenIDs[r.ID]; exists {
					continue
				}
				seenIDs[r.ID] = struct{}{}
			}
			allRules = append(allRules, r)
			newCount++
		}

		if newCount == 0 {
			break
		}

		total := resp.Count
		if total <= 0 {
			total = resp.Total
		}
		if total > 0 && len(allRules) >= total {
			break
		}
		if len(rules) < pageSize {
			break
		}
	}
	return allRules, nil
}

func (c *yixunClient) resetListenPort(rule yixunRule) error {
	if rule.ID <= 0 {
		return errors.New("规则ID无效")
	}
	body := rule.Raw
	if body == nil {
		body = map[string]interface{}{"id": rule.ID}
	}
	body["listen_port"] = 0
	var resp yixunResponse
	if err := c.do("POST", fmt.Sprintf("/api/v1/user/forward/%d", rule.ID), body, &resp); err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("保存规则失败：%s", resp.Msg)
	}
	return nil
}

func (c *yixunClient) do(method, path string, body interface{}, out interface{}) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", c.token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := readLimitedHTTPBody(resp.Body, 8<<20)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d：%s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("返回数据解析失败：%v", err)
	}
	return nil
}

func parseYixunRules(data json.RawMessage) []yixunRule {
	var rawItems []map[string]interface{}
	if err := json.Unmarshal(data, &rawItems); err != nil {
		var wrapper map[string]interface{}
		if err2 := json.Unmarshal(data, &wrapper); err2 == nil {
			for _, key := range []string{"list", "records", "data", "rows"} {
				if listVal, ok := wrapper[key]; ok {
					if listBytes, err3 := json.Marshal(listVal); err3 == nil {
						var items []map[string]interface{}
						if err4 := json.Unmarshal(listBytes, &items); err4 == nil && len(items) > 0 {
							rawItems = items
							break
						}
					}
				}
			}
		}
	}
	if len(rawItems) == 0 {
		return nil
	}
	rules := make([]yixunRule, 0, len(rawItems))
	for _, raw := range rawItems {
		itemBytes, _ := json.Marshal(raw)
		var rule yixunRule
		_ = json.Unmarshal(itemBytes, &rule)
		rule.Raw = raw
		rules = append(rules, rule)
	}
	return rules
}

func ruleMatches(entryID int, node middleNodeLink, rule yixunRule) bool {
	if id, ok := intFromAny(rule.DeviceGroupIn); ok && id != entryID {
		return false
	}
	name := strings.TrimSpace(rule.Name)
	if name == "" {
		name = strings.TrimSpace(strings.Split(rule.DisplayName, " (#")[0])
	}
	cleanNodeName := sanitizeYixunRuleName(node.Name)
	if name != "" && name != node.Name && !strings.Contains(name, node.Name) &&
		name != cleanNodeName && !strings.Contains(name, cleanNodeName) {
		return false
	}
	return ruleConfigHasDest(rule.Config, node.Host, node.Port)
}

func sanitizeYixunRuleName(name string) string {
	name = strings.ReplaceAll(name, "\r", " ")
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.ReplaceAll(name, "\t", " ")
	name = strings.ReplaceAll(name, "#", "-")
	name = strings.TrimSpace(name)
	for strings.Contains(name, "  ") {
		name = strings.ReplaceAll(name, "  ", " ")
	}
	return name
}

func ruleConfigHasDest(config interface{}, host string, port int) bool {
	var data map[string]interface{}
	switch v := config.(type) {
	case string:
		_ = json.Unmarshal([]byte(v), &data)
	case map[string]interface{}:
		data = v
	default:
		raw, _ := json.Marshal(v)
		_ = json.Unmarshal(raw, &data)
	}
	if len(data) == 0 {
		return true
	}
	target := net.JoinHostPort(host, strconv.Itoa(port))
	targetPlain := fmt.Sprintf("%s:%d", host, port)
	values, _ := data["dest"].([]interface{})
	if len(values) == 0 {
		return strings.Contains(fmt.Sprint(config), targetPlain)
	}
	for _, value := range values {
		text := strings.TrimSpace(fmt.Sprint(value))
		if text == target || text == targetPlain {
			return true
		}
	}
	return false
}

func intFromAny(value interface{}) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		i, err := v.Int64()
		return int(i), err == nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(v))
		return i, err == nil
	default:
		return 0, false
	}
}

func emptyString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

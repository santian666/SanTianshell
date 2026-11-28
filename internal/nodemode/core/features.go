package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type AutoDeleteSettings struct {
	Enabled     bool   `json:"enabled"`
	FailureDays int    `json:"failure_days"`
	CheckTime   string `json:"check_time"`
	Timezone    string `json:"timezone"`
	Concurrency int    `json:"concurrency"`
}

type HealthStatus struct {
	ID            int    `json:"id"`
	VPSAlias      string `json:"vps_alias"`
	VPSIP         string `json:"vps_ip"`
	NodeGroup     string `json:"node_group"`
	NodeTag       string `json:"node_tag"`
	Filename      string `json:"filename"`
	InboundPort   int    `json:"inbound_port"`
	FailCount     int    `json:"fail_count"`
	LastStatus    string `json:"last_status"`
	LastMessage   string `json:"last_message"`
	LastCheckedAt string `json:"last_checked_at"`
}

type AutoDeleteRunResult struct {
	Success bool           `json:"success"`
	Message string         `json:"message"`
	Total   int            `json:"total"`
	Failed  int            `json:"failed"`
	Deleted int            `json:"deleted"`
	Items   []HealthStatus `json:"items"`
}

type NodeConnectionRecord struct {
	ID            int    `json:"id"`
	IP            string `json:"ip"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	RenewalDate   string `json:"renewal_date"`
	Remark        string `json:"remark"`
	Country       string `json:"country"`
	ConnectionStr string `json:"connection_str"`
	CreatedAt     string `json:"created_at"`
}

type ImportResult struct {
	Success  bool     `json:"success"`
	Message  string   `json:"message"`
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors"`
}

type RuleGroup struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Rules       []string `json:"rules"`
	Builtin     bool     `json:"builtin"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

type ProxyDetectRequest struct {
	Target      string `json:"target"`
	Nodes       string `json:"nodes"`
	Concurrency int    `json:"concurrency"`
	TestURL     string `json:"testUrl"`
}

type ProxyDetectResult struct {
	Line           string `json:"line"`
	Protocol       string `json:"protocol"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Country        string `json:"country"`
	ISP            string `json:"isp"`
	Success        bool   `json:"success"`
	Latency        int64  `json:"latency"`
	TCPLatency     int64  `json:"tcpLatency"`
	URLLatency     int64  `json:"urlLatency"`
	Message        string `json:"message"`
	DetectTarget   string `json:"detectTarget"`
	DetectLocation string `json:"detectLocation"`
	DetectExitIP   string `json:"detectExitIp"`
}

type ProxyProgressCallback func(index int, total int, item ProxyDetectResult)

type parsedProxyLine struct {
	Result   ProxyDetectResult
	Username string
	Password string
	SNI      string
	Security string
	Network  string
	Path     string
	RawURL   string
}

type proxyDetectDialer interface {
	Dial(network, address string) (net.Conn, error)
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

const proxyDetectURL = "https://cp.cloudflare.com/generate_204"

type OutboundIPResult struct {
	VPSAlias   string `json:"vps_alias"`
	NodeTag    string `json:"node_tag"`
	Filename   string `json:"filename"`
	OutboundIP string `json:"outbound_ip"`
	Success    bool   `json:"success"`
	Message    string `json:"message"`
}

const dbSecretPrefix = "DPAPI:"

func protectDBSecret(plain string) string {
	if plain == "" || strings.HasPrefix(plain, dbSecretPrefix) {
		return plain
	}
	sealed, err := protectMiddleSettings([]byte(plain))
	if err != nil || len(sealed) == 0 {
		return plain
	}
	return dbSecretPrefix + base64.StdEncoding.EncodeToString(sealed)
}

func unprotectDBSecret(stored string) string {
	if !strings.HasPrefix(stored, dbSecretPrefix) {
		return stored
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, dbSecretPrefix))
	if err != nil {
		return stored
	}
	plain, err := unprotectMiddleSettings(raw)
	if err != nil {
		return stored
	}
	return string(plain)
}

func (s *Service) GetAutoDeleteSettings() (AutoDeleteSettings, error) {
	db, release, err := s.acquireDB()
	if err != nil {
		return AutoDeleteSettings{}, err
	}
	defer release()
	settings := defaultAutoDeleteSettings()
	var data string
	err = db.QueryRow(`select value from app_settings where key='auto_delete'`).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return AutoDeleteSettings{}, err
	}
	_ = json.Unmarshal([]byte(data), &settings)
	normalizeAutoDeleteSettings(&settings)
	return settings, nil
}

func (s *Service) SaveAutoDeleteSettings(settings AutoDeleteSettings) (AutoDeleteSettings, error) {
	normalizeAutoDeleteSettings(&settings)
	db, release, err := s.acquireDB()
	if err != nil {
		return settings, err
	}
	defer release()
	data, _ := json.Marshal(settings)
	_, err = db.Exec(`insert into app_settings(key,value,updated_at) values('auto_delete',?,?)
		on conflict(key) do update set value=excluded.value,updated_at=excluded.updated_at`, string(data), nowText())
	return settings, err
}

func (s *Service) RunAutoDeleteCheck(vpsAlias string) (AutoDeleteRunResult, error) {
	return s.runAutoDeleteCheck(vpsAlias, false)
}

func (s *Service) runAutoDeleteCheck(vpsAlias string, scheduled bool) (AutoDeleteRunResult, error) {
	if !s.autoDeleteRunMu.TryLock() {
		return AutoDeleteRunResult{Success: false, Message: "自动删除任务正在运行"}, nil
	}
	defer s.autoDeleteRunMu.Unlock()

	settings, err := s.GetAutoDeleteSettings()
	if err != nil {
		return AutoDeleteRunResult{}, err
	}
	if scheduled && !settings.Enabled {
		return AutoDeleteRunResult{Success: true, Message: "自动删除未启用"}, nil
	}

	nodes, err := s.GetNodes(vpsAlias)
	if err != nil {
		return AutoDeleteRunResult{}, err
	}
	db, release, err := s.acquireDB()
	if err != nil {
		return AutoDeleteRunResult{}, err
	}
	defer release()
	now := nowText()
	failedItems := []HealthStatus{}
	deleteByAlias := map[string][]string{}
	deleted := 0
	checks := make([]CheckResult, len(nodes))
	jobs := make(chan int)
	workerCount := settings.Concurrency
	if workerCount > len(nodes) {
		workerCount = len(nodes)
	}
	var checkWG sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		checkWG.Add(1)
		go func() {
			defer checkWG.Done()
			for index := range jobs {
				node := nodes[index]
				checks[index] = s.TestTCP(firstNonEmpty(node.OutboundIP, "127.0.0.1"), node.OutboundPort)
			}
		}()
	}
	for index := range nodes {
		jobs <- index
	}
	close(jobs)
	checkWG.Wait()
	tx, txErr := db.Begin()
	if txErr != nil {
		return AutoDeleteRunResult{}, txErr
	}
	upsertStmt, prepErr := tx.Prepare(`insert into health_status(vps_alias,vps_ip,node_group,node_tag,filename,inbound_port,fail_count,last_status,last_message,last_checked_at)
			values(?,?,?,?,?,?,?,?,?,?)
			on conflict(vps_alias,filename) do update set vps_ip=excluded.vps_ip,node_group=excluded.node_group,node_tag=excluded.node_tag,
			inbound_port=excluded.inbound_port,fail_count=excluded.fail_count,last_status=excluded.last_status,last_message=excluded.last_message,last_checked_at=excluded.last_checked_at`)
	if prepErr != nil {
		_ = tx.Rollback()
		return AutoDeleteRunResult{}, prepErr
	}
	for index, node := range nodes {
		status := "ok"
		message := "正常"
		failCount := 0
		result := checks[index]
		if node.OutboundIP == "" || node.OutboundPort <= 0 || !result.Success {
			status = "failed"
			message = firstNonEmpty(result.Message, "落地地址不可用")
			failCount = 1
		}
		vps, _ := s.getVPS(node.VPSAlias)
		if status == "failed" {
			var oldCount int
			_ = tx.QueryRow(`select coalesce(fail_count,0) from health_status where vps_alias=? and filename=?`, node.VPSAlias, node.FileName).Scan(&oldCount)
			failCount += oldCount
		}
		_, _ = upsertStmt.Exec(
			node.VPSAlias, vps.Address, groupFromTag(node.Tag), node.Tag, node.FileName, node.ListenPort, failCount, status, message, now)
		if status == "failed" {
			item := HealthStatus{VPSAlias: node.VPSAlias, VPSIP: vps.Address, NodeGroup: groupFromTag(node.Tag), NodeTag: node.Tag, Filename: node.FileName, InboundPort: node.ListenPort, FailCount: failCount, LastStatus: status, LastMessage: message, LastCheckedAt: now}
			failedItems = append(failedItems, item)
			if !scheduled || failCount >= settings.FailureDays {
				deleteByAlias[node.VPSAlias] = append(deleteByAlias[node.VPSAlias], node.FileName)
			}
		}
	}
	_ = upsertStmt.Close()
	if err := tx.Commit(); err != nil {
		return AutoDeleteRunResult{}, err
	}
	for alias, filenames := range deleteByAlias {
		count, err := s.moveNodesToDeletedRecords(alias, filenames)
		if err != nil {
			return AutoDeleteRunResult{}, err
		}
		deleted += count
		delTx, delErr := db.Begin()
		if delErr == nil {
			for _, filename := range filenames {
				_, _ = delTx.Exec(`delete from health_status where vps_alias=? and filename=?`, alias, filename)
			}
			_ = delTx.Commit()
		}
	}
	message := fmt.Sprintf("检测完成：检查 %d 个，失败 %d 个，转入删除记录 %d 个。本次只修改本地配置，未改动远端服务器", len(nodes), len(failedItems), deleted)
	if scheduled {
		message = fmt.Sprintf("定时检测完成：检查 %d 个，失败 %d 个，转入删除记录 %d 个。本次只修改本地配置，未改动远端服务器", len(nodes), len(failedItems), deleted)
	}
	return AutoDeleteRunResult{Success: true, Message: message, Total: len(nodes), Failed: len(failedItems), Deleted: deleted, Items: failedItems}, nil
}

func (s *Service) moveNodesToDeletedRecords(alias string, filenames []string) (int, error) {
	unlock := s.lockNodeConfigAliases(alias)
	defer unlock()
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, filename := range filenames {
		config, ok := configs[filename]
		if !ok {
			continue
		}
		if err := s.createDeletedRecord(alias, filename, config); err != nil {
			return deleted, err
		}
		delete(configs, filename)
		deleted++
	}
	if deleted == 0 {
		return 0, nil
	}
	return deleted, s.saveNodeConfigMap(alias, configs)
}

func (s *Service) StartAutoDeleteScheduler(ctx context.Context) {
	s.autoDeleteSchedulerOnce.Do(func() {
		go s.autoDeleteScheduler(ctx)
	})
}

func (s *Service) autoDeleteScheduler(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	check := func() {
		settings, err := s.GetAutoDeleteSettings()
		if err != nil || !settings.Enabled {
			return
		}
		loc, err := time.LoadLocation(settings.Timezone)
		if err != nil {
			loc = time.Local
		}
		now := time.Now().In(loc)
		checkTime, err := time.Parse("15:04", settings.CheckTime)
		if err != nil {
			return
		}
		if now.Hour() != checkTime.Hour() || now.Minute() != checkTime.Minute() {
			return
		}
		dateKey := now.Format("2006-01-02")
		s.autoDeleteSchedulerMu.Lock()
		if s.lastAutoDeleteDate == dateKey {
			s.autoDeleteSchedulerMu.Unlock()
			return
		}
		s.lastAutoDeleteDate = dateKey
		s.autoDeleteSchedulerMu.Unlock()
		_, _ = s.runAutoDeleteCheck("", true)
	}
	select {
	case <-ctx.Done():
		return
	default:
	}
	check()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func (s *Service) GetHealthStatus() ([]HealthStatus, error) {
	db, release, err := s.acquireDB()
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := db.Query(`select id,coalesce(vps_alias,''),coalesce(vps_ip,''),coalesce(node_group,''),coalesce(node_tag,''),coalesce(filename,''),coalesce(inbound_port,0),coalesce(fail_count,0),coalesce(last_status,''),coalesce(last_message,''),coalesce(last_checked_at,'') from health_status order by last_checked_at desc,id desc limit 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []HealthStatus{}
	for rows.Next() {
		var item HealthStatus
		if err := rows.Scan(&item.ID, &item.VPSAlias, &item.VPSIP, &item.NodeGroup, &item.NodeTag, &item.Filename, &item.InboundPort, &item.FailCount, &item.LastStatus, &item.LastMessage, &item.LastCheckedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) ClearHealthStatus(confirmText string) (int, error) {
	if strings.TrimSpace(confirmText) != "确认清空" {
		return 0, errors.New("请输入：确认清空")
	}
	db, release, err := s.acquireDB()
	if err != nil {
		return 0, err
	}
	defer release()
	result, err := db.Exec(`delete from health_status`)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

func (s *Service) DeleteDeletedRecords(ids []int, confirmText string) (OperationResult, error) {
	if strings.TrimSpace(confirmText) != "确认删除" {
		return OperationResult{Success: false, Message: "请输入：确认删除"}, nil
	}
	db, release, err := s.acquireDB()
	if err != nil {
		return OperationResult{}, err
	}
	defer release()
	tx, err := db.Begin()
	if err != nil {
		return OperationResult{}, err
	}
	deleted := []string{}
	for _, id := range ids {
		result, err := tx.Exec(`delete from deleted_records where id=?`, id)
		if err != nil {
			_ = tx.Rollback()
			return OperationResult{Success: false, Message: "永久删除记录失败"}, err
		}
		if n, _ := result.RowsAffected(); n > 0 {
			deleted = append(deleted, strconv.Itoa(id))
		}
	}
	if err := tx.Commit(); err != nil {
		return OperationResult{Success: false, Message: "永久删除记录失败"}, err
	}
	return OperationResult{Success: true, Message: "删除记录已永久删除", Count: len(deleted), Items: deleted}, nil
}

func (s *Service) RestoreDeletedRecords(ids []int) (OperationResult, error) {
	return s.restoreDeletedRecordsBatch(ids)
}
func (s *Service) ImportNodeConnections(text string) (ImportResult, error) {
	db, release, err := s.acquireDB()
	if err != nil {
		return ImportResult{}, err
	}
	defer release()
	tx, err := db.Begin()
	if err != nil {
		return ImportResult{}, err
	}
	insertStmt, err := tx.Prepare(`insert or ignore into node_connection_records(ip,port,username,password,renewal_date,remark,country,connection_str,created_at) values(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return ImportResult{}, err
	}
	defer insertStmt.Close()

	result := ImportResult{Success: true}
	now := nowText()
	for lineNo, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		item, ok := parseNodeConnectionLine(line)
		if !ok {
			result.Skipped++
			result.Errors = append(result.Errors, fmt.Sprintf("第 %d 行格式错误", lineNo+1))
			continue
		}
		exists := false
		if rows, qErr := tx.Query(`select coalesce(password,'') from node_connection_records where ip=? and port=? and coalesce(username,'')=?`, item.IP, item.Port, item.Username); qErr == nil {
			for rows.Next() {
				var storedPass string
				if rows.Scan(&storedPass) == nil && unprotectDBSecret(storedPass) == item.Password {
					exists = true
					break
				}
			}
			_ = rows.Close()
		}
		if exists {
			result.Skipped++
			continue
		}
		_, err := insertStmt.Exec(
			item.IP, item.Port, item.Username, protectDBSecret(item.Password), item.RenewalDate, item.Remark, item.Country, protectDBSecret(item.ConnectionStr), now)
		if err != nil {
			result.Skipped++
			result.Errors = append(result.Errors, err.Error())
			continue
		}
		result.Imported++
	}
	if err := tx.Commit(); err != nil {
		return ImportResult{}, err
	}
	result.Message = fmt.Sprintf("导入完成：成功 %d，跳过 %d", result.Imported, result.Skipped)
	return result, nil
}

func (s *Service) SearchNodeConnections(keyword string, limit int) ([]NodeConnectionRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	return s.queryNodeConnections(keyword, limit)
}

func (s *Service) queryNodeConnections(keyword string, limit int) ([]NodeConnectionRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	db, release, err := s.acquireDB()
	if err != nil {
		return nil, err
	}
	defer release()
	kw := strings.ToLower(strings.TrimSpace(keyword))
	var rows *sql.Rows
	if kw == "" {
		rows, err = db.Query(`select id,ip,port,coalesce(username,''),coalesce(password,''),coalesce(renewal_date,''),coalesce(remark,''),coalesce(country,''),coalesce(connection_str,''),coalesce(created_at,'') from node_connection_records order by id desc limit ?`, limit)
	} else {
		rows, err = db.Query(`select id,ip,port,coalesce(username,''),coalesce(password,''),coalesce(renewal_date,''),coalesce(remark,''),coalesce(country,''),coalesce(connection_str,''),coalesce(created_at,'') from node_connection_records order by id desc`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []NodeConnectionRecord{}
	for rows.Next() {
		var item NodeConnectionRecord
		if err := rows.Scan(&item.ID, &item.IP, &item.Port, &item.Username, &item.Password, &item.RenewalDate, &item.Remark, &item.Country, &item.ConnectionStr, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.Password = unprotectDBSecret(item.Password)
		item.ConnectionStr = unprotectDBSecret(item.ConnectionStr)
		if kw != "" {
			if !strings.Contains(strings.ToLower(item.IP), kw) &&
				!strings.Contains(strings.ToLower(item.Country), kw) &&
				!strings.Contains(strings.ToLower(item.Remark), kw) &&
				!strings.Contains(strings.ToLower(item.ConnectionStr), kw) {
				continue
			}
		}
		items = append(items, item)
		if len(items) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) DeleteNodeConnection(id int) error {
	db, release, err := s.acquireDB()
	if err != nil {
		return err
	}
	defer release()
	_, err = db.Exec(`delete from node_connection_records where id=?`, id)
	return err
}

func (s *Service) ClearNodeConnections(confirmText string) (int, error) {
	if strings.TrimSpace(confirmText) != "确认清空" {
		return 0, errors.New("请输入：确认清空")
	}
	db, release, err := s.acquireDB()
	if err != nil {
		return 0, err
	}
	defer release()
	result, err := db.Exec(`delete from node_connection_records`)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

func (s *Service) ExportNodeConnections(keyword string) (string, error) {
	items, err := s.queryNodeConnections(keyword, 100000)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"IP", "端口", "用户名", "密码", "续费日期", "备注", "国家", "连接串"})
	for _, item := range items {
		_ = w.Write([]string{item.IP, strconv.Itoa(item.Port), item.Username, item.Password, item.RenewalDate, item.Remark, item.Country, item.ConnectionStr})
	}
	w.Flush()
	return b.String(), w.Error()
}

func (s *Service) GetRuleGroups() ([]RuleGroup, error) {
	db, release, err := s.acquireDB()
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := db.Query(`select id,name,coalesce(description,''),coalesce(rules,'[]'),coalesce(builtin,0),coalesce(created_at,''),coalesce(updated_at,'') from route_rule_groups order by builtin desc,name asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RuleGroup{}
	for rows.Next() {
		var item RuleGroup
		var rulesText string
		var builtin int
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &rulesText, &builtin, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(rulesText), &item.Rules)
		item.Builtin = builtin == 1
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) SaveRuleGroup(group RuleGroup) error {
	group.Name = strings.TrimSpace(group.Name)
	if group.Name == "" {
		return errors.New("规则组名称不能为空")
	}
	group.Rules = cleanRules(group.Rules)
	if len(group.Rules) == 0 {
		return errors.New("规则不能为空")
	}
	db, release, err := s.acquireDB()
	if err != nil {
		return err
	}
	defer release()
	data, _ := json.Marshal(group.Rules)
	_, err = db.Exec(`insert into route_rule_groups(name,description,rules,builtin,created_at,updated_at) values(?,?,?,?,?,?)
		on conflict(name) do update set description=excluded.description,rules=excluded.rules,updated_at=excluded.updated_at`,
		group.Name, group.Description, string(data), boolInt(group.Builtin), nowText(), nowText())
	return err
}

func (s *Service) DeleteRuleGroup(name string) error {
	name = strings.TrimSpace(name)
	db, release, err := s.acquireDB()
	if err != nil {
		return err
	}
	defer release()
	var builtin int
	_ = db.QueryRow(`select coalesce(builtin,0) from route_rule_groups where name=?`, name).Scan(&builtin)
	if builtin == 1 {
		return errors.New("内置规则组不能删除")
	}
	_, err = db.Exec(`delete from route_rule_groups where name=?`, name)
	return err
}

func (s *Service) ApplyRuleGroup(alias string, filenames []string, ruleName string, action string) (OperationResult, error) {
	rules, err := s.findRuleGroupRules(ruleName)
	if err != nil {
		return OperationResult{}, err
	}
	unlock := s.lockNodeConfigAliases(alias)
	defer unlock()
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return OperationResult{}, err
	}
	target := map[string]bool{}
	for _, filename := range filenames {
		target[filename] = true
	}
	changed := []string{}
	for filename, config := range configs {
		if len(target) > 0 && !target[filename] {
			continue
		}
		if applyRulesToConfig(config, ruleName, rules, action) {
			configs[filename] = config
			changed = append(changed, filename)
		}
	}
	if err := s.saveNodeConfigMap(alias, configs); err != nil {
		return OperationResult{}, err
	}
	return OperationResult{Success: true, Message: "规则组处理完成", Count: len(changed), Items: changed}, nil
}

func (s *Service) TestProxyNodes(req ProxyDetectRequest) ([]ProxyDetectResult, error) {
	testURL := strings.TrimSpace(req.TestURL)
	if testURL == "" {
		testURL = proxyDetectURL
	}
	return s.testProxyNodesInternal(context.Background(), req, testURL, nil)
}

func (s *Service) TestProxyNodesWithProgress(ctx context.Context, req ProxyDetectRequest, onProgress ProxyProgressCallback) ([]ProxyDetectResult, error) {
	testURL := strings.TrimSpace(req.TestURL)
	if testURL == "" {
		testURL = proxyDetectURL
	}
	return s.testProxyNodesInternal(ctx, req, testURL, onProgress)
}

func (s *Service) testProxyNodes(ctx context.Context, req ProxyDetectRequest, testURL string) ([]ProxyDetectResult, error) {
	return s.testProxyNodesInternal(ctx, req, testURL, nil)
}

func (s *Service) testProxyNodesInternal(ctx context.Context, req ProxyDetectRequest, testURL string, onProgress ProxyProgressCallback) ([]ProxyDetectResult, error) {
	lines := splitLines(req.Nodes)
	if len(lines) == 0 {
		return nil, errors.New("节点列表不能为空")
	}
	target := strings.TrimSpace(req.Target)
	remote := target != "" && !strings.EqualFold(target, "local")
	var (
		testLine       func(string) ProxyDetectResult
		release        = func() {}
		detectLocation = "本机"
		exitIPResult   <-chan string
	)
	if !remote {
		forward := &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
		testLine = func(line string) ProxyDetectResult {
			parsed := parseProxyInput(line)
			result := testProxyLineWithDialer(ctx, line, testURL, forward)
			result.Message = redactProxyDetectText(result.Message, parsed)
			result.DetectTarget = "local"
			result.DetectLocation = detectLocation
			return result
		}
	} else {
		vps, err := s.getVPS(target)
		if err != nil {
			return nil, fmt.Errorf("检测位置 %q 无效: %w", target, err)
		}
		client, releaseSSH, err := s.acquireSSH(vps, 30)
		if err != nil {
			return nil, fmt.Errorf("连接检测 VPS %q 失败: %w", target, err)
		}
		release = releaseSSH
		if err := ensureRemoteCurl(client, target); err != nil {
			release()
			return nil, err
		}
		detectLocation = remoteProxyDetectLocation(client, target)
		exitIPResult = remoteProxyDetectExitIP(client)
		testLine = func(line string) ProxyDetectResult {
			result := testRemoteProxyLine(ctx, client, line, testURL)
			result.DetectTarget = target
			result.DetectLocation = detectLocation
			return result
		}
	}
	defer release()
	req.Concurrency = clampProxyConcurrency(req.Concurrency)
	results := make([]ProxyDetectResult, len(lines))
	jobs := make(chan int)
	var wg sync.WaitGroup
	workerCount := req.Concurrency
	if workerCount > len(lines) {
		workerCount = len(lines)
	}
	if remote && workerCount > 8 {
		workerCount = 8
	}
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					results[index] = parseProxyLine(lines[index])
					results[index].Message = "检测已取消"
					results[index].DetectTarget = firstNonEmpty(target, "local")
					results[index].DetectLocation = detectLocation
					if onProgress != nil {
						onProgress(index, len(lines), results[index])
					}
					continue
				}
				results[index] = testLine(lines[index])
				if ctx.Err() == nil {
					lookupIP := results[index].Host
					if results[index].DetectExitIP != "" {
						lookupIP = results[index].DetectExitIP
					}
					info := s.lookupIPInfo(lookupIP)
					results[index].Country = info.Country
					results[index].ISP = info.ISP
				}
				if onProgress != nil {
					onProgress(index, len(lines), results[index])
				}
			}
		}()
	}
sendLoop:
	for i := range lines {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break sendLoop
		}
	}
	close(jobs)
	wg.Wait()
	if exitIPResult != nil {
		exitIP := <-exitIPResult
		for i := range results {
			if results[i].DetectExitIP == "" {
				results[i].DetectExitIP = exitIP
			}
		}
	}
	if err := ctx.Err(); err != nil {
		for i := range results {
			if results[i].Line == "" {
				results[i] = parseProxyLine(lines[i])
				results[i].Message = "检测已取消"
				results[i].DetectTarget = firstNonEmpty(target, "local")
				results[i].DetectLocation = detectLocation
			}
		}
	}
	return results, nil
}

func clampProxyConcurrency(value int) int {
	if value < 1 {
		return 1
	}
	if value > 50 {
		return 50
	}
	return value
}

func (s *Service) QueryOutboundIP(alias string, filename string) (OutboundIPResult, error) {
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return OutboundIPResult{}, err
	}
	config, ok := configs[filename]
	if !ok {
		return OutboundIPResult{VPSAlias: alias, Filename: filename, Success: false, Message: "节点不存在"}, nil
	}
	return s.queryOutboundRealIP(alias, filename, config), nil
}

func (s *Service) QueryAllOutboundIP(alias string) ([]OutboundIPResult, error) {
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)
	items := []OutboundIPResult{}
	for _, name := range names {
		items = append(items, s.queryOutboundRealIP(alias, name, configs[name]))
	}
	return items, nil
}

func ensureFeatureTables(db *sql.DB) error {
	if err := ensureRecordTables(db); err != nil {
		return err
	}
	schema := strings.Join([]string{
		`create table if not exists app_settings (key text primary key,value text,updated_at timestamp);`,
		`create table if not exists health_status (id integer primary key autoincrement,vps_alias text,vps_ip text,node_group text,node_tag text,filename text,inbound_port integer,fail_count integer default 0,last_status text,last_message text,last_checked_at timestamp,unique(vps_alias,filename));`,
		`create table if not exists node_connection_records (id integer primary key autoincrement,ip text not null,port integer not null,username text,password text,renewal_date date,remark text,country text,connection_str text,created_at timestamp default current_timestamp,unique(ip,port,username,password));`,
		`create table if not exists route_rule_groups (id integer primary key autoincrement,name text not null unique,description text,rules text not null,builtin integer default 0,created_at timestamp default current_timestamp,updated_at timestamp default current_timestamp);`,
		`create table if not exists node_rule_mappings (id integer primary key autoincrement,node_tag text not null,rule_group_name text not null,created_at timestamp default current_timestamp,unique(node_tag,rule_group_name));`,
		`create index if not exists idx_health_status_vps on health_status(vps_alias);`,
		`create index if not exists idx_health_status_checked on health_status(last_checked_at desc,id desc);`,
		`create index if not exists idx_node_connection_search on node_connection_records(ip,country,remark);`,
	}, "\n")
	_, err := db.Exec(schema)
	return err
}

func defaultAutoDeleteSettings() AutoDeleteSettings {
	return AutoDeleteSettings{Enabled: false, FailureDays: 3, CheckTime: "03:00", Timezone: "Asia/Shanghai", Concurrency: 10}
}

func normalizeAutoDeleteSettings(settings *AutoDeleteSettings) {
	if settings.FailureDays <= 0 {
		settings.FailureDays = 3
	}
	if settings.CheckTime == "" {
		settings.CheckTime = "03:00"
	}
	if settings.Timezone == "" {
		settings.Timezone = "Asia/Shanghai"
	}
	if settings.Concurrency <= 0 || settings.Concurrency > 100 {
		settings.Concurrency = 10
	}
}

func parseNodeConnectionLine(line string) (NodeConnectionRecord, bool) {
	parts := strings.Split(line, "\t")
	if len(parts) == 1 {
		parts = strings.Fields(line)
	}
	if len(parts) == 0 {
		return NodeConnectionRecord{}, false
	}
	conn := parts[0]
	var host, portText, rest string
	if strings.HasPrefix(conn, "[") {
		closeIdx := strings.Index(conn, "]")
		if closeIdx <= 1 || closeIdx+1 >= len(conn) || conn[closeIdx+1] != ':' {
			return NodeConnectionRecord{}, false
		}
		host = conn[1:closeIdx]
		afterHost := conn[closeIdx+2:]
		colonIdx := strings.Index(afterHost, ":")
		if colonIdx < 0 {
			portText = afterHost
		} else {
			portText = afterHost[:colonIdx]
			rest = afterHost[colonIdx+1:]
		}
	} else {
		connParts := strings.SplitN(conn, ":", 3)
		if len(connParts) < 2 {
			return NodeConnectionRecord{}, false
		}
		host = connParts[0]
		portText = connParts[1]
		if len(connParts) == 3 {
			rest = connParts[2]
		}
	}
	host = strings.TrimSpace(host)
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if host == "" || err != nil || port <= 0 || port > 65535 {
		return NodeConnectionRecord{}, false
	}
	item := NodeConnectionRecord{IP: host, Port: port, ConnectionStr: conn}
	if rest != "" {
		userPass := strings.SplitN(rest, ":", 2)
		item.Username = userPass[0]
		if len(userPass) == 2 {
			item.Password = userPass[1]
		}
	}
	if len(parts) > 1 && isDate(parts[1]) {
		item.RenewalDate = parts[1]
	}
	if len(parts) > 2 {
		item.Remark = parts[2]
	}
	if len(parts) > 3 {
		item.Country = parts[3]
	}
	return item, true
}

func testProxyLineWithDialer(ctx context.Context, line string, testURL string, forward proxyDetectDialer) ProxyDetectResult {
	parsed := parseProxyInput(line)
	result := parsed.Result
	if result.Host == "" || result.Port < 1 || result.Port > 65535 {
		result.Message = "无法解析节点地址"
		return result
	}
	address := net.JoinHostPort(result.Host, strconv.Itoa(result.Port))
	start := time.Now()
	tcpCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	conn, err := forward.DialContext(tcpCtx, "tcp", address)
	cancel()
	result.TCPLatency = time.Since(start).Milliseconds()
	result.Latency = result.TCPLatency
	if err != nil {
		result.Message = "TCP 连接失败: " + proxyDetectErrorMessage(err)
		return result
	}
	_ = conn.Close()
	parsed.Result = result

	switch result.Protocol {
	case "socks5":
		return testSOCKS5Proxy(ctx, parsed, testURL, forward)
	case "http", "https":
		return testHTTPProxy(ctx, parsed, testURL, forward)
	case "trojan":
		return testTrojanProxy(ctx, parsed, testURL, forward)
	case "vless":
		return testVLESSProxy(ctx, parsed, testURL, forward)
	case "vmess":
		return testVMessProxy(ctx, parsed, testURL, forward)
	case "anytls":
		return testAnyTLSProxy(ctx, parsed, testURL, forward)
	case "ss", "shadowsocks":
		result.Success = true
		result.Message = "Shadowsocks 端口连通正常"
		return result
	default:
		name := strings.ToUpper(firstNonEmpty(result.Protocol, "未知"))
		result.Success = true
		result.Message = fmt.Sprintf("%s 服务器端口连通正常", name)
		return result
	}
}

func testTrojanProxy(ctx context.Context, parsed parsedProxyLine, testURL string, forward proxyDetectDialer) ProxyDetectResult {
	result := parsed.Result
	address := net.JoinHostPort(result.Host, strconv.Itoa(result.Port))
	sni := parsed.SNI
	if sni == "" {
		sni = result.Host
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, 8*time.Second)
	defer dialCancel()

	conn, err := forward.DialContext(dialCtx, "tcp", address)
	if err != nil {
		result.Message = "Trojan TCP 连接失败: " + proxyDetectErrorMessage(err)
		return result
	}
	defer conn.Close()

	tlsConfig := &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true,
	}
	tlsConn := tls.Client(conn, tlsConfig)
	tlsStart := time.Now()
	if err := tlsConn.HandshakeContext(dialCtx); err != nil {
		result.Success = false
		result.URLLatency = -1
		result.Message = fmt.Sprintf("Trojan 服务器端口可达，但未验证 Trojan 协议与实际 URL 可用性: TLS 握手失败 (%s)", err.Error())
		return result
	}
	result.URLLatency = time.Since(tlsStart).Milliseconds()

	if parsed.Password == "" {
		result.Success = true
		result.Message = fmt.Sprintf("Trojan TLS 握手成功 (SNI: %s, 延迟 %d ms)", sni, result.URLLatency)
		return result
	}

	targetU, err := url.Parse(testURL)
	if err != nil {
		targetU, _ = url.Parse(proxyDetectURL)
	}
	targetHost := targetU.Hostname()
	targetPort := targetU.Port()
	if targetPort == "" {
		if strings.EqualFold(targetU.Scheme, "https") {
			targetPort = "443"
		} else {
			targetPort = "80"
		}
	}
	targetPortInt, _ := strconv.Atoi(targetPort)

	h := sha256.Sum224([]byte(parsed.Password))
	hexHash := hex.EncodeToString(h[:])

	var reqBuf bytes.Buffer
	reqBuf.WriteString(hexHash)
	reqBuf.WriteString("\r\n")
	reqBuf.WriteByte(1) // CONNECT
	reqBuf.WriteByte(3) // DOMAIN
	reqBuf.WriteByte(byte(len(targetHost)))
	reqBuf.WriteString(targetHost)
	_ = binary.Write(&reqBuf, binary.BigEndian, uint16(targetPortInt))
	reqBuf.WriteString("\r\n")

	targetPath := targetU.Path
	if targetPath == "" {
		targetPath = "/"
	}
	if targetU.RawQuery != "" {
		targetPath += "?" + targetU.RawQuery
	}
	fmt.Fprintf(&reqBuf, "GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: SanTianShell\r\nConnection: close\r\n\r\n", targetPath, targetHost)

	_ = tlsConn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := tlsConn.Write(reqBuf.Bytes()); err != nil {
		result.Success = false
		result.Message = "Trojan 服务器端口可达，但未验证 Trojan 协议与实际 URL 可用性: 请求发送失败 (" + err.Error() + ")"
		return result
	}

	respBuf := make([]byte, 1024)
	n, readErr := tlsConn.Read(respBuf)
	if n > 0 {
		respStr := string(respBuf[:n])
		if strings.HasPrefix(respStr, "HTTP/") {
			result.Success = true
			result.Message = fmt.Sprintf("Trojan 协议与 TLS 验证通过 (SNI: %s)", sni)
			return result
		}
	}

	if readErr != nil {
		result.Success = false
		result.Message = fmt.Sprintf("Trojan 服务器端口可达，但未验证 Trojan 协议与实际 URL 可用性: 代理响应异常 (%v)", readErr)
		return result
	}

	result.Success = true
	result.Message = fmt.Sprintf("Trojan TLS 握手与端口验证通过 (SNI: %s)", sni)
	return result
}

func testVLESSProxy(ctx context.Context, parsed parsedProxyLine, testURL string, forward proxyDetectDialer) ProxyDetectResult {
	result := parsed.Result
	address := net.JoinHostPort(result.Host, strconv.Itoa(result.Port))
	sni := parsed.SNI
	if sni == "" {
		sni = result.Host
	}
	security := strings.ToLower(parsed.Security)
	if security == "" {
		security = "reality"
	}

	if security == "reality" || security == "tls" {
		dialCtx, dialCancel := context.WithTimeout(ctx, 8*time.Second)
		defer dialCancel()
		conn, err := forward.DialContext(dialCtx, "tcp", address)
		if err != nil {
			result.Message = "TCP 连接失败: " + proxyDetectErrorMessage(err)
			return result
		}
		defer conn.Close()

		tlsConfig := &tls.Config{
			ServerName:         sni,
			InsecureSkipVerify: true,
		}
		tlsConn := tls.Client(conn, tlsConfig)
		tlsStart := time.Now()
		if err := tlsConn.HandshakeContext(dialCtx); err != nil {
			result.Success = false
			result.URLLatency = -1
			result.Message = fmt.Sprintf("VLESS (%s) 服务器端口可达，但未验证 VLESS 协议与实际 URL 可用性: TLS 握手失败 (%s)", strings.ToUpper(security), err.Error())
			return result
		}
		result.URLLatency = time.Since(tlsStart).Milliseconds()
		result.Success = true
		result.Message = fmt.Sprintf("VLESS (%s) 握手成功 (SNI: %s, 延迟 %d ms)", strings.ToUpper(security), sni, result.URLLatency)
		return result
	}

	result.Success = false
	result.URLLatency = -1
	result.Message = "VLESS 服务器端口可达，但未验证 VLESS 协议与实际 URL 可用性"
	return result
}

func testVMessProxy(ctx context.Context, parsed parsedProxyLine, testURL string, forward proxyDetectDialer) ProxyDetectResult {
	result := parsed.Result
	address := net.JoinHostPort(result.Host, strconv.Itoa(result.Port))
	sni := parsed.SNI
	if sni == "" {
		sni = result.Host
	}

	if strings.EqualFold(parsed.Security, "tls") {
		dialCtx, dialCancel := context.WithTimeout(ctx, 8*time.Second)
		defer dialCancel()
		conn, err := forward.DialContext(dialCtx, "tcp", address)
		if err != nil {
			result.Message = "TCP 连接失败: " + proxyDetectErrorMessage(err)
			return result
		}
		defer conn.Close()

		tlsConfig := &tls.Config{
			ServerName:         sni,
			InsecureSkipVerify: true,
		}
		tlsConn := tls.Client(conn, tlsConfig)
		tlsStart := time.Now()
		if err := tlsConn.HandshakeContext(dialCtx); err != nil {
			result.Success = false
			result.URLLatency = -1
			result.Message = fmt.Sprintf("VMess 服务器端口可达，但未验证 VMess 协议与实际 URL 可用性: TLS 握手失败 (%s)", err.Error())
			return result
		}
		result.URLLatency = time.Since(tlsStart).Milliseconds()
		result.Success = true
		result.Message = fmt.Sprintf("VMess (TLS) 握手成功 (SNI: %s, 延迟 %d ms)", sni, result.URLLatency)
		return result
	}

	result.Success = false
	result.URLLatency = -1
	result.Message = "VMess 服务器端口可达，但未验证 VMess 协议与实际 URL 可用性"
	return result
}

func testAnyTLSProxy(ctx context.Context, parsed parsedProxyLine, testURL string, forward proxyDetectDialer) ProxyDetectResult {
	result := parsed.Result
	address := net.JoinHostPort(result.Host, strconv.Itoa(result.Port))
	sni := parsed.SNI
	if sni == "" {
		sni = result.Host
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, 8*time.Second)
	defer dialCancel()
	conn, err := forward.DialContext(dialCtx, "tcp", address)
	if err != nil {
		result.Message = "TCP 连接失败: " + proxyDetectErrorMessage(err)
		return result
	}
	defer conn.Close()

	tlsConfig := &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true,
	}
	tlsConn := tls.Client(conn, tlsConfig)
	tlsStart := time.Now()
	if err := tlsConn.HandshakeContext(dialCtx); err != nil {
		result.Success = false
		result.URLLatency = -1
		result.Message = fmt.Sprintf("AnyTLS 服务器端口可达，但未验证 AnyTLS 协议与实际 URL 可用性: TLS 握手失败 (%s)", err.Error())
		return result
	}
	result.URLLatency = time.Since(tlsStart).Milliseconds()
	result.Success = true
	result.Message = fmt.Sprintf("AnyTLS 握手成功 (SNI: %s, 延迟 %d ms)", sni, result.URLLatency)
	return result
}

func testSOCKS5Proxy(ctx context.Context, parsed parsedProxyLine, testURL string, forward proxyDetectDialer) ProxyDetectResult {
	result := parsed.Result
	dialer, err := socks5DialerWithForward(proxyDialConfig{
		Host: parsed.Result.Host, Port: parsed.Result.Port, Username: parsed.Username, Password: parsed.Password,
	}, forward)
	if err != nil {
		result.Message = "SOCKS5 代理配置无效: " + err.Error()
		return result
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if contextDialer, ok := dialer.(interface {
				DialContext(context.Context, string, string) (net.Conn, error)
			}); ok {
				return contextDialer.DialContext(ctx, network, address)
			}
			return dialer.Dial(network, address)
		},
		TLSHandshakeTimeout: 8 * time.Second,
	}
	return testProxyURL(ctx, result, testURL, transport, "SOCKS5")
}

func testHTTPProxy(ctx context.Context, parsed parsedProxyLine, testURL string, forward proxyDetectDialer) ProxyDetectResult {
	result := parsed.Result
	proxyURL := &url.URL{Scheme: result.Protocol, Host: net.JoinHostPort(result.Host, strconv.Itoa(result.Port))}
	if parsed.Username != "" || parsed.Password != "" {
		proxyURL.User = url.UserPassword(parsed.Username, parsed.Password)
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(proxyURL),
		DialContext:           forward.DialContext,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
	}
	return testProxyURL(ctx, result, testURL, transport, "HTTP")
}

func testProxyURL(ctx context.Context, result ProxyDetectResult, testURL string, transport *http.Transport, protocol string) ProxyDetectResult {
	defer transport.CloseIdleConnections()
	requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, testURL, nil)
	if err != nil {
		result.Message = protocol + " 测试 URL 无效: " + err.Error()
		return result
	}
	start := time.Now()
	response, err := (&http.Client{Transport: transport, Timeout: 8 * time.Second}).Do(request)
	result.URLLatency = time.Since(start).Milliseconds()
	if err != nil {
		result.Message = protocol + " 代理验证失败: " + proxyDetectErrorMessage(err)
		return result
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
	_ = response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		result.Message = fmt.Sprintf("%s 代理验证失败: 测试 URL 返回 HTTP %d", protocol, response.StatusCode)
		return result
	}
	bodyStr := strings.TrimSpace(string(body))
	if isValidIPText(bodyStr) {
		result.DetectExitIP = bodyStr
	}
	result.Success = true
	if result.DetectExitIP != "" {
		result.Message = fmt.Sprintf("%s 代理验证通过 (出口 IP: %s)", protocol, result.DetectExitIP)
	} else {
		result.Message = protocol + " 代理验证通过"
	}
	return result
}

func proxyDetectErrorMessage(err error) string {
	message := err.Error()
	if strings.Contains(strings.ToLower(message), "administratively prohibited") {
		return "该 VPS 禁止 SSH 端口转发，无法从此处检测"
	}
	return message
}

func newProxyDetectResult(_ string) ProxyDetectResult {
	return ProxyDetectResult{Line: "-", Country: "--", ISP: "-", Latency: -1, TCPLatency: -1, URLLatency: -1}
}

func parseProxyLine(line string) ProxyDetectResult {
	return parseProxyInput(line).Result
}

func parseProxyInput(line string) parsedProxyLine {
	line = strings.TrimSpace(line)
	result := newProxyDetectResult("")
	lower := strings.ToLower(line)
	if strings.HasPrefix(lower, "vmess://") {
		return parseVMessLine(line)
	}
	if strings.HasPrefix(lower, "ss://") {
		return parseSSLine(line)
	}
	if strings.Contains(line, "://") {
		u, err := url.Parse(line)
		if err != nil {
			return parsedProxyLine{Result: result}
		}
		result.Protocol = strings.ToLower(u.Scheme)
		if result.Protocol == "socks" {
			result.Protocol = "socks5"
		}
		result.Host = u.Hostname()
		result.Port = asInt(u.Port())
		parsed := parsedProxyLine{Result: result, RawURL: line}
		if u.User != nil {
			parsed.Username = u.User.Username()
			parsed.Password, _ = u.User.Password()
		}
		q := u.Query()
		parsed.SNI = firstNonEmpty(q.Get("sni"), q.Get("peer"), q.Get("server_name"), q.Get("host"))
		parsed.Security = firstNonEmpty(q.Get("security"), q.Get("encryption"))
		parsed.Network = firstNonEmpty(q.Get("type"), q.Get("net"))
		parsed.Path = u.Path
		if parsed.Password == "" && (result.Protocol == "trojan" || result.Protocol == "vless" || result.Protocol == "anytls") {
			parsed.Password = parsed.Username
		}
		parsed.Result.Line = proxyDetectEndpoint(parsed.Result)
		return parsed
	}
	parts := splitPlainProxyLine(line)
	if len(parts) >= 2 {
		result.Protocol = "socks5"
		result.Host = parts[0]
		result.Port = asInt(parts[1])
	}
	parsed := parsedProxyLine{Result: result}
	if len(parts) >= 3 {
		parsed.Username = parts[2]
	}
	if len(parts) >= 4 {
		parsed.Password = strings.Join(parts[3:], ":")
	}
	parsed.Result.Line = proxyDetectEndpoint(parsed.Result)
	return parsed
}

func proxyDetectEndpoint(result ProxyDetectResult) string {
	if result.Host == "" || result.Port < 1 || result.Port > 65535 {
		return "-"
	}
	return net.JoinHostPort(result.Host, strconv.Itoa(result.Port))
}

func splitPlainProxyLine(line string) []string {
	if strings.HasPrefix(line, "[") {
		if end := strings.Index(line, "]"); end > 1 && len(line) > end+2 && line[end+1] == ':' {
			return append([]string{line[1:end]}, strings.Split(line[end+2:], ":")...)
		}
	}
	return strings.FieldsFunc(line, func(r rune) bool { return r == ':' || r == '|' || r == ',' || r == '\t' || r == ' ' })
}

func parseVMessLine(line string) parsedProxyLine {
	result := newProxyDetectResult(line)
	result.Protocol = "vmess"
	parsed := parsedProxyLine{Result: result, RawURL: line}
	raw := line[len("vmess://"):]
	raw = strings.SplitN(raw, "#", 2)[0]
	decoded, err := decodeShareBase64(raw)
	if err != nil {
		return parsed
	}
	var obj map[string]any
	if json.Unmarshal([]byte(decoded), &obj) == nil {
		result.Host = asString(firstAny(obj["add"], obj["server"]))
		result.Port = asInt(firstAny(obj["port"], obj["server_port"]))
		parsed.Security = asString(firstAny(obj["tls"], obj["security"]))
		parsed.SNI = asString(firstAny(obj["sni"], obj["host"]))
		parsed.Network = asString(firstAny(obj["net"], obj["type"]))
		parsed.Path = asString(obj["path"])
		parsed.Username = asString(firstAny(obj["id"], obj["uuid"]))
	}
	result.Line = proxyDetectEndpoint(result)
	parsed.Result = result
	return parsed
}

func parseVMess(line string) ProxyDetectResult {
	return parseVMessLine(line).Result
}

func parseSSLine(line string) parsedProxyLine {
	result := newProxyDetectResult(line)
	result.Protocol = "ss"
	parsed := parsedProxyLine{Result: result, RawURL: line}
	raw := strings.TrimPrefix(line, "ss://")
	raw = strings.Split(raw, "#")[0]
	if strings.Contains(raw, "@") {
		parts := strings.SplitN(raw, "@", 2)
		hpParts := strings.SplitN(parts[1], ":", 2)
		if len(hpParts) == 2 {
			result.Host = hpParts[0]
			result.Port = asInt(hpParts[1])
		}
	} else if decoded, err := decodeShareBase64(raw); err == nil {
		if u, err := url.Parse("ss://" + decoded); err == nil {
			result.Host = u.Hostname()
			result.Port = asInt(u.Port())
		}
	}
	result.Line = proxyDetectEndpoint(result)
	parsed.Result = result
	return parsed
}

func (s *Service) queryOutboundRealIP(alias string, filename string, config map[string]any) OutboundIPResult {
	item := OutboundIPResult{VPSAlias: alias, Filename: filename, NodeTag: extractNodeTag(config)}
	outbound := findNodeTestOutbound(config, filename)
	if outbound == nil {
		item.Message = "未找到出站配置"
		return item
	}
	vps, err := s.getVPS(alias)
	if err != nil {
		item.Message = err.Error()
		return item
	}
	client, release, err := s.acquireSSH(vps, 30)
	if err != nil {
		item.Message = "SSH连接失败: " + err.Error()
		return item
	}
	defer release()
	singBoxPath := findRemoteSingBox(client)
	if singBoxPath == "" {
		item.Message = "VPS未找到 sing-box"
		return item
	}
	ip, message := testOutboundIPWithRemoteSingBox(client, singBoxPath, outbound, 15)
	if ip == "" {
		item.Message = message
		return item
	}
	item.Success = true
	item.OutboundIP = ip
	item.Message = message
	return item
}

func findNodeTestOutbound(config map[string]any, filename string) map[string]any {
	outbounds, _ := config["outbounds"].([]any)
	if len(outbounds) == 0 {
		return nil
	}
	nodeTag := extractNodeTag(config)
	fileTag := strings.TrimSuffix(filename, ".json")
	if nodeTag == "" {
		nodeTag = fileTag
	}
	if route, ok := config["route"].(map[string]any); ok {
		if rules, ok := route["rules"].([]any); ok {
			for _, raw := range rules {
				rule, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if nodeTag != "" && !ruleInboundContains(rule["inbound"], nodeTag) {
					continue
				}
				targetTag := asString(rule["outbound"])
				for _, outboundRaw := range outbounds {
					outbound, ok := outboundRaw.(map[string]any)
					if ok && targetTag != "" && asString(outbound["tag"]) == targetTag {
						return cloneMap(outbound)
					}
				}
			}
		}
	}
	expected := map[string]bool{
		nodeTag + "-out":         true,
		nodeTag:                  true,
		fileTag:                  true,
		fileTag + "-out":         true,
		strings.ToLower(fileTag): true,
	}
	for _, outboundRaw := range outbounds {
		outbound, ok := outboundRaw.(map[string]any)
		if ok && expected[asString(outbound["tag"])] {
			return cloneMap(outbound)
		}
	}
	for _, outboundRaw := range outbounds {
		outbound, ok := outboundRaw.(map[string]any)
		if !ok {
			continue
		}
		outType := strings.ToLower(asString(firstAny(outbound["type"], outbound["protocol"])))
		outTag := asString(outbound["tag"])
		if outType != "direct" && outType != "block" && outType != "dns" && outTag != "global-middle-proxy" {
			return cloneMap(outbound)
		}
	}
	if outbound, ok := outbounds[0].(map[string]any); ok {
		return cloneMap(outbound)
	}
	return nil
}

func ruleInboundContains(value any, nodeTag string) bool {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if asString(item) == nodeTag {
				return true
			}
		}
	case []string:
		for _, item := range typed {
			if item == nodeTag {
				return true
			}
		}
	default:
		return asString(value) == nodeTag
	}
	return false
}

func findRemoteSingBox(client *ssh.Client) string {
	command := "if command -v sing-box >/dev/null 2>&1; then command -v sing-box; elif [ -x /usr/local/bin/sing-box ]; then echo /usr/local/bin/sing-box; elif [ -x /usr/bin/sing-box ]; then echo /usr/bin/sing-box; elif [ -x /etc/sing-box/sing-box ]; then echo /etc/sing-box/sing-box; elif [ -x /etc/sing-box/bin/sing-box ]; then echo /etc/sing-box/bin/sing-box; else echo ''; fi"
	result := executeSSH(client, command, 15)
	if !result.Success {
		return ""
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func testOutboundIPWithRemoteSingBox(client *ssh.Client, singBoxPath string, outbound map[string]any, timeoutSeconds int) (string, string) {
	port := randomRemotePort()
	workID := strings.ReplaceAll(randomUUID(), "-", "")
	remoteDir := "/tmp/sb_node_ip"
	configPath := remoteDir + "/" + workID + ".json"
	logPath := remoteDir + "/" + workID + ".log"
	config := buildRemoteSingBoxIPConfig(outbound, port)
	data, err := json.Marshal(config)
	if err != nil {
		return "", "生成临时配置失败: " + err.Error()
	}
	configB64 := base64.StdEncoding.EncodeToString(data)
	writeCmd := "mkdir -p " + shellQuote(remoteDir) + " && printf %s " + shellQuote(configB64) + " | base64 -d > " + shellQuote(configPath)
	if result := executeSSH(client, writeCmd, 15); !result.Success {
		return "", "写入临时配置失败: " + strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout, result.Message))
	}
	pid := ""
	defer func() {
		parts := []string{}
		if pid != "" && regexp.MustCompile(`^\d+$`).MatchString(pid) {
			parts = append(parts, "kill "+shellQuote(pid)+" >/dev/null 2>&1 || true")
		}
		parts = append(parts, "rm -f "+shellQuote(configPath)+" "+shellQuote(logPath))
		_ = executeSSH(client, strings.Join(parts, "; "), 8)
	}()
	startCmd := "if command -v timeout >/dev/null 2>&1; then nohup timeout --signal=TERM 180s " + shellQuote(singBoxPath) + " run -c " + shellQuote(configPath) + " > " + shellQuote(logPath) + " 2>&1 & else nohup " + shellQuote(singBoxPath) + " run -c " + shellQuote(configPath) + " > " + shellQuote(logPath) + " 2>&1 & fi; echo $!"
	startResult := executeSSH(client, startCmd, 10)
	if !startResult.Success {
		return "", "启动 sing-box 失败: " + strings.TrimSpace(firstNonEmpty(startResult.Stderr, startResult.Stdout, startResult.Message))
	}
	fields := strings.Fields(strings.TrimSpace(startResult.Stdout))
	if len(fields) > 0 {
		pid = fields[len(fields)-1]
	}
	time.Sleep(time.Second)
	checkCmd := "if kill -0 " + shellQuote(pid) + " >/dev/null 2>&1; then echo RUNNING; else echo STOPPED; cat " + shellQuote(logPath) + " 2>/dev/null | tail -c 500; fi"
	checkResult := executeSSH(client, checkCmd, 10)
	if !strings.HasPrefix(strings.TrimSpace(checkResult.Stdout), "RUNNING") {
		return "", "sing-box 启动失败: " + strings.TrimSpace(firstNonEmpty(checkResult.Stdout, checkResult.Stderr, checkResult.Message))
	}
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	services := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
		"https://checkip.amazonaws.com",
	}
	lastError := ""
	for _, serviceURL := range services {
		curlCmd := "curl -x " + shellQuote(proxyURL) + " -sS --connect-timeout " + strconv.Itoa(timeoutSeconds) + " --max-time " + strconv.Itoa(timeoutSeconds) + " " + shellQuote(serviceURL)
		result := executeSSH(client, curlCmd, timeoutSeconds+5)
		stdout := strings.TrimSpace(result.Stdout)
		if result.Success && isValidIPText(stdout) {
			return stdout, "查询成功"
		}
		lastError = strings.TrimSpace(firstNonEmpty(result.Stderr, stdout, result.Message))
	}
	if lastError == "" {
		lastError = "未知错误"
	}
	return "", "所有 IP 查询服务失败: " + lastError
}

func buildRemoteSingBoxIPConfig(outbound map[string]any, inboundPort int) map[string]any {
	outboundCopy := cloneMap(outbound)
	outboundCopy["tag"] = "proxy"
	return map[string]any{
		"log": map[string]any{"disabled": false, "level": "error", "timestamp": true},
		"inbounds": []any{map[string]any{
			"type":        "mixed",
			"tag":         "mixed-in",
			"listen":      "127.0.0.1",
			"listen_port": inboundPort,
		}},
		"outbounds": []any{outboundCopy},
		"route":     map[string]any{"final": "proxy"},
	}
}

func randomRemotePort() int {
	n, err := rand.Int(rand.Reader, big.NewInt(16000))
	if err != nil {
		return 26000 + int(time.Now().UnixNano()%16000)
	}
	return 26000 + int(n.Int64())
}

func isValidIPText(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "\n") {
		return false
	}
	return net.ParseIP(value) != nil
}

func (s *Service) findRuleGroupRules(name string) ([]string, error) {
	groups, err := s.GetRuleGroups()
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		if strings.EqualFold(group.Name, name) {
			return group.Rules, nil
		}
	}
	return nil, errors.New("规则组不存在")
}

func applyRulesToConfig(config map[string]any, ruleName string, rules []string, action string) bool {
	route, _ := config["route"].(map[string]any)
	if route == nil {
		route = map[string]any{}
		config["route"] = route
	}
	rawRules, _ := route["rules"].([]any)
	tag := "rule_group:" + ruleName
	filtered := []any{}
	for _, raw := range rawRules {
		if rule, ok := raw.(map[string]any); ok && asString(rule["tag"]) == tag {
			continue
		}
		filtered = append(filtered, raw)
	}
	if action == "remove" {
		route["rules"] = filtered
		return len(filtered) != len(rawRules)
	}
	for _, ruleLine := range rules {
		filtered = append(filtered, map[string]any{"tag": tag, "domain_suffix": []any{ruleLine}, "outbound": "direct"})
	}
	route["rules"] = filtered
	return true
}

func cleanRules(rules []string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule != "" && !seen[rule] {
			seen[rule] = true
			result = append(result, rule)
		}
	}
	return result
}

func splitLines(text string) []string {
	result := []string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func groupFromTag(tag string) string {
	return tag
}

func nowText() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

func isDate(value string) bool {
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func firstAny(values ...any) any {
	for _, value := range values {
		if value != nil && asString(value) != "" {
			return value
		}
	}
	return nil
}

package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunMiddleAccelerationAnyTLS(t *testing.T) {
	input := strings.Join([]string{
		"anytls://2189856b-0da2-47c8-b508-a2318ff6a6c4@203.0.113.10:20000?allowInsecure=1&insecure=1&security=tls&sni=203.0.113.10#123-1",
		"anytls://5d1eefb4-3a03-4c7d-8327-fb4690dfb8bd@203.0.113.10:20001?allowInsecure=1&insecure=1&security=tls&sni=203.0.113.10#123-2",
		"anytls://55cfd998-2ca4-49a6-b69a-00439ca5ab9e@203.0.113.10:20002?allowInsecure=1&insecure=1&security=tls&sni=203.0.113.10#123-3",
		"anytls://f6d4e2a5-eeca-42e9-8e43-50792d8052dd@203.0.113.10:20003?allowInsecure=1&insecure=1&security=tls&sni=203.0.113.10#123-4",
	}, "\n")
	nameToPort := map[string]int{"123-1": 52434, "123-2": 52435, "123-3": 52436, "123-4": 52437}
	var imported string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":"token"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/user/devicegroup":
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":[{"id":7,"type":"DeviceGroupType_Inbound","name":"entry"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/user/forward/batch_create":
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("批量导入请求解析失败：%v", err)
			}
			imported = body["content"].(string)
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":null}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/user/forward":
			items := make([]map[string]interface{}, 0, len(nameToPort))
			sourcePorts := map[string]string{"123-1": "20000", "123-2": "20001", "123-3": "20002", "123-4": "20003"}
			for name, port := range nameToPort {
				config, _ := json.Marshal(map[string]interface{}{"dest": []string{"203.0.113.10:" + sourcePorts[name]}})
				items = append(items, map[string]interface{}{
					"id":              port,
					"name":            name,
					"listen_port":     port,
					"status":          "ForwardRuleStatus_Normal",
					"device_group_in": 7,
					"config":          string(config),
				})
			}
			resp, _ := json.Marshal(map[string]interface{}{
				"code": 0,
				"msg":  "ok",
				"data": items,
			})
			_, _ = w.Write(resp)
		default:
			t.Fatalf("未预期请求：%s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	service := &Service{}
	result, err := service.RunMiddleAcceleration(MiddleAccelerationRequest{
		RelayURL:     server.URL,
		Username:     "user",
		Password:     "pass",
		EntryID:      "7",
		MiddleDomain: "s.tiktokbusiness.io",
		InputText:    input,
		MaxRetry:     1,
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if !result.Success {
		t.Fatalf("结果未成功：%s\n日志：%s", result.Message, strings.Join(result.Logs, "\n"))
	}
	wantImport := strings.Join([]string{
		"123-1##203.0.113.10#20000",
		"123-2##203.0.113.10#20001",
		"123-3##203.0.113.10#20002",
		"123-4##203.0.113.10#20003",
	}, "\n")
	if imported != wantImport {
		t.Fatalf("批量规则不一致\nwant:\n%s\ngot:\n%s", wantImport, imported)
	}
	for _, expected := range []string{
		"anytls://2189856b-0da2-47c8-b508-a2318ff6a6c4@s.tiktokbusiness.io:52434",
		"anytls://5d1eefb4-3a03-4c7d-8327-fb4690dfb8bd@s.tiktokbusiness.io:52435",
		"anytls://55cfd998-2ca4-49a6-b69a-00439ca5ab9e@s.tiktokbusiness.io:52436",
		"anytls://f6d4e2a5-eeca-42e9-8e43-50792d8052dd@s.tiktokbusiness.io:52437",
	} {
		if !strings.Contains(result.Output, expected) {
			t.Fatalf("输出缺少：%s\n实际输出：\n%s", expected, result.Output)
		}
	}
}

func TestRunMiddleAccelerationPreservesPasswordWhitespace(t *testing.T) {
	var gotPassword string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("登录请求解析失败：%v", err)
		}
		gotPassword = body["password"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"msg":"测试结束","data":null}`))
	}))
	defer server.Close()

	service := &Service{}
	_, err := service.RunMiddleAcceleration(MiddleAccelerationRequest{
		RelayURL:     server.URL,
		Username:     "user",
		Password:     " pass with spaces ",
		EntryID:      "7",
		MiddleDomain: "relay.example.com",
		InputText:    "anytls://token@203.0.113.10:20000#test",
		MaxRetry:     1,
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if gotPassword != " pass with spaces " {
		t.Fatalf("密码被意外修改：%q", gotPassword)
	}
}

func TestMiddleStatusRetryDelay(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 0},
		{attempt: 2, want: 2 * time.Second},
		{attempt: 3, want: 5 * time.Second},
		{attempt: 4, want: 10 * time.Second},
	}
	for _, item := range cases {
		if got := middleStatusRetryDelay(item.attempt); got != item.want {
			t.Fatalf("第 %d 次重试等待错误：want %s, got %s", item.attempt, item.want, got)
		}
	}
}

func TestMiddleExcelRoundTripAndOutputColumns(t *testing.T) {
	inputRows := [][]string{
		{"名称", "网络链接", "二维码"},
		{"节点一", "socks5://user:pass@127.0.0.1:1080", "扫码"},
		{"节点二", "http://127.0.0.1:8080", "扫码"},
	}
	data, err := buildSimpleXLSX(inputRows, nil)
	if err != nil {
		t.Fatalf("生成测试 Excel 失败：%v", err)
	}
	rows, err := readXLSXRows(data)
	if err != nil {
		t.Fatalf("读取测试 Excel 失败：%v", err)
	}
	if len(rows) != 3 || findColumn(rows[0], "网络链接") != 1 || rows[2][1] != inputRows[2][1] {
		t.Fatalf("Excel 往返数据不一致：%#v", rows)
	}

	outputRows, images := buildMiddleExcelRows(rows, 1, map[int]string{1: "socks5://relay.example.com:20001"})
	if findColumn(outputRows[0], "中转链接") != 2 || findColumn(outputRows[0], "中转二维码") != 4 {
		t.Fatalf("中转列位置错误：%#v", outputRows[0])
	}
	if outputRows[1][2] != "socks5://relay.example.com:20001" || outputRows[2][2] != "" {
		t.Fatalf("中转结果未写入对应行：%#v", outputRows)
	}
	if len(images) != 3 {
		t.Fatalf("二维码数量错误：want 3, got %d", len(images))
	}
}

func TestMiddlePlainProxyParsingAndReplacement(t *testing.T) {
	cases := []struct {
		input  string
		output string
	}{
		{input: "203.0.113.10:1080", output: "relay.example.com:20001"},
		{input: "203.0.113.10:1080:user:pass:with:colon", output: "relay.example.com:20001:user:pass:with:colon"},
		{input: "[2001:db8::1]:1080:user:pass", output: "relay.example.com:20001:user:pass"},
	}
	for _, item := range cases {
		node, err := parseMiddleNodeLink(item.input)
		if err != nil {
			t.Fatalf("解析 %q 失败：%v", item.input, err)
		}
		got, err := replaceMiddleNodeEndpoint(node, "relay.example.com", 20001)
		if err != nil {
			t.Fatalf("替换 %q 失败：%v", item.input, err)
		}
		if got != item.output {
			t.Fatalf("替换结果错误：want %q, got %q", item.output, got)
		}
	}
}

func TestRunMiddleAccelerationReuseExisting(t *testing.T) {
	input := strings.Join([]string{
		"anytls://token1@203.0.113.10:20000#node-1",
		"anytls://token2@203.0.113.10:20001#node-2",
	}, "\n")
	var batchCreateCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":"token"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/user/devicegroup":
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":[{"id":7,"type":"DeviceGroupType_Inbound","name":"entry"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/user/forward/batch_create":
			batchCreateCalled = true
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":null}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/user/forward":
			// 后台已经存在 node-1 和 node-2 的正常规则
			c1, _ := json.Marshal(map[string]interface{}{"dest": []string{"203.0.113.10:20000"}})
			c2, _ := json.Marshal(map[string]interface{}{"dest": []string{"203.0.113.10:20001"}})
			items := []map[string]interface{}{
				{"id": 101, "name": "node-1", "listen_port": 31001, "status": "ForwardRuleStatus_Normal", "device_group_in": 7, "config": string(c1)},
				{"id": 102, "name": "node-2", "listen_port": 31002, "status": "ForwardRuleStatus_Normal", "device_group_in": 7, "config": string(c2)},
			}
			resp, _ := json.Marshal(map[string]interface{}{"code": 0, "msg": "ok", "data": items})
			_, _ = w.Write(resp)
		default:
			t.Fatalf("未预期请求：%s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	service := &Service{}
	result, err := service.RunMiddleAcceleration(MiddleAccelerationRequest{
		RelayURL:      server.URL,
		Username:      "user",
		Password:      "pass",
		EntryID:       "7",
		MiddleDomain:  "relay.example.com",
		InputText:     input,
		MaxRetry:      1,
		ReuseExisting: true,
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if !result.Success {
		t.Fatalf("执行未成功：%s", result.Message)
	}
	if batchCreateCalled {
		t.Fatal("全部规则均已有正常规则，不应该再调用 batch_create 创建重复规则")
	}
	if !strings.Contains(result.Output, "anytls://token1@relay.example.com:31001") {
		t.Fatalf("输出未正确复用 node-1 端口 31001: %s", result.Output)
	}
	if !strings.Contains(result.Output, "anytls://token2@relay.example.com:31002") {
		t.Fatalf("输出未正确复用 node-2 端口 31002: %s", result.Output)
	}
}

func TestFindNodeLinkColumnSmartMatching(t *testing.T) {
	// 测试场景 1: "节点链接" 表头
	rows1 := [][]string{
		{"序号", "节点链接", "备注"},
		{"1", "vmess://eyJhZGQiOiIxLjEuMS4xIiwicG9ydCI6NDQzfQ==", "香港"},
	}
	col1, err := findNodeLinkColumn(rows1, nil)
	if err != nil || col1 != 1 {
		t.Fatalf("匹配“节点链接”失败: col=%d, err=%v", col1, err)
	}

	// 测试场景 2: "URL" 表头
	rows2 := [][]string{
		{"URL", "地区"},
		{"socks5://1.2.3.4:1080", "日本"},
	}
	col2, err := findNodeLinkColumn(rows2, nil)
	if err != nil || col2 != 0 {
		t.Fatalf("匹配“URL”失败: col=%d, err=%v", col2, err)
	}

	// 测试场景 3: 表头没有标准关键字，但内容嗅探到协议链接
	rows3 := [][]string{
		{"ColA", "ColB", "ColC"},
		{"abc", "trojan://pass@1.2.3.4:443", "other"},
		{"def", "vmess://eyJhZGQiOiIyLjIuMi4yIiwicG9ydCI6NDQzfQ==", "other2"},
	}
	col3, err := findNodeLinkColumn(rows3, nil)
	if err != nil || col3 != 1 {
		t.Fatalf("内容嗅探识别链接列失败: col=%d, err=%v", col3, err)
	}
}

func TestSanitizeYixunRuleNameAndMatching(t *testing.T) {
	raw := "HK#01\r\n优质节点\t测试  # Reality"
	want := "HK-01 优质节点 测试 - Reality"
	got := sanitizeYixunRuleName(raw)
	if got != want {
		t.Fatalf("清洗节点名称不符：want %q, got %q", want, got)
	}

	// 验证 ruleMatches 能够通过 cleanNodeName 正常匹配
	node := middleNodeLink{
		Name: "US#01-节点",
		Host: "198.51.100.1",
		Port: 443,
	}
	destConfig, _ := json.Marshal(map[string]interface{}{"dest": []string{"198.51.100.1:443"}})
	rule := yixunRule{
		ID:            88,
		Name:          "US-01-节点", // 易巡后台已被清洗为 -
		ListenPort:    50001,
		DeviceGroupIn: 7,
		Config:        string(destConfig),
	}
	if !ruleMatches(7, node, rule) {
		t.Fatal("ruleMatches 未能正确匹配经清洗后的节点规则名称")
	}
}

func TestRunMiddleAccelerationChunkedBatch(t *testing.T) {
	// 构造 30 个待建节点，验证自动分批为 2 批 (25 + 5)
	var links []string
	for i := 1; i <= 30; i++ {
		links = append(links, fmt.Sprintf("socks5://198.51.100.%d:%d#Node#%d", i, 1000+i, i))
	}
	input := strings.Join(links, "\n")

	var batchCounts []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":"token"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/user/devicegroup":
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":[{"id":7,"type":"DeviceGroupType_Inbound","name":"entry"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/user/forward/batch_create":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			content := body["content"].(string)
			lines := strings.Split(strings.TrimSpace(content), "\n")
			batchCounts = append(batchCounts, len(lines))
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":null}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/user/forward":
			var items []map[string]interface{}
			for i := 1; i <= 30; i++ {
				c, _ := json.Marshal(map[string]interface{}{"dest": []string{fmt.Sprintf("198.51.100.%d:%d", i, 1000+i)}})
				items = append(items, map[string]interface{}{
					"id":              3000 + i,
					"name":            fmt.Sprintf("Node-%d", i),
					"listen_port":     40000 + i,
					"status":          "ForwardRuleStatus_Normal",
					"device_group_in": 7,
					"config":          string(c),
				})
			}
			resp, _ := json.Marshal(map[string]interface{}{
				"code": 0,
				"msg":  "ok",
				"data": items,
			})
			_, _ = w.Write(resp)
		default:
			t.Fatalf("未预期请求：%s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	service := &Service{}
	result, err := service.RunMiddleAcceleration(MiddleAccelerationRequest{
		RelayURL:     server.URL,
		Username:     "user",
		Password:     "pass",
		EntryID:      "7",
		MiddleDomain: "relay.example.com",
		InputText:    input,
		MaxRetry:     1,
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if !result.Success {
		t.Fatalf("执行未成功：%s\n日志：%v", result.Message, result.Logs)
	}
	if len(batchCounts) != 2 || batchCounts[0] != 25 || batchCounts[1] != 5 {
		t.Fatalf("分批导入数量不符，期望 [25, 5]，实际：%v", batchCounts)
	}
}

func TestYixunForwardListPagination(t *testing.T) {
	pageRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/user/forward" {
			pageRequests++
			page := r.URL.Query().Get("page")
			if page == "1" {
				// 第1页返回 1000 条规则，总数 1200
				items := make([]map[string]interface{}, 1000)
				for i := 0; i < 1000; i++ {
					items[i] = map[string]interface{}{
						"id":          i + 1,
						"name":        fmt.Sprintf("rule-%d", i+1),
						"listen_port": 20000 + i,
					}
				}
				resp, _ := json.Marshal(map[string]interface{}{
					"code":  0,
					"msg":   "ok",
					"data":  items,
					"count": 1200,
				})
				_, _ = w.Write(resp)
				return
			} else if page == "2" {
				// 第2页返回剩余 200 条规则
				items := make([]map[string]interface{}, 200)
				for i := 0; i < 200; i++ {
					items[i] = map[string]interface{}{
						"id":          1000 + i + 1,
						"name":        fmt.Sprintf("rule-%d", 1000+i+1),
						"listen_port": 21000 + i,
					}
				}
				resp, _ := json.Marshal(map[string]interface{}{
					"code":  0,
					"msg":   "ok",
					"data":  items,
					"count": 1200,
				})
				_, _ = w.Write(resp)
				return
			}
		}
		t.Fatalf("未预期请求：%s %s", r.Method, r.URL.String())
	}))
	defer server.Close()

	client := &yixunClient{
		baseURL: server.URL,
		client:  server.Client(),
	}
	rules, err := client.forwardList()
	if err != nil {
		t.Fatalf("forwardList 失败：%v", err)
	}
	if len(rules) != 1200 {
		t.Fatalf("规则获取数量不符：期望 1200，实际 %d", len(rules))
	}
	if pageRequests != 2 {
		t.Fatalf("分页请求次数不符：期望 2 次，实际 %d 次", pageRequests)
	}
}



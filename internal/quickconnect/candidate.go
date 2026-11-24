package quickconnect

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxQuickConnectInput = 2048

// Candidate 是一条快速连接输入的可能解释。Password 不返回前端，避免候选区显示明文密码。
type Candidate struct {
	ID           string `json:"id"`
	Scheme       string `json:"scheme"`
	Name         string `json:"name"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	User         string `json:"user"`
	HasPassword  bool   `json:"hasPassword"`
	PasswordHint string `json:"passwordHint"`
	Recommended  bool   `json:"recommended"`
	Reason       string `json:"reason"`
	Password     string `json:"-"`
	score        int
}

type candidateField struct {
	value    string
	position int
}

type candidateTarget struct {
	target       Target
	tokenIndex   int
	score        int
	explicitUser bool
	explicitPort bool
}

// ParseCandidates 解析一行连接信息，并按可信度返回最多六种解释。
func ParseCandidates(raw string) ([]Candidate, error) {
	body, name, err := splitQuickConnectName(raw)
	if err != nil {
		return nil, err
	}
	tokens, err := splitValues(body)
	if err != nil {
		return nil, fmt.Errorf("连接信息格式错误: %w", err)
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("请输入连接信息")
	}
	if len(tokens) > 12 {
		return nil, fmt.Errorf("连接信息字段过多；含空格的用户名或密码请使用引号")
	}

	if exact, ok, exactErr := parseExactCandidate(tokens, name); ok || exactErr != nil {
		if exactErr != nil {
			return nil, exactErr
		}
		return finalizeCandidates([]Candidate{exact}), nil
	}

	targets, err := selectCandidateTargets(tokens)
	if err != nil {
		return nil, err
	}

	options := make([]Candidate, 0, 8)
	for _, target := range targets {
		fields := make([]candidateField, 0, len(tokens)-1)
		for index, value := range tokens {
			if index != target.tokenIndex {
				fields = append(fields, candidateField{value: value, position: index})
			}
		}

		portChoices := []int{-1}
		if !target.explicitPort {
			for index, field := range fields {
				if _, portErr := parsePort(field.value); portErr == nil {
					portChoices = append(portChoices, index)
				}
			}
		}

		for _, portChoice := range portChoices {
			userChoices := []int{-1}
			if !target.explicitUser {
				userChoices = append(userChoices, candidateUsernameIndexes(target, fields, portChoice)...)
			}
			for _, userChoice := range userChoices {
				option := buildCandidate(target, fields, portChoice, userChoice, name)
				options = append(options, option)
			}
		}
	}
	return finalizeCandidates(options), nil
}

func candidateUsernameIndexes(target candidateTarget, fields []candidateField, portChoice int) []int {
	indexes := make([]int, 0, 3)
	seen := make(map[int]bool)
	add := func(index int) {
		if index < 0 || index == portChoice || seen[index] || !looksLikeUsername(fields[index].value) {
			return
		}
		seen[index] = true
		indexes = append(indexes, index)
	}

	if target.tokenIndex == 0 {
		expectedPosition := 1
		if portChoice >= 0 && fields[portChoice].position == expectedPosition {
			expectedPosition++
		}
		for index, field := range fields {
			if field.position == expectedPosition {
				add(index)
				break
			}
		}
	} else {
		for index, field := range fields {
			if field.position < target.tokenIndex {
				add(index)
				break
			}
		}
	}
	for index, field := range fields {
		if commonUsername(field.value) {
			add(index)
		}
	}
	if len(indexes) == 0 {
		for index := range fields {
			if index != portChoice {
				add(index)
				if len(indexes) > 0 {
					break
				}
			}
		}
	}
	return indexes
}

// SelectCandidate 重新解析原始输入并返回用户选中的解释。
func SelectCandidate(raw, id string) (Candidate, error) {
	candidates, err := ParseCandidates(raw)
	if err != nil {
		return Candidate{}, err
	}
	for _, candidate := range candidates {
		if candidate.ID == id {
			return candidate, nil
		}
	}
	return Candidate{}, fmt.Errorf("所选连接解释已失效，请重新确认")
}

func splitQuickConnectName(raw string) (string, string, error) {
	if utf8.RuneCountInString(raw) > maxQuickConnectInput {
		return "", "", fmt.Errorf("连接信息不能超过 %d 个字符", maxQuickConnectInput)
	}
	if strings.IndexFunc(raw, func(character rune) bool {
		return unicode.IsControl(character) && character != '\t'
	}) >= 0 {
		return "", "", fmt.Errorf("连接信息不能包含换行或控制字符")
	}
	body := raw
	name := ""
	if delimiter := lastQuickConnectNameDelimiter(raw); delimiter >= 0 {
		body = raw[:delimiter]
		name = strings.TrimSpace(raw[delimiter+4:])
		if name == "" {
			return "", "", fmt.Errorf("“----”后必须填写主机名")
		}
		if utf8.RuneCountInString(name) > 128 {
			return "", "", fmt.Errorf("主机名不能超过 128 个字符")
		}
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", "", fmt.Errorf("请输入 IP 或主机地址")
	}
	return body, name, nil
}

func lastQuickConnectNameDelimiter(raw string) int {
	last := -1
	var quote rune
	escaped := false
	for index, character := range raw {
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if character != '-' || !strings.HasPrefix(raw[index:], "----") || index == 0 {
			continue
		}
		previous, _ := utf8.DecodeLastRuneInString(raw[:index])
		if unicode.IsSpace(previous) {
			last = index
		}
	}
	return last
}

func parseExactCandidate(tokens []string, name string) (Candidate, bool, error) {
	if len(tokens) != 1 {
		return Candidate{}, false, nil
	}
	value := tokens[0]
	if candidate, ok, err := parseURLPasswordCandidate(value, name); ok || err != nil {
		return candidate, ok, err
	}
	if candidate, ok, err := parseBarePasswordCandidate(value, name); ok || err != nil {
		return candidate, ok, err
	}
	if candidate, ok, err := parseColonCandidate(value, name); ok || err != nil {
		return candidate, ok, err
	}
	return Candidate{}, false, nil
}

func parseURLPasswordCandidate(value, name string) (Candidate, bool, error) {
	if !strings.Contains(value, "://") {
		return Candidate{}, false, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User == nil {
		return Candidate{}, false, nil
	}
	password, hasPassword := parsed.User.Password()
	if !hasPassword {
		return Candidate{}, false, nil
	}
	if password == "" {
		return Candidate{}, true, fmt.Errorf("密码不能为空")
	}
	username := parsed.User.Username()
	parsed.User = url.User(username)
	target, err := parseURLTarget(parsed.String())
	if err != nil {
		return Candidate{}, true, err
	}
	return exactCandidate(target, password, name, "识别为 URL 中的用户名、密码、主机和端口"), true, nil
}

func parseBarePasswordCandidate(value, name string) (Candidate, bool, error) {
	if strings.Contains(value, "://") {
		return Candidate{}, false, nil
	}
	at := strings.LastIndexByte(value, '@')
	if at <= 0 {
		return Candidate{}, false, nil
	}
	credentials := value[:at]
	separator := strings.IndexByte(credentials, ':')
	if separator <= 0 {
		return Candidate{}, false, nil
	}
	username := credentials[:separator]
	password := credentials[separator+1:]
	if password == "" {
		return Candidate{}, true, fmt.Errorf("密码不能为空")
	}
	target, err := Parse(username + "@" + value[at+1:])
	if err != nil {
		return Candidate{}, true, err
	}
	return exactCandidate(target, password, name, "识别为 用户名:密码@主机:端口"), true, nil
}

func parseColonCandidate(value, name string) (Candidate, bool, error) {
	if net.ParseIP(value) != nil || strings.Contains(value, "::") || strings.HasPrefix(value, "[") || strings.ContainsAny(value, "@/\\") {
		return Candidate{}, false, nil
	}
	parts := strings.Split(value, ":")
	if len(parts) < 3 {
		return Candidate{}, false, nil
	}
	host := parts[0]
	if err := validateHost(host); err != nil {
		return Candidate{}, false, nil
	}
	port := 22
	userIndex := 1
	passwordIndex := 2
	reason := "识别为 主机:用户名:密码"
	if parsedPort, portErr := parsePort(parts[1]); portErr == nil {
		if len(parts) < 4 {
			return Candidate{}, true, fmt.Errorf("主机:端口 格式后还需要用户名和密码")
		}
		port = parsedPort
		userIndex = 2
		passwordIndex = 3
		reason = "识别为 主机:端口:用户名:密码"
	} else if strings.IndexFunc(parts[1], func(character rune) bool { return !unicode.IsDigit(character) }) < 0 {
		return Candidate{}, true, fmt.Errorf("端口必须是 1 到 65535 的整数")
	}
	user := parts[userIndex]
	password := strings.Join(parts[passwordIndex:], ":")
	if err := validateUser(user); err != nil {
		return Candidate{}, true, err
	}
	if user == "" || password == "" {
		return Candidate{}, true, fmt.Errorf("用户名和密码不能为空")
	}
	target := Target{Scheme: SchemeSSH, User: user, Host: host, Port: port}
	return exactCandidate(target, password, name, reason), true, nil
}

func exactCandidate(target Target, password, name, reason string) Candidate {
	if target.User == "" {
		target.User = defaultUser(target.Scheme)
	}
	if name == "" {
		name = target.Host
	} else {
		reason += "；使用 ---- 后内容作为主机名"
	}
	passwordHint := "未提供"
	if password != "" {
		passwordHint = "连接地址中"
		reason += "；密码：已填写（" + passwordHint + "）"
	}
	return Candidate{
		Scheme: target.Scheme, Name: name, Host: target.Host, Port: target.Port, User: target.User,
		Password: password, HasPassword: password != "", PasswordHint: passwordHint, Reason: reason, score: 2000,
	}
}

func selectCandidateTargets(tokens []string) ([]candidateTarget, error) {
	targets := make([]candidateTarget, 0, len(tokens))
	strong := make([]candidateTarget, 0, 1)
	for index, value := range tokens {
		target, err := Parse(value)
		if err != nil {
			continue
		}
		score := candidateTargetScore(value, target, index)
		candidate := candidateTarget{
			target: target, tokenIndex: index, score: score,
			explicitUser: target.User != "", explicitPort: targetTokenHasPort(value),
		}
		targets = append(targets, candidate)
		if candidateTargetHasStrongSignal(value, candidate) {
			strong = append(strong, candidate)
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("无法识别 IP 或主机地址")
	}
	if len(strong) > 0 {
		sort.SliceStable(strong, func(left, right int) bool { return strong[left].score > strong[right].score })
		return strong[:1], nil
	}
	sort.SliceStable(targets, func(left, right int) bool { return targets[left].score > targets[right].score })
	return targets, nil
}

func candidateTargetHasStrongSignal(value string, candidate candidateTarget) bool {
	ipText := candidate.target.Host
	if zone := strings.LastIndexByte(ipText, '%'); zone >= 0 {
		ipText = ipText[:zone]
	}
	return net.ParseIP(ipText) != nil || strings.Contains(candidate.target.Host, ".") ||
		strings.Contains(value, "://") || candidate.explicitUser || candidate.explicitPort
}

func candidateTargetScore(value string, target Target, index int) int {
	score := 0
	if index == 0 {
		score += 100
	}
	ipText := target.Host
	if zone := strings.LastIndexByte(ipText, '%'); zone >= 0 {
		ipText = ipText[:zone]
	}
	if net.ParseIP(ipText) != nil {
		score += 180
	} else if strings.Contains(target.Host, ".") {
		score += 150
	}
	if strings.Contains(value, "://") {
		score += 140
	}
	if target.User != "" {
		score += 60
	}
	if targetTokenHasPort(value) {
		score += 30
	}
	return score
}

func targetTokenHasPort(value string) bool {
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		return err == nil && parsed.Port() != ""
	}
	authority := value
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		authority = authority[at+1:]
	}
	if strings.HasPrefix(authority, "[") {
		closeBracket := strings.IndexByte(authority, ']')
		return closeBracket >= 0 && strings.HasPrefix(authority[closeBracket+1:], ":")
	}
	return strings.Count(authority, ":") == 1
}

func buildCandidate(target candidateTarget, fields []candidateField, portChoice, userChoice int, name string) Candidate {
	port := target.target.Port
	if portChoice >= 0 {
		port, _ = parsePort(fields[portChoice].value)
	}
	user := target.target.User
	if user == "" {
		user = defaultUser(target.target.Scheme)
		if userChoice >= 0 {
			user = fields[userChoice].value
		}
	}
	passwordParts := make([]string, 0, len(fields))
	passwordPositions := make([]int, 0, len(fields))
	for index, field := range fields {
		if index != portChoice && index != userChoice {
			passwordParts = append(passwordParts, field.value)
			passwordPositions = append(passwordPositions, field.position+1)
		}
	}
	password := strings.Join(passwordParts, " ")
	passwordHint := formatPasswordHint(passwordPositions)
	if name == "" {
		name = target.target.Host
	}

	score := target.score
	if target.explicitPort {
		score += 35
	} else if portChoice >= 0 {
		score += 35
		if fields[portChoice].position == target.tokenIndex+1 {
			score += 25
		}
	}
	if target.explicitUser {
		score += 60
	} else if userChoice >= 0 {
		if commonUsername(user) {
			score += 70
		} else {
			score += 25
		}
		if expectedUserPosition(target.tokenIndex, fields, portChoice, userChoice) {
			score += 35
		}
	} else {
		score += 12
	}
	if password != "" {
		score += 20
	}
	if conventionalNumericPassword(target, fields, portChoice, userChoice) {
		score += 40
	}
	if len(fields) == 1 && !target.explicitUser {
		if portChoice >= 0 {
			score += 55
		} else if userChoice < 0 && !commonUsername(fields[0].value) {
			score += 45
		} else if userChoice >= 0 && !commonUsername(user) {
			score -= 20
		}
	}

	reasonParts := make([]string, 0, 4)
	if target.explicitUser {
		reasonParts = append(reasonParts, "用户名来自连接地址")
	} else if userChoice >= 0 {
		reasonParts = append(reasonParts, "识别用户名")
	} else {
		reasonParts = append(reasonParts, "用户名默认 "+user)
	}
	if target.explicitPort {
		reasonParts = append(reasonParts, "端口来自连接地址")
	} else if portChoice >= 0 {
		reasonParts = append(reasonParts, "数字字段作为端口")
	} else {
		reasonParts = append(reasonParts, fmt.Sprintf("端口默认 %d", port))
	}
	if password != "" {
		reasonParts = append(reasonParts, "密码：已填写（"+passwordHint+"）")
	} else if target.target.Scheme == SchemeSSH {
		reasonParts = append(reasonParts, "密码：未提供，使用 SSH Agent")
	} else {
		reasonParts = append(reasonParts, "密码：未提供，由远程桌面询问")
	}
	if name != target.target.Host {
		reasonParts = append(reasonParts, "使用 ---- 后内容作为主机名")
	}
	return Candidate{
		Scheme: target.target.Scheme, Name: name, Host: target.target.Host, Port: port, User: user,
		Password: password, HasPassword: password != "", PasswordHint: passwordHint,
		Reason: strings.Join(reasonParts, "；"), score: score,
	}
}

func formatPasswordHint(positions []int) string {
	if len(positions) == 0 {
		return "未提供"
	}
	contiguous := true
	for index := 1; index < len(positions); index++ {
		if positions[index] != positions[index-1]+1 {
			contiguous = false
			break
		}
	}
	if len(positions) == 1 {
		return fmt.Sprintf("第 %d 项", positions[0])
	}
	if contiguous {
		return fmt.Sprintf("第 %d–%d 项", positions[0], positions[len(positions)-1])
	}
	items := make([]string, len(positions))
	for index, position := range positions {
		items[index] = fmt.Sprint(position)
	}
	return "第 " + strings.Join(items, "、") + " 项"
}

func conventionalNumericPassword(target candidateTarget, fields []candidateField, portChoice, userChoice int) bool {
	if target.tokenIndex != 0 || len(fields) != 2 || portChoice >= 0 || userChoice < 0 {
		return false
	}
	if fields[userChoice].position != 1 {
		return false
	}
	for index, field := range fields {
		if index == userChoice || field.position != 2 {
			continue
		}
		_, err := parsePort(field.value)
		return err == nil
	}
	return false
}

func expectedUserPosition(targetIndex int, fields []candidateField, portChoice, userChoice int) bool {
	userPosition := fields[userChoice].position
	if targetIndex == 0 {
		expected := 1
		if portChoice >= 0 && fields[portChoice].position == expected {
			expected++
		}
		return userPosition == expected
	}
	return userPosition < targetIndex
}

func looksLikeUsername(value string) bool {
	if value == "" || utf8.RuneCountInString(value) > 64 {
		return false
	}
	onlyDigits := true
	for _, character := range value {
		if !unicode.IsDigit(character) {
			onlyDigits = false
		}
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && !strings.ContainsRune("._-\\", character) {
			return false
		}
	}
	return !onlyDigits
}

func commonUsername(value string) bool {
	switch strings.ToLower(value) {
	case "root", "admin", "administrator", "ubuntu", "debian", "centos", "ec2-user", "opc", "azureuser", "deploy", "user":
		return true
	default:
		return false
	}
}

func defaultUser(scheme string) string {
	if scheme == SchemeRDP {
		return "administrator"
	}
	return "root"
}

func finalizeCandidates(options []Candidate) []Candidate {
	sort.SliceStable(options, func(left, right int) bool { return options[left].score > options[right].score })
	seen := make(map[string]bool)
	unique := make([]Candidate, 0, len(options))
	for _, option := range options {
		key := strings.Join([]string{
			option.Scheme, option.Name, option.Host, fmt.Sprint(option.Port), option.User, fmt.Sprint(option.HasPassword),
		}, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, option)
	}
	if len(unique) > 6 {
		unique = unique[:6]
	}
	for index := range unique {
		unique[index].ID = fmt.Sprintf("option-%d", index+1)
		unique[index].Recommended = index == 0
	}
	return unique
}

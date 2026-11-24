package quickconnect

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// SSHConfigHost 是 OpenSSH config 中可直接选择的非通配符主机。
type SSHConfigHost struct {
	Alias        string `json:"alias"`
	HostName     string `json:"hostName"`
	User         string `json:"user"`
	Port         int    `json:"port"`
	IdentityFile string `json:"identityFile"`
}

type configOptions struct {
	hostName     string
	user         string
	identityFile string
	port         int
	hostNameSet  bool
	userSet      bool
	identitySet  bool
	portSet      bool
}

type configBlock struct {
	patterns []string
	options  configOptions
}

type configReader struct {
	root         string
	resolvedRoot string
	visited      map[string]bool
	blocks       []*configBlock
	current      *configBlock
	aliases      []string
	aliasSeen    map[string]bool
}

// LoadOpenSSHConfig 读取当前用户 %USERPROFILE%/.ssh/config。
// 配置文件不存在时返回空列表，不视为错误。
func LoadOpenSSHConfig() ([]SSHConfigHost, error) {
	home := strings.TrimSpace(os.Getenv("USERPROFILE"))
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("无法确定用户目录: %w", err)
		}
	}
	configPath := filepath.Join(home, ".ssh", "config")
	hosts, err := ReadOpenSSHConfig(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return []SSHConfigHost{}, nil
	}
	return hosts, err
}

// ReadOpenSSHConfig 读取指定的用户 OpenSSH config。
// Include 仅允许引用主配置文件所在的 .ssh 目录及其子目录。
func ReadOpenSSHConfig(configPath string) ([]SSHConfigHost, error) {
	if strings.TrimSpace(configPath) == "" {
		return nil, fmt.Errorf("OpenSSH config 路径不能为空")
	}
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("解析 OpenSSH config 路径失败: %w", err)
	}
	root := filepath.Dir(absPath)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("解析 .ssh 目录失败: %w", err)
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return nil, fmt.Errorf("解析 .ssh 目录失败: %w", err)
	}

	reader := &configReader{
		root:         root,
		resolvedRoot: resolvedRoot,
		visited:      make(map[string]bool),
		aliasSeen:    make(map[string]bool),
	}
	global := &configBlock{}
	reader.blocks = append(reader.blocks, global)
	reader.current = global
	if err := reader.readFile(absPath, false); err != nil {
		return nil, err
	}
	return reader.result(), nil
}

func (r *configReader) readFile(filePath string, included bool) error {
	resolvedPath, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		return err
	}
	resolvedPath, err = filepath.Abs(resolvedPath)
	if err != nil {
		return err
	}
	if included && !pathWithin(r.resolvedRoot, resolvedPath) {
		return fmt.Errorf("Include 超出 .ssh 目录: %s", filePath)
	}
	key := strings.ToLower(filepath.Clean(resolvedPath))
	if r.visited[key] {
		return nil
	}
	r.visited[key] = true

	file, err := os.Open(resolvedPath)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if err := r.readLine(scanner.Text(), resolvedPath, lineNumber); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取 %s 失败: %w", resolvedPath, err)
	}
	return nil
}

func (r *configReader) readLine(line, source string, lineNumber int) error {
	keyword, value, err := splitDirective(line)
	if err != nil {
		return fmt.Errorf("%s:%d: %w", source, lineNumber, err)
	}
	if keyword == "" {
		return nil
	}
	values, err := splitValues(value)
	if err != nil {
		return fmt.Errorf("%s:%d: %w", source, lineNumber, err)
	}

	switch strings.ToLower(keyword) {
	case "include":
		if len(values) == 0 {
			return fmt.Errorf("%s:%d: Include 缺少路径", source, lineNumber)
		}
		for _, pattern := range values {
			if err := r.readIncludes(pattern); err != nil {
				return fmt.Errorf("%s:%d: %w", source, lineNumber, err)
			}
		}
	case "host":
		if len(values) == 0 {
			return fmt.Errorf("%s:%d: Host 缺少名称", source, lineNumber)
		}
		block := &configBlock{patterns: append([]string(nil), values...)}
		r.blocks = append(r.blocks, block)
		r.current = block
		for _, alias := range values {
			if isLiteralAlias(alias) {
				key := strings.ToLower(alias)
				if !r.aliasSeen[key] {
					r.aliasSeen[key] = true
					r.aliases = append(r.aliases, alias)
				}
			}
		}
	case "hostname", "user", "port", "identityfile":
		if len(values) != 1 {
			return fmt.Errorf("%s:%d: %s 必须有且仅有一个值", source, lineNumber, keyword)
		}
		if hasControl(values[0]) {
			return fmt.Errorf("%s:%d: %s 包含控制字符", source, lineNumber, keyword)
		}
		if err := applyOption(&r.current.options, strings.ToLower(keyword), values[0]); err != nil {
			return fmt.Errorf("%s:%d: %w", source, lineNumber, err)
		}
	}
	return nil
}

func (r *configReader) readIncludes(pattern string) error {
	if pattern == "" || hasControl(pattern) {
		return fmt.Errorf("Include 路径无效")
	}
	normalized, err := r.normalizeInclude(pattern)
	if err != nil {
		return err
	}
	matches, err := filepath.Glob(normalized)
	if err != nil {
		return fmt.Errorf("Include 路径格式错误: %w", err)
	}
	sort.Strings(matches)
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if info.IsDir() {
			continue
		}
		if err := r.readFile(match, true); err != nil {
			return err
		}
	}
	return nil
}

func (r *configReader) normalizeInclude(pattern string) (string, error) {
	pattern = filepath.FromSlash(pattern)
	homePrefix := "~" + string(filepath.Separator)
	sshPrefix := homePrefix + ".ssh" + string(filepath.Separator)
	switch {
	case strings.EqualFold(pattern, "~"+string(filepath.Separator)+".ssh"):
		pattern = r.root
	case strings.HasPrefix(strings.ToLower(pattern), strings.ToLower(sshPrefix)):
		pattern = filepath.Join(r.root, pattern[len(sshPrefix):])
	case strings.HasPrefix(pattern, homePrefix) || pattern == "~":
		return "", fmt.Errorf("Include 仅允许使用 .ssh 目录")
	case !filepath.IsAbs(pattern):
		pattern = filepath.Join(r.root, pattern)
	}
	pattern = filepath.Clean(pattern)
	if !pathWithin(r.root, pattern) {
		return "", fmt.Errorf("Include 超出 .ssh 目录: %s", pattern)
	}
	return pattern, nil
}

func (r *configReader) result() []SSHConfigHost {
	hosts := make([]SSHConfigHost, 0, len(r.aliases))
	for _, alias := range r.aliases {
		var merged configOptions
		for _, block := range r.blocks {
			if block.matches(alias) {
				mergeFirst(&merged, block.options)
			}
		}
		hostName := alias
		if merged.hostNameSet {
			hostName = merged.hostName
		}
		port := 22
		if merged.portSet {
			port = merged.port
		}
		hosts = append(hosts, SSHConfigHost{
			Alias:        alias,
			HostName:     hostName,
			User:         merged.user,
			Port:         port,
			IdentityFile: merged.identityFile,
		})
	}
	return hosts
}

func (b *configBlock) matches(alias string) bool {
	if len(b.patterns) == 0 {
		return true
	}
	alias = strings.ToLower(alias)
	positive := false
	for _, rawPattern := range b.patterns {
		negated := strings.HasPrefix(rawPattern, "!")
		patternText := strings.TrimPrefix(rawPattern, "!")
		matched, err := path.Match(strings.ToLower(patternText), alias)
		if err != nil {
			continue
		}
		if negated && matched {
			return false
		}
		if !negated && matched {
			positive = true
		}
	}
	return positive
}

func applyOption(options *configOptions, keyword, value string) error {
	switch keyword {
	case "hostname":
		if !options.hostNameSet {
			options.hostName, options.hostNameSet = value, true
		}
	case "user":
		if !options.userSet {
			options.user, options.userSet = value, true
		}
	case "identityfile":
		if !options.identitySet {
			options.identityFile, options.identitySet = value, true
		}
	case "port":
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("Port 必须是 1 到 65535 的整数")
		}
		if !options.portSet {
			options.port, options.portSet = port, true
		}
	}
	return nil
}

func mergeFirst(destination *configOptions, source configOptions) {
	if source.hostNameSet && !destination.hostNameSet {
		destination.hostName, destination.hostNameSet = source.hostName, true
	}
	if source.userSet && !destination.userSet {
		destination.user, destination.userSet = source.user, true
	}
	if source.identitySet && !destination.identitySet {
		destination.identityFile, destination.identitySet = source.identityFile, true
	}
	if source.portSet && !destination.portSet {
		destination.port, destination.portSet = source.port, true
	}
}

func isLiteralAlias(alias string) bool {
	return alias != "" && !strings.HasPrefix(alias, "!") && !strings.ContainsAny(alias, "*?[") && !hasControl(alias)
}

func splitDirective(line string) (keyword, value string, err error) {
	if strings.IndexFunc(line, func(r rune) bool {
		return unicode.IsControl(r) && r != '\t'
	}) >= 0 {
		return "", "", fmt.Errorf("配置行包含控制字符")
	}
	line = stripComment(line)
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", nil
	}
	separator := strings.IndexFunc(line, func(r rune) bool {
		return unicode.IsSpace(r) || r == '='
	})
	if separator < 0 {
		return line, "", nil
	}
	keyword = line[:separator]
	value = strings.TrimSpace(line[separator:])
	if strings.HasPrefix(value, "=") {
		value = strings.TrimSpace(value[1:])
	}
	return keyword, value, nil
}

func stripComment(line string) string {
	var quote rune
	escaped := false
	for index, current := range line {
		if escaped {
			escaped = false
			continue
		}
		if current == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if current == quote {
				quote = 0
			}
			continue
		}
		if current == '\'' || current == '"' {
			quote = current
			continue
		}
		if current == '#' {
			return line[:index]
		}
	}
	return line
}

func splitValues(value string) ([]string, error) {
	var values []string
	var current strings.Builder
	var quote rune
	escaped := false
	started := false
	flush := func() {
		if started {
			values = append(values, current.String())
			current.Reset()
			started = false
		}
	}
	for _, character := range value {
		if escaped {
			if unicode.IsSpace(character) || character == '\\' || character == '\'' || character == '"' || character == '#' {
				current.WriteRune(character)
			} else {
				current.WriteRune('\\')
				current.WriteRune(character)
			}
			started = true
			escaped = false
			continue
		}
		if character == '\\' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				current.WriteRune(character)
			}
			started = true
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			started = true
			continue
		}
		if unicode.IsSpace(character) {
			flush()
			continue
		}
		current.WriteRune(character)
		started = true
	}
	if escaped {
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, fmt.Errorf("引号未闭合")
	}
	flush()
	return values, nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

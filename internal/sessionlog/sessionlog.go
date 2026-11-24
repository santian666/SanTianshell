// Package sessionlog 提供 SSH 会话终端输出的本地日志记录。
package sessionlog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxFileSize int64 = 10 << 20

// SessionInfo 是写入日志头的非敏感会话信息。
type SessionInfo struct {
	Name     string
	Address  string
	Port     int
	Username string
}

// Logger 将一个 SSH 会话写入独立日志文件。
type Logger struct {
	mu sync.Mutex

	info        SessionInfo
	dir         string
	path        string
	rotatedPath string
	file        *os.File
	size        int64
	limit       int64
	closed      bool
	ansiState   ansiState
	utf8Tail    []byte
}

type ansiState uint8

const (
	ansiText ansiState = iota
	ansiEscape
	ansiCSI
	ansiOSC
	ansiOSCEscape
)

// New 在 logDir 中为当前会话创建日志文件。
func New(logDir string, info SessionInfo) (*Logger, error) {
	return newLogger(logDir, info, maxFileSize, time.Now())
}

func newLogger(logDir string, info SessionInfo, limit int64, now time.Time) (*Logger, error) {
	if strings.TrimSpace(logDir) == "" {
		return nil, errors.New("日志目录不能为空")
	}
	if limit <= 0 {
		return nil, errors.New("日志文件大小限制必须大于 0")
	}

	dir, err := filepath.Abs(logDir)
	if err != nil {
		return nil, fmt.Errorf("解析日志目录: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建日志目录: %w", err)
	}

	base := safeFileName(info.Name)
	if base == "" {
		base = safeFileName(info.Address)
	}
	if base == "" {
		base = "SSH会话"
	}
	timestamp := now.Format("20060102-150405.000")
	file, path, err := createUniqueFile(dir, base+"_"+timestamp)
	if err != nil {
		return nil, err
	}

	l := &Logger{
		info:        info,
		dir:         dir,
		path:        path,
		rotatedPath: strings.TrimSuffix(path, filepath.Ext(path)) + ".1.log",
		file:        file,
		limit:       limit,
	}
	header := l.header(now, false)
	if int64(len(header)) > limit {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, errors.New("日志文件大小限制小于日志头")
	}
	if err := l.writeBytes(header); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return l, nil
}

// Path 返回当前日志文件的绝对路径。
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.path
}

// RotatedPath 返回轮转文件路径；文件可能尚未生成。
func (l *Logger) RotatedPath() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rotatedPath
}

// Write 清除终端控制序列后写入日志。可由多个 goroutine 并发调用。
func (l *Logger) Write(p []byte) (int, error) {
	if l == nil {
		return 0, os.ErrInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, os.ErrClosed
	}

	cleaned := l.cleanANSI(p)
	cleaned = l.cleanUTF8(cleaned, false)
	if len(cleaned) == 0 {
		return len(p), nil
	}
	if err := l.writeBytes(cleaned); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close 关闭日志。重复调用不会报错。
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true

	var errs []error
	if tail := l.cleanUTF8(nil, true); len(tail) > 0 {
		if err := l.writeBytes(tail); err != nil {
			errs = append(errs, err)
		}
	}
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			errs = append(errs, err)
		}
		l.file = nil
	}
	return errors.Join(errs...)
}

func (l *Logger) header(now time.Time, rotated bool) []byte {
	label := "开始时间"
	if rotated {
		label = "续写时间"
	}
	name := cleanMetadata(l.info.Name)
	if name == "" {
		name = "未命名主机"
	}
	address := cleanMetadata(l.info.Address)
	if l.info.Port > 0 && address != "" {
		address = fmt.Sprintf("%s:%d", address, l.info.Port)
	}
	username := cleanMetadata(l.info.Username)
	return []byte(fmt.Sprintf(
		"SanTianShell SSH 会话日志\n%s: %s\n主机: %s\n地址: %s\n用户: %s\n%s\n",
		label,
		now.Format(time.RFC3339),
		name,
		address,
		username,
		strings.Repeat("-", 48),
	))
}

func (l *Logger) writeBytes(data []byte) error {
	for len(data) > 0 {
		available := l.limit - l.size
		if available <= 0 {
			if err := l.rotate(); err != nil {
				return err
			}
			available = l.limit - l.size
		}

		count := len(data)
		if int64(count) > available {
			count = validUTF8Prefix(data, int(available))
			if count == 0 {
				oldAvailable := available
				if err := l.rotate(); err != nil {
					return err
				}
				if l.limit-l.size <= oldAvailable {
					return errors.New("日志文件大小限制不足以写入完整 UTF-8 字符")
				}
				continue
			}
		}
		n, err := l.file.Write(data[:count])
		l.size += int64(n)
		if err != nil {
			return fmt.Errorf("写入会话日志: %w", err)
		}
		if n != count {
			return io.ErrShortWrite
		}
		data = data[count:]
	}
	return nil
}

func (l *Logger) rotate() error {
	if l.file == nil {
		return os.ErrClosed
	}
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("关闭待轮转日志: %w", err)
	}
	l.file = nil
	if err := os.Remove(l.rotatedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("清理旧轮转日志: %w", err)
	}
	if err := os.Rename(l.path, l.rotatedPath); err != nil {
		return fmt.Errorf("轮转会话日志: %w", err)
	}

	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("创建轮转后日志: %w", err)
	}
	l.file = file
	l.size = 0
	header := l.header(time.Now(), true)
	if int64(len(header)) > l.limit {
		_ = file.Close()
		l.file = nil
		return errors.New("日志文件大小限制小于日志头")
	}
	return l.writeBytes(header)
}

func createUniqueFile(dir, stem string) (*os.File, string, error) {
	for i := 0; i < 1000; i++ {
		name := stem + ".log"
		if i > 0 {
			name = fmt.Sprintf("%s-%d.log", stem, i+1)
		}
		path, err := pathInside(dir, name)
		if err != nil {
			return nil, "", err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return file, path, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", fmt.Errorf("创建会话日志: %w", err)
		}
	}
	return nil, "", errors.New("无法生成唯一的会话日志文件名")
}

func pathInside(dir, name string) (string, error) {
	path := filepath.Join(dir, filepath.Base(name))
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("日志文件路径超出指定目录")
	}
	return path, nil
}

func safeFileName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			r = '_'
		}
		b.WriteRune(r)
	}
	name = strings.Trim(b.String(), " .")
	name = truncateRunes(name, 72)
	if isWindowsReservedName(name) {
		name = "_" + name
	}
	return name
}

func isWindowsReservedName(name string) bool {
	base := strings.ToUpper(strings.TrimSpace(strings.SplitN(name, ".", 2)[0]))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		return base[3] >= '1' && base[3] <= '9'
	}
	return false
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

func cleanMetadata(value string) string {
	value = strings.ToValidUTF8(value, "�")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
}

func (l *Logger) cleanANSI(input []byte) []byte {
	cleaned := make([]byte, 0, len(input))
	for _, b := range input {
		switch l.ansiState {
		case ansiText:
			if b == 0x1b {
				l.ansiState = ansiEscape
				continue
			}
			cleaned = append(cleaned, b)
		case ansiEscape:
			switch b {
			case '[':
				l.ansiState = ansiCSI
			case ']':
				l.ansiState = ansiOSC
			default:
				l.ansiState = ansiText
			}
		case ansiCSI:
			if b >= 0x40 && b <= 0x7e {
				l.ansiState = ansiText
			}
		case ansiOSC:
			switch b {
			case 0x07:
				l.ansiState = ansiText
			case 0x1b:
				l.ansiState = ansiOSCEscape
			}
		case ansiOSCEscape:
			if b == '\\' {
				l.ansiState = ansiText
			} else if b != 0x1b {
				l.ansiState = ansiOSC
			}
		}
	}
	return cleaned
}

func (l *Logger) cleanUTF8(input []byte, final bool) []byte {
	data := make([]byte, 0, len(l.utf8Tail)+len(input))
	data = append(data, l.utf8Tail...)
	data = append(data, input...)
	l.utf8Tail = l.utf8Tail[:0]

	var cleaned bytes.Buffer
	for len(data) > 0 {
		if !utf8.FullRune(data) && !final {
			l.utf8Tail = append(l.utf8Tail, data...)
			break
		}
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			cleaned.WriteRune(utf8.RuneError)
			data = data[1:]
			continue
		}
		data = data[size:]
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			continue
		}
		cleaned.WriteRune(r)
	}
	return cleaned.Bytes()
}

func validUTF8Prefix(data []byte, limit int) int {
	if limit >= len(data) {
		return len(data)
	}
	if limit <= 0 {
		return 0
	}
	for limit > 0 && !utf8.Valid(data[:limit]) {
		limit--
	}
	return limit
}

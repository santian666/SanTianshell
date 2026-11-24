package remote

import (
	"crypto/des"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type finalShellHost struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	UserName           string `json:"user_name"`
	Password           string `json:"password"`
	SecretKeyID        string `json:"secret_key_id"`
	Description        string `json:"description"`
	TerminalEncoding   string `json:"terminal_encoding"`
	ConnectionType     int    `json:"conection_type"`
	AuthenticationType int    `json:"authentication_type"`
	DriveStoreRedirect bool   `json:"drivestoredirect"`
	Width              int    `json:"width"`
	Height             int    `json:"height"`
	AccessTime         int64  `json:"access_time"`
}

func IsFinalShellConfig(raw []byte) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	if items, ok := value.([]any); ok {
		if len(items) == 0 {
			return false
		}
		value = items[0]
	}
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	_, ok = object["conection_type"]
	return ok
}

func ParseFinalShellHosts(raw []byte) ([]Host, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		var value json.RawMessage
		if objectErr := json.Unmarshal(raw, &value); objectErr != nil || len(value) == 0 || value[0] != '{' {
			return nil, errors.New("FinalShell 配置必须是 JSON 对象或数组")
		}
		values = []json.RawMessage{value}
	}
	hosts := make([]Host, 0, len(values))
	for _, value := range values {
		var marker map[string]json.RawMessage
		if err := json.Unmarshal(value, &marker); err != nil {
			return nil, errors.New("FinalShell 主机记录格式错误")
		}
		if _, ok := marker["conection_type"]; !ok {
			return nil, errors.New("不是 FinalShell 导出的主机配置")
		}
		var source finalShellHost
		if err := json.Unmarshal(value, &source); err != nil {
			return nil, fmt.Errorf("解析 FinalShell 主机失败: %w", err)
		}
		if strings.TrimSpace(source.Host) == "" {
			return nil, errors.New("FinalShell 主机地址为空")
		}
		if source.Port < 1 || source.Port > 65535 {
			return nil, fmt.Errorf("FinalShell 主机 %s 的端口无效", source.Name)
		}
		protocol := "ssh"
		if source.Port == 3389 {
			protocol = "rdp"
		}
		username := strings.TrimSpace(source.UserName)
		if username == "" {
			if protocol == "rdp" {
				username = "administrator"
			} else {
				username = "root"
			}
		}
		host := Host{
			ID:            "fs-" + strings.TrimSpace(source.ID),
			Name:          strings.TrimSpace(source.Name),
			Group:         "FinalShell",
			Address:       strings.TrimSpace(source.Host),
			Port:          source.Port,
			Username:      username,
			Remark:        strings.TrimSpace(source.Description),
			Protocol:      protocol,
			AuthType:      "password",
			Encoding:      strings.TrimSpace(source.TerminalEncoding),
			Timeout:       12,
			KeepAlive:     30,
			RDPMapDrives:  source.DriveStoreRedirect,
			RDPResolution: "1280x1024",
		}
		if host.ID == "fs-" {
			host.ID = ""
		}
		if host.Name == "" {
			host.Name = host.Address
		}
		if host.Encoding == "" {
			host.Encoding = "UTF-8"
		}
		if source.Width > 0 && source.Height > 0 {
			host.RDPResolution = fmt.Sprintf("%dx%d", source.Width, source.Height)
		}
		if source.AccessTime > 0 {
			host.LastUsed = time.UnixMilli(source.AccessTime).Format(time.RFC3339)
		}
		if strings.TrimSpace(source.Password) != "" {
			password, err := DecodeFinalShellPassword(source.Password)
			if err != nil {
				return nil, fmt.Errorf("FinalShell 主机 %s 的密码解密失败: %w", host.Name, err)
			}
			host.Password = password
		} else if strings.TrimSpace(source.SecretKeyID) != "" {
			host.AuthType = "key"
			host.Remark = strings.TrimSpace(host.Remark + "；FinalShell 私钥需要重新选择")
		}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

func DecodeFinalShellPassword(value string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(raw) <= 8 || (len(raw)-8)%des.BlockSize != 0 {
		return "", errors.New("密码密文格式无效")
	}
	head, encrypted := raw[:8], raw[8:]
	key, err := finalShellRandomKey(head)
	if err != nil {
		return "", err
	}
	block, err := des.NewCipher(key[:8])
	if err != nil {
		return "", err
	}
	plain := make([]byte, len(encrypted))
	for offset := 0; offset < len(encrypted); offset += des.BlockSize {
		block.Decrypt(plain[offset:offset+des.BlockSize], encrypted[offset:offset+des.BlockSize])
	}
	padding := int(plain[len(plain)-1])
	if padding < 1 || padding > des.BlockSize || padding > len(plain) {
		return "", errors.New("密码填充无效")
	}
	for _, value := range plain[len(plain)-padding:] {
		if int(value) != padding {
			return "", errors.New("密码填充无效")
		}
	}
	plain = plain[:len(plain)-padding]
	if !utf8.Valid(plain) {
		return "", errors.New("密码不是有效的 UTF-8 文本")
	}
	return string(plain), nil
}

type finalShellJavaRandom struct{ seed uint64 }

func newFinalShellJavaRandom(seed int64) *finalShellJavaRandom {
	return &finalShellJavaRandom{seed: (uint64(seed) ^ 0x5deece66d) & ((1 << 48) - 1)}
}

func (r *finalShellJavaRandom) next(bits uint) uint32 {
	r.seed = (r.seed*0x5deece66d + 0xb) & ((1 << 48) - 1)
	return uint32(r.seed >> (48 - bits))
}

func (r *finalShellJavaRandom) nextInt(bound int32) int32 {
	if bound&(bound-1) == 0 {
		return int32((int64(bound) * int64(r.next(31))) >> 31)
	}
	for {
		bits := int32(r.next(31))
		value := bits % bound
		if int32(bits-value+bound-1) >= 0 {
			return value
		}
	}
}

func (r *finalShellJavaRandom) nextLong() int64 {
	high := int64(int32(r.next(32)))
	low := int64(int32(r.next(32)))
	return high<<32 + low
}

func finalShellRandomKey(head []byte) ([]byte, error) {
	divisor := newFinalShellJavaRandom(int64(int8(head[5]))).nextInt(127)
	if divisor == 0 {
		return nil, errors.New("无法生成 FinalShell 密钥")
	}
	random := newFinalShellJavaRandom(3680984568597093857 / int64(divisor))
	for count := int(int8(head[0])); count > 0; count-- {
		random.nextLong()
	}
	second := newFinalShellJavaRandom(random.nextLong())
	values := []int64{
		int64(int8(head[4])), second.nextLong(), int64(int8(head[7])), int64(int8(head[3])),
		second.nextLong(), int64(int8(head[1])), random.nextLong(), int64(int8(head[2])),
	}
	serialized := make([]byte, 8*len(values))
	for index, value := range values {
		binary.BigEndian.PutUint64(serialized[index*8:], uint64(value))
	}
	digest := md5.Sum(serialized)
	return digest[:], nil
}

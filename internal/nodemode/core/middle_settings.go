package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const middleSettingsHistoryLimit = 20

var middleSettingsFileMagic = []byte("VMS1")

type MiddleSettings struct {
	RelayURL     string `json:"relayUrl"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	EntryID      string `json:"entryId"`
	MiddleDomain string `json:"middleDomain"`
}

type MiddleAccountHistory struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type MiddleSettingsHistory struct {
	Current       MiddleSettings         `json:"current"`
	RelayURLs     []string               `json:"relayUrls"`
	Accounts      []MiddleAccountHistory `json:"accounts"`
	EntryIDs      []string               `json:"entryIds"`
	MiddleDomains []string               `json:"middleDomains"`
}

func (s *Service) GetMiddleSettingsHistory() (MiddleSettingsHistory, error) {
	s.middleSettingsMu.Lock()
	defer s.middleSettingsMu.Unlock()
	return s.getMiddleSettingsHistoryLocked()
}

func (s *Service) getMiddleSettingsHistoryLocked() (MiddleSettingsHistory, error) {
	path := s.middleSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return MiddleSettingsHistory{}, nil
		}
		return MiddleSettingsHistory{}, errors.New("读取中转设置历史失败")
	}
	if len(data) <= len(middleSettingsFileMagic) || string(data[:len(middleSettingsFileMagic)]) != string(middleSettingsFileMagic) {
		return MiddleSettingsHistory{}, errors.New("中转设置历史文件格式无效")
	}
	plain, err := unprotectMiddleSettings(data[len(middleSettingsFileMagic):])
	if err != nil {
		return MiddleSettingsHistory{}, errors.New("无法解密中转设置历史，请在当前 Windows 用户下使用")
	}
	var history MiddleSettingsHistory
	if err := json.Unmarshal(plain, &history); err != nil {
		return MiddleSettingsHistory{}, errors.New("中转设置历史内容无效")
	}
	return history, nil
}

func (s *Service) SaveMiddleSettings(settings MiddleSettings) (MiddleSettingsHistory, error) {
	s.middleSettingsMu.Lock()
	defer s.middleSettingsMu.Unlock()
	settings = cleanMiddleSettings(settings)
	history, err := s.getMiddleSettingsHistoryLocked()
	if err != nil {
		return MiddleSettingsHistory{}, err
	}
	history.Current = settings
	history.RelayURLs = prependMiddleHistory(history.RelayURLs, settings.RelayURL)
	history.EntryIDs = prependMiddleHistory(history.EntryIDs, settings.EntryID)
	history.MiddleDomains = prependMiddleHistory(history.MiddleDomains, settings.MiddleDomain)
	history.Accounts = prependMiddleAccount(history.Accounts, settings.Username, settings.Password)

	plain, err := json.Marshal(history)
	if err != nil {
		return MiddleSettingsHistory{}, errors.New("生成中转设置历史失败")
	}
	protected, err := protectMiddleSettings(plain)
	if err != nil {
		return MiddleSettingsHistory{}, errors.New("加密中转设置历史失败")
	}
	path := s.middleSettingsPath()
	data := append(append([]byte(nil), middleSettingsFileMagic...), protected...)
	if err := writeBytesAtomic(path, data, 0600); err != nil {
		return MiddleSettingsHistory{}, errors.New("保存中转设置历史失败")
	}
	return history, nil
}

func (s *Service) middleSettingsPath() string {
	return filepath.Join(s.dataDir, "runtime", "middle_settings.dat")
}

func cleanMiddleSettings(settings MiddleSettings) MiddleSettings {
	settings.RelayURL = strings.TrimSpace(settings.RelayURL)
	settings.Username = strings.TrimSpace(settings.Username)
	settings.EntryID = strings.TrimSpace(settings.EntryID)
	settings.MiddleDomain = strings.TrimSpace(settings.MiddleDomain)
	return settings
}

func prependMiddleHistory(items []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	result := make([]string, 0, min(len(items)+1, middleSettingsHistoryLimit))
	result = append(result, value)
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || item == value {
			continue
		}
		result = append(result, item)
		if len(result) == middleSettingsHistoryLimit {
			break
		}
	}
	return result
}

func prependMiddleAccount(items []MiddleAccountHistory, username string, password string) []MiddleAccountHistory {
	username = strings.TrimSpace(username)
	if username == "" {
		return items
	}
	if password == "" {
		for _, item := range items {
			if item.Username == username {
				password = item.Password
				break
			}
		}
	}
	if password == "" {
		return items
	}
	result := make([]MiddleAccountHistory, 0, min(len(items)+1, middleSettingsHistoryLimit))
	result = append(result, MiddleAccountHistory{Username: username, Password: password})
	for _, item := range items {
		item.Username = strings.TrimSpace(item.Username)
		if item.Username == "" || item.Username == username {
			continue
		}
		result = append(result, item)
		if len(result) == middleSettingsHistoryLimit {
			break
		}
	}
	return result
}

package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

type Store struct{ path string }

func New() (*Store, error) {
	base, err := windows.KnownFolderPath(windows.FOLDERID_RoamingAppData, windows.KF_FLAG_CREATE)
	if err != nil || base == "" {
		base, err = os.UserConfigDir()
	}
	if err != nil {
		return nil, err
	}
	if base == "" {
		return nil, errors.New("Windows 用户配置目录为空")
	}
	dir := filepath.Join(base, "SanTianShell")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dir, "settings.dat")}, nil
}

func (s *Store) Load(v any) error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	plain, err := crypt(raw, false)
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}

func (s *Store) Save(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	sealed, err := crypt(raw, true)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func crypt(input []byte, encrypt bool) ([]byte, error) {
	if len(input) == 0 {
		return []byte{}, nil
	}
	in := windows.DataBlob{Size: uint32(len(input)), Data: &input[0]}
	var out windows.DataBlob
	var err error
	if encrypt {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	runtime.KeepAlive(input)
	if err != nil {
		return nil, err
	}
	if out.Size == 0 {
		return []byte{}, nil
	}
	if out.Data == nil {
		return nil, errors.New("Windows DPAPI 返回了无效数据")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

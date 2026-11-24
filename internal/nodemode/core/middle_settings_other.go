//go:build !windows

package core

import "errors"

func protectMiddleSettings([]byte) ([]byte, error) {
	return nil, errors.New("中转设置加密仅支持 Windows")
}

func unprotectMiddleSettings([]byte) ([]byte, error) {
	return nil, errors.New("中转设置解密仅支持 Windows")
}

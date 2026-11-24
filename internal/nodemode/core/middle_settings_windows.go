//go:build windows

package core

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func protectMiddleSettings(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return []byte{}, nil
	}
	input := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var output windows.DataBlob
	if err := windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output); err != nil {
		return nil, err
	}
	runtime.KeepAlive(data)
	if output.Size == 0 {
		return []byte{}, nil
	}
	if output.Data == nil {
		return nil, errors.New("Windows DPAPI 返回了无效数据")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	return append([]byte(nil), unsafe.Slice(output.Data, output.Size)...), nil
}

func unprotectMiddleSettings(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return []byte{}, nil
	}
	input := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var output windows.DataBlob
	if err := windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output); err != nil {
		return nil, err
	}
	runtime.KeepAlive(data)
	if output.Size == 0 {
		return []byte{}, nil
	}
	if output.Data == nil {
		return nil, errors.New("Windows DPAPI 返回了无效数据")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	return append([]byte(nil), unsafe.Slice(output.Data, output.Size)...), nil
}

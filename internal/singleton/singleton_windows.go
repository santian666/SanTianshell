//go:build windows

package singleton

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procFindWindow     = windows.NewLazySystemDLL("user32.dll").NewProc("FindWindowW")
	procShowWindow     = windows.NewLazySystemDLL("user32.dll").NewProc("ShowWindow")
	procSetForeground  = windows.NewLazySystemDLL("user32.dll").NewProc("SetForegroundWindow")
	procIsIconic       = windows.NewLazySystemDLL("user32.dll").NewProc("IsIconic")
	mutexAlreadyExists = windows.ERROR_ALREADY_EXISTS
)

type Handle windows.Handle

// Acquire 创建当前 Windows 用户范围内的单实例互斥体。
// 返回的句柄必须在进程结束前保持打开；第二次启动返回 already=true。
func Acquire(name, title string) (handle Handle, already bool, err error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, false, err
	}
	created, createErr := windows.CreateMutex(nil, false, namePtr)
	handle = Handle(created)
	err = createErr
	if err != nil && !errors.Is(err, mutexAlreadyExists) {
		return 0, false, err
	}
	if errors.Is(err, mutexAlreadyExists) {
		return handle, true, nil
	}
	return handle, false, nil
}

func Release(handle Handle) { _ = windows.CloseHandle(windows.Handle(handle)) }

// Activate 按窗口标题唤醒已运行的主窗口。
func Activate(title string) {
	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	window, _, _ := procFindWindow.Call(0, uintptr(unsafe.Pointer(titlePtr)))
	if window == 0 {
		return
	}
	if iconic, _, _ := procIsIconic.Call(window); iconic != 0 {
		procShowWindow.Call(window, 9) // SW_RESTORE
	}
	procShowWindow.Call(window, 5) // SW_SHOW
	procSetForeground.Call(window)
}

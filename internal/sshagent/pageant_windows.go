//go:build windows

package sshagent

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	pageantMaxMessage  = 8192
	pageantCopyDataID  = 0x804e50ba
	windowMessageCopy  = 74
	sendAbortIfHung    = 0x0002
	sendBlock          = 0x0001
	pageantSendTimeout = 5000
)

var (
	sendMessageTimeoutProc = user32.NewProc("SendMessageTimeoutW")
	pageantQueryMu         sync.Mutex
	pageantSequence        atomic.Uint32
	pageantQuery           = queryPageant
)

type pageantCopyData struct {
	data   uintptr
	length uint32
	value  unsafe.Pointer
}

type pageantConnection struct {
	ctx    context.Context
	mu     sync.Mutex
	buffer []byte
	closed bool
}

func newPageantConnection(ctx context.Context) *pageantConnection {
	return &pageantConnection{ctx: ctx}
}

func (c *pageantConnection) Write(packet []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, errors.New("Pageant 连接已关闭")
	}
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	response, err := pageantQuery(packet)
	if err != nil {
		return 0, err
	}
	c.buffer = append(c.buffer[:0], response...)
	return len(packet), nil
}

func (c *pageantConnection) Read(target []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.buffer) == 0 {
		if c.closed {
			return 0, errors.New("Pageant 连接已关闭")
		}
		return 0, io.EOF
	}
	n := copy(target, c.buffer)
	c.buffer = c.buffer[n:]
	return n, nil
}

func (c *pageantConnection) Close() error {
	c.mu.Lock()
	c.closed = true
	c.buffer = nil
	c.mu.Unlock()
	return nil
}

func queryPageant(packet []byte) ([]byte, error) {
	if len(packet) < 4 || len(packet) > pageantMaxMessage {
		return nil, errors.New("Pageant 请求长度无效")
	}
	if int(binary.BigEndian.Uint32(packet[:4]))+4 != len(packet) {
		return nil, errors.New("Pageant 请求格式无效")
	}

	pageantQueryMu.Lock()
	defer pageantQueryMu.Unlock()
	window := findPageantWindow()
	if window == 0 {
		return nil, errors.New("未检测到 Pageant")
	}

	mapName := fmt.Sprintf("PageantRequest%08x%08x", windows.GetCurrentProcessId(), pageantSequence.Add(1))
	mapNameUTF16, err := syscall.UTF16PtrFromString(mapName)
	if err != nil {
		return nil, fmt.Errorf("创建 Pageant 请求名称失败: %w", err)
	}
	mapping, err := syscall.CreateFileMapping(syscall.InvalidHandle, nil, syscall.PAGE_READWRITE, 0, pageantMaxMessage, mapNameUTF16)
	if err != nil {
		return nil, fmt.Errorf("创建 Pageant 共享内存失败: %w", err)
	}
	defer syscall.CloseHandle(mapping)

	view, err := syscall.MapViewOfFile(mapping, syscall.FILE_MAP_WRITE, 0, 0, pageantMaxMessage)
	if err != nil {
		return nil, fmt.Errorf("映射 Pageant 共享内存失败: %w", err)
	}
	defer syscall.UnmapViewOfFile(view)
	var written uintptr
	if err := windows.WriteProcessMemory(windows.CurrentProcess(), view, &packet[0], uintptr(len(packet)), &written); err != nil {
		return nil, fmt.Errorf("写入 Pageant 共享内存失败: %w", err)
	}
	if written != uintptr(len(packet)) {
		return nil, errors.New("写入 Pageant 共享内存不完整")
	}

	mapNameBytes := append([]byte(mapName), 0)
	copyData := pageantCopyData{
		data:   pageantCopyDataID,
		length: uint32(len(mapNameBytes)),
		value:  unsafe.Pointer(&mapNameBytes[0]),
	}
	var pageantResult uintptr
	callOK, _, callErr := sendMessageTimeoutProc.Call(
		window,
		windowMessageCopy,
		0,
		uintptr(unsafe.Pointer(&copyData)),
		sendAbortIfHung|sendBlock,
		pageantSendTimeout,
		uintptr(unsafe.Pointer(&pageantResult)),
	)
	runtime.KeepAlive(mapNameBytes)
	runtime.KeepAlive(copyData)
	if callOK == 0 {
		if callErr != nil && !errors.Is(callErr, windows.ERROR_SUCCESS) {
			return nil, fmt.Errorf("Pageant 请求失败: %w", callErr)
		}
		return nil, errors.New("Pageant 请求超时或无响应")
	}
	if pageantResult == 0 {
		return nil, errors.New("Pageant 拒绝了 SSH Agent 请求")
	}
	header := make([]byte, 4)
	var read uintptr
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), view, &header[0], uintptr(len(header)), &read); err != nil || read != uintptr(len(header)) {
		if err != nil {
			return nil, fmt.Errorf("读取 Pageant 响应长度失败: %w", err)
		}
		return nil, errors.New("读取 Pageant 响应长度不完整")
	}
	responseLength := int(binary.BigEndian.Uint32(header))
	if responseLength < 1 || responseLength+4 > pageantMaxMessage {
		return nil, errors.New("Pageant 返回长度无效")
	}
	response := make([]byte, responseLength+4)
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), view, &response[0], uintptr(len(response)), &read); err != nil || read != uintptr(len(response)) {
		if err != nil {
			return nil, fmt.Errorf("读取 Pageant 响应失败: %w", err)
		}
		return nil, errors.New("读取 Pageant 响应不完整")
	}
	return response, nil
}

func findPageantWindow() uintptr {
	name, err := windows.UTF16PtrFromString("Pageant")
	if err != nil {
		return 0
	}
	window, _, _ := findWindowProc.Call(uintptr(unsafe.Pointer(name)), 0)
	runtime.KeepAlive(name)
	return window
}

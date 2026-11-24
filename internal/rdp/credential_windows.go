package rdp

import (
	"crypto/sha256"
	"errors"
	"os"
	"runtime"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	credentialTypeGeneric       = 1
	credentialPersistSession    = 1
	maxCredentialTargetLength   = 337
	maxCredentialUsernameLength = 513
	maxCredentialBlobSize       = 5 * 512
)

var (
	advapi32        = windows.NewLazySystemDLL("advapi32.dll")
	procCredWriteW  = advapi32.NewProc("CredWriteW")
	procCredDeleteW = advapi32.NewProc("CredDeleteW")
)

type nativeCredentialAttribute struct {
	Keyword   *uint16
	Flags     uint32
	ValueSize uint32
	Value     *byte
}

type nativeCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         *nativeCredentialAttribute
	TargetAlias        *uint16
	UserName           *uint16
}

type windowsCredentialBackend struct{}

func (windowsCredentialBackend) Write(target, username, password string) error {
	targetPointer, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	usernamePointer, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return err
	}
	passwordUTF16 := utf16.Encode([]rune(password))
	var passwordPointer *byte
	if len(passwordUTF16) > 0 {
		passwordPointer = (*byte)(unsafe.Pointer(&passwordUTF16[0]))
	}
	credential := nativeCredential{
		Type:               credentialTypeGeneric,
		TargetName:         targetPointer,
		CredentialBlobSize: uint32(len(passwordUTF16) * 2),
		CredentialBlob:     passwordPointer,
		Persist:            credentialPersistSession,
		UserName:           usernamePointer,
	}
	result, _, callErr := procCredWriteW.Call(uintptr(unsafe.Pointer(&credential)), 0)
	runtime.KeepAlive(targetPointer)
	runtime.KeepAlive(usernamePointer)
	runtime.KeepAlive(passwordUTF16)
	runtime.KeepAlive(credential)
	for index := range passwordUTF16 {
		passwordUTF16[index] = 0
	}
	if result == 0 {
		return windowsCallError("CredWriteW", callErr)
	}
	return nil
}

func (windowsCredentialBackend) Delete(target string) error {
	targetPointer, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	result, _, callErr := procCredDeleteW.Call(
		uintptr(unsafe.Pointer(targetPointer)),
		credentialTypeGeneric,
		0,
	)
	runtime.KeepAlive(targetPointer)
	if result != 0 || errors.Is(callErr, windows.ERROR_NOT_FOUND) {
		return nil
	}
	return windowsCallError("CredDeleteW", callErr)
}

func windowsCallError(name string, err error) error {
	if errno, ok := err.(syscall.Errno); !ok || errno == 0 {
		err = windows.ERROR_GEN_FAILURE
	}
	return os.NewSyscallError(name, err)
}

func credentialHash(username, password string) [32]byte {
	// 仅用于活动会话间比较，不在内存中长期保留明文密码。
	return sha256.Sum256([]byte(username + "\x00" + password))
}

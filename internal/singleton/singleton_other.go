//go:build !windows

package singleton

type Handle uintptr

func Acquire(string, string) (Handle, bool, error) { return 0, false, nil }
func Release(Handle)                               {}
func Activate(string)                              {}

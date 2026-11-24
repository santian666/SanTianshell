//go:build !windows

package core

func systemLocalProxyDialConfig() (proxyDialConfig, error) {
	return proxyDialConfig{}, nil
}

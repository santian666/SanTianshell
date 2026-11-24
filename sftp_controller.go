package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"santianshell/internal/remote"
	"santianshell/internal/sshagent"

	runtime2 "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/crypto/ssh"
)

func (a *App) ListFiles(id string, clientEpoch int, remotePath string) ([]remote.FileInfo, error) {
	if remotePath == "" {
		remotePath = "/"
	}
	shell, err := a.connectedShell(id, clientEpoch)
	if err != nil {
		return nil, err
	}
	return shell.ListTimeout(remotePath, 10*time.Second)
}
func (a *App) GetInitialDirectory(id string) (string, error) {
	return "/", nil
}
func (a *App) ReadRemoteFile(id string, clientEpoch int, path string) (string, error) {
	shell, err := a.connectedShell(id, clientEpoch)
	if err != nil {
		return "", err
	}
	return shell.Read(path)
}
func (a *App) SaveRemoteFile(id string, clientEpoch int, path, content string) error {
	shell, err := a.connectedShell(id, clientEpoch)
	if err != nil {
		return err
	}
	return shell.WriteFile(path, content)
}
func (a *App) dedicatedSFTPHost(id string) (remote.Host, bool) {
	a.mu.RLock()
	var host remote.Host
	found := false
	for _, item := range a.settings.Hosts {
		if item.ID == id {
			host = a.resolveCredential(item)
			found = true
			break
		}
	}
	if !found {
		if temp, exists := a.temporaryHosts[id]; exists {
			host = a.resolveCredential(temp)
			found = true
		}
	}
	a.mu.RUnlock()
	if !found || !isSSHHostProtocol(host.Protocol) || strings.TrimSpace(host.HostKey) == "" {
		return remote.Host{}, false
	}
	if strings.HasSuffix(host.AuthType, "_mfa") {
		return remote.Host{}, false
	}
	if a.ensureSSHJumpChainTrusted(host, false) != nil {
		return remote.Host{}, false
	}
	if host.AuthType == "agent" {
		host.AgentSigners = func(ctx context.Context) ([]ssh.Signer, io.Closer, error) {
			session, err := sshagent.Open(ctx, sshagent.DefaultMaxSigners)
			if err != nil {
				return nil, nil, err
			}
			return session.Signers(), session, nil
		}
	}
	host = a.wireSSHJumpDial(host, false, false)
	return host, true
}

func (a *App) UploadFile(id string, clientEpoch int, remoteDir string) (bool, error) {
	shell, e := a.connectedShell(id, clientEpoch)
	if e != nil {
		return false, e
	}
	ctx, e := a.runtimeContext()
	if e != nil {
		return false, e
	}
	local, e := runtime2.OpenFileDialog(ctx, runtime2.OpenDialogOptions{Title: "选择上传文件"})
	if e != nil {
		return false, e
	}
	if local == "" {
		return false, nil
	}
	remoteTarget := path.Join(remoteDir, filepath.Base(local))
	if sftpHost, ok := a.dedicatedSFTPHost(id); ok {
		e = remote.Upload(sftpHost, local, remoteTarget)
		if e == nil {
			return true, nil
		}
		var connErr *remote.ConnectionError
		if !errors.As(e, &connErr) {
			return false, e
		}
	}
	if e = shell.Upload(local, remoteTarget); e != nil {
		return false, e
	}
	return true, nil
}
func (a *App) DownloadFile(id string, clientEpoch int, remotePath, name string) (bool, error) {
	shell, e := a.connectedShell(id, clientEpoch)
	if e != nil {
		return false, e
	}
	ctx, e := a.runtimeContext()
	if e != nil {
		return false, e
	}
	a.mu.RLock()
	dir := a.settings.DownloadDirectory
	a.mu.RUnlock()
	if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
		dir = defaultDownloadDirectory()
	}
	local, e := runtime2.SaveFileDialog(ctx, runtime2.SaveDialogOptions{Title: "保存下载文件", DefaultDirectory: dir, DefaultFilename: filepath.Base(name)})
	if e != nil {
		return false, e
	}
	if local == "" {
		return false, nil
	}
	if sftpHost, ok := a.dedicatedSFTPHost(id); ok {
		e = remote.Download(sftpHost, remotePath, local)
		if e == nil {
			return true, nil
		}
		var connErr *remote.ConnectionError
		if !errors.As(e, &connErr) {
			return false, e
		}
	}
	if e = shell.Download(remotePath, local); e != nil {
		return false, e
	}
	return true, nil
}
func (a *App) CreateRemoteFile(id string, clientEpoch int, path string) error {
	shell, err := a.connectedShell(id, clientEpoch)
	if err != nil {
		return err
	}
	return shell.CreateFile(path)
}
func (a *App) CreateRemoteDirectory(id string, clientEpoch int, path string) error {
	shell, err := a.connectedShell(id, clientEpoch)
	if err != nil {
		return err
	}
	return shell.CreateDirectory(path)
}
func (a *App) RenameRemotePath(id string, clientEpoch int, oldPath, newPath string) error {
	shell, err := a.connectedShell(id, clientEpoch)
	if err != nil {
		return err
	}
	return shell.Rename(oldPath, newPath)
}
func (a *App) DeleteRemotePath(id string, clientEpoch int, path string) error {
	shell, err := a.connectedShell(id, clientEpoch)
	if err != nil {
		return err
	}
	return shell.Remove(path)
}
func (a *App) ChmodRemotePath(id string, clientEpoch int, path, mode string) error {
	value, e := strconv.ParseUint(mode, 8, 32)
	if e != nil || value > 0777 {
		return errors.New("权限格式无效，请输入 000 到 777")
	}
	shell, err := a.connectedShell(id, clientEpoch)
	if err != nil {
		return err
	}
	return shell.Chmod(path, os.FileMode(value))
}

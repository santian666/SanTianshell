package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const transferBufferSize = 64 << 10

const transferProgressStep = 1 << 20

// TransferStats 是一次主机间传输的累计进度。
type TransferStats struct {
	Files       int   `json:"files"`
	Directories int   `json:"directories"`
	Bytes       int64 `json:"bytes"`
}

// TransferBetweenClients 通过客户端本地中转，在两个已连接的 SSH 主机间流式复制文件或目录。
// overwrite 仅允许覆盖目标普通文件；目录会合并，符号链接和其他特殊文件不会复制。
func TransferBetweenClients(
	ctx context.Context,
	source, target *ssh.Client,
	sourcePath, targetPath string,
	overwrite bool,
	onProgress func(TransferStats),
) (TransferStats, error) {
	var stats TransferStats
	if ctx == nil {
		return stats, errors.New("传输上下文不能为空")
	}
	if source == nil || target == nil {
		return stats, errors.New("源主机和目标主机连接不能为空")
	}
	sourcePath, targetPath, err := cleanTransferPaths(sourcePath, targetPath)
	if err != nil {
		return stats, err
	}
	if err = ctx.Err(); err != nil {
		return stats, err
	}

	sourceSFTP, err := sftp.NewClient(source)
	if err != nil {
		return stats, fmt.Errorf("打开源主机 SFTP 失败: %w", err)
	}
	defer sourceSFTP.Close()
	targetSFTP, err := sftp.NewClient(target)
	if err != nil {
		return stats, fmt.Errorf("打开目标主机 SFTP 失败: %w", err)
	}
	defer targetSFTP.Close()

	stopCancel := context.AfterFunc(ctx, func() {
		_ = sourceSFTP.Close()
		_ = targetSFTP.Close()
	})
	defer stopCancel()

	if err = rejectRemoteSymlinkPath(sourceSFTP, sourcePath); err != nil {
		return stats, fmt.Errorf("源路径不安全 %s: %w", sourcePath, err)
	}
	info, err := sourceSFTP.Lstat(sourcePath)
	if err != nil {
		return stats, transferError(ctx, fmt.Errorf("读取源路径失败 %s: %w", sourcePath, err))
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return stats, fmt.Errorf("拒绝复制符号链接: %s", sourcePath)
	}
	if source == target && unsafeSameClientPath(sourcePath, targetPath, info.IsDir()) {
		return stats, errors.New("同一主机不能复制到相同路径或互相包含的目录")
	}

	transfer := &clientTransfer{
		ctx:        ctx,
		source:     sourceSFTP,
		target:     targetSFTP,
		overwrite:  overwrite,
		onProgress: onProgress,
		stats:      &stats,
	}
	if info.IsDir() {
		if err = transfer.preflightDirectory(sourcePath, targetPath); err != nil {
			return stats, transferError(ctx, err)
		}
		err = transfer.copyDirectory(sourcePath, targetPath, info)
	} else if info.Mode().IsRegular() {
		err = transfer.copyFile(sourcePath, targetPath, info)
	} else {
		err = fmt.Errorf("只支持复制普通文件和目录: %s", sourcePath)
	}
	if err != nil {
		return stats, transferError(ctx, err)
	}
	return stats, nil
}

type clientTransfer struct {
	ctx        context.Context
	source     *sftp.Client
	target     *sftp.Client
	overwrite  bool
	onProgress func(TransferStats)
	stats      *TransferStats
	notified   int64
}

// preflightDirectory 只读遍历整棵源树，避免默认不覆盖模式复制一半后才发现冲突。
func (t *clientTransfer) preflightDirectory(sourcePath, targetPath string) error {
	if err := t.checkContext(); err != nil {
		return err
	}
	if err := validateExistingRemoteDirectoryPath(t.target, pathpkg.Dir(targetPath)); err != nil {
		return fmt.Errorf("目标父目录不安全 %s: %w", pathpkg.Dir(targetPath), err)
	}
	if info, err := t.target.Lstat(targetPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("目标目录是符号链接: %s", targetPath)
		}
		if !info.IsDir() {
			return fmt.Errorf("目标路径已存在且不是目录: %s", targetPath)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("读取目标路径失败 %s: %w", targetPath, err)
	}

	items, err := t.source.ReadDir(sourcePath)
	if err != nil {
		return fmt.Errorf("预检源目录失败 %s: %w", sourcePath, err)
	}
	for _, item := range items {
		if err = t.checkContext(); err != nil {
			return err
		}
		sourceChild := pathpkg.Join(sourcePath, item.Name())
		targetChild := pathpkg.Join(targetPath, item.Name())
		childInfo, statErr := t.source.Lstat(sourceChild)
		if statErr != nil {
			return fmt.Errorf("预检源文件失败 %s: %w", sourceChild, statErr)
		}
		if childInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("拒绝复制符号链接: %s", sourceChild)
		}
		switch {
		case childInfo.IsDir():
			if targetInfo, targetErr := t.target.Lstat(targetChild); targetErr == nil {
				if targetInfo.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("目标目录是符号链接: %s", targetChild)
				}
				if !targetInfo.IsDir() {
					return fmt.Errorf("目标路径类型冲突: %s", targetChild)
				}
			} else if !os.IsNotExist(targetErr) {
				return fmt.Errorf("读取目标路径失败 %s: %w", targetChild, targetErr)
			}
			if err = t.preflightDirectory(sourceChild, targetChild); err != nil {
				return err
			}
		case childInfo.Mode().IsRegular():
			if targetInfo, targetErr := t.target.Lstat(targetChild); targetErr == nil {
				if targetInfo.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("目标文件是符号链接: %s", targetChild)
				}
				if !targetInfo.Mode().IsRegular() {
					return fmt.Errorf("目标路径类型冲突: %s", targetChild)
				}
				if !t.overwrite {
					return fmt.Errorf("目标文件已存在，未启用覆盖: %s", targetChild)
				}
			} else if !os.IsNotExist(targetErr) {
				return fmt.Errorf("读取目标路径失败 %s: %w", targetChild, targetErr)
			}
		default:
			return fmt.Errorf("拒绝复制特殊文件: %s", sourceChild)
		}
	}
	return nil
}

func (t *clientTransfer) copyDirectory(sourcePath, targetPath string, info os.FileInfo) error {
	if err := t.checkContext(); err != nil {
		return err
	}
	created, err := ensureRemoteDirectory(t.target, targetPath)
	if err != nil {
		return fmt.Errorf("创建目标目录失败 %s: %w", targetPath, err)
	}
	if created {
		if err = t.target.Chmod(targetPath, transferMode(info.Mode())); err != nil {
			return fmt.Errorf("保留目录权限失败 %s: %w", targetPath, err)
		}
	}
	t.stats.Directories++
	t.notify(true)

	items, err := t.source.ReadDir(sourcePath)
	if err != nil {
		return fmt.Errorf("读取源目录失败 %s: %w", sourcePath, err)
	}
	for _, item := range items {
		if err = t.checkContext(); err != nil {
			return err
		}
		sourceChild := pathpkg.Join(sourcePath, item.Name())
		targetChild := pathpkg.Join(targetPath, item.Name())
		childInfo, statErr := t.source.Lstat(sourceChild)
		if statErr != nil {
			return fmt.Errorf("读取源文件信息失败 %s: %w", sourceChild, statErr)
		}
		if childInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("拒绝复制符号链接: %s", sourceChild)
		}
		switch {
		case childInfo.IsDir():
			err = t.copyDirectory(sourceChild, targetChild, childInfo)
		case childInfo.Mode().IsRegular():
			err = t.copyFile(sourceChild, targetChild, childInfo)
		default:
			err = fmt.Errorf("拒绝复制特殊文件: %s", sourceChild)
		}
		if err != nil {
			return err
		}
	}
	_ = t.target.Chtimes(targetPath, info.ModTime(), info.ModTime())
	return nil
}

func (t *clientTransfer) copyFile(sourcePath, targetPath string, info os.FileInfo) error {
	if err := t.checkContext(); err != nil {
		return err
	}
	if _, err := ensureRemoteDirectory(t.target, pathpkg.Dir(targetPath)); err != nil {
		return fmt.Errorf("创建目标父目录失败 %s: %w", pathpkg.Dir(targetPath), err)
	}
	targetExists, err := validateTransferTarget(t.target, targetPath, t.overwrite)
	if err != nil {
		return err
	}

	sourceFile, err := t.source.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("打开源文件失败 %s: %w", sourcePath, err)
	}
	defer sourceFile.Close()
	temporary, temporaryPath, err := createRemoteTemporaryFile(t.target, targetPath)
	if err != nil {
		return fmt.Errorf("创建目标临时文件失败 %s: %w", targetPath, err)
	}
	closed := false
	committed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		if !committed {
			_ = t.target.Remove(temporaryPath)
		}
	}()

	buffer := make([]byte, transferBufferSize)
	for {
		if err = t.checkContext(); err != nil {
			return err
		}
		read, readErr := sourceFile.Read(buffer)
		if read > 0 {
			if err = writeTransferChunk(t.ctx, temporary, buffer[:read]); err != nil {
				return fmt.Errorf("写入目标文件失败 %s: %w", targetPath, err)
			}
			t.stats.Bytes += int64(read)
			t.notify(false)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("读取源文件失败 %s: %w", sourcePath, readErr)
		}
	}
	if err = temporary.Chmod(transferMode(info.Mode())); err != nil {
		return fmt.Errorf("保留文件权限失败 %s: %w", targetPath, err)
	}
	_ = t.target.Chtimes(temporaryPath, info.ModTime(), info.ModTime())
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("关闭目标临时文件失败 %s: %w", targetPath, err)
	}
	closed = true
	if err = t.checkContext(); err != nil {
		return err
	}

	// 提交前再次检查，避免默认不覆盖时被并发创建的目标悄悄替换。
	targetExists, err = validateTransferTarget(t.target, targetPath, t.overwrite)
	if err != nil {
		return err
	}
	if err = replaceRemoteFile(t.target, temporaryPath, targetPath, targetExists); err != nil {
		return fmt.Errorf("提交目标文件失败 %s: %w", targetPath, err)
	}
	committed = true
	t.stats.Files++
	t.notify(true)
	return nil
}

func (t *clientTransfer) checkContext() error {
	select {
	case <-t.ctx.Done():
		return t.ctx.Err()
	default:
		return nil
	}
}

func (t *clientTransfer) notify(force bool) {
	if t.onProgress != nil && (force || t.stats.Bytes-t.notified >= transferProgressStep) {
		t.notified = t.stats.Bytes
		t.onProgress(*t.stats)
	}
}

func cleanTransferPaths(sourcePath, targetPath string) (string, string, error) {
	if strings.TrimSpace(sourcePath) == "" || strings.TrimSpace(targetPath) == "" {
		return "", "", errors.New("源路径和目标路径不能为空")
	}
	sourcePath = pathpkg.Clean(sourcePath)
	targetPath = pathpkg.Clean(targetPath)
	if targetPath == "/" || targetPath == "." {
		return "", "", errors.New("目标路径不能是根目录或当前目录")
	}
	if sourcePath == "." {
		return "", "", errors.New("源路径不能是当前目录")
	}
	if escapesRemoteBase(sourcePath) || escapesRemoteBase(targetPath) {
		return "", "", errors.New("路径不能越过远程当前目录")
	}
	return sourcePath, targetPath, nil
}

func escapesRemoteBase(remotePath string) bool {
	return remotePath == ".." || strings.HasPrefix(remotePath, "../")
}

func unsafeSameClientPath(sourcePath, targetPath string, sourceIsDirectory bool) bool {
	if sourcePath == targetPath {
		return true
	}
	if !sourceIsDirectory {
		return false
	}
	return remotePathContains(sourcePath, targetPath) || remotePathContains(targetPath, sourcePath)
}

func remotePathContains(parent, child string) bool {
	if parent == "/" {
		return child != "/"
	}
	return strings.HasPrefix(child, strings.TrimSuffix(parent, "/")+"/")
}

func rejectRemoteSymlinkPath(client *sftp.Client, remotePath string) error {
	absolute := strings.HasPrefix(remotePath, "/")
	current := ""
	if absolute {
		current = "/"
	}
	for _, part := range strings.Split(strings.TrimPrefix(remotePath, "/"), "/") {
		if part == "" || part == "." {
			continue
		}
		current = pathpkg.Join(current, part)
		if absolute && !strings.HasPrefix(current, "/") {
			current = "/" + current
		}
		info, err := client.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("拒绝经过符号链接: %s", current)
		}
	}
	return nil
}

func validateExistingRemoteDirectoryPath(client *sftp.Client, directory string) error {
	directory = pathpkg.Clean(directory)
	if directory == "." || directory == "/" {
		return nil
	}
	absolute := strings.HasPrefix(directory, "/")
	current := ""
	if absolute {
		current = "/"
	}
	for _, part := range strings.Split(strings.TrimPrefix(directory, "/"), "/") {
		if part == "" || part == "." {
			continue
		}
		current = pathpkg.Join(current, part)
		if absolute && !strings.HasPrefix(current, "/") {
			current = "/" + current
		}
		info, err := client.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("目标目录包含符号链接: %s", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("目标路径不是目录: %s", current)
		}
	}
	return nil
}

func ensureRemoteDirectory(client *sftp.Client, directory string) (bool, error) {
	directory = pathpkg.Clean(directory)
	if directory == "." || directory == "/" {
		return false, nil
	}
	if escapesRemoteBase(directory) {
		return false, errors.New("目标目录不能越过远程当前目录")
	}
	absolute := strings.HasPrefix(directory, "/")
	current := ""
	if absolute {
		current = "/"
	}
	createdTarget := false
	for _, part := range strings.Split(strings.TrimPrefix(directory, "/"), "/") {
		if part == "" || part == "." {
			continue
		}
		current = pathpkg.Join(current, part)
		if absolute && !strings.HasPrefix(current, "/") {
			current = "/" + current
		}
		info, err := client.Lstat(current)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return false, fmt.Errorf("目标目录包含符号链接: %s", current)
			}
			if !info.IsDir() {
				return false, fmt.Errorf("目标路径不是目录: %s", current)
			}
		case os.IsNotExist(err):
			if err = client.Mkdir(current); err != nil {
				info, statErr := client.Lstat(current)
				if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
					return false, err
				}
			}
			if current == directory {
				createdTarget = true
			}
		default:
			return false, err
		}
	}
	return createdTarget, nil
}

func validateTransferTarget(client *sftp.Client, targetPath string, overwrite bool) (bool, error) {
	info, err := client.Lstat(targetPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("读取目标路径失败 %s: %w", targetPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("拒绝覆盖目标符号链接: %s", targetPath)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("目标已存在且不是普通文件: %s", targetPath)
	}
	if !overwrite {
		return false, fmt.Errorf("目标文件已存在，未启用覆盖: %s", targetPath)
	}
	return true, nil
}

func writeTransferChunk(ctx context.Context, target io.Writer, content []byte) error {
	for len(content) > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		written, err := target.Write(content)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		content = content[written:]
	}
	return nil
}

func transferMode(mode os.FileMode) os.FileMode {
	return mode & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
}

func transferError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

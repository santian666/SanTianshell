package core

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

func TestLoadVPSListOnFirstRunReturnsEmptyList(t *testing.T) {
	service := NewServiceWithDataDir(t.TempDir())
	list, err := service.loadVPSList()
	if err != nil {
		t.Fatalf("首次运行读取 VPS 列表失败：%v", err)
	}
	if list == nil || len(list) != 0 {
		t.Fatalf("首次运行 VPS 列表 = %#v，期望空列表", list)
	}
	if _, err := os.Stat(service.sshPath()); !os.IsNotExist(err) {
		t.Fatalf("只读空列表不应创建 ssh.json，stat 错误：%v", err)
	}
}

func TestDeleteNodeRecordOnFreshDBDoesNotFailMissingTable(t *testing.T) {
	service := NewServiceWithDataDir(t.TempDir())
	if err := service.DeleteNodeRecord(123); err != nil {
		t.Fatalf("空库首次调用 DeleteNodeRecord 失败：%v", err)
	}
}

func TestConcurrentMiddleSettingsHistoryPreservesIntegrity(t *testing.T) {
	service := NewServiceWithDataDir(t.TempDir())
	const workers = 12
	var wait sync.WaitGroup
	errs := make(chan error, workers*2)
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func(idx int) {
			defer wait.Done()
			_, err := service.SaveMiddleSettings(MiddleSettings{
				RelayURL:     fmt.Sprintf("https://relay-%d.example.com", idx),
				Username:     fmt.Sprintf("user-%d", idx),
				Password:     fmt.Sprintf("pass-%d", idx),
				EntryID:      fmt.Sprintf("%d", idx+1),
				MiddleDomain: fmt.Sprintf("m-%d.example.com", idx),
			})
			if err != nil {
				errs <- err
				return
			}
			if _, err := service.GetMiddleSettingsHistory(); err != nil {
				errs <- err
			}
		}(i)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发读写中转设置历史失败：%v", err)
	}
}

func TestConcurrentSaveVPSPreservesAllEntries(t *testing.T) {
	service := NewServiceWithDataDir(t.TempDir())
	if err := writeJSON(service.sshPath(), []VPS{}); err != nil {
		t.Fatal(err)
	}

	const count = 24
	var wait sync.WaitGroup
	errors := make(chan error, count)
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			alias := fmt.Sprintf("vps-%02d", index)
			if err := service.SaveVPS(VPS{Alias: alias, Address: fmt.Sprintf("192.0.2.%d", index+1), Port: 22, Username: "root"}); err != nil {
				errors <- err
			}
		}(index)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Fatalf("并发保存 VPS 失败：%v", err)
	}

	list, err := service.loadVPSList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != count {
		t.Fatalf("并发保存后 VPS 数量 = %d，期望 %d", len(list), count)
	}
}

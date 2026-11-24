package core

import (
	"bytes"
	"os"
	"testing"
)

func TestMiddleSettingsHistoryEncryptedAndLinked(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	first, err := service.SaveMiddleSettings(MiddleSettings{
		RelayURL:     "https://relay-one.example.com",
		Username:     "first-user",
		Password:     "first-password",
		EntryID:      "11",
		MiddleDomain: "one.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Accounts) != 1 || first.Accounts[0].Password != "first-password" {
		t.Fatalf("unexpected first account history: %#v", first.Accounts)
	}
	_, err = service.SaveMiddleSettings(MiddleSettings{
		RelayURL:     "https://relay-two.example.com",
		Username:     "second-user",
		Password:     "second-password",
		EntryID:      "22",
		MiddleDomain: "two.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	history, err := service.SaveMiddleSettings(MiddleSettings{
		RelayURL:     "https://relay-one.example.com",
		Username:     "first-user",
		Password:     "updated-password",
		EntryID:      "11",
		MiddleDomain: "one.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.RelayURLs) != 2 || history.RelayURLs[0] != "https://relay-one.example.com" {
		t.Fatalf("relay history should be unique and newest first: %#v", history.RelayURLs)
	}
	if len(history.Accounts) != 2 || history.Accounts[0].Username != "first-user" || history.Accounts[0].Password != "updated-password" {
		t.Fatalf("account password should update as one linked record: %#v", history.Accounts)
	}
	stored, err := os.ReadFile(service.middleSettingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("updated-password")) || bytes.Contains(stored, []byte("first-user")) {
		t.Fatal("middle settings history must not store credentials as plaintext")
	}
	loaded, err := service.GetMiddleSettingsHistory()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Current.Password != "updated-password" || loaded.Accounts[0].Password != "updated-password" {
		t.Fatalf("encrypted history did not round trip: %#v", loaded)
	}
}

func TestMiddleSettingsPreservesPasswordWhitespace(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	history, err := service.SaveMiddleSettings(MiddleSettings{
		RelayURL:     "https://relay.example.com",
		Username:     "user",
		Password:     " pass with spaces ",
		EntryID:      "7",
		MiddleDomain: "relay.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if history.Current.Password != " pass with spaces " || history.Accounts[0].Password != " pass with spaces " {
		t.Fatalf("密码被意外修改：%#v", history)
	}
}

package sshprompt

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBrokerAskResolve(t *testing.T) {
	broker := New()
	response, err := broker.Ask(context.Background(), Request{Kind: "hostkey"}, func(request Request) {
		if request.ID == "" {
			t.Fatal("请求缺少 ID")
		}
		if !broker.Resolve(request.ID, Response{Accepted: true, Answers: []string{"123456"}}) {
			t.Fatal("响应未送达")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Accepted || len(response.Answers) != 1 || response.Answers[0] != "123456" {
		t.Fatalf("响应不正确: %#v", response)
	}
}

func TestBrokerContextCancel(t *testing.T) {
	broker := New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := broker.Ask(ctx, Request{}, func(Request) {})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("期望超时，得到 %v", err)
	}
}

func TestBrokerClose(t *testing.T) {
	broker := New()
	broker.Close()
	_, err := broker.Ask(context.Background(), Request{}, func(Request) {})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("期望 ErrClosed，得到 %v", err)
	}
}

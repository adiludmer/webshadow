package model

import (
	"context"
	"errors"
	"testing"
)

func TestFakeFollowsScript(t *testing.T) {
	f := NewFake("fake", "one", "two")
	ctx := context.Background()
	for _, want := range []string{"one", "two"} {
		resp, err := f.Complete(ctx, Request{Messages: []Message{{RoleUser, "hi"}}})
		if err != nil || resp.Text != want {
			t.Fatalf("Complete = %q, %v; want %q", resp.Text, err, want)
		}
	}
	if _, err := f.Complete(ctx, Request{}); !errors.Is(err, ErrScriptExhausted) {
		t.Fatalf("want ErrScriptExhausted, got %v", err)
	}
	if got := len(f.Requests()); got != 3 {
		t.Fatalf("recorded %d requests, want 3", got)
	}
	if f.Requests()[0].Messages[0].Content != "hi" {
		t.Fatalf("request not recorded: %+v", f.Requests()[0])
	}
}

func TestFakeHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewFake("fake", "x").Complete(ctx, Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

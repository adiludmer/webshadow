package cdp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestASlowCommandDoesNotEndTheConnection covers a browser that stops
// reading for longer than a command's timeout: the command fails, and the
// connection, which carries every other target's events, stays up.
func TestASlowCommandDoesNotEndTheConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ws.SetReadLimit(64 << 20)
		defer ws.CloseNow()
		// Busy: nothing is read until well after the first command's
		// timeout.
		time.Sleep(500 * time.Millisecond)
		for {
			_, data, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			var m message
			if json.Unmarshal(data, &m) != nil {
				return
			}
			reply, _ := json.Marshal(message{ID: m.ID, Result: json.RawMessage(`{}`)})
			if err := ws.Write(context.Background(), websocket.MessageText, reply); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c, err := Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Large enough that the write is still in progress, filling the socket
	// buffers, when the timeout ends.
	big := map[string]string{"source": strings.Repeat("x", 4<<20)}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Call(ctx, "", "Page.addScriptToEvaluateOnNewDocument", big); err == nil {
		t.Fatal("the slow command did not time out")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if _, err := c.Call(ctx2, "", "Runtime.enable", nil); err != nil {
		t.Fatalf("the connection did not survive a slow command: %v", err)
	}
}

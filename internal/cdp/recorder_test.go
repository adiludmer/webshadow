package cdp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/adiludmer/webshadow/internal/chromium"
	"github.com/adiludmer/webshadow/internal/mitm"
	"github.com/adiludmer/webshadow/internal/recording"
)

func chromiumForTest(t *testing.T) string {
	t.Helper()
	if p := os.Getenv(chromium.EnvExecutable); p != "" {
		return p
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skipf("no Chromium found; set %s to run browser tests", chromium.EnvExecutable)
	return ""
}

const shopPage = `<!doctype html><title>shop</title>
<form action="/search" method="post">
  <input type="search" name="q" placeholder="Search the shop">
  <input type="password" name="pw" autocomplete="current-password">
  <button>Go</button>
</form>`

const resultsPage = `<!doctype html><title>results</title>
<a id="first" href="/item" style="display:block;width:200px;height:40px">First result</a>`

// TestRecordsNavigationAndInteractions drives a local shop the way a user
// would (type, submit, click a result) and checks the recording holds the
// navigations, the interactions with the password redacted, and a
// timeline where each action precedes the request it caused.
func TestRecordsNavigationAndInteractions(t *testing.T) {
	exe := chromiumForTest(t)
	itemSeen := make(chan struct{}, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			io.WriteString(w, shopPage)
		case "/search":
			io.WriteString(w, resultsPage)
		case "/item":
			io.WriteString(w, "<!doctype html><title>item</title><p>A laptop")
			select {
			case itemSeen <- struct{}{}:
			default:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()

	ca, err := mitm.LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := recording.Create(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := mitm.NewTransport()
	tr.Proxy = nil
	ln, err := mitm.Listen("127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &mitm.Proxy{CA: ca, Store: store, Transport: tr, Logf: t.Logf}
	go proxy.Serve(ln)
	defer proxy.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	b, err := chromium.Launch(ctx, chromium.LaunchOptions{
		Executable: exe, ProfileDir: t.TempDir(), ProxyAddr: ln.Addr().String(),
		SPKIHash: ca.LeafSPKIHash(), Headless: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	rec, err := Start(ctx, b.DevToolsURL, store, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Open(ctx, origin.URL+"/"); err != nil {
		t.Fatal(err)
	}
	page := pageSession(t, rec)

	eval := func(expr string) json.RawMessage {
		t.Helper()
		res, err := rec.conn.Call(ctx, page, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true})
		if err != nil {
			t.Fatalf("evaluate %s: %v", expr, err)
		}
		return res
	}
	waitFor := func(expr string) {
		t.Helper()
		for ctx.Err() == nil {
			var r struct {
				Result struct {
					Value bool `json:"value"`
				} `json:"result"`
			}
			json.Unmarshal(eval(expr), &r)
			if r.Result.Value {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", expr)
	}
	input := func(method string, params map[string]any) {
		t.Helper()
		if _, err := rec.conn.Call(ctx, page, method, params); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}

	waitFor(`document.title === "shop" && document.readyState === "complete"`)
	eval(`document.querySelector("[name=q]").focus()`)
	input("Input.insertText", map[string]any{"text": "laptop"})
	eval(`document.querySelector("[name=pw]").focus()`)
	input("Input.insertText", map[string]any{"text": "hunter2"})
	eval(`document.querySelector("[name=q]").focus()`)
	input("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13, "text": "\r"})
	input("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13})
	waitFor(`document.title === "results" && document.readyState === "complete"`)
	for _, typ := range []string{"mousePressed", "mouseReleased"} {
		input("Input.dispatchMouseEvent", map[string]any{"type": typ, "x": 20, "y": 20, "button": "left", "clickCount": 1})
	}
	select {
	case <-itemSeen:
	case <-ctx.Done():
		t.Fatal("result page never requested")
	}
	waitFor(`document.title === "item"`)

	b.Close()
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	proxy.Close()
	store.Close()

	raw, err := os.ReadFile(store.Dir() + "/" + recording.BrowserFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2") {
		t.Error("the password reached browser.jsonl")
	}
	r, err := recording.Load(store.Dir())
	if err != nil {
		t.Fatal(err)
	}

	var timeline []string
	for _, e := range recording.Timeline(r, false) {
		timeline = append(timeline, e.Kind+" "+e.Text)
	}
	joined := strings.Join(timeline, "\n")
	steps := []string{
		"browser.navigation " + origin.URL + "/",
		`browser.input role=searchbox name=q label="Search the shop" value="laptop"`,
		`browser.input role=textbox name=pw value=<redacted>`,
		`browser.enter role=searchbox name=q label="Search the shop" value="laptop"`,
		`browser.submit role=form form=POST ` + origin.URL + "/search",
		"http.request POST " + origin.URL + "/search",
		"browser.navigation " + origin.URL + "/search",
		`browser.click role=link text="First result" href=` + origin.URL + "/item",
		"http.request GET " + origin.URL + "/item",
		"browser.navigation " + origin.URL + "/item",
	}
	at := 0
	for _, step := range steps {
		i := indexWithPrefix(timeline[at:], step)
		if i < 0 {
			t.Fatalf("timeline lacks %q after line %d:\n%s", step, at, joined)
		}
		at += i + 1
	}

	var submit struct {
		Fields []struct {
			Name     string  `json:"name"`
			Value    *string `json:"value"`
			Redacted bool    `json:"redacted"`
		} `json:"fields"`
	}
	var sawNetwork, sawTarget bool
	for _, ev := range r.Events {
		switch ev.Type {
		case "interaction.submit":
			json.Unmarshal(ev.Payload, &submit)
		case "Network.requestWillBeSent":
			sawNetwork = sawNetwork || strings.Contains(string(ev.Payload), `"requestId"`)
		case "Target.targetCreated":
			sawTarget = true
		}
		if ev.SessionID != store.ID() {
			t.Errorf("event %s has session %q", ev.ID, ev.SessionID)
		}
	}
	if len(submit.Fields) != 2 || submit.Fields[0].Value == nil || *submit.Fields[0].Value != "laptop" || !submit.Fields[1].Redacted {
		t.Errorf("submit fields = %+v", submit.Fields)
	}
	if !sawNetwork || !sawTarget {
		t.Errorf("network context recorded=%v, target events recorded=%v", sawNetwork, sawTarget)
	}
}

func indexWithPrefix(lines []string, prefix string) int {
	for i, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return i
		}
	}
	return -1
}

// pageSession returns the CDP session of the first page.
func pageSession(t *testing.T, r *Recorder) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for s, tg := range r.sessions {
		if tg.Type == "page" {
			return s
		}
	}
	t.Fatal("no page session")
	return ""
}

package cdp

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adiludmer/webshadow/internal/recording"
)

//go:embed instrument.js
var instrumentScript string

const (
	bindingName = "__webshadowEvent"
	worldName   = "webshadow"
)

// Recorder writes browser events to a recording session.
type Recorder struct {
	conn  *Conn
	store *recording.Store
	logf  func(string, ...any)

	mu       sync.Mutex
	sessions map[string]*target // by CDP session id
	targets  map[string]*target // by target id

	setup sync.WaitGroup
	done  chan struct{}

	// pages receives the session id of each page once it is set up.
	pages chan string
	// closing is set by Close, so the loop does not report the end it caused.
	closing atomic.Bool
}

type target struct {
	ID    string
	Type  string
	URL   string
	Title string
}

type targetInfo struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	URL      string `json:"url"`
	Title    string `json:"title"`
}

// Start connects to the browser's DevTools endpoint and records until the
// browser goes away or Close is called. logf reports setup failures; it
// never receives page content.
func Start(ctx context.Context, wsURL string, store *recording.Store, logf func(string, ...any)) (*Recorder, error) {
	conn, err := Dial(ctx, wsURL)
	if err != nil {
		return nil, err
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	r := &Recorder{
		conn:     conn,
		store:    store,
		logf:     logf,
		sessions: map[string]*target{},
		targets:  map[string]*target{},
		done:     make(chan struct{}),
		pages:    make(chan string, 16),
	}
	go r.loop()
	if _, err := conn.Call(ctx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}); err != nil {
		conn.Close()
		return nil, err
	}
	// New targets pause until set up, so the interaction script is in
	// place before any page script runs.
	if _, err := conn.Call(ctx, "", "Target.setAutoAttach", map[string]any{
		"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true,
	}); err != nil {
		conn.Close()
		return nil, err
	}
	return r, nil
}

// Done is closed once the recorder has written every event it received.
func (r *Recorder) Done() <-chan struct{} { return r.done }

// Close disconnects and waits until every received event is written. A
// browser that already exited leaves no closing handshake to complete, so
// errors from it are not reported.
func (r *Recorder) Close() error {
	r.closing.Store(true)
	r.conn.Close()
	<-r.done
	return nil
}

func (r *Recorder) loop() {
	defer close(r.done)
	for {
		ev, ok := r.conn.Next()
		if !ok {
			break
		}
		r.handle(ev)
	}
	// An EOF is the browser going away, which ends every session.
	if err := r.conn.Err(); err != nil && !r.closing.Load() && !errors.Is(err, io.EOF) {
		r.logf("cdp: connection to the browser ended: %v", err)
	}
	r.setup.Wait()
}

func (r *Recorder) handle(ev Event) {
	switch ev.Method {
	case "Target.attachedToTarget":
		var p struct {
			SessionID          string     `json:"sessionId"`
			TargetInfo         targetInfo `json:"targetInfo"`
			WaitingForDebugger bool       `json:"waitingForDebugger"`
		}
		if json.Unmarshal(ev.Params, &p) != nil {
			return
		}
		t := r.track(p.TargetInfo)
		r.mu.Lock()
		r.sessions[p.SessionID] = t
		r.mu.Unlock()
		r.setup.Add(1)
		go func() {
			defer r.setup.Done()
			r.prepare(p.SessionID, p.TargetInfo.Type, p.WaitingForDebugger)
		}()
		return
	case "Target.detachedFromTarget":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			r.mu.Lock()
			delete(r.sessions, p.SessionID)
			r.mu.Unlock()
		}
		return
	case "Target.targetCreated", "Target.targetInfoChanged":
		var p struct {
			TargetInfo targetInfo `json:"targetInfo"`
		}
		if json.Unmarshal(ev.Params, &p) != nil {
			return
		}
		r.track(p.TargetInfo)
		r.write(ev, p.TargetInfo.TargetID, "", p.TargetInfo.URL, ev.Method, ev.Params)
		return
	case "Target.targetDestroyed":
		var p struct {
			TargetID string `json:"targetId"`
		}
		if json.Unmarshal(ev.Params, &p) != nil {
			return
		}
		r.mu.Lock()
		url := ""
		if t := r.targets[p.TargetID]; t != nil {
			url = t.URL
		}
		delete(r.targets, p.TargetID)
		r.mu.Unlock()
		r.write(ev, p.TargetID, "", url, ev.Method, ev.Params)
		return
	}

	r.mu.Lock()
	t := r.sessions[ev.SessionID]
	var targetID, pageURL, kind string
	if t != nil {
		targetID, pageURL, kind = t.ID, t.URL, t.Type
	}
	r.mu.Unlock()

	switch ev.Method {
	case "Page.frameNavigated":
		var p struct {
			Frame struct {
				ID       string `json:"id"`
				ParentID string `json:"parentId"`
				URL      string `json:"url"`
			} `json:"frame"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			if p.Frame.ParentID == "" {
				pageURL = p.Frame.URL
				r.setURL(t, pageURL)
			}
			r.write(ev, targetID, p.Frame.ID, p.Frame.URL, ev.Method, ev.Params)
		}
	case "Page.navigatedWithinDocument":
		var p struct {
			FrameID string `json:"frameId"`
			URL     string `json:"url"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			r.write(ev, targetID, p.FrameID, p.URL, ev.Method, ev.Params)
		}
	case "Page.domContentEventFired", "Page.loadEventFired":
		// Out-of-process iframes (ads, mostly) report their own load events;
		// only a page's own are lifecycle the user sees.
		if kind != "page" {
			return
		}
		r.write(ev, targetID, "", pageURL, ev.Method, ev.Params)
	case "Page.frameRequestedNavigation":
		var p struct {
			FrameID string `json:"frameId"`
		}
		json.Unmarshal(ev.Params, &p)
		r.write(ev, targetID, p.FrameID, pageURL, ev.Method, ev.Params)
	case "Network.requestWillBeSent", "Network.responseReceived", "Network.loadingFailed":
		frame, payload := networkContext(ev.Method, ev.Params)
		r.write(ev, targetID, frame, pageURL, ev.Method, payload)
	case "Runtime.bindingCalled":
		var p struct {
			Name    string `json:"name"`
			Payload string `json:"payload"`
		}
		if json.Unmarshal(ev.Params, &p) != nil || p.Name != bindingName {
			return
		}
		var in struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		}
		if json.Unmarshal([]byte(p.Payload), &in) != nil || in.Type == "" {
			return
		}
		r.write(ev, targetID, "", in.URL, "interaction."+in.Type, json.RawMessage(p.Payload))
	}
}

// track records what is known about a target and returns it.
func (r *Recorder) track(info targetInfo) *target {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.targets[info.TargetID]
	if t == nil {
		t = &target{ID: info.TargetID}
		r.targets[info.TargetID] = t
	}
	t.Type, t.URL, t.Title = info.Type, info.URL, info.Title
	return t
}

func (r *Recorder) setURL(t *target, url string) {
	if t == nil {
		return
	}
	r.mu.Lock()
	t.URL = url
	r.mu.Unlock()
}

// prepare enables the domains a target needs and lets it run.
func (r *Recorder) prepare(session, kind string, waiting bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	call := func(method string, params any) {
		if _, err := r.conn.Call(ctx, session, method, params); err != nil && !isClosed(r.conn.Done()) {
			r.logf("cdp: %s on %s target: %v", method, kind, err)
		}
	}
	if kind == "page" || kind == "iframe" {
		call("Page.enable", nil)
		call("Network.enable", map[string]any{"maxPostDataSize": 0})
		call("Runtime.addBinding", map[string]any{"name": bindingName, "executionContextName": worldName})
		call("Page.addScriptToEvaluateOnNewDocument", map[string]any{
			"source": instrumentScript, "worldName": worldName, "runImmediately": true,
		})
		call("Runtime.enable", nil)
		// Out-of-process iframes and workers started by this page.
		call("Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true})
	}
	if waiting {
		call("Runtime.runIfWaitingForDebugger", nil)
	}
	if kind == "page" {
		select {
		case r.pages <- session:
		default:
		}
	}
}

// Open loads url in the first page once the recorder is attached to it,
// so the page's first navigation is recorded too.
func (r *Recorder) Open(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	select {
	case session := <-r.pages:
		_, err := r.conn.Call(ctx, session, "Page.navigate", map[string]any{"url": url})
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Recorder) write(ev Event, targetID, frameID, pageURL, typ string, payload json.RawMessage) {
	clock := r.store.Clock()
	err := r.store.AppendBrowserEvent(&recording.BrowserEvent{
		Timestamp: recording.Stamp{T: clock.Since(ev.Received), Wall: ev.Received.UTC()},
		TargetID:  targetID,
		FrameID:   frameID,
		Type:      typ,
		PageURL:   pageURL,
		Payload:   payload,
	})
	if err != nil && err != recording.ErrClosed {
		r.logf("cdp: record event: %v", err)
	}
}

// networkContext keeps what a later analyzer needs to tie a browser
// request to a proxy exchange: ids, URL, method, type and initiator.
// Headers and bodies are already in http.jsonl and are left out.
func networkContext(method string, params json.RawMessage) (frameID string, payload json.RawMessage) {
	var p struct {
		RequestID   string  `json:"requestId"`
		LoaderID    string  `json:"loaderId,omitempty"`
		FrameID     string  `json:"frameId,omitempty"`
		DocumentURL string  `json:"documentURL,omitempty"`
		Type        string  `json:"type,omitempty"`
		Timestamp   float64 `json:"timestamp,omitempty"`
		WallTime    float64 `json:"wallTime,omitempty"`
		Request     *struct {
			URL    string `json:"url"`
			Method string `json:"method"`
		} `json:"request,omitempty"`
		Response *struct {
			URL               string `json:"url"`
			Status            int    `json:"status"`
			MimeType          string `json:"mimeType"`
			Protocol          string `json:"protocol,omitempty"`
			FromDiskCache     bool   `json:"fromDiskCache,omitempty"`
			FromServiceWorker bool   `json:"fromServiceWorker,omitempty"`
		} `json:"response,omitempty"`
		Initiator *struct {
			Type       string `json:"type"`
			URL        string `json:"url,omitempty"`
			LineNumber *int   `json:"lineNumber,omitempty"`
		} `json:"initiator,omitempty"`
		ErrorText string `json:"errorText,omitempty"`
		Canceled  bool   `json:"canceled,omitempty"`
	}
	if json.Unmarshal(params, &p) != nil {
		return "", nil
	}
	out, _ := json.Marshal(p)
	return p.FrameID, out
}

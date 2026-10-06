// Package cdp records what the browser does through the Chrome DevTools
// Protocol: navigation, page and target lifecycle, network request context
// and, through a small injected script, user interactions. It only
// records; it never drives the browser.
package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Event is one CDP event, stamped when it arrived.
type Event struct {
	SessionID string
	Method    string
	Params    json.RawMessage
	Received  time.Time
}

// Conn is a flattened-session CDP connection to the browser endpoint.
type Conn struct {
	ws     *websocket.Conn
	nextID atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan *message
	err     error // set when the connection ends

	// Events are queued without bound so the reader never blocks on a
	// slow consumer, which could otherwise deadlock a consumer that calls
	// back into the browser.
	qmu    sync.Mutex
	queue  []Event
	signal chan struct{}
	done   chan struct{}
}

type message struct {
	ID        int64           `json:"id,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Dial connects to a browser-level DevTools WebSocket URL.
func Dial(ctx context.Context, url string) (*Conn, error) {
	// Loopback only, never through a proxy from the environment.
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		return nil, fmt.Errorf("cdp: %w", err)
	}
	// CDP messages carry whole URLs, titles and DOM-sized payloads.
	ws.SetReadLimit(256 << 20)
	c := &Conn{
		ws:      ws,
		pending: map[int64]chan *message{},
		signal:  make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	go c.read()
	return c, nil
}

func (c *Conn) read() {
	var err error
	for {
		var data []byte
		_, data, err = c.ws.Read(context.Background())
		if err != nil {
			break
		}
		received := time.Now()
		var m message
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.ID != 0 {
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- &m
			}
			continue
		}
		if m.Method != "" {
			c.qmu.Lock()
			c.queue = append(c.queue, Event{SessionID: m.SessionID, Method: m.Method, Params: m.Params, Received: received})
			c.qmu.Unlock()
			select {
			case c.signal <- struct{}{}:
			default:
			}
		}
	}
	c.mu.Lock()
	c.err = err
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.done)
}

// Next returns the next event, waiting for one. It returns false once the
// connection has ended and every queued event has been returned.
func (c *Conn) Next() (Event, bool) {
	for {
		c.qmu.Lock()
		if len(c.queue) > 0 {
			ev := c.queue[0]
			c.queue[0] = Event{}
			c.queue = c.queue[1:]
			c.qmu.Unlock()
			return ev, true
		}
		c.qmu.Unlock()
		select {
		case <-c.signal:
		case <-c.done:
			c.qmu.Lock()
			empty := len(c.queue) == 0
			c.qmu.Unlock()
			if empty {
				return Event{}, false
			}
		}
	}
}

// Call sends a command, to the browser when sessionID is empty or to an
// attached target otherwise, and waits for its result.
func (c *Conn) Call(ctx context.Context, sessionID, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	m := message{ID: id, SessionID: sessionID, Method: method}
	if params != nil {
		p, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		m.Params = p
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	ch := make(chan *message, 1)
	c.mu.Lock()
	if c.err != nil || isClosed(c.done) {
		c.mu.Unlock()
		return nil, errClosed
	}
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.ws.Write(ctx, websocket.MessageText, data); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return nil, errClosed
		}
		if r.Error != nil {
			return nil, fmt.Errorf("cdp %s: %s (%d)", method, r.Error.Message, r.Error.Code)
		}
		return r.Result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

var errClosed = errors.New("cdp: connection closed")

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Close ends the connection.
func (c *Conn) Close() error {
	err := c.ws.Close(websocket.StatusNormalClosure, "")
	<-c.done
	return err
}

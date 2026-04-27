package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	protocolVersion = 3
	clientIDDefault = "talon"
	clientVersion   = "0.1.0-dev"
)

type Frame struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	OK      *bool           `json:"ok,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   *FrameError     `json:"error,omitempty"`
	Event   string          `json:"event,omitempty"`
	Seq     *int            `json:"seq,omitempty"`
}

type FrameError struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

func (e *FrameError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return e.Message
}

type EventHandler func(event string, payload json.RawMessage)

type Client struct {
	url   string
	token string
	ws    *websocket.Conn

	OnEvent EventHandler

	mu      sync.Mutex
	pending map[string]chan *Frame
	closed  bool
}

func NewClient(url, token string) *Client {
	return &Client{
		url:     url,
		token:   token,
		pending: make(map[string]chan *Frame),
	}
}

func (c *Client) Connect(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, c.url, &websocket.DialOptions{})
	if err != nil {
		return fmt.Errorf("dial gateway %s: %w", c.url, err)
	}
	conn.SetReadLimit(64 * 1024 * 1024)
	c.ws = conn

	nonceCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go c.readLoop(ctx, nonceCh, errCh)

	var nonce string
	select {
	case n := <-nonceCh:
		nonce = n
	case e := <-errCh:
		return fmt.Errorf("waiting for connect.challenge: %w", e)
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timeout waiting for connect.challenge")
	}
	_ = nonce

	connectParams := map[string]any{
		"minProtocol": protocolVersion,
		"maxProtocol": protocolVersion,
		"client": map[string]any{
			"id":          "openclaw-tui",
			"displayName": "talon",
			"version":     clientVersion,
			"platform":    runtime.GOOS,
			"mode":        "ui",
		},
		"role":   "operator",
		"scopes": []string{"operator.admin", "operator.read", "operator.write", "operator.approvals", "operator.pairing"},
	}
	if c.token != "" {
		connectParams["auth"] = map[string]any{"token": c.token}
	}

	resp, err := c.Request(ctx, "connect", connectParams)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	_ = resp
	return nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	if c.ws != nil {
		return c.ws.Close(websocket.StatusNormalClosure, "")
	}
	return nil
}

func (c *Client) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	var paramsRaw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		paramsRaw = b
	}
	frame := Frame{Type: "req", ID: id, Method: method, Params: paramsRaw}
	respCh := make(chan *Frame, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("client closed")
	}
	c.pending[id] = respCh
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	b, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		return nil, fmt.Errorf("write %s: %w", method, err)
	}

	select {
	case resp := <-respCh:
		if resp.OK != nil && !*resp.OK {
			return nil, resp.Error
		}
		return resp.Payload, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) readLoop(ctx context.Context, nonceCh chan<- string, errCh chan<- error) {
	defer close(nonceCh)
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if !closed {
				select {
				case errCh <- err:
				default:
				}
			}
			return
		}
		var f Frame
		if err := json.Unmarshal(data, &f); err != nil {
			continue
		}
		switch f.Type {
		case "event":
			if f.Event == "connect.challenge" {
				var p struct {
					Nonce string `json:"nonce"`
				}
				if err := json.Unmarshal(f.Payload, &p); err == nil && p.Nonce != "" {
					select {
					case nonceCh <- p.Nonce:
					default:
					}
				}
				continue
			}
			if c.OnEvent != nil {
				c.OnEvent(f.Event, f.Payload)
			}
		case "res":
			c.mu.Lock()
			ch, ok := c.pending[f.ID]
			c.mu.Unlock()
			if ok {
				select {
				case ch <- &f:
				default:
				}
			}
		}
	}
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

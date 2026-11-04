package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// page is one browser tab driven over the Chrome DevTools Protocol. Only the
// few methods a capture needs are used; responses are matched by id and
// events are ignored.
type page struct {
	conn  *websocket.Conn
	next  int
	debug string
	id    string
}

// newPage opens a tab on a browser listening on the debugging port.
func newPage(debug string) (*page, error) {
	req, _ := http.NewRequest(http.MethodPut, "http://"+debug+"/json/new?about:blank", nil)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var target struct {
		ID                   string `json:"id"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(raw, &target); err != nil || target.WebSocketDebuggerURL == "" {
		return nil, fmt.Errorf("no DevTools target: %s", raw)
	}
	conn, _, err := websocket.DefaultDialer.Dial(target.WebSocketDebuggerURL, nil)
	if err != nil {
		return nil, err
	}
	return &page{conn: conn, debug: debug, id: target.ID}, nil
}

// close disconnects and closes the tab, so no page (and no console SSE
// stream) keeps running into the next capture.
func (p *page) close() {
	p.conn.Close()
	if resp, err := http.Get("http://" + p.debug + "/json/close/" + p.id); err == nil {
		resp.Body.Close()
	}
}

// call sends one command and waits for its response.
func (p *page) call(method string, params any, result any) error {
	p.next++
	id := p.next
	if err := p.conn.WriteJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	p.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		var msg struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := p.conn.ReadJSON(&msg); err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		if msg.ID != id {
			continue // an event or an earlier response
		}
		if msg.Error != nil {
			return fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		if result != nil {
			return json.Unmarshal(msg.Result, result)
		}
		return nil
	}
}

// evaluate returns a JavaScript expression's value.
func (p *page) evaluate(expr string) (json.RawMessage, error) {
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails any `json:"exceptionDetails"`
	}
	if err := p.call("Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true}, &out); err != nil {
		return nil, err
	}
	if out.ExceptionDetails != nil {
		return nil, fmt.Errorf("evaluating %q threw: %v", expr, out.ExceptionDetails)
	}
	return out.Result.Value, nil
}

// capture loads url at the given viewport, waits until ready (a JavaScript
// condition, plus all web fonts loaded) and returns a full-page PNG. Waiting
// on rendered content, not on virtual time, matters: the console holds an SSE
// stream open, so a "network idle" or virtual-time wait never settles.
func (p *page) capture(url string, width, height int, ready string) ([]byte, error) {
	metrics := func(h int) error {
		return p.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": width, "height": h, "deviceScaleFactor": 1, "mobile": width < 600}, nil)
	}
	if err := metrics(height); err != nil {
		return nil, err
	}
	if err := p.call("Emulation.setTimezoneOverride", map[string]any{"timezoneId": "UTC"}, nil); err != nil {
		return nil, err
	}
	if err := p.call("Emulation.setLocaleOverride", map[string]any{"locale": "en-US"}, nil); err != nil {
		return nil, err
	}
	if err := p.call("Page.navigate", map[string]any{"url": url}, nil); err != nil {
		return nil, err
	}
	condition := "(" + ready + ") && document.fonts.status === 'loaded'"
	deadline := time.Now().Add(20 * time.Second)
	for {
		v, err := p.evaluate("(() => { try { return !!(" + condition + "); } catch (e) { return false; } })()")
		if err == nil && string(v) == "true" {
			break
		}
		if time.Now().After(deadline) {
			seen, _ := p.evaluate(`JSON.stringify({text: document.body ? document.body.innerText.slice(0, 300) : "", canvas: document.querySelectorAll("canvas").length, svg: document.querySelectorAll("svg").length, fonts: document.fonts.status})`)
			return nil, fmt.Errorf("%s: page never became ready (%s); page shows %s", url, ready, seen)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Full page: grow the viewport to the content height, then capture.
	h, err := p.evaluate("Math.ceil(document.documentElement.scrollHeight)")
	if err != nil {
		return nil, err
	}
	var full int
	if err := json.Unmarshal(h, &full); err != nil {
		return nil, fmt.Errorf("page height: %w", err)
	}
	if full > height {
		if err := metrics(full); err != nil {
			return nil, err
		}
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := p.call("Page.captureScreenshot", map[string]any{"format": "png", "fromSurface": true}, &shot); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(shot.Data)
}

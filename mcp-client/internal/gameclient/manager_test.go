package gameclient

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ws "github.com/dobyte/due/network/ws/v2"
	"github.com/dobyte/due/v2/cluster/client"
	"github.com/dobyte/due/v2/core/buffer"
	"github.com/dobyte/due/v2/encoding"
	"github.com/dobyte/due/v2/packet"
	"github.com/gorilla/websocket"
)

func TestManagerReceivesWebSocketBody(t *testing.T) {
	m, err := NewManager()
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	t.Cleanup(func() {
		m.component.Close()
		m.component.Destroy()
	})

	sc, peer := connectWebSocketPeer(t, m)
	route, seq := int32(2003), int32(17)
	wantBody := []byte(` {"code":0,"room_key":"回归测试","players":[1,2]} `)
	writeWebSocketMessage(t, peer, route, seq, wantBody)

	got, err := sc.WaitMessage(&route, 3*time.Second)
	if err != nil {
		t.Fatalf("wait for WebSocket message: %v", err)
	}
	if !bytes.Equal(got.Data, wantBody) {
		t.Errorf("received body len=%d bytes=%q, want %q", len(got.Data), []byte(got.Data), wantBody)
	}
	if got.Route != route || got.Seq != seq {
		t.Errorf("received route/seq = %d/%d, want %d/%d", got.Route, got.Seq, route, seq)
	}
	if got.Time.IsZero() {
		t.Error("received message has no timestamp")
	}
}

func TestManagerInboxOwnsWebSocketBody(t *testing.T) {
	wantBody := []byte(`{"code":0,"room_key":"owned-body"}`)
	checked := make(chan error, 1)
	m := &Manager{
		byID:  make(map[int64]*SimClient),
		byCID: make(map[int64]*SimClient),
	}
	m.component = client.NewClient(
		client.WithClient(ws.NewClient(ws.WithClientHeartbeatInterval(0))),
		client.WithCodec(encoding.Invoke("json")),
	)
	m.proxy = m.component.Proxy()
	m.proxy.SetDefaultRouteHandler(func(ctx *client.Context) {
		m.onMessage(ctx)

		// This is the real pooled receive buffer, still owned by the framework.
		// Overwrite it after enqueue, while the callback is valid, rather than
		// relying on nondeterministic sync.Pool reuse or retaining the Context.
		buf, ok := ctx.Data().(buffer.Buffer)
		if !ok {
			checked <- fmt.Errorf("receive data type = %T, want buffer.Buffer", ctx.Data())
			return
		}
		data := buf.Bytes()
		if !bytes.Equal(data, wantBody) {
			checked <- fmt.Errorf("callback body = %q, want %q", data, wantBody)
			return
		}
		clear(data)
		checked <- nil
	})
	withStdoutToStderr(func() {
		m.component.Init()
		m.component.Start()
	})
	t.Cleanup(func() {
		m.component.Close()
		m.component.Destroy()
	})

	sc, peer := connectWebSocketPeer(t, m)
	writeWebSocketMessage(t, peer, 2003, 18, wantBody)
	select {
	case err := <-checked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("receive callback did not finish checking the pooled buffer")
	}

	messages := sc.PollMessages()
	if len(messages) != 1 {
		t.Fatalf("inbox contains %d messages, want 1", len(messages))
	}
	if !bytes.Equal(messages[0].Data, wantBody) {
		t.Errorf("inbox body after receive buffer overwrite len=%d bytes=%q, want %q",
			len(messages[0].Data), []byte(messages[0].Data), wantBody)
	}
}

func connectWebSocketPeer(t *testing.T, m *Manager) (*SimClient, *websocket.Conn) {
	t.Helper()
	peers := make(chan *websocket.Conn, 1)
	var upgrader websocket.Upgrader
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade local WebSocket: %v", err)
			return
		}
		peers <- conn
	}))
	t.Cleanup(server.Close)

	sc, err := m.Connect(t.Name(), "ws"+strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatalf("connect local WebSocket: %v", err)
	}
	select {
	case peer := <-peers:
		t.Cleanup(func() {
			// Destroy joins the framework's connection readers/writers before the
			// server peer is closed, avoiding an asynchronous remote-close path.
			m.component.Close()
			m.component.Destroy()
			if err := peer.Close(); err != nil {
				t.Errorf("close local WebSocket peer: %v", err)
			}
		})
		return sc, peer
	case <-time.After(3 * time.Second):
		t.Fatal("local WebSocket peer did not connect")
		return nil, nil
	}
}

func writeWebSocketMessage(t *testing.T, peer *websocket.Conn, route, seq int32, body []byte) {
	t.Helper()
	frame, err := packet.PackMessage(route, seq, body)
	if err != nil {
		t.Fatalf("pack local WebSocket message: %v", err)
	}
	defer frame.Release()
	if err := peer.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set local WebSocket write deadline: %v", err)
	}
	if err := peer.WriteMessage(websocket.BinaryMessage, frame.Bytes()); err != nil {
		t.Fatalf("write local WebSocket message: %v", err)
	}
}

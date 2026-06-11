package tv

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// mockTV stands in for a Frame TV's art-app WebSocket channel. It performs the
// connect/ready handshake, then answers art_app_request messages the same way
// real firmware does (a d2d_service_message whose data is a JSON-encoded string).
func mockTV(t *testing.T) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		_ = conn.WriteJSON(map[string]any{
			"event": "ms.channel.connect",
			"data":  map[string]any{"token": "tok-123", "id": "client-1"},
		})
		_ = conn.WriteJSON(map[string]any{"event": "ms.channel.ready"})

		for {
			var env struct {
				Method string `json:"method"`
				Params struct {
					Event string `json:"event"`
					Data  string `json:"data"`
				} `json:"params"`
			}
			if err := conn.ReadJSON(&env); err != nil {
				return
			}
			if env.Params.Event != "art_app_request" {
				continue
			}
			var req map[string]any
			if err := json.Unmarshal([]byte(env.Params.Data), &req); err != nil {
				continue
			}
			reply := func(payload map[string]any) {
				b, _ := json.Marshal(payload)
				_ = conn.WriteJSON(map[string]any{
					"event": "d2d_service_message",
					"data":  string(b),
				})
			}
			switch req["request"] {
			case "api_version":
				reply(map[string]any{"event": "api_version", "version": "4.3.4.0", "id": req["id"]})
			case "get_artmode_status":
				reply(map[string]any{"event": "artmode_status", "value": "on", "id": req["id"]})
			case "send_image":
				handleMockUpload(t, conn, req, reply)
			case "select_image":
				reply(map[string]any{"event": "image_selected", "content_id": req["content_id"], "id": req["id"]})
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// handleMockUpload emulates the TV's D2D upload handshake: announce a data
// socket via "ready_to_use", accept the connection and drain the framed image,
// then confirm with "image_added".
func handleMockUpload(t *testing.T, ws *websocket.Conn, req map[string]any, reply func(map[string]any)) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Errorf("mock listen: %v", err)
		return
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	reply(map[string]any{
		"event":     "ready_to_use",
		"id":        req["id"],
		"conn_info": map[string]any{"ip": "127.0.0.1", "port": port, "key": "seckey", "secured": false},
	})

	conn, err := ln.Accept()
	if err != nil {
		t.Errorf("mock accept: %v", err)
		return
	}
	defer conn.Close()

	var lenBuf [4]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		t.Errorf("mock read header len: %v", err)
		return
	}
	header := make([]byte, binary.BigEndian.Uint32(lenBuf[:]))
	if _, err := io.ReadFull(conn, header); err != nil {
		t.Errorf("mock read header: %v", err)
		return
	}
	var hdr struct {
		FileLength int    `json:"fileLength"`
		SecKey     string `json:"secKey"`
	}
	if err := json.Unmarshal(header, &hdr); err != nil {
		t.Errorf("mock decode header: %v", err)
		return
	}
	if _, err := io.ReadFull(conn, make([]byte, hdr.FileLength)); err != nil {
		t.Errorf("mock read body: %v", err)
		return
	}

	reply(map[string]any{"event": "image_added", "content_id": "MY_F0099", "id": req["id"]})
}

// wsURL rewrites an httptest https:// URL into the wss:// form dialArt expects.
func wsURL(httpsURL string) string {
	u := strings.Replace(httpsURL, "https://", "wss://", 1)
	return u + artChannel + "?name=cGxleC1waG90by1mcmFtZQ%3D%3D"
}

func TestArtClientRoundTrip(t *testing.T) {
	srv := mockTV(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	art, err := dialArt(ctx, wsURL(srv.URL), "10.0.0.83", "")
	if err != nil {
		t.Fatalf("dialArt: %v", err)
	}
	defer art.Close()

	if got := art.Token(); got != "tok-123" {
		t.Errorf("Token() = %q, want tok-123", got)
	}

	ver, err := art.APIVersion()
	if err != nil {
		t.Fatalf("APIVersion: %v", err)
	}
	if ver != "4.3.4.0" {
		t.Errorf("APIVersion() = %q, want 4.3.4.0", ver)
	}

	status, err := art.ArtModeStatus()
	if err != nil {
		t.Fatalf("ArtModeStatus: %v", err)
	}
	if status != "on" {
		t.Errorf("ArtModeStatus() = %q, want on", status)
	}
}

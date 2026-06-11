package tv

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	// DefaultWSPort is the secure WebSocket port. The art-app channel only works
	// over wss://<ip>:8002 — port 8001 (ws) does not serve it on modern firmware.
	DefaultWSPort = 8002

	// artChannel is the Tizen channel that hosts the Art Mode app API.
	artChannel = "/api/v2/channels/com.samsung.art-app"

	// clientName identifies us to the TV. It shows up in the TV's list of
	// connected devices and is sent base64-encoded in the connect URL.
	clientName = "plex-photo-frame"
)

// ArtClient is a connection to a Frame TV's Art Mode WebSocket channel.
//
// It is intentionally minimal: it speaks just enough of the protocol to probe
// a TV (api version, art-mode status, device info). Upload/select/delete will
// build on the same request/response plumbing.
type ArtClient struct {
	ip    string
	conn  *websocket.Conn
	token string // returned by the TV on connect; reuse to skip re-prompting
}

// wsEnvelope is the outer frame the TV sends on every WebSocket message. The
// data field is polymorphic: an object for ms.channel.* events, and a
// JSON-encoded *string* for d2d_service_message responses.
type wsEnvelope struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// DialArt opens the Art Mode channel for the TV at ip.
//
// token may be empty on the first connection. On firmware that enforces token
// auth, the TV shows an "Allow this device?" prompt; once accepted it returns a
// token in the connect frame (see ArtClient.Token) that you can pass back on
// future connections to avoid the prompt.
//
// NOTE on TLS: Frame TVs serve port 8002 with a self-signed certificate that
// cannot be validated against any CA, so we skip verification. This is scoped
// to a single device on the local network reached by IP; do not copy this
// transport for general-purpose HTTPS clients.
func DialArt(ctx context.Context, ip, token string) (*ArtClient, error) {
	return dialArt(ctx, artURL(ip, token), ip, token)
}

// artURL builds the wss connect URL for the art-app channel.
func artURL(ip, token string) string {
	q := url.Values{}
	q.Set("name", base64.StdEncoding.EncodeToString([]byte(clientName)))
	if token != "" {
		q.Set("token", token)
	}
	u := url.URL{
		Scheme:   "wss",
		Host:     fmt.Sprintf("%s:%d", ip, DefaultWSPort),
		Path:     artChannel,
		RawQuery: q.Encode(),
	}
	return u.String()
}

// dialArt connects to a fully-formed art-channel URL. Splitting this out from
// DialArt lets tests point the client at a mock TV WebSocket server.
func dialArt(ctx context.Context, wsURL, ip, token string) (*ArtClient, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 8 * time.Second,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true}, // self-signed TV cert, LAN-only
	}

	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial art channel: %w", err)
	}

	c := &ArtClient{ip: ip, conn: conn, token: token}

	// The TV opens with ms.channel.connect, which carries the auth token. Read
	// exactly that frame to capture the token, then clear the deadline. We must
	// not let a read deadline fire here: once a gorilla read times out, the
	// connection's read side is poisoned for all subsequent reads. Any other
	// handshake frame (e.g. ms.channel.ready) is harmless and gets skipped by
	// request(), which ignores everything that isn't a d2d_service_message.
	c.conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	var env wsEnvelope
	if err := c.conn.ReadJSON(&env); err == nil && env.Event == "ms.channel.connect" {
		var d struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(env.Data, &d) == nil && d.Token != "" {
			c.token = d.Token
		}
	}
	c.conn.SetReadDeadline(time.Time{})
	return c, nil
}

// Token returns the auth token the TV issued for this connection, if any.
func (c *ArtClient) Token() string { return c.token }

// Close tears down the WebSocket connection.
func (c *ArtClient) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// emit wraps an art request payload in the ms.channel.emit envelope and sends
// it on the WebSocket.
func (c *ArtClient) emit(payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.conn.WriteJSON(map[string]any{
		"method": "ms.channel.emit",
		"params": map[string]any{
			"event": "art_app_request",
			"to":    "host",
			"data":  string(data),
		},
	})
}

// request sends an art_app_request and waits for the correlated
// d2d_service_message response, returning the decoded inner payload.
//
// extra carries request-specific fields (e.g. a content_id); pass nil if none.
func (c *ArtClient) request(reqType string, extra map[string]any, timeout time.Duration) (map[string]any, error) {
	return c.requestEvent(reqType, uuid.NewString(), extra, "", timeout)
}

// requestEvent is the general form: it tags the request with id (also sent as
// request_id so the TV echoes it back for correlation), and waits for the first
// d2d_service_message that matches id and, when waitEvent is non-empty, carries
// that inner event name (e.g. "ready_to_use", "image_added").
func (c *ArtClient) requestEvent(reqType, id string, extra map[string]any, waitEvent string, timeout time.Duration) (map[string]any, error) {
	payload := map[string]any{
		"request":    reqType,
		"id":         id,
		"request_id": id,
	}
	for k, v := range extra {
		payload[k] = v
	}
	if err := c.emit(payload); err != nil {
		return nil, fmt.Errorf("send %s: %w", reqType, err)
	}
	return c.recvD2D(id, waitEvent, timeout)
}

// recvD2D reads frames until it finds a d2d_service_message whose decoded
// payload matches matchID (when set) and waitEvent (when set). An inner event
// of "error" is surfaced as a Go error.
func (c *ArtClient) recvD2D(matchID, waitEvent string, timeout time.Duration) (map[string]any, error) {
	c.conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		var env wsEnvelope
		if err := c.conn.ReadJSON(&env); err != nil {
			return nil, fmt.Errorf("await response: %w", err)
		}
		if env.Event != "d2d_service_message" {
			continue // ping/keepalive or unrelated channel event
		}
		// data is a JSON-encoded string; unwrap it then decode the object.
		var inner string
		if err := json.Unmarshal(env.Data, &inner); err != nil {
			continue
		}
		out := map[string]any{}
		if err := json.Unmarshal([]byte(inner), &out); err != nil {
			continue
		}

		event, _ := out["event"].(string)
		if event == "error" {
			return nil, fmt.Errorf("TV rejected request (error_code=%v): %v",
				out["error_code"], out["request_data"])
		}
		if matchID != "" {
			id := asString(out["request_id"])
			if id == "" {
				id = asString(out["id"])
			}
			if id != matchID {
				continue
			}
		}
		if waitEvent != "" && event != waitEvent {
			continue
		}
		return out, nil
	}
}

// APIVersion returns the Art Mode API version reported by the TV. Roughly,
// "2.03" is the pre-2022 API and "4.3.4.0" is the 2022+ API. It tries the new
// request name first, then falls back to the legacy one.
func (c *ArtClient) APIVersion() (string, error) {
	for _, req := range []string{"api_version", "get_api_version"} {
		resp, err := c.request(req, nil, 8*time.Second)
		if err != nil {
			continue
		}
		if v, ok := resp["version"].(string); ok && v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("TV did not report an art API version")
}

// ArtModeStatus returns "on" or "off" depending on whether the TV is currently
// showing Art Mode (as opposed to regular TV / standby).
func (c *ArtClient) ArtModeStatus() (string, error) {
	resp, err := c.request("get_artmode_status", nil, 8*time.Second)
	if err != nil {
		return "", err
	}
	if v, ok := resp["value"].(string); ok {
		return strings.ToLower(v), nil
	}
	return "", fmt.Errorf("unexpected artmode_status response: %v", resp)
}

package tv

import (
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// MatteNone disables matting (the photo fills the frame).
const MatteNone = "none"

// Default timeouts for the upload flow. Uploading a few-MB JPEG over LAN is
// quick, but the TV can be slow to acknowledge, so we stay generous.
const (
	uploadTimeout = 30 * time.Second
	socketTimeout = 30 * time.Second
)

// connInfo is the D2D (device-to-device) socket descriptor the TV returns in
// the "ready_to_use" response to a send_image request. The client opens a TCP
// (optionally TLS) connection to ip:port and streams the image there.
type connInfo struct {
	IP      string
	Port    int
	Key     string
	Secured bool
}

// parseConnInfo extracts the conn_info block from a D2D payload. The TV sends
// it either as a nested object or as a JSON-encoded string, so handle both.
func parseConnInfo(payload map[string]any) (connInfo, error) {
	raw, ok := payload["conn_info"]
	if !ok {
		return connInfo{}, fmt.Errorf("response has no conn_info: %v", payload)
	}

	var m map[string]any
	switch v := raw.(type) {
	case string:
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			return connInfo{}, fmt.Errorf("decode conn_info string: %w", err)
		}
	case map[string]any:
		m = v
	default:
		return connInfo{}, fmt.Errorf("unexpected conn_info type %T", raw)
	}

	ci := connInfo{
		IP:      asString(m["ip"]),
		Port:    asInt(m["port"]),
		Key:     asString(m["key"]),
		Secured: asBool(m["secured"]),
	}
	if ci.IP == "" || ci.Port == 0 {
		return connInfo{}, fmt.Errorf("incomplete conn_info: %v", m)
	}
	return ci, nil
}

// Upload streams an encoded image to the TV's "My Photos" and returns its new
// content_id. fileType is an extension like "jpg" or "png"; matteID is a
// "<shape>_<color>" matte (e.g. "modern_apricot") or MatteNone.
//
// Protocol (2022+ / new art API):
//  1. send_image art request describing the file and a socket conn_info
//  2. TV replies "ready_to_use" with the ip/port/key for a one-shot data socket
//  3. open that socket, send [u32 header length][header JSON][image bytes]
//  4. TV replies "image_added" carrying the content_id
func (c *ArtClient) Upload(data []byte, fileType, matteID string) (string, error) {
	ft := normalizeFileType(fileType)
	if matteID == "" {
		matteID = MatteNone
	}

	id := uuid.NewString()
	extra := map[string]any{
		"file_type":         ft,
		"file_size":         len(data),
		"image_date":        time.Now().Format("2006:01:02 15:04:05"),
		"matte_id":          matteID,
		"portrait_matte_id": matteID,
		"conn_info": map[string]any{
			"d2d_mode":      "socket",
			"connection_id": rand.Int63n(4 * 1024 * 1024 * 1024),
			"id":            id,
		},
	}

	ready, err := c.requestEvent("send_image", id, extra, "ready_to_use", uploadTimeout)
	if err != nil {
		return "", fmt.Errorf("send_image handshake: %w", err)
	}

	ci, err := parseConnInfo(ready)
	if err != nil {
		return "", err
	}
	if err := uploadOverSocket(ci, data, ft, socketTimeout); err != nil {
		return "", fmt.Errorf("stream image: %w", err)
	}

	done, err := c.recvD2D("", "image_added", uploadTimeout)
	if err != nil {
		return "", fmt.Errorf("await image_added: %w", err)
	}
	cid := asString(done["content_id"])
	if cid == "" {
		return "", fmt.Errorf("image_added missing content_id: %v", done)
	}
	return cid, nil
}

// Display uploads an image (with the given matte) and immediately shows it,
// returning the new content_id. This is the primitive a playlist swap loop
// calls each interval.
func (c *ArtClient) Display(data []byte, fileType, matteID string) (string, error) {
	cid, err := c.Upload(data, fileType, matteID)
	if err != nil {
		return "", err
	}
	if err := c.SelectImage(cid, true); err != nil {
		return cid, fmt.Errorf("select uploaded image %s: %w", cid, err)
	}
	return cid, nil
}

// uploadOverSocket performs the binary D2D transfer described by ci.
func uploadOverSocket(ci connInfo, data []byte, fileType string, timeout time.Duration) error {
	addr := net.JoinHostPort(ci.IP, strconv.Itoa(ci.Port))
	raw, err := (&net.Dialer{Timeout: timeout}).Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}

	var conn net.Conn = raw
	if ci.Secured {
		// Same self-signed-cert situation as the control channel: LAN device,
		// no validatable CA. Verification is intentionally skipped here only.
		tlsConn := tls.Client(raw, &tls.Config{InsecureSkipVerify: true})
		if err := tlsConn.Handshake(); err != nil {
			raw.Close()
			return fmt.Errorf("tls handshake: %w", err)
		}
		conn = tlsConn
	}
	defer conn.Close()

	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}

	header, err := json.Marshal(map[string]any{
		"num":        0,
		"total":      1,
		"fileLength": len(data),
		"fileName":   "image",
		"fileType":   fileType,
		"secKey":     ci.Key,
		"version":    "0.0.1",
	})
	if err != nil {
		return err
	}

	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(header)))
	if _, err := conn.Write(lenBuf[:]); err != nil {
		return fmt.Errorf("write header length: %w", err)
	}
	if _, err := conn.Write(header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("write image: %w", err)
	}
	return nil
}

// DeleteImages removes uploaded artworks from the TV's "My Photos" by content
// id. Used to clean up the previously-shown photo so uploads don't pile up.
func (c *ArtClient) DeleteImages(contentIDs ...string) error {
	if len(contentIDs) == 0 {
		return nil
	}
	list := make([]map[string]any, 0, len(contentIDs))
	for _, id := range contentIDs {
		if id == "" {
			continue
		}
		list = append(list, map[string]any{"content_id": id})
	}
	if len(list) == 0 {
		return nil
	}
	_, err := c.request("delete_image_list", map[string]any{"content_id_list": list}, uploadTimeout)
	return err
}

// SelectImage tells the TV to make content_id the active artwork. When show is
// true the TV switches to display it immediately.
func (c *ArtClient) SelectImage(contentID string, show bool) error {
	_, err := c.request("select_image", map[string]any{
		"category_id": nil,
		"content_id":  contentID,
		"show":        show,
	}, uploadTimeout)
	return err
}

// SelectImageNoWait asks the TV to display content_id without waiting for a
// confirmation frame. Frame firmware does not reliably acknowledge select_image
// (in particular for matted images it sends nothing), and because a timed-out
// gorilla read poisons the socket for all later reads, a blocking select would
// both hang and break the connection. Emitting fire-and-forget sidesteps both:
// any later request (e.g. KeepAlive) simply skips the stale select reply.
func (c *ArtClient) SelectImageNoWait(contentID string, show bool) error {
	id := uuid.NewString()
	return c.emit(map[string]any{
		"request":     "select_image",
		"id":          id,
		"request_id":  id,
		"category_id": nil,
		"content_id":  contentID,
		"show":        show,
	})
}

// ChangeMatte re-mattes an already-uploaded artwork. matteID is a
// "<shape>_<color>" value or MatteNone.
func (c *ArtClient) ChangeMatte(contentID, matteID string) error {
	if matteID == "" {
		matteID = MatteNone
	}
	_, err := c.request("change_matte", map[string]any{
		"content_id": contentID,
		"matte_id":   matteID,
	}, uploadTimeout)
	return err
}

// PhotoFilter is one Art Mode post-processing effect — the same "effects" the
// SmartThings app applies to a photo (e.g. a painterly / canvas / ink look).
// Applied with SetPhotoFilter after an image is uploaded.
type PhotoFilter struct {
	ID   string
	Name string
}

// GetPhotoFilterList queries the post-processing filters this TV supports.
func (c *ArtClient) GetPhotoFilterList() ([]PhotoFilter, error) {
	resp, err := c.request("get_photo_filter_list", nil, uploadTimeout)
	if err != nil {
		return nil, err
	}
	// Firmware differs on the key name; accept either.
	raw, ok := resp["filter_list"]
	if !ok {
		raw, ok = resp["photo_filter_list"]
	}
	if !ok {
		return nil, fmt.Errorf("response has no filter_list: %v", resp)
	}
	s, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("unexpected filter_list type %T", raw)
	}

	var objs []map[string]any
	if err := json.Unmarshal([]byte(s), &objs); err != nil {
		return nil, fmt.Errorf("decode filter_list: %w", err)
	}
	out := make([]PhotoFilter, 0, len(objs))
	for _, o := range objs {
		id := firstString(o, "filter_id", "filterId", "id")
		if id == "" {
			continue
		}
		out = append(out, PhotoFilter{ID: id, Name: firstString(o, "filter_name", "filterName", "name")})
	}
	return out, nil
}

// SetPhotoFilter applies a post-processing filter (from GetPhotoFilterList) to
// an already-uploaded artwork. filterID of "" or "none" clears the effect.
func (c *ArtClient) SetPhotoFilter(contentID, filterID string) error {
	if filterID == "" {
		filterID = "none"
	}
	_, err := c.request("set_photo_filter", map[string]any{
		"content_id": contentID,
		"filter_id":  filterID,
	}, uploadTimeout)
	return err
}

// firstString returns the first non-empty string value among the given keys.
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := asString(m[k]); s != "" {
			return s
		}
	}
	return ""
}

// MatteList describes the matte shapes and colors a TV supports. Matte ids
// passed to Upload/ChangeMatte are formed as "<type>_<color>".
type MatteList struct {
	Types  []string
	Colors []string
}

// GetMatteList queries the matte shapes and colors available on this TV.
func (c *ArtClient) GetMatteList() (*MatteList, error) {
	resp, err := c.request("get_matte_list", nil, uploadTimeout)
	if err != nil {
		return nil, err
	}

	ml := &MatteList{}
	// Firmware differs on the key name; accept either.
	if v, ok := resp["matte_type_list"]; ok {
		ml.Types = decodeStringList(v)
	} else if v, ok := resp["matte_list"]; ok {
		ml.Types = decodeStringList(v)
	}
	if v, ok := resp["matte_color_list"]; ok {
		ml.Colors = decodeStringList(v)
	}
	return ml, nil
}

// decodeStringList parses a matte list value, which Samsung sends as a
// JSON-encoded string of either ["a","b"] or [{"matte_type":"a"},...].
func decodeStringList(v any) []string {
	s, ok := v.(string)
	if !ok {
		return nil
	}

	var plain []string
	if json.Unmarshal([]byte(s), &plain) == nil && len(plain) > 0 {
		return plain
	}

	var objs []map[string]any
	if json.Unmarshal([]byte(s), &objs) == nil {
		out := make([]string, 0, len(objs))
		for _, o := range objs {
			if name := asString(o["matte_type"]); name != "" {
				out = append(out, name)
			} else if name := asString(o["color"]); name != "" {
				out = append(out, name)
			}
		}
		return out
	}
	return nil
}

func normalizeFileType(fileType string) string {
	ft := fileType
	if ft == "" {
		ft = "jpg"
	}
	ft = trimDotLower(ft)
	if ft == "jpeg" {
		ft = "jpg"
	}
	return ft
}

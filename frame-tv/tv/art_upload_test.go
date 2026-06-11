package tv

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"
)

func TestParseConnInfo(t *testing.T) {
	t.Run("object form", func(t *testing.T) {
		ci, err := parseConnInfo(map[string]any{
			"conn_info": map[string]any{
				"ip": "10.0.0.83", "port": float64(12842), "key": "abc", "secured": true,
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ci.IP != "10.0.0.83" || ci.Port != 12842 || ci.Key != "abc" || !ci.Secured {
			t.Errorf("got %+v", ci)
		}
	})

	t.Run("string form with stringy fields", func(t *testing.T) {
		ci, err := parseConnInfo(map[string]any{
			"conn_info": `{"ip":"10.0.0.83","port":"9000","key":"k","secured":"false"}`,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ci.Port != 9000 || ci.Secured {
			t.Errorf("got %+v", ci)
		}
	})

	t.Run("missing conn_info", func(t *testing.T) {
		if _, err := parseConnInfo(map[string]any{"event": "ready_to_use"}); err == nil {
			t.Error("expected error for missing conn_info")
		}
	})

	t.Run("incomplete conn_info", func(t *testing.T) {
		if _, err := parseConnInfo(map[string]any{"conn_info": map[string]any{"ip": "10.0.0.83"}}); err == nil {
			t.Error("expected error for missing port")
		}
	})
}

func TestUploadOverSocketFraming(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	ci := connInfo{IP: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Key: "sekret"}
	data := []byte("\xff\xd8\xff\xe0 pretend jpeg bytes")

	errc := make(chan error, 1)
	go func() { errc <- uploadOverSocket(ci, data, "jpg", 5*time.Second) }()

	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer conn.Close()

	var lenBuf [4]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		t.Fatalf("read len: %v", err)
	}
	header := make([]byte, binary.BigEndian.Uint32(lenBuf[:]))
	if _, err := io.ReadFull(conn, header); err != nil {
		t.Fatalf("read header: %v", err)
	}
	var hdr struct {
		FileLength int    `json:"fileLength"`
		FileType   string `json:"fileType"`
		SecKey     string `json:"secKey"`
		Total      int    `json:"total"`
	}
	if err := json.Unmarshal(header, &hdr); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if hdr.FileLength != len(data) {
		t.Errorf("fileLength = %d, want %d", hdr.FileLength, len(data))
	}
	if hdr.FileType != "jpg" {
		t.Errorf("fileType = %q, want jpg", hdr.FileType)
	}
	if hdr.SecKey != "sekret" {
		t.Errorf("secKey = %q, want sekret", hdr.SecKey)
	}
	if hdr.Total != 1 {
		t.Errorf("total = %d, want 1", hdr.Total)
	}

	body := make([]byte, len(data))
	if _, err := io.ReadFull(conn, body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != string(data) {
		t.Errorf("body mismatch")
	}

	if err := <-errc; err != nil {
		t.Fatalf("uploadOverSocket: %v", err)
	}
}

func TestDisplayRoundTrip(t *testing.T) {
	srv := mockTV(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	art, err := dialArt(ctx, wsURL(srv.URL), "10.0.0.83", "")
	if err != nil {
		t.Fatalf("dialArt: %v", err)
	}
	defer art.Close()

	cid, err := art.Display([]byte("\xff\xd8\xff\xe0 fake jpeg"), "jpg", "modern_apricot")
	if err != nil {
		t.Fatalf("Display: %v", err)
	}
	if cid != "MY_F0099" {
		t.Errorf("content_id = %q, want MY_F0099", cid)
	}
}

func TestNormalizeFileType(t *testing.T) {
	cases := map[string]string{
		"jpg": "jpg", "JPG": "jpg", ".jpeg": "jpg", "JPEG": "jpg", "png": "png", "": "jpg",
	}
	for in, want := range cases {
		if got := normalizeFileType(in); got != want {
			t.Errorf("normalizeFileType(%q) = %q, want %q", in, got, want)
		}
	}
}

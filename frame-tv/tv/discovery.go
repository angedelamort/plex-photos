// Package tv talks to Samsung Tizen TVs (specifically The Frame) so the
// frame-tv service can discover a TV and drive its Art Mode over WebSocket.
//
// There are two layers here:
//
//   - discovery.go: a cheap, auth-free HTTP GET against the TV's REST endpoint
//     (http://<ip>:8001/api/v2/). This is how you learn whether an IP is a TV
//     at all and whether it advertises Frame TV art support. It never triggers
//     the "allow this device?" prompt on the TV.
//   - art.go: the secure WebSocket (wss://<ip>:8002) art-app channel used to
//     query and control Art Mode.
package tv

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DefaultRESTPort is the plain-HTTP port the Tizen device-info API listens on.
const DefaultRESTPort = 8001

// DeviceInfo is the JSON payload returned by http://<ip>:8001/api/v2/.
//
// Samsung returns every value as a string (even booleans), so the helper
// methods below normalize the ones we actually care about.
type DeviceInfo struct {
	Device struct {
		FrameTVSupport   string `json:"FrameTVSupport"`
		TokenAuthSupport string `json:"TokenAuthSupport"`
		VoiceSupport     string `json:"VoiceSupport"`
		PowerState       string `json:"PowerState"`
		Model            string `json:"model"`
		ModelName        string `json:"modelName"`
		Name             string `json:"name"`
		Resolution       string `json:"resolution"`
		OS               string `json:"OS"`
		WifiMac          string `json:"wifiMac"`
		CountryCode      string `json:"countryCode"`
		NetworkType      string `json:"networkType"`
	} `json:"device"`

	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	URI     string `json:"uri"`
	Version string `json:"version"`
}

// IsFrameTV reports whether the TV advertises Frame (Art Mode) support.
func (d *DeviceInfo) IsFrameTV() bool {
	return strings.EqualFold(strings.TrimSpace(d.Device.FrameTVSupport), "true")
}

// TokenAuth reports whether the WebSocket control channel expects a token
// (i.e. you must accept a prompt on the TV the first time you connect).
func (d *DeviceInfo) TokenAuth() bool {
	return strings.EqualFold(strings.TrimSpace(d.Device.TokenAuthSupport), "true")
}

// PoweredOn reports whether the TV says it is currently on. A Frame TV in Art
// Mode still reports "on".
func (d *DeviceInfo) PoweredOn() bool {
	return strings.EqualFold(strings.TrimSpace(d.Device.PowerState), "on")
}

// GetDeviceInfo fetches and parses the Tizen device-info document for the TV at
// ip. It uses the plain-HTTP port 8001, which requires no token and does not
// prompt the user on the TV, making it the right first call when probing.
func GetDeviceInfo(ctx context.Context, ip string) (*DeviceInfo, error) {
	url := fmt.Sprintf("http://%s:%d/api/v2/", ip, DefaultRESTPort)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach TV at %s: %w", ip, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TV returned HTTP %d", resp.StatusCode)
	}

	var info DeviceInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode device info: %w", err)
	}
	return &info, nil
}

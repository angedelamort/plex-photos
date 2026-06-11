package tv

import (
	"context"
	"fmt"
	"strings"
)

// ProbeResult is a human-readable snapshot of what we could learn about a TV.
// Fields are best-effort: a failure at one step records an error and probing
// continues so you still get whatever was reachable.
type ProbeResult struct {
	IP               string
	Reachable        bool
	Device           *DeviceInfo
	ArtModeSupported bool
	ArtAPIVersion    string
	ArtModeStatus    string
	Token            string
	Errors           []string
}

func (r *ProbeResult) addErr(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

// Probe gathers everything we can discover about the TV at ip without changing
// any of its settings. token may be empty.
//
// Step 1 (REST) never prompts the TV. Step 2 (the art WebSocket) may pop an
// "allow this device?" prompt on first connect for token-auth firmware.
func Probe(ctx context.Context, ip, token string) *ProbeResult {
	res := &ProbeResult{IP: ip, Token: token}

	info, err := GetDeviceInfo(ctx, ip)
	if err != nil {
		res.addErr("device info: %v", err)
		return res
	}
	res.Reachable = true
	res.Device = info
	res.ArtModeSupported = info.IsFrameTV()

	if !res.ArtModeSupported {
		res.addErr("TV does not advertise Frame/Art Mode support")
		return res
	}

	art, err := DialArt(ctx, ip, token)
	if err != nil {
		res.addErr("art channel: %v", err)
		return res
	}
	defer art.Close()
	res.Token = art.Token()

	if v, err := art.APIVersion(); err != nil {
		res.addErr("api version: %v", err)
	} else {
		res.ArtAPIVersion = v
	}

	if s, err := art.ArtModeStatus(); err != nil {
		res.addErr("art mode status: %v", err)
	} else {
		res.ArtModeStatus = s
	}

	return res
}

// String renders the probe result as a readable multi-line report.
func (r *ProbeResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "TV @ %s\n", r.IP)
	fmt.Fprintf(&b, "  reachable:      %v\n", r.Reachable)
	if r.Device != nil {
		d := r.Device.Device
		fmt.Fprintf(&b, "  name:           %s\n", d.Name)
		fmt.Fprintf(&b, "  model:          %s (%s)\n", d.ModelName, d.Model)
		fmt.Fprintf(&b, "  resolution:     %s\n", d.Resolution)
		fmt.Fprintf(&b, "  power state:    %s\n", d.PowerState)
		fmt.Fprintf(&b, "  frame tv:       %v\n", r.Device.IsFrameTV())
		fmt.Fprintf(&b, "  token auth:     %v\n", r.Device.TokenAuth())
		fmt.Fprintf(&b, "  rest version:   %s\n", r.Device.Version)
	}
	fmt.Fprintf(&b, "  art supported:  %v\n", r.ArtModeSupported)
	if r.ArtAPIVersion != "" {
		fmt.Fprintf(&b, "  art api ver:    %s\n", r.ArtAPIVersion)
	}
	if r.ArtModeStatus != "" {
		fmt.Fprintf(&b, "  art mode:       %s\n", r.ArtModeStatus)
	}
	if r.Token != "" {
		fmt.Fprintf(&b, "  token:          %s\n", r.Token)
	}
	for _, e := range r.Errors {
		fmt.Fprintf(&b, "  error:          %s\n", e)
	}
	return b.String()
}

package tv

import (
	"encoding/json"
	"testing"
)

// sampleFrameInfo is a real device-info payload from a 2022 Frame TV (anonymized),
// used to lock our struct tags against actual Samsung output.
const sampleFrameInfo = `{
  "device": {
    "FrameTVSupport": "true",
    "TokenAuthSupport": "true",
    "VoiceSupport": "true",
    "PowerState": "on",
    "model": "22_PONTUSM_FTV",
    "modelName": "QN43LS03BDFXZA",
    "name": "Frame TV",
    "resolution": "3840x2160",
    "OS": "Tizen",
    "wifiMac": "00:00:00:00:00:00",
    "countryCode": "US",
    "networkType": "wireless"
  },
  "id": "uuid:0000",
  "name": "Frame TV",
  "type": "Samsung SmartTV",
  "uri": "http://10.0.0.83:8001/api/v2/",
  "version": "2.0.25"
}`

// sampleNonFrameInfo is a regular (non-Frame) Tizen TV.
const sampleNonFrameInfo = `{
  "device": {
    "FrameTVSupport": "false",
    "TokenAuthSupport": "true",
    "PowerState": "on",
    "modelName": "QN55Q60AAFXZA",
    "name": "Living Room TV"
  },
  "type": "Samsung SmartTV",
  "version": "2.0.25"
}`

func TestDeviceInfoParsing(t *testing.T) {
	var info DeviceInfo
	if err := json.Unmarshal([]byte(sampleFrameInfo), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !info.IsFrameTV() {
		t.Error("IsFrameTV() = false, want true")
	}
	if !info.TokenAuth() {
		t.Error("TokenAuth() = false, want true")
	}
	if !info.PoweredOn() {
		t.Error("PoweredOn() = false, want true")
	}
	if info.Device.ModelName != "QN43LS03BDFXZA" {
		t.Errorf("ModelName = %q, want QN43LS03BDFXZA", info.Device.ModelName)
	}
	if info.Device.Resolution != "3840x2160" {
		t.Errorf("Resolution = %q, want 3840x2160", info.Device.Resolution)
	}
}

func TestDeviceInfoNonFrame(t *testing.T) {
	var info DeviceInfo
	if err := json.Unmarshal([]byte(sampleNonFrameInfo), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.IsFrameTV() {
		t.Error("IsFrameTV() = true for a non-Frame TV, want false")
	}
}

func TestDeviceInfoHelpersNormalize(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"true", true},
		{"True", true},
		{" TRUE ", true},
		{"false", false},
		{"", false},
	}
	for _, tc := range cases {
		var info DeviceInfo
		info.Device.FrameTVSupport = tc.raw
		if got := info.IsFrameTV(); got != tc.want {
			t.Errorf("IsFrameTV(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

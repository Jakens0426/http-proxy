package core

import (
	"strings"
	"testing"
)

func TestValidateProxyForSingBoxAllowsSupportedVLESSFlow(t *testing.T) {
	proxy := &ProxyInfo{
		Name:     "VLESS Vision",
		Tag:      "vless-vision",
		Protocol: "vless",
		Flow:     SupportedVLESSFlowVision,
	}

	if err := ValidateProxyForSingBox(proxy); err != nil {
		t.Fatalf("ValidateProxyForSingBox() error = %v", err)
	}
}

func TestValidateProxyForSingBoxRejectsUnsupportedVLESSFlow(t *testing.T) {
	proxy := &ProxyInfo{
		Name:     "VLESS Bad",
		Tag:      "vless-bad",
		Protocol: "vless",
		Flow:     "xtls-rprx-vision-udp443",
		SourceID: "sub-one",
	}

	err := ValidateProxyForSingBox(proxy)
	if err == nil {
		t.Fatal("ValidateProxyForSingBox() error nil, want failure")
	}
	errText := err.Error()
	for _, want := range []string{"vless-bad", "VLESS Bad", "sub-one", "xtls-rprx-vision-udp443"} {
		if !strings.Contains(errText, want) {
			t.Fatalf("error = %q, want %q", errText, want)
		}
	}
}

func TestValidateProxyForSingBoxIgnoresSSFlowField(t *testing.T) {
	proxy := &ProxyInfo{
		Name:     "SS",
		Tag:      "ss-one",
		Protocol: "ss",
		Flow:     "xtls-rprx-vision-udp443",
	}

	if err := ValidateProxyForSingBox(proxy); err != nil {
		t.Fatalf("ValidateProxyForSingBox() error = %v", err)
	}
}

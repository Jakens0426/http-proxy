package core

import (
	"fmt"
	"strings"
	"time"
)

const SupportedVLESSFlowVision = "xtls-rprx-vision"

func ValidateProxyForSingBox(p *ProxyInfo) error {
	if p == nil {
		return fmt.Errorf("proxy is nil")
	}
	if strings.EqualFold(p.Protocol, "vless") {
		flow := strings.TrimSpace(p.Flow)
		if flow != "" && flow != SupportedVLESSFlowVision {
			return fmt.Errorf("unsupported vless flow: tag=%s name=%q source=%s flow=%q", p.Tag, p.Name, p.SourceID, p.Flow)
		}
	}
	return nil
}

func UnsupportedProxyTestResult(p *ProxyInfo, err error) *TestResult {
	tag := ""
	if p != nil {
		tag = p.Tag
	}
	return &TestResult{
		Tag:       tag,
		Err:       err.Error(),
		Timestamp: time.Now().UTC(),
	}
}

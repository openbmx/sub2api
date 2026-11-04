package service

import (
	"net/url"
	"strings"
)

func allowAnthropicRequestHeader(name string, target *url.URL) bool {
	if allowedHeaders[name] {
		return true
	}
	if !isOfficialAnthropicTarget(target) {
		return false
	}
	return strings.HasPrefix(name, "anthropic-") || strings.HasPrefix(name, "x-claude-code-")
}

func isOfficialAnthropicTarget(target *url.URL) bool {
	if target == nil || target.Scheme != "https" {
		return false
	}
	host := strings.ToLower(target.Hostname())
	awsOrigin := strings.HasPrefix(host, "aws-external-anthropic.") && strings.HasSuffix(host, ".api.aws")
	return host == "api.anthropic.com" || awsOrigin
}

func validateAnthropicSafetyBetaForTarget(body []byte, beta, targetURL string) error {
	target, err := url.Parse(targetURL)
	if err == nil && isOfficialAnthropicTarget(target) {
		return validateAnthropicSafetyBeta(body, beta)
	}
	return nil
}

package service

import "github.com/Wei-Shaw/sub2api/internal/pkg/claude"

func resolveAnthropicUpstreamModel(account *Account, model string) string {
	if account.Type == AccountTypeAPIKey || account.Type == AccountTypeServiceAccount || account.IsAnthropicOAuthOrSetupToken() {
		if mapped, matched := account.ResolveMappedModel(model); matched {
			return mapped
		}
		if account.Platform == PlatformAnthropic && account.Type != AccountTypeAPIKey {
			if mapped, matched := account.ResolveMappedModel(claude.NormalizeModelID(model)); matched {
				return mapped
			}
		}
	}
	if account.Platform == PlatformAnthropic && account.Type == AccountTypeServiceAccount {
		return normalizeVertexAnthropicModelID(claude.NormalizeModelID(model))
	}
	if account.Platform == PlatformAnthropic && account.Type != AccountTypeAPIKey {
		return claude.NormalizeModelID(model)
	}
	return model
}

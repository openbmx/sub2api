package handler

import (
	"fmt"

	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// Binding a third-party identity to an admin account adds a login path, and an
// OAuth login for a bound identity is issued tokens without a TOTP challenge.
// The admin panel's BindAuthIdentity is therefore gated by step-up for admin
// targets; the self-service flow (bind-token, then /auth/oauth/*/bind/start)
// reaches the same result and is gated the same way. Without it, a stolen admin
// session could plant its own OAuth login on the account — one that survives a
// password reset and skips 2FA — without ever passing step-up.
//
// The flow has two entry points:
//   - PrepareOAuthBindAccessTokenCookie is authenticated, so it runs the regular
//     EnforceStepUp and the profile page can prompt for a code before redirecting.
//   - bind/start is public and trusts whatever bind access-token cookie arrives;
//     a client can set that cookie itself, so oauthBindStepUpSatisfied repeats
//     the check against the session of the token in the cookie. This is the
//     authoritative check.
//
// Both apply the rule BindAuthIdentity applies: admin accounts only, and only
// while step_up_enabled is on.

// requireAdminOAuthBindStepUp writes the error response and returns false when
// the authenticated caller is an admin without a step-up grant.
func (h *AuthHandler) requireAdminOAuthBindStepUp(c *gin.Context) bool {
	subject, ok := servermiddleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 || h.userService == nil {
		// bind/start still makes the authoritative decision.
		return true
	}
	user, err := h.userService.GetByID(c.Request.Context(), subject.UserID)
	if err != nil {
		servermiddleware.AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to load user")
		return false
	}
	if user.Role != service.RoleAdmin {
		return true
	}
	return servermiddleware.EnforceStepUp(c, h.totpService, h.userService, h.settingSvc)
}

// oauthBindStepUpSatisfied is the bind/start counterpart of EnforceStepUp: the
// user comes from the bind access-token cookie rather than the request's auth
// subject, and the session from that token's sid. A missing setting service
// keeps the gate on, as EnforceStepUp does.
func (h *AuthHandler) oauthBindStepUpSatisfied(c *gin.Context, user *service.User, claims *service.JWTClaims) error {
	if user == nil || user.Role != service.RoleAdmin {
		return nil
	}
	if h.settingSvc != nil && !h.settingSvc.IsStepUpEnabled(c.Request.Context()) {
		return nil
	}
	if !user.TotpEnabled || h.totpService == nil {
		return service.ErrInvalidToken
	}
	granted, err := h.totpService.HasStepUpGrant(c.Request.Context(), user.ID, oauthBindStepUpSessionKey(claims))
	if err != nil || !granted {
		return service.ErrInvalidToken
	}
	return nil
}

// oauthBindStepUpSessionKey derives the step-up session key from a token's
// claims the way servermiddleware.StepUpSessionKey derives it from a request
// authenticated with that token. A test keeps the two in step.
func oauthBindStepUpSessionKey(claims *service.JWTClaims) string {
	if claims.SessionID != "" {
		return claims.SessionID
	}
	return fmt.Sprintf("u%d", claims.UserID)
}

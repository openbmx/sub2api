//go:build unit

package server

import (
	"regexp"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/server/routes"
	"github.com/gin-gonic/gin"
)

// routeParamPattern matches gin path parameters (:id) and catch-alls (*rest).
var routeParamPattern = regexp.MustCompile(`[:*][^/]+`)

// TestIPv6RestrictedPathsCoverEveryGatewayRoute walks the routes the gateway
// actually registers, so a root alias added upstream (as the Seedance
// /api/v3, /v3 and bare /contents aliases were) fails here instead of silently
// escaping the IPv6 block. The prefix list lives in a fork file that no merge
// conflict will ever point at.
func TestIPv6RestrictedPathsCoverEveryGatewayRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	routes.RegisterGatewayRoutes(r, &handler.Handlers{}, nil, nil, nil, nil, nil, nil, &config.Config{})

	registered := r.Routes()
	if len(registered) == 0 {
		t.Fatal("no gateway routes were registered")
	}
	for _, route := range registered {
		path := routeParamPattern.ReplaceAllString(route.Path, "x")
		if !middleware.IsIPv6RestrictedPath(path) {
			t.Errorf("%s %s is a gateway route but is not covered by ipv6RestrictedPathPrefixes", route.Method, route.Path)
		}
	}
}

// TestIPv6RestrictedPathsLeavePanelAPIAlone pins the other side: the admin and
// user panel under /api/v1 must stay reachable over IPv6.
func TestIPv6RestrictedPathsLeavePanelAPIAlone(t *testing.T) {
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/admin/settings", "/", "/assets/index.js"} {
		if middleware.IsIPv6RestrictedPath(path) {
			t.Errorf("%s must not be subject to the IPv6 gateway block", path)
		}
	}
}

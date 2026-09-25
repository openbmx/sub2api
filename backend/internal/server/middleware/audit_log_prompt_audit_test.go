//go:build unit

package middleware

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The prompt-audit preview takes a pasted real prompt. Key-based redaction does
// not recognize "content"/"custom_prompt", so without this entry up to 16 KB of
// production prompt text lands in audit_logs, where deleting prompt-audit events
// never reaches it. The route is a fork addition registered in routes/admin.go.
func TestAuditLogOmitsPromptAuditPreviewBody(t *testing.T) {
	require.Contains(t, auditBodyOmittedRoutes, "POST /api/v1/admin/prompt-audit/preview")
}

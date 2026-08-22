package securityaudit

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func configManagerWithMockDB(t *testing.T) (*ConfigManager, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	manager := NewConfigManager(db, staticSettingRepository{values: map[string]string{
		SettingKeyPromptAuditConfig: "",
		SettingKeyRiskControl:       "false",
	}}, nil, prefixEncryptor{}, testTotpKeyConfig())
	return manager, mock
}

// Saving config queues behind another save on a global advisory lock. The wait
// is wanted — the loser goes on to read the winner's version and report a
// conflict the administrator can act on — but it has to be bounded, or a save
// whose holder is wedged pins one of the shared pool's connections for as long
// as the request survives. sqlmock matches in order, so this fails if the
// timeout is ever dropped or moved after the lock request.
func TestConfigSaveArmsLockTimeoutBeforeTakingTheLock(t *testing.T) {
	manager, mock := configManagerWithMockDB(t)

	mock.ExpectBegin()
	mock.ExpectExec("set_config").
		WithArgs(promptAuditConfigLockTimeout).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs(promptAuditConfigLockKey).
		WillReturnError(lockTimeoutError())
	mock.ExpectRollback()

	_, err := manager.Save(context.Background(), UpdateConfigRequest{ExpectedConfigVersion: 1}, 7)

	require.Error(t, err)
	require.Equal(t, ErrorCodeConfigSaveBusy, infraerrors.Reason(err),
		"a save that never reached the version comparison must not be reported as a CAS conflict")
	require.NoError(t, mock.ExpectationsWereMet())
}

// The timeout must stay far above how long a save actually takes, otherwise
// ordinary concurrent administrators would start seeing timeouts instead of the
// version conflict that tells them to rebase their draft.
func TestConfigSaveLockTimeoutLeavesRoomForNormalContention(t *testing.T) {
	require.NotEmpty(t, promptAuditConfigLockTimeout)
	require.NotEqual(t, "0", promptAuditConfigLockTimeout,
		"PostgreSQL reads 0 as 'disabled', which is the unbounded wait this replaced")
	require.NotEqual(t, promptAuditAdmissionLockTimeout, promptAuditConfigLockTimeout,
		"an admin save may queue far longer than a request-path admission")
}

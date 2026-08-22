package securityaudit

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// A contended admission must be retried rather than dropped on the first miss.
// Dropping immediately meant that an agent fanning several requests out inside
// the same millisecond lost audit coverage for every request but the one that
// happened to win the lock, even with the queue almost entirely empty.
func TestCreateStagingWithCapacityRetriesContendedAdmission(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	for i := 0; i < promptAuditAdmissionAttempts; i++ {
		mock.ExpectBegin()
		mock.ExpectQuery("pg_try_advisory_xact_lock").
			WithArgs(promptAuditAdmissionLockKey).
			WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_xact_lock"}).AddRow(false))
		mock.ExpectRollback()
	}

	repo := NewPostgreSQLRepository(db)
	_, err = repo.CreateStagingWithCapacity(
		context.Background(), PromptSnapshot{RequestID: "contended"}, 1, 3, 1024)

	require.ErrorIs(t, err, ErrQueueAdmissionBusy,
		"sustained contention must still surface as queue_admission_busy")
	require.NoError(t, mock.ExpectationsWereMet(),
		"admission must be attempted %d times before giving up", promptAuditAdmissionAttempts)
}

// A caller that is already gone must fail out on the first transaction instead
// of burning the whole retry budget, and must surface as itself rather than as
// contention — otherwise one lost caller gets two different error codes
// depending on whether it died during a backoff or inside the next BeginTx.
func TestCreateStagingWithCapacityDoesNotRetryDepartedCaller(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	repo := NewPostgreSQLRepository(db)
	_, err = repo.CreateStagingWithCapacity(ctx, PromptSnapshot{RequestID: "departed"}, 1, 3, 1024)

	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, ErrQueueAdmissionBusy)
	require.NoError(t, mock.ExpectationsWereMet(), "a departed caller must not be retried")
}

func TestAdmissionBackoffIsBoundedAndJittered(t *testing.T) {
	for attempt := 0; attempt < promptAuditAdmissionAttempts+4; attempt++ {
		for i := 0; i < 64; i++ {
			wait := admissionBackoff(attempt)
			require.Positive(t, wait,
				"a zero pause would let collided enqueues retry in lockstep")
			require.LessOrEqual(t, wait, promptAuditAdmissionBackoffMax,
				"backoff must stay far inside the enqueue budget")
		}
	}

	// The jitter is what makes the retry work at all: identical pauses would put
	// the losers of one collision straight into the next one.
	distinct := map[time.Duration]struct{}{}
	for i := 0; i < 256; i++ {
		distinct[admissionBackoff(3)] = struct{}{}
	}
	require.Greater(t, len(distinct), 1, "admission backoff must be jittered")
}

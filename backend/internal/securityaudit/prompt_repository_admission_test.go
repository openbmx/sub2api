package securityaudit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func lockTimeoutError() *pq.Error {
	return &pq.Error{Code: pgLockNotAvailable, Message: "canceling statement due to lock timeout"}
}

// The bounded wait must be armed before the lock is requested, or the enqueue
// would queue on the advisory lock with no timeout at all and hold a pooled
// connection for as long as the holder takes. sqlmock matches in order, so the
// expectations below fail if the two statements are ever swapped or the
// set_config call is dropped.
func TestCreateStagingWithCapacityArmsLockTimeoutBeforeTakingTheLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectExec("set_config").
		WithArgs(promptAuditAdmissionLockTimeout).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs(promptAuditAdmissionLockKey).
		WillReturnError(lockTimeoutError())
	mock.ExpectRollback()

	repo := NewPostgreSQLRepository(db)
	_, err = repo.CreateStagingWithCapacity(
		context.Background(), PromptSnapshot{RequestID: "contended"}, 1, 3, 1024)

	require.ErrorIs(t, err, ErrQueueAdmissionBusy,
		"a lock wait that outlives the timeout is genuine admission contention")
	require.NoError(t, mock.ExpectationsWereMet())
}

// The blocking lock must not be paired with the non-blocking probe: a waiter has
// to queue behind the current holder rather than be rejected the moment it finds
// the lock taken. That regression is invisible in behaviour tests — both spell
// "no job" — so assert the statement itself.
func TestCreateStagingWithCapacityUsesBlockingAdvisoryLock(t *testing.T) {
	var executed []string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(
		sqlmock.QueryMatcherFunc(func(expected, actual string) error {
			executed = append(executed, actual)
			return sqlmock.QueryMatcherRegexp.Match(expected, actual)
		})))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectExec("set_config").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("pg_advisory_xact_lock").WillReturnError(lockTimeoutError())
	mock.ExpectRollback()

	repo := NewPostgreSQLRepository(db)
	_, _ = repo.CreateStagingWithCapacity(
		context.Background(), PromptSnapshot{RequestID: "blocking"}, 1, 3, 1024)

	var lockStatement string
	for _, statement := range executed {
		if strings.Contains(statement, "advisory") {
			lockStatement = statement
		}
	}
	require.NotEmpty(t, lockStatement, "admission must take the advisory lock")
	require.NotContains(t, lockStatement, "pg_try_advisory_xact_lock",
		"the non-blocking probe drops simultaneous arrivals instead of queueing them")
}

// 55P03 is only contention when it comes from the lock statement. Raised later it
// means something else blocked the INSERT, and mapping that to a busy queue would
// point whoever reads the log at the wrong subsystem.
func TestCreateStagingWithCapacityOnlyTreatsLockStatementTimeoutAsContention(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectExec("set_config").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("INSERT INTO prompt_audit_jobs").
		WillReturnError(lockTimeoutError())
	mock.ExpectRollback()

	repo := NewPostgreSQLRepository(db)
	_, err = repo.CreateStagingWithCapacity(
		context.Background(), PromptSnapshot{RequestID: "late-timeout"}, 1, 3, 1024)

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrQueueAdmissionBusy)
	var pqErr *pq.Error
	require.True(t, errors.As(err, &pqErr), "the original failure must reach the caller")
	require.NoError(t, mock.ExpectationsWereMet())
}

// A caller that is already gone must not take a slot or a connection.
func TestCreateStagingWithCapacityRejectsDepartedCallerWithoutTouchingTheDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	repo := NewPostgreSQLRepository(db)
	_, err = repo.CreateStagingWithCapacity(ctx, PromptSnapshot{RequestID: "departed"}, 1, 3, 1024)

	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, ErrQueueAdmissionBusy)
	require.NoError(t, mock.ExpectationsWereMet(), "no transaction may be opened")
}

// Every exit path must hand its admission slot back. A leak would not fail any
// single call — it would quietly shrink the slot pool until enqueues stopped
// reaching the database at all, so drive more calls than there are slots and let
// a deadline turn a leak into a failure instead of a hang.
func TestCreateStagingWithCapacityReleasesItsSlotOnEveryPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	repo := NewPostgreSQLRepository(db)
	rounds := cap(repo.admissionSlots) * 3
	for i := 0; i < rounds; i++ {
		mock.ExpectBegin()
		mock.ExpectExec("set_config").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("pg_advisory_xact_lock").WillReturnError(lockTimeoutError())
		mock.ExpectRollback()
	}

	for i := 0; i < rounds; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := repo.CreateStagingWithCapacity(ctx, PromptSnapshot{RequestID: "slot"}, 1, 3, 1024)
		cancel()
		require.ErrorIs(t, err, ErrQueueAdmissionBusy,
			"call %d blocked or failed differently, which means a slot was never returned", i)
	}
	require.NoError(t, mock.ExpectationsWereMet())
	require.Empty(t, repo.admissionSlots, "all admission slots must be free once calls return")
}

// A queue that cannot admit anything is settled before a slot, a connection or
// the global lock is taken. Answering it inside the lock instead would turn one
// bad configuration value into hot-path serialisation for every request.
func TestCreateStagingWithCapacityRejectsImpossibleCapacityWithoutTakingAnything(t *testing.T) {
	for _, capacity := range []int{0, -1} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)

		repo := NewPostgreSQLRepository(db)
		_, err = repo.CreateStagingWithCapacity(
			context.Background(), PromptSnapshot{RequestID: "no-capacity"}, 1, 3, capacity)

		require.ErrorIs(t, err, ErrQueueFull, "capacity %d", capacity)
		require.NoError(t, mock.ExpectationsWereMet(),
			"capacity %d must be rejected without opening a transaction", capacity)
		require.Empty(t, repo.admissionSlots, "capacity %d must not consume a slot", capacity)
		_ = db.Close()
	}
}

// A repository built without the constructor has no slot channel. Sending on a
// nil channel blocks forever, and a background context never releases it, so the
// enqueue path would wedge rather than merely lose its cap.
func TestCreateStagingWithCapacitySurvivesUnsetAdmissionSlots(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectExec("set_config").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("pg_advisory_xact_lock").WillReturnError(lockTimeoutError())
	mock.ExpectRollback()

	repo := &PostgreSQLRepository{db: db, clock: realClock{}}
	require.Nil(t, repo.admissionSlots, "this test is meaningless if the zero value is populated")

	done := make(chan error, 1)
	go func() {
		_, callErr := repo.CreateStagingWithCapacity(
			context.Background(), PromptSnapshot{RequestID: "unset-slots"}, 1, 3, 1024)
		done <- callErr
	}()

	select {
	case callErr := <-done:
		require.ErrorIs(t, callErr, ErrQueueAdmissionBusy)
	case <-time.After(10 * time.Second):
		t.Fatal("admission blocked forever on a nil slot channel")
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

// The cap is what keeps a burst of enqueues from parking every pooled connection
// on the advisory lock, so it has to follow the pool this repository was actually
// handed rather than a constant that happens to suit one deployment.
func TestAdmissionSlotsNeverClaimMoreThanAShareOfThePool(t *testing.T) {
	for _, tt := range []struct {
		name    string
		maxOpen int
		want    int
	}{
		{name: "unbounded pool falls back to the ceiling", maxOpen: 0, want: promptAuditAdmissionSlotCeiling},
		{name: "large pool is capped by the ceiling", maxOpen: 256, want: promptAuditAdmissionSlotCeiling},
		{name: "production pool", maxOpen: 20, want: 5},
		{name: "small pool yields a smaller share", maxOpen: 12, want: 3},
		{name: "pool smaller than the divisor still admits one", maxOpen: 2, want: 1},
		{name: "single connection still admits one", maxOpen: 1, want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, _, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			db.SetMaxOpenConns(tt.maxOpen)

			slots := admissionSlotsFor(db)
			require.Equal(t, tt.want, slots)
			require.Positive(t, slots,
				"a zero cap would deadlock admission instead of bounding it")
			if tt.maxOpen > 0 {
				require.LessOrEqual(t, slots, max(1, tt.maxOpen/promptAuditAdmissionPoolDivisor),
					"admission must not exceed its share of the pool")
			}
			if tt.maxOpen > 1 {
				require.Less(t, slots, tt.maxOpen,
					"admission must leave connections for the gateway's own queries")
			}
			require.Equal(t, slots, cap(NewPostgreSQLRepository(db).admissionSlots))
		})
	}
}

func TestAdmissionLockTimeoutIsConfigured(t *testing.T) {
	require.NotEmpty(t, promptAuditAdmissionLockTimeout,
		"an unset lock_timeout would make the advisory lock wait indefinitely")
	require.NotEqual(t, "0", promptAuditAdmissionLockTimeout,
		"PostgreSQL reads 0 as 'disabled', not 'do not wait'")
}

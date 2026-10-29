//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// multiBalanceUserRepoStub 支持多用户的原子余额桩（userRepoStub 的 SetBalance 会 panic）。
type multiBalanceUserRepoStub struct {
	*userRepoStub
	setCalls []int64
}

func (s *multiBalanceUserRepoStub) SetBalance(ctx context.Context, id int64, value float64) (BalanceChange, error) {
	user, ok := s.usersByID[id]
	if !ok {
		return BalanceChange{}, ErrUserNotFound
	}
	change := BalanceChange{Old: user.Balance, New: value}
	user.Balance = value
	s.setCalls = append(s.setCalls, id)
	return change, nil
}

func TestAdminService_BatchUpdateUserStatus_DisablesAndSkipsAdmin(t *testing.T) {
	repo := &userRepoStub{usersByID: map[int64]*User{
		1: {ID: 1, Role: RoleUser, Status: StatusActive},
		2: {ID: 2, Role: RoleAdmin, Status: StatusActive},
	}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{userRepo: repo, authCacheInvalidator: invalidator}

	result, err := svc.BatchUpdateUserStatus(context.Background(), []int64{1, 2, 999, 1}, StatusDisabled)
	require.NoError(t, err)
	require.Equal(t, 1, result.Affected)
	require.Equal(t, []int64{1}, result.SuccessIDs)
	require.Len(t, result.Skipped, 2)
	require.Equal(t, int64(2), result.Skipped[0].ID)
	require.Equal(t, "cannot disable admin user", result.Skipped[0].Reason)
	require.Equal(t, int64(999), result.Skipped[1].ID)
	require.Equal(t, StatusDisabled, repo.usersByID[1].Status)
	require.Equal(t, StatusActive, repo.usersByID[2].Status)
	require.Equal(t, []int64{1}, invalidator.userIDs)
}

func TestAdminService_BatchUpdateUserStatus_NoopWhenAlreadyTarget(t *testing.T) {
	repo := &userRepoStub{usersByID: map[int64]*User{
		1: {ID: 1, Role: RoleUser, Status: StatusActive},
	}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{userRepo: repo, authCacheInvalidator: invalidator}

	result, err := svc.BatchUpdateUserStatus(context.Background(), []int64{1}, StatusActive)
	require.NoError(t, err)
	require.Equal(t, 1, result.Affected)
	require.Empty(t, repo.updated)
	require.Empty(t, invalidator.userIDs)
}

func TestAdminService_BatchUpdateUserStatus_InvalidStatus(t *testing.T) {
	svc := &adminServiceImpl{userRepo: &userRepoStub{}}
	_, err := svc.BatchUpdateUserStatus(context.Background(), []int64{1}, "banned")
	require.Error(t, err)
}

func TestAdminService_BatchDeleteUsers_SkipsAdminAndMissing(t *testing.T) {
	repo := &userRepoStub{usersByID: map[int64]*User{
		1: {ID: 1, Role: RoleUser, Status: StatusActive},
		2: {ID: 2, Role: RoleAdmin, Status: StatusActive},
	}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{userRepo: repo, authCacheInvalidator: invalidator}

	result, err := svc.BatchDeleteUsers(context.Background(), []int64{1, 2, 999})
	require.NoError(t, err)
	require.Equal(t, 1, result.Affected)
	require.Equal(t, []int64{1}, result.SuccessIDs)
	require.Len(t, result.Skipped, 2)
	require.Equal(t, int64(2), result.Skipped[0].ID)
	// Reasons are shown to the admin, so they carry the error's message rather
	// than ApplicationError's debug format (code=… reason=… metadata=…).
	require.Contains(t, result.Skipped[0].Reason, "cannot delete admin user")
	require.NotContains(t, result.Skipped[0].Reason, "code=")
	require.Equal(t, int64(999), result.Skipped[1].ID)
	require.Equal(t, "user not found", result.Skipped[1].Reason)
	require.Equal(t, []int64{1}, repo.deletedIDs)
	require.Equal(t, []int64{1}, invalidator.userIDs)
}

func TestAdminService_BatchUpdateUserBalance_SetToValue(t *testing.T) {
	repo := &multiBalanceUserRepoStub{userRepoStub: &userRepoStub{usersByID: map[int64]*User{
		1: {ID: 1, Role: RoleUser, Balance: 10},
		2: {ID: 2, Role: RoleUser, Balance: 3},
	}}}
	redeemRepo := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{userRepo: repo, redeemCodeRepo: redeemRepo, authCacheInvalidator: invalidator}

	result, err := svc.BatchUpdateUserBalance(context.Background(), []int64{1, 2, 999}, 5, "set", "campaign")
	require.NoError(t, err)
	require.Equal(t, 2, result.Affected)
	require.Equal(t, []int64{1, 2}, result.SuccessIDs)
	require.Len(t, result.Skipped, 1)
	require.Equal(t, int64(999), result.Skipped[0].ID)
	require.Equal(t, 5.0, repo.usersByID[1].Balance)
	require.Equal(t, 5.0, repo.usersByID[2].Balance)
	// 每个发生变化的用户都要有调整流水（diff = 5-10 = -5 与 5-3 = +2）
	require.Len(t, redeemRepo.created, 2)
	require.Equal(t, -5.0, redeemRepo.created[0].Value)
	require.Equal(t, 2.0, redeemRepo.created[1].Value)
	require.Equal(t, "campaign", redeemRepo.created[0].Notes)
	require.ElementsMatch(t, []int64{1, 2}, invalidator.userIDs)
}

// cancelOnCommitUserRepoStub cancels the batch context right after a balance
// change commits — where a client or proxy timeout lands in practice — and fails
// reads on a done context the way database/sql does.
type cancelOnCommitUserRepoStub struct {
	*multiBalanceUserRepoStub
	cancel context.CancelFunc
}

func (s *cancelOnCommitUserRepoStub) SetBalance(ctx context.Context, id int64, value float64) (BalanceChange, error) {
	change, err := s.multiBalanceUserRepoStub.SetBalance(ctx, id, value)
	s.cancel()
	return change, err
}

func (s *cancelOnCommitUserRepoStub) GetByID(ctx context.Context, id int64) (*User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.multiBalanceUserRepoStub.GetByID(ctx, id)
}

// A cancellation between "balance committed" and "user read back" used to
// report an applied credit as skipped — no adjustment record, no cache
// invalidation — and a retry of the skipped users then credited them twice.
// It also kept running later users on the cancelled context.
func TestAdminService_BatchUpdateUserBalance_DeadlineFallsBetweenUsers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &cancelOnCommitUserRepoStub{multiBalanceUserRepoStub: &multiBalanceUserRepoStub{userRepoStub: &userRepoStub{usersByID: map[int64]*User{
		1: {ID: 1, Role: RoleUser, Balance: 10},
		2: {ID: 2, Role: RoleUser, Balance: 3},
	}}}, cancel: cancel}
	redeemRepo := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{userRepo: repo, redeemCodeRepo: redeemRepo, authCacheInvalidator: invalidator}

	result, err := svc.BatchUpdateUserBalance(ctx, []int64{1, 2}, 5, "set", "campaign")
	require.NoError(t, err)
	require.Equal(t, []int64{1}, result.SuccessIDs, "a committed change must be reported as applied")
	require.Len(t, redeemRepo.created, 1, "and keep its adjustment record")
	require.Equal(t, []int64{1}, invalidator.userIDs)
	require.Len(t, result.Skipped, 1)
	require.Equal(t, int64(2), result.Skipped[0].ID)
	require.True(t, strings.HasPrefix(result.Skipped[0].Reason, BatchNotProcessedReasonPrefix), result.Skipped[0].Reason)
	require.Equal(t, 3.0, repo.usersByID[2].Balance, "users after the deadline must not be touched")
}

// failingReadBackUserRepoStub commits balance changes but cannot read users back,
// like a DB blip right after the UPDATE.
type failingReadBackUserRepoStub struct {
	*multiBalanceUserRepoStub
}

func (s *failingReadBackUserRepoStub) GetByID(context.Context, int64) (*User, error) {
	return nil, context.DeadlineExceeded
}

// A read-back failing after the commit used to report the credit as skipped,
// and the UI re-selects skipped users for a retry that credits them twice.
func TestAdminService_BatchUpdateUserBalance_ReadBackFailureIsNotASkip(t *testing.T) {
	repo := &failingReadBackUserRepoStub{multiBalanceUserRepoStub: &multiBalanceUserRepoStub{userRepoStub: &userRepoStub{usersByID: map[int64]*User{
		1: {ID: 1, Role: RoleUser, Balance: 10},
		2: {ID: 2, Role: RoleUser, Balance: 3},
	}}}}
	redeemRepo := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{userRepo: repo, redeemCodeRepo: redeemRepo, authCacheInvalidator: invalidator}

	result, err := svc.BatchUpdateUserBalance(context.Background(), []int64{1, 2}, 5, "set", "campaign")
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, result.SuccessIDs, "committed changes must be reported as applied")
	require.Empty(t, result.Skipped)
	require.Len(t, redeemRepo.created, 2, "each committed change keeps its adjustment record")
	require.ElementsMatch(t, []int64{1, 2}, invalidator.userIDs)
}

// The single-user call still reports the failed read-back, but the committed
// change keeps its follow-ups instead of skipping them.
func TestAdminService_UpdateUserBalance_ReadBackFailureKeepsFollowUps(t *testing.T) {
	repo := &failingReadBackUserRepoStub{multiBalanceUserRepoStub: &multiBalanceUserRepoStub{userRepoStub: &userRepoStub{usersByID: map[int64]*User{
		1: {ID: 1, Role: RoleUser, Balance: 10},
	}}}}
	redeemRepo := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{userRepo: repo, redeemCodeRepo: redeemRepo, authCacheInvalidator: invalidator}

	_, err := svc.UpdateUserBalance(context.Background(), 1, 5, "set", "")
	require.Error(t, err)
	require.Equal(t, 5.0, repo.usersByID[1].Balance)
	require.Len(t, redeemRepo.created, 1)
	require.Equal(t, []int64{1}, invalidator.userIDs)
}

func TestAdminService_BatchUpdateUserBalance_InvalidOperation(t *testing.T) {
	svc := &adminServiceImpl{userRepo: &userRepoStub{}}
	_, err := svc.BatchUpdateUserBalance(context.Background(), []int64{1}, 5, "multiply", "")
	require.Error(t, err)
}

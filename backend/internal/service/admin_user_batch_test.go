//go:build unit

package service

import (
	"context"
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
	require.Equal(t, "cannot delete admin user", result.Skipped[0].Reason)
	require.Equal(t, int64(999), result.Skipped[1].ID)
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

func TestAdminService_BatchUpdateUserBalance_InvalidOperation(t *testing.T) {
	svc := &adminServiceImpl{userRepo: &userRepoStub{}}
	_, err := svc.BatchUpdateUserBalance(context.Background(), []int64{1}, 5, "multiply", "")
	require.Error(t, err)
}

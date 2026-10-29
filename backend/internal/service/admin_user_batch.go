package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// batchBalanceStepTimeout bounds one user's adjustment inside a batch.
const batchBalanceStepTimeout = 30 * time.Second

// BatchNotProcessedReasonPrefix starts the Skipped reason of users a batch did
// not reach before its deadline, so a follow-up can target exactly them.
const BatchNotProcessedReasonPrefix = "not processed: "

// batchSkipReason is the Skipped reason shown to the admin. ApplicationError's
// Error() is a debug format (code=… reason=… metadata=…), and ErrUserNotFound
// as well as the admin guards in DeleteUser are ApplicationErrors, so their
// message is used instead. infraerrors.Message is not enough on its own: it
// turns a plain error into the generic unknown-error message.
func batchSkipReason(err error) string {
	var appErr *infraerrors.ApplicationError
	if errors.As(err, &appErr) && appErr.Message != "" {
		return appErr.Message
	}
	return err.Error()
}

// BatchUserOperationSkipped 记录批量用户操作中被跳过的单个用户及原因。
type BatchUserOperationSkipped struct {
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
}

// BatchUserOperationResult 汇总一次批量用户操作的结果。
type BatchUserOperationResult struct {
	Affected   int                         `json:"affected"`
	SuccessIDs []int64                     `json:"success_ids"`
	Skipped    []BatchUserOperationSkipped `json:"skipped"`
}

// dedupeUserIDs 过滤非法 ID 并去重，保持首次出现的顺序。
func dedupeUserIDs(userIDs []int64) []int64 {
	cleaned := make([]int64, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID <= 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		cleaned = append(cleaned, userID)
	}
	return cleaned
}

// BatchUpdateUserStatus 批量启用/禁用用户。管理员账号不允许被禁用，会记入 Skipped。
// 逐个复用单用户更新路径，保证与单用户操作相同的守卫与缓存失效语义。
func (s *adminServiceImpl) BatchUpdateUserStatus(ctx context.Context, userIDs []int64, status string) (*BatchUserOperationResult, error) {
	if status != StatusActive && status != StatusDisabled {
		return nil, fmt.Errorf("invalid status: %q", status)
	}

	result := &BatchUserOperationResult{}
	for _, userID := range dedupeUserIDs(userIDs) {
		user, err := s.userRepo.GetByID(ctx, userID)
		if err != nil {
			result.Skipped = append(result.Skipped, BatchUserOperationSkipped{ID: userID, Reason: batchSkipReason(err)})
			continue
		}
		if user.Role == RoleAdmin && status == StatusDisabled {
			result.Skipped = append(result.Skipped, BatchUserOperationSkipped{ID: userID, Reason: "cannot disable admin user"})
			continue
		}
		if user.Status != status {
			user.Status = status
			if err := s.userRepo.Update(ctx, user, UserUpdateFields{Status: true}); err != nil {
				result.Skipped = append(result.Skipped, BatchUserOperationSkipped{ID: userID, Reason: batchSkipReason(err)})
				continue
			}
			// 状态参与网关鉴权快照，必须失效缓存，否则封禁在 L2 TTL 内不生效。
			if s.authCacheInvalidator != nil {
				s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
			}
		}
		result.Affected++
		result.SuccessIDs = append(result.SuccessIDs, userID)
	}
	return result, nil
}

// BatchDeleteUsers 批量删除用户。复用单用户删除路径（含 API Key 级联与事务），
// 管理员账号等删除失败的用户记入 Skipped。
func (s *adminServiceImpl) BatchDeleteUsers(ctx context.Context, userIDs []int64) (*BatchUserOperationResult, error) {
	result := &BatchUserOperationResult{}
	for _, userID := range dedupeUserIDs(userIDs) {
		if err := s.DeleteUser(ctx, userID); err != nil {
			result.Skipped = append(result.Skipped, BatchUserOperationSkipped{ID: userID, Reason: batchSkipReason(err)})
			continue
		}
		result.Affected++
		result.SuccessIDs = append(result.SuccessIDs, userID)
	}
	return result, nil
}

// BatchUpdateUserBalance 批量调整用户余额。operation 支持 set/add/subtract，
// 逐个复用单用户原子余额路径（含调整流水与缓存失效），失败的用户记入 Skipped。
//
// Skipped 只能表示「没有入账」：界面会把 Skipped 的用户重新选中供重试，已入账却被
// 报成 Skipped 的用户重试时会被重复入账。因此：
//   - ctx 的截止时间只在用户之间生效：每个用户的调整在脱离 ctx 取消的独立上下文里
//     跑完，截止后尚未处理的用户以 BatchNotProcessedReasonPrefix 开头的原因记入
//     Skipped，明确表示「未执行」；
//   - 提交之后不再回读用户（applyUserBalanceChange）：回读失败曾让已提交的调整被报
//     成失败。
func (s *adminServiceImpl) BatchUpdateUserBalance(ctx context.Context, userIDs []int64, balance float64, operation string, notes string) (*BatchUserOperationResult, error) {
	switch operation {
	case "set", "add", "subtract":
	default:
		return nil, fmt.Errorf("unsupported balance operation: %q", operation)
	}

	result := &BatchUserOperationResult{}
	for _, userID := range dedupeUserIDs(userIDs) {
		if err := ctx.Err(); err != nil {
			result.Skipped = append(result.Skipped, BatchUserOperationSkipped{ID: userID, Reason: BatchNotProcessedReasonPrefix + err.Error()})
			continue
		}
		stepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), batchBalanceStepTimeout)
		// Not UpdateUserBalance: its read-back can fail after the commit, and a
		// committed credit reported as skipped is retried and credited twice.
		err := s.applyUserBalanceChange(stepCtx, userID, balance, operation, notes)
		cancel()
		if err != nil {
			result.Skipped = append(result.Skipped, BatchUserOperationSkipped{ID: userID, Reason: batchSkipReason(err)})
			continue
		}
		result.Affected++
		result.SuccessIDs = append(result.SuccessIDs, userID)
	}
	return result, nil
}

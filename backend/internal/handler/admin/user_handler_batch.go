package admin

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// maxBatchUserIDs 与 batch-limits 保持一致的单次批量上限。
const maxBatchUserIDs = 500

// BatchUpdateStatusRequest 批量启用/禁用用户请求。
type BatchUpdateStatusRequest struct {
	UserIDs []int64 `json:"user_ids" binding:"required,min=1"`
	Status  string  `json:"status" binding:"required,oneof=active disabled"`
}

// BatchUpdateStatus 批量启用/禁用用户
// POST /api/v1/admin/users/batch-status
func (h *UserHandler) BatchUpdateStatus(c *gin.Context) {
	var req BatchUpdateStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if len(req.UserIDs) > maxBatchUserIDs {
		response.BadRequest(c, "user_ids cannot exceed 500")
		return
	}

	result, err := h.adminService.BatchUpdateUserStatus(c.Request.Context(), req.UserIDs, req.Status)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// BatchDeleteUsersRequest 批量删除用户请求。
type BatchDeleteUsersRequest struct {
	UserIDs []int64 `json:"user_ids" binding:"required,min=1"`
}

// BatchDelete 批量删除用户
// POST /api/v1/admin/users/batch-delete
func (h *UserHandler) BatchDelete(c *gin.Context) {
	var req BatchDeleteUsersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if len(req.UserIDs) > maxBatchUserIDs {
		response.BadRequest(c, "user_ids cannot exceed 500")
		return
	}

	result, err := h.adminService.BatchDeleteUsers(c.Request.Context(), req.UserIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// BatchUpdateBalanceRequest 批量调整用户余额请求。
// operation=set 时 balance 允许为 0（余额清零）；add/subtract 时 balance 必须 > 0。
type BatchUpdateBalanceRequest struct {
	UserIDs   []int64 `json:"user_ids"`
	All       bool    `json:"all"`
	Balance   float64 `json:"balance"`
	Operation string  `json:"operation" binding:"required,oneof=set add subtract"`
	Notes     string  `json:"notes"`
}

// BatchUpdateBalance 批量调整用户余额（支持设置为指定值）
// POST /api/v1/admin/users/batch-balance
func (h *UserHandler) BatchUpdateBalance(c *gin.Context) {
	var req BatchUpdateBalanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if !req.All && len(req.UserIDs) == 0 {
		response.BadRequest(c, "user_ids is required unless all=true")
		return
	}
	if !req.All && len(req.UserIDs) > maxBatchUserIDs {
		response.BadRequest(c, "user_ids cannot exceed 500")
		return
	}
	if req.Operation == "set" {
		if req.Balance < 0 {
			response.BadRequest(c, "balance cannot be negative when operation is set")
			return
		}
	} else if req.Balance <= 0 {
		response.BadRequest(c, "balance must be greater than 0")
		return
	}

	userIDs := req.UserIDs
	if req.All {
		userIDs = nil
		page := 1
		const pageSize = 500
		for {
			users, _, err := h.adminService.ListUsers(c.Request.Context(), page, pageSize, service.UserListFilters{}, "id", "asc")
			if err != nil {
				response.ErrorFrom(c, err)
				return
			}
			for _, user := range users {
				userIDs = append(userIDs, user.ID)
			}
			if len(users) < pageSize {
				break
			}
			page++
		}
	}

	if len(userIDs) == 0 {
		response.Success(c, &service.BatchUserOperationResult{})
		return
	}

	idempotencyPayload := struct {
		UserIDs []int64                   `json:"user_ids"`
		Body    BatchUpdateBalanceRequest `json:"body"`
	}{
		UserIDs: userIDs,
		Body:    req,
	}
	executeAdminIdempotentJSON(c, "admin.users.balance.batch_update", idempotencyPayload, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.adminService.BatchUpdateUserBalance(ctx, userIDs, req.Balance, req.Operation, req.Notes)
	})
}

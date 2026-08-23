package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// ---- stubAdminService 对新批量接口的基础实现（保持接口满足） ----

func (s *stubAdminService) BatchUpdateUserStatus(_ context.Context, userIDs []int64, _ string) (*service.BatchUserOperationResult, error) {
	return &service.BatchUserOperationResult{Affected: len(userIDs), SuccessIDs: userIDs}, nil
}

func (s *stubAdminService) BatchDeleteUsers(_ context.Context, userIDs []int64) (*service.BatchUserOperationResult, error) {
	return &service.BatchUserOperationResult{Affected: len(userIDs), SuccessIDs: userIDs}, nil
}

func (s *stubAdminService) BatchUpdateUserBalance(_ context.Context, userIDs []int64, _ float64, _ string, _ string) (*service.BatchUserOperationResult, error) {
	return &service.BatchUserOperationResult{Affected: len(userIDs), SuccessIDs: userIDs}, nil
}

// ---- 测试专用 stub：记录调用参数 ----

type batchUserAdminServiceStub struct {
	*stubAdminService
	statusCalls []struct {
		userIDs []int64
		status  string
	}
	deleteCalls  [][]int64
	balanceCalls []struct {
		userIDs   []int64
		balance   float64
		operation string
		notes     string
	}
}

func (s *batchUserAdminServiceStub) BatchUpdateUserStatus(_ context.Context, userIDs []int64, status string) (*service.BatchUserOperationResult, error) {
	s.statusCalls = append(s.statusCalls, struct {
		userIDs []int64
		status  string
	}{append([]int64(nil), userIDs...), status})
	return &service.BatchUserOperationResult{Affected: len(userIDs), SuccessIDs: userIDs}, nil
}

func (s *batchUserAdminServiceStub) BatchDeleteUsers(_ context.Context, userIDs []int64) (*service.BatchUserOperationResult, error) {
	s.deleteCalls = append(s.deleteCalls, append([]int64(nil), userIDs...))
	return &service.BatchUserOperationResult{Affected: len(userIDs), SuccessIDs: userIDs}, nil
}

func (s *batchUserAdminServiceStub) BatchUpdateUserBalance(_ context.Context, userIDs []int64, balance float64, operation string, notes string) (*service.BatchUserOperationResult, error) {
	s.balanceCalls = append(s.balanceCalls, struct {
		userIDs   []int64
		balance   float64
		operation string
		notes     string
	}{append([]int64(nil), userIDs...), balance, operation, notes})
	return &service.BatchUserOperationResult{Affected: len(userIDs), SuccessIDs: userIDs}, nil
}

func setupBatchUserRouter(serviceStub service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewUserHandler(serviceStub, nil, nil, nil, nil, nil, nil)
	router.POST("/api/v1/admin/users/batch-status", handler.BatchUpdateStatus)
	router.POST("/api/v1/admin/users/batch-delete", handler.BatchDelete)
	router.POST("/api/v1/admin/users/batch-balance", handler.BatchUpdateBalance)
	return router
}

func postBatchUser(t *testing.T, router *gin.Engine, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestUserHandlerBatchUpdateStatus(t *testing.T) {
	serviceStub := &batchUserAdminServiceStub{stubAdminService: newStubAdminService()}
	router := setupBatchUserRouter(serviceStub)

	recorder := postBatchUser(t, router, "/api/v1/admin/users/batch-status", []byte(`{"user_ids":[1,2,3],"status":"disabled"}`))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, serviceStub.statusCalls, 1)
	require.Equal(t, []int64{1, 2, 3}, serviceStub.statusCalls[0].userIDs)
	require.Equal(t, "disabled", serviceStub.statusCalls[0].status)

	var response struct {
		Data service.BatchUserOperationResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, 3, response.Data.Affected)
}

func TestUserHandlerBatchUpdateStatusRejectsInvalidRequests(t *testing.T) {
	tooManyIDs := make([]int64, 501)
	for index := range tooManyIDs {
		tooManyIDs[index] = int64(index + 1)
	}
	tooManyBody, err := json.Marshal(map[string]any{"user_ids": tooManyIDs, "status": "disabled"})
	require.NoError(t, err)

	tests := []struct {
		name string
		body []byte
	}{
		{name: "missing user ids", body: []byte(`{"status":"disabled"}`)},
		{name: "empty user ids", body: []byte(`{"user_ids":[],"status":"disabled"}`)},
		{name: "invalid status", body: []byte(`{"user_ids":[1],"status":"banned"}`)},
		{name: "more than 500 ids", body: tooManyBody},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serviceStub := &batchUserAdminServiceStub{stubAdminService: newStubAdminService()}
			recorder := postBatchUser(t, setupBatchUserRouter(serviceStub), "/api/v1/admin/users/batch-status", test.body)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Empty(t, serviceStub.statusCalls)
		})
	}
}

func TestUserHandlerBatchDelete(t *testing.T) {
	serviceStub := &batchUserAdminServiceStub{stubAdminService: newStubAdminService()}
	router := setupBatchUserRouter(serviceStub)

	recorder := postBatchUser(t, router, "/api/v1/admin/users/batch-delete", []byte(`{"user_ids":[4,5]}`))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, serviceStub.deleteCalls, 1)
	require.Equal(t, []int64{4, 5}, serviceStub.deleteCalls[0])
}

func TestUserHandlerBatchDeleteRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "missing user ids", body: []byte(`{}`)},
		{name: "empty user ids", body: []byte(`{"user_ids":[]}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serviceStub := &batchUserAdminServiceStub{stubAdminService: newStubAdminService()}
			recorder := postBatchUser(t, setupBatchUserRouter(serviceStub), "/api/v1/admin/users/batch-delete", test.body)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Empty(t, serviceStub.deleteCalls)
		})
	}
}

func TestUserHandlerBatchUpdateBalanceSetToValue(t *testing.T) {
	serviceStub := &batchUserAdminServiceStub{stubAdminService: newStubAdminService()}
	router := setupBatchUserRouter(serviceStub)

	// set 允许 0：批量清零余额
	recorder := postBatchUser(t, router, "/api/v1/admin/users/batch-balance", []byte(`{"user_ids":[1,2],"balance":0,"operation":"set","notes":"reset"}`))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, serviceStub.balanceCalls, 1)
	require.Equal(t, []int64{1, 2}, serviceStub.balanceCalls[0].userIDs)
	require.Equal(t, 0.0, serviceStub.balanceCalls[0].balance)
	require.Equal(t, "set", serviceStub.balanceCalls[0].operation)
	require.Equal(t, "reset", serviceStub.balanceCalls[0].notes)
}

func TestUserHandlerBatchUpdateBalanceRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "missing user ids", body: []byte(`{"balance":10,"operation":"set"}`)},
		{name: "negative set", body: []byte(`{"user_ids":[1],"balance":-1,"operation":"set"}`)},
		{name: "zero add", body: []byte(`{"user_ids":[1],"balance":0,"operation":"add"}`)},
		{name: "invalid operation", body: []byte(`{"user_ids":[1],"balance":10,"operation":"multiply"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serviceStub := &batchUserAdminServiceStub{stubAdminService: newStubAdminService()}
			recorder := postBatchUser(t, setupBatchUserRouter(serviceStub), "/api/v1/admin/users/batch-balance", test.body)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Empty(t, serviceStub.balanceCalls)
		})
	}
}

func TestUserHandlerBatchUpdateBalanceAllUsesEveryListedUser(t *testing.T) {
	base := newStubAdminService()
	base.users = []service.User{{ID: 21}, {ID: 22}}
	serviceStub := &batchUserAdminServiceStub{stubAdminService: base}
	recorder := postBatchUser(
		t,
		setupBatchUserRouter(serviceStub),
		"/api/v1/admin/users/batch-balance",
		[]byte(`{"all":true,"balance":5,"operation":"set"}`),
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, serviceStub.balanceCalls, 1)
	require.Equal(t, []int64{21, 22}, serviceStub.balanceCalls[0].userIDs)
}

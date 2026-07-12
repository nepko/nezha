package controller

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/service/singleton"
)

// setupApprovalPolicyTest 提供独立的 :memory: SQLite，并替换全局 singleton.ServerShared
// 为受控 stub（仅含一个离线服务器），隔离测试间全局状态污染：
//   - 单连接限制消除 SQLite 内存库多连接导致的 "no such table" 偶发失败；
//   - 离线服务器使 dispatchCommandToServers 走"服务器离线"分支（continue），
//     避免依赖完整 PAT 上下文（生产审批路由由 apiToken/jwt 中间件填充，HasPermission
//     不会缺 key），专注验证审批通道的命令策略拦截语义。
func setupApprovalPolicyTest(t *testing.T) (cleanup func()) {
	t.Helper()
	origDB := singleton.DB
	origConf := singleton.Conf
	origServer := singleton.ServerShared

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	var sqlDB *sql.DB
	sqlDB, err = db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.CommandPolicy{}, &model.CommandApproval{}, &model.AuditLog{},
	))
	singleton.DB = db
	singleton.Conf = &singleton.ConfigClass{Config: &model.Config{}}
	invalidateEnabledPolicyCache()

	sc := singleton.NewEmptyServerClassForTest()
	srv := &model.Server{}
	srv.ID = 7
	srv.SetUserID(1)
	sc.InsertForTest(srv)
	singleton.ServerShared = sc

	return func() {
		// WriteAuditLog 内部以异步 goroutine 落库，等待其完成再恢复全局
		// singleton.DB，避免异步写操作在 cleanup 把 DB 置回初始（可能 nil）后崩溃。
		time.Sleep(50 * time.Millisecond)
		singleton.DB = origDB
		singleton.Conf = origConf
		singleton.ServerShared = origServer
	}
}

func ctxForApprover(userID uint64, role model.Role) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/command-approval/1/approve", nil)
	if userID != 0 {
		c.Set(model.CtxKeyAuthorizedUser, &model.User{Common: model.Common{ID: userID}, Role: role})
		c.Set("user_id", uint(userID))
	}
	return c
}

// TestApproveCommandApproval_BlockedByPolicy_NotDispatched 回归守卫：
// 命令策略硬拒（黑名单命中且 RequireApproval=false）的命令，即便已创建审批单、
// 管理员点击"通过"，也必须被 fail-closed 拦截，不得经审批通道绕过命令策略下发；
// 且审批单状态必须保持 pending（不应被标记为已通过）。
func TestApproveCommandApproval_BlockedByPolicy_NotDispatched(t *testing.T) {
	cleanup := setupApprovalPolicyTest(t)
	defer cleanup()

	require.NoError(t, singleton.DB.Create(&model.CommandPolicy{
		Name:            "blk-hard",
		Type:            model.CommandPolicyBlacklist,
		CommandsRaw:     `["rm -rf"]`,
		RequireApproval: false,
		Enabled:         true,
	}).Error)
	invalidateEnabledPolicyCache()

	ids, _ := json.Marshal([]uint64{7})
	approval := model.CommandApproval{
		Command:   "rm -rf /",
		ServerIDs: string(ids),
		Status:    model.CommandApprovalPending,
		UserID:    10,
		Username:  "alice",
	}
	require.NoError(t, singleton.DB.Create(&approval).Error)

	c := ctxForApprover(1, model.RoleAdmin)
	c.AddParam("id", strconv.FormatUint(approval.ID, 10))
	_, err := approveCommandApproval(c)
	require.Error(t, err, "黑名单硬拒命令的审批必须被安全策略 fail-closed 拦截")
	assert.Contains(t, err.Error(), "被安全策略拦截", "错误应明确指出被策略拦截")

	var after model.CommandApproval
	require.NoError(t, singleton.DB.First(&after, approval.ID).Error)
	assert.EqualValues(t, model.CommandApprovalPending, after.Status,
		"被策略拦截的审批单必须保持 pending，绝不能标记为已通过")
	assert.Equal(t, uint64(0), after.Approver, "被拦截的审批单不应记录审批人")
}

// TestApproveCommandApproval_AllowedCommand_NotBlocked 反例守卫：
// 未命中黑名单的命令审批不应被策略误拦，应正常走到下发（本测试中服务器为离线态，
// 仅影响分发结果，不影响"策略未误拦"这一断言），且审批单标记为已通过。
func TestApproveCommandApproval_AllowedCommand_NotBlocked(t *testing.T) {
	cleanup := setupApprovalPolicyTest(t)
	defer cleanup()

	require.NoError(t, singleton.DB.Create(&model.CommandPolicy{
		Name:            "blk-hard",
		Type:            model.CommandPolicyBlacklist,
		CommandsRaw:     `["rm -rf"]`,
		RequireApproval: false,
		Enabled:         true,
	}).Error)
	invalidateEnabledPolicyCache()

	ids, _ := json.Marshal([]uint64{7})
	approval := model.CommandApproval{
		Command:   "ls -la",
		ServerIDs: string(ids),
		Status:    model.CommandApprovalPending,
		UserID:    10,
		Username:  "alice",
	}
	require.NoError(t, singleton.DB.Create(&approval).Error)

	c := ctxForApprover(1, model.RoleAdmin)
	c.AddParam("id", strconv.FormatUint(approval.ID, 10))
	_, err := approveCommandApproval(c)
	require.NoError(t, err, "未命中策略的命令审批不应被安全策略误拦")

	var after model.CommandApproval
	require.NoError(t, singleton.DB.First(&after, approval.ID).Error)
	assert.EqualValues(t, model.CommandApprovalApproved, after.Status,
		"非 blocked 命令审批应正常标记为已通过")
	assert.Equal(t, uint64(1), after.Approver, "审批人应被正确记录")
}

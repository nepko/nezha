package controller

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/service/singleton"
)

// TestAIExecuteCommand_RespectsPATServerAllowlist 回归测试：AI execute_command
// 工具必须像 MCP server.exec 一样尊重 PAT 的 server_ids 白名单。一个被限定
// admin:* 作用域、但 server_ids=[7] 的受限 PAT，经 AI 工具对 allowlist 外
// 服务器(9, 在线) 下发命令必须被拒；对 allowlist 内服务器(7, 在线) 必须放行
// 并真实下发。此前该工具只检查"服务器是否在线"而漏掉 PAT 服务器作用域校验，
// 导致受限 admin PAT 可借 AI 越权对 allowlist 外服务器执行命令。
func TestAIExecuteCommand_RespectsPATServerAllowlist(t *testing.T) {
	cleanup, uid := setupMCPTest(t)
	defer cleanup()

	// 自建服务器类，注册 7 与 9 两个在线服务器（9 在 PAT 白名单外）。
	sc := singleton.NewEmptyServerClassForTest()
	for _, id := range []uint64{7, 9} {
		s := &model.Server{}
		s.ID = id
		s.Name = fmt.Sprintf("s%d", id)
		s.SetUserID(100)
		sc.InsertForTest(s)
	}
	singleton.ServerShared = sc
	for _, id := range []uint64{7, 9} {
		s, _ := singleton.ServerShared.Get(id)
		s.SetTaskStream(&e2eStream{dispatch: agentSim})
	}

	// admin:* 但仅允许 server_ids=[7] 的受限 PAT。
	tok, _ := mkToken(t, uid, []string{model.ScopeAdminAll}, []uint64{7})

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(model.CtxKeyAuthorizedUser, &model.User{Common: model.Common{ID: uid}, Role: model.RoleMember})
	c.Set(apiTokenCtxKey, tok)
	c.Set(model.CtxKeyAPIToken, tok)

	// 1) allowlist 外在线服务器(9) → 必须被拒（越权缺口闭合）。
	outDenied, err := aiToolExecuteCommand(c, map[string]any{"server_id": 9, "command": "echo hi"})
	require.NoError(t, err)
	require.Contains(t, outDenied, "无法访问服务器 9")
	require.NotContains(t, outDenied, "已发送")

	// 2) 未注册服务器(8) → 同样不可执行（无任意服务器越权）。
	outUnknown, err := aiToolExecuteCommand(c, map[string]any{"server_id": 8, "command": "echo hi"})
	require.NoError(t, err)
	require.Contains(t, outUnknown, "无法访问服务器 8")
	require.NotContains(t, outUnknown, "已发送")

	// 3) allowlist 内在线服务器(7) → 应放行并真实下发。
	outAllowed, err := aiToolExecuteCommand(c, map[string]any{"server_id": 7, "command": "echo hi"})
	require.NoError(t, err)
	require.Contains(t, outAllowed, "已发送")
	require.Contains(t, outAllowed, "7")
}

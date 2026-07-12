package controller

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/service/singleton"
)

// insertTestPolicy 创建一条启用的黑名单命令策略并使其进入 evaluateCommandPolicy
// 的缓存。这里用 UpdateColumn 直接把 commands 列写成合法 JSON 数组，绕过
// model.CommandPolicy.BeforeSave 对 CommandsRaw 的再编码，以稳定验证 server.exec
// 对命令策略的接入（策略匹配本身是 evaluateCommandPolicy 的职责，已被快捷命令/
// 批量命令/AI 助手复用）。
func insertTestPolicy(t *testing.T, name, pattern string, requireApproval bool) {
	t.Helper()
	require.NoError(t, singleton.DB.AutoMigrate(&model.CommandPolicy{}))
	pol := model.CommandPolicy{
		Name:            name,
		Type:            model.CommandPolicyBlacklist,
		CommandsRaw:     "[" + quoteJSONString(pattern) + "]",
		Enabled:         true,
		RequireApproval: requireApproval,
	}
	require.NoError(t, singleton.DB.Create(&pol).Error)
	invalidateEnabledPolicyCache()
	invalidatePolicyRegexCache()
}

func quoteJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func resetPolicyCaches() {
	invalidateEnabledPolicyCache()
	invalidatePolicyRegexCache()
}

// TestServerExec_BlockedByCommandPolicy 锁定：server.exec 必须受命令策略（黑名单）
// 约束，命中即拒绝执行并记录 policy_blocked 审计 outcome。这是修复“MCP 工具绕过
// 命令策略”安全漏洞的核心回归。
func TestServerExec_BlockedByCommandPolicy(t *testing.T) {
	ts, tok, cleanup := setupEndToEnd(t)
	defer cleanup()
	insertTestPolicy(t, "block-rm", "blockme", false)
	defer resetPolicyCaches()

	env := e2eCall(t, ts, tok, "tools/call", "server.exec", map[string]any{
		"server_id": 7, "cmd": "blockme",
	})
	res := env["result"].(map[string]any)
	require.True(t, res["isError"] == true,
		"server.exec must be blocked by command policy, got: %v", res)

	var log model.MCPAuditLog
	require.NoError(t, singleton.DB.Where("tool = ?", "server.exec").
		Order("id desc").First(&log).Error)
	require.Equal(t, model.MCPOutcomePolicyBlocked, log.Outcome,
		"policy-blocked server.exec must be audited with outcome=policy_blocked")
}

// TestServerExec_RequiresApprovalByCommandPolicy 锁定：命中“需审批”策略的命令，
// 因 server.exec 是同步、程序化调用（LLM/脚本）无法等待人工审批，必须拒绝执行
// 并记录 policy_approval_required 审计 outcome，而非悄悄放行。
func TestServerExec_RequiresApprovalByCommandPolicy(t *testing.T) {
	ts, tok, cleanup := setupEndToEnd(t)
	defer cleanup()
	insertTestPolicy(t, "need-approval", "approveme", true)
	defer resetPolicyCaches()

	env := e2eCall(t, ts, tok, "tools/call", "server.exec", map[string]any{
		"server_id": 7, "cmd": "approveme",
	})
	res := env["result"].(map[string]any)
	require.True(t, res["isError"] == true,
		"server.exec requiring approval must be rejected (synchronous, no human approval), got: %v", res)

	var log model.MCPAuditLog
	require.NoError(t, singleton.DB.Where("tool = ?", "server.exec").
		Order("id desc").First(&log).Error)
	require.Equal(t, model.MCPOutcomePolicyApprovalRequired, log.Outcome,
		"approval-required server.exec must be audited with outcome=policy_approval_required")
}

// TestServerExec_AllowedWhenPolicyDoesNotMatch 保证：策略存在但不命中时，server.exec
// 仍正常执行（不误伤合法命令）——修复只拦截应拦截的，不扩大拒绝面。
func TestServerExec_AllowedWhenPolicyDoesNotMatch(t *testing.T) {
	ts, tok, cleanup := setupEndToEnd(t)
	defer cleanup()
	insertTestPolicy(t, "block-rm", "blockme", false)
	defer resetPolicyCaches()

	env := e2eCall(t, ts, tok, "tools/call", "server.exec", map[string]any{
		"server_id": 7, "cmd": "echo hello",
	})
	res := env["result"].(map[string]any)
	require.False(t, res["isError"] == true,
		"server.exec must run when no policy matches, got: %v", res)
	require.Contains(t, res["content"].([]any)[0].(map[string]any)["text"], "simulated",
		"policy-missed server.exec must still return agent stdout")
}

package controller

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	pb "github.com/nezhahq/nezha/proto"
	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/service/singleton"
)

// CommandPolicyDecision 命令策略评估结果
type CommandPolicyDecision struct {
	Allow         bool
	Blocked       bool
	NeedsApproval bool
	Reason        string
	PolicyName    string
}

// policyRegexCache 缓存命令策略正则编译结果，避免每次评估全量重编译。
// 编译失败的正则也会缓存其错误，避免对同一坏正则反复编译。
// 注：策略正则由管理员在后台维护，变更频率极低；如需精确失效可在
// 策略增删改路由中调用 policyRegexCache.Delete(pat) 清空对应条目。
var policyRegexCache sync.Map

type cachedRegex struct {
	re  *regexp.Regexp
	err error
}

func getCompiledRegex(pat string) (*regexp.Regexp, error) {
	if v, ok := policyRegexCache.Load(pat); ok {
		c := v.(cachedRegex)
		return c.re, c.err
	}
	re, err := regexp.Compile(pat)
	policyRegexCache.Store(pat, cachedRegex{re: re, err: err})
	return re, err
}

// invalidatePolicyRegexCache 清空已编译正则缓存，使管理员对策略正则的增删改
// 在下次评估时立即生效，无需重启进程。策略由管理员低频维护，全量清除即可，
// 不必解析旧 pattern 精确删除单条（避免漏清导致旧编译结果残留）。
func invalidatePolicyRegexCache() {
	policyRegexCache.Range(func(k, _ any) bool {
		policyRegexCache.Delete(k)
		return true
	})
}

// enabledPolicyCache 缓存「当前启用的命令策略」列表，避免每次评估都全表查询
// DB。与 policyRegexCache 同源失效：任意策略增删改后整体重建。
var enabledPolicyCache atomic.Value // []model.CommandPolicy

// loadEnabledPolicies 返回当前启用的策略列表，优先命中内存缓存。
func loadEnabledPolicies() ([]model.CommandPolicy, error) {
	if cached, ok := enabledPolicyCache.Load().([]model.CommandPolicy); ok && cached != nil {
		return cached, nil
	}
	var policies []model.CommandPolicy
	if err := singleton.DB.Where("enabled = ?", true).Find(&policies).Error; err != nil {
		return nil, err
	}
	for i := range policies {
		_ = policies[i].AfterFind(singleton.DB)
	}
	enabledPolicyCache.Store(policies)
	return policies, nil
}

// invalidateEnabledPolicyCache 在策略变更后清空列表缓存。
func invalidateEnabledPolicyCache() {
	enabledPolicyCache.Store([]model.CommandPolicy(nil))
}

// parsePolicyPatterns 把 commands 列（合法 JSON 数组文本）解析为策略正则模式列表。
// 兼容历史双重编码数据：早期 model.CommandPolicy.BeforeSave 把 CommandsRaw 又
// json.Marshal 一次，导致 commands 列变成被引号包裹两次的字符串，evaluateCommandPolicy
// 直接 json.Unmarshal 进 []string 会永远失败、命令策略整条链路失效。此处先尝试直接
// 解析为数组，失败再尝试把它当作一层 JSON 字符串解包后解析，从而兼容存量数据。
func parsePolicyPatterns(encoded string) ([]string, error) {
	var patterns []string
	if err := json.Unmarshal([]byte(encoded), &patterns); err == nil {
		return patterns, nil
	}
	var inner string
	if err := json.Unmarshal([]byte(encoded), &inner); err == nil {
		if err2 := json.Unmarshal([]byte(inner), &patterns); err2 == nil {
			return patterns, nil
		}
	}
	return nil, fmt.Errorf("invalid command policy patterns: %s", encoded)
}

// evaluateCommandPolicy 评估命令是否通过已启用的策略：
//   - 黑名单命中且需审批 → NeedsApproval
//   - 黑名单命中且无需审批 → Blocked
//   - 白名单启用且未命中任何模式 → Blocked
func evaluateCommandPolicy(command string) (CommandPolicyDecision, error) {
	// 二开：命令审批总开关（默认关闭）。关闭时即使存在 RequireApproval 策略也直接放行，
	// 彻底避免个人/单人场景下的审批摩擦；开启后才按策略执行审批闸。
	if !singleton.Conf.EnableCommandApproval {
		return CommandPolicyDecision{Allow: true}, nil
	}
	policies, err := loadEnabledPolicies()
	if err != nil {
		return CommandPolicyDecision{}, err
	}

	decision := CommandPolicyDecision{Allow: true}
	for _, p := range policies {
		patterns, perr := parsePolicyPatterns(p.Commands)
		if perr != nil || len(patterns) == 0 {
			continue
		}
		matched := false
		for _, pat := range patterns {
			re, err := getCompiledRegex(pat)
			if err != nil {
				continue
			}
			if re.MatchString(command) {
				matched = true
				break
			}
		}

		if p.Type == model.CommandPolicyBlacklist {
			if matched {
				decision.Allow = false
				decision.PolicyName = p.Name
				if p.RequireApproval {
					decision.NeedsApproval = true
					decision.Reason = "命中黑名单策略，需审批: " + p.Name
				} else {
					decision.Blocked = true
					decision.Reason = "命中黑名单策略，已被拒绝: " + p.Name
				}
				return decision, nil
			}
		} else if p.Type == model.CommandPolicyWhitelist {
			if !matched {
				decision.Allow = false
				decision.Blocked = true
				decision.PolicyName = p.Name
				decision.Reason = singleton.Localizer.T("command policy whitelist blocked") + ": " + p.Name
				return decision, nil
			}
		}
	}
	return decision, nil
}

func createCommandApproval(command string, userID uint64, username string, serverIDs []uint64) (*model.CommandApproval, error) {
	idsJSON, _ := json.Marshal(serverIDs)
	approval := model.CommandApproval{
		Command:   command,
		UserID:    userID,
		Username:  username,
		ServerIDs: string(idsJSON),
		Status:    model.CommandApprovalPending,
	}
	if err := singleton.DB.Create(&approval).Error; err != nil {
		return nil, err
	}
	return &approval, nil
}

// toUint64IDs 将 []uint 转为 []uint64（兼容前端/请求中的 uint ID）。
func toUint64IDs(in []uint) []uint64 {
	out := make([]uint64, len(in))
	for i, v := range in {
		out[i] = uint64(v)
	}
	return out
}

// dispatchCommandToServers 向指定服务器下发命令（审批通过后调用）。
// 安全闸：与 executeBatchCommand / MCP server.exec / AI execute_command 一致，
// 下发前必须逐服务器经 server.HasPermission(c) 授权，尊重受限 PAT 的 server_ids
// 白名单（与 R22 的授权一致性要求对齐）。命令级策略硬闸由 approveCommandApproval
// 在调用本函数前统一评估，避免审批通道成为绕过命令策略的旁路。
func dispatchCommandToServers(c *gin.Context, command string, serverIDs []uint64) ([]*model.BatchCommandResult, error) {
	serverMap := singleton.ServerShared.GetList()
	results := make([]*model.BatchCommandResult, 0)
	for _, id := range serverIDs {
		server, ok := serverMap[id]
		result := &model.BatchCommandResult{ServerID: id}
		if !ok || server.GetTaskStream() == nil {
			result.Error = "服务器离线"
			results = append(results, result)
			continue
		}
		// 服务器访问授权：受限 PAT 即使持有 admin:* 作用域，也只能在
		// 其 server_ids 白名单内服务器下发，与 AI/PAT 命令下发路径一致。
		if !server.HasPermission(c) {
			result.Error = "无权限访问该服务器"
			results = append(results, result)
			continue
		}
		taskData, _ := json.Marshal(command)
		if err := server.SendTask(&pb.Task{Type: model.TaskTypeCommand, Data: string(taskData)}); err != nil {
			result.Error = err.Error()
		} else {
			result.Success = true
			result.Output = "命令已下发"
		}
		results = append(results, result)
	}
	return results, nil
}

// ListCommandApprovals 列出命令审批单
// @Summary List command approvals
// @Security BearerAuth
// @Tags admin required
// @Produce json
// @Success 200 {object} model.CommonResponse[[]model.CommandApproval]
// @Router /command-approval [get]
func listCommandApprovals(c *gin.Context) ([]model.CommandApproval, error) {
	var approvals []model.CommandApproval
	if err := singleton.DB.Order("status asc, created_at desc").Find(&approvals).Error; err != nil {
		return nil, err
	}
	return approvals, nil
}

// ApproveCommandApproval 通过审批并下发命令
// @Summary Approve command approval
// @Security BearerAuth
// @Tags admin required
// @Produce json
// @Success 200 {object} model.CommonResponse[[]*model.BatchCommandResult]
// @Router /command-approval/:id/approve [post]
func approveCommandApproval(c *gin.Context) ([]*model.BatchCommandResult, error) {
	id := c.Param("id")
	var approval model.CommandApproval
	if err := singleton.DB.First(&approval, id).Error; err != nil {
		return nil, err
	}
	if approval.Status != model.CommandApprovalPending {
		return nil, newGormError("approval already handled")
	}

	var serverIDs []uint64
	if err := json.Unmarshal([]byte(approval.ServerIDs), &serverIDs); err != nil {
		return nil, err
	}

	// 命令策略硬闸：审批通过不代表可凌驾于安全策略之上。命中黑名单（硬拒）或
	// 白名单未命中等 Blocked 判定必须 fail-closed 拦截，防止"审批通道"成为绕过
	// 命令策略的旁路。NeedsApproval 不在拦截之列——审批单正是为这类命令建立，
	// 二次拦截会形成死循环。拦截时审批单保持 pending，不标记已通过。
	if decision, err := evaluateCommandPolicy(approval.Command); err == nil && decision.Blocked {
		singleton.WriteAuditLog(c, model.AuditActionConfigUpdate, "command_approval", approval.ID,
			"blocked by policy, not dispatched: "+approval.Command, false)
		return nil, fmt.Errorf("命令被安全策略拦截，未执行: %s", decision.Reason)
	}

	approval.Status = model.CommandApprovalApproved
	approval.Approver = uint64(c.GetUint("user_id"))
	if err := singleton.DB.Save(&approval).Error; err != nil {
		return nil, err
	}

	results, err := dispatchCommandToServers(c, approval.Command, serverIDs)
	if err != nil {
		return nil, err
	}
	singleton.WriteAuditLog(c, model.AuditActionConfigUpdate, "command_approval", approval.ID, "approved: "+approval.Command, true)
	return results, nil
}

// RejectCommandApproval 拒绝审批
// @Summary Reject command approval
// @Security BearerAuth
// @Tags admin required
// @Accept json
// @Param body body map[string]string true "reason"
// @Produce json
// @Success 200 {object} model.CommonResponse[any]
// @Router /command-approval/:id/reject [post]
func rejectCommandApproval(c *gin.Context) (any, error) {
	id := c.Param("id")
	var body struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&body)

	var approval model.CommandApproval
	if err := singleton.DB.First(&approval, id).Error; err != nil {
		return nil, err
	}
	if approval.Status != model.CommandApprovalPending {
		return nil, newGormError("approval already handled")
	}
	approval.Status = model.CommandApprovalRejected
	approval.Approver = uint64(c.GetUint("user_id"))
	approval.Reason = body.Reason
	if err := singleton.DB.Save(&approval).Error; err != nil {
		return nil, err
	}
	singleton.WriteAuditLog(c, model.AuditActionConfigUpdate, "command_approval", approval.ID, "rejected: "+approval.Command, true)
	return nil, nil
}

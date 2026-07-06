package controller

import (
	"encoding/json"
	"regexp"
	"sync"

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

// evaluateCommandPolicy 评估命令是否通过已启用的策略：
//   - 黑名单命中且需审批 → NeedsApproval
//   - 黑名单命中且无需审批 → Blocked
//   - 白名单启用且未命中任何模式 → Blocked
func evaluateCommandPolicy(command string) (CommandPolicyDecision, error) {
	var policies []model.CommandPolicy
	if err := singleton.DB.Where("enabled = ?", true).Find(&policies).Error; err != nil {
		return CommandPolicyDecision{}, err
	}
	for i := range policies {
		_ = policies[i].AfterFind(singleton.DB)
	}

	decision := CommandPolicyDecision{Allow: true}
	for _, p := range policies {
		var patterns []string
		if err := json.Unmarshal([]byte(p.Commands), &patterns); err != nil || len(patterns) == 0 {
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

// dispatchCommandToServers 向指定服务器下发命令（审批通过后调用）
func dispatchCommandToServers(command string, serverIDs []uint64) ([]*model.BatchCommandResult, error) {
	serverMap := singleton.ServerShared.GetList()
	results := make([]*model.BatchCommandResult, 0)
	for _, id := range serverIDs {
		server, ok := serverMap[id]
		result := &model.BatchCommandResult{ServerID: id}
		if ok && server.GetTaskStream() != nil {
			taskData, _ := json.Marshal(command)
			if err := server.SendTask(&pb.Task{Type: model.TaskTypeCommand, Data: string(taskData)}); err != nil {
				result.Error = err.Error()
			} else {
				result.Success = true
				result.Output = "命令已下发"
			}
		} else {
			result.Error = "服务器离线"
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

	approval.Status = model.CommandApprovalApproved
	approval.Approver = uint64(c.GetUint("user_id"))
	if err := singleton.DB.Save(&approval).Error; err != nil {
		return nil, err
	}

	results, err := dispatchCommandToServers(approval.Command, serverIDs)
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

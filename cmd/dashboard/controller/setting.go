package controller

import (
	"errors"
	"log"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/service/rpc"
	"github.com/nezhahq/nezha/service/singleton"
)

// List settings
// @Summary List settings
// @Schemes
// @Description List settings
// @Security BearerAuth
// @Tags common
// @Produce json
// @Success 200 {object} model.CommonResponse[model.SettingResponse]
// @Router /setting [get]
func listConfig(c *gin.Context) (*model.SettingResponse, error) {
	u, authorized := c.Get(model.CtxKeyAuthorizedUser)
	var isAdmin bool
	if authorized {
		user := u.(*model.User)
		isAdmin = user.Role.IsAdmin()
	}

	config := *singleton.Conf
	config.Language = strings.ReplaceAll(config.Language, "_", "-")

	conf := model.SettingResponse{
		Config: model.Setting{
			ConfigForGuests:                config.ConfigForGuests,
			ConfigDashboard:                config.ConfigDashboard,
			IgnoredIPNotificationServerIDs: config.IgnoredIPNotificationServerIDs,
			Oauth2Providers:                config.Oauth2Providers,
		},
		Version:           singleton.Version,
		FrontendTemplates: singleton.FrontendTemplates,
		TSDBEnabled:       singleton.TSDBEnabled(),
	}

	if !authorized || !isAdmin {
		configForGuests := config.ConfigForGuests
		var configDashboard model.ConfigDashboard
		if authorized {
			configDashboard.AgentTLS = singleton.Conf.AgentTLS
			configDashboard.InstallHost = singleton.Conf.InstallHost
		}
	conf = model.SettingResponse{
		Config: model.Setting{
			ConfigForGuests: configForGuests,
			ConfigDashboard: configDashboard,
			Oauth2Providers: config.Oauth2Providers,
			TerminalRecordingEnabled:       singleton.Conf.TerminalRecordingEnabled,
			TerminalRecordingRetentionDays: singleton.Conf.TerminalRecordingRetentionDays,
			TerminalIdleTimeoutSeconds:     singleton.Conf.TerminalIdleTimeoutSeconds,
			FMEnhancedEnabled:              singleton.Conf.FMEnhancedEnabled,
			EnableCommandApproval:          singleton.Conf.EnableCommandApproval,
			AIEnabled:                      singleton.Conf.AIEnabled,
			AIBaseURL:                      singleton.Conf.AIBaseURL,
			AIModel:                        singleton.Conf.AIModel,
			AITokenBudgetDaily:             singleton.Conf.AITokenBudgetDaily,
			AIApiKeySet:                    singleton.Conf.AIApiKey != "",
		},
		TSDBEnabled: singleton.TSDBEnabled(),
	}
}

	return &conf, nil
}

// Edit config
// @Summary Edit config
// @Security BearerAuth
// @Schemes
// @Description Edit config
// @Tags admin required
// @Accept json
// @Param body body model.SettingForm true "SettingForm"
// @Produce json
// @Success 200 {object} model.CommonResponse[any]
// @Router /setting [patch]
func updateConfig(c *gin.Context) (any, error) {
	var sf model.SettingForm
	if err := c.ShouldBindJSON(&sf); err != nil {
		return nil, err
	}
	var userTemplateValid bool
	for _, v := range singleton.FrontendTemplates {
		if !userTemplateValid && v.Path == sf.UserTemplate && !v.IsAdmin {
			userTemplateValid = true
		}
		if userTemplateValid {
			break
		}
	}
	if !userTemplateValid {
		return nil, errors.New("invalid user template")
	}

	singleton.Conf.Language = strings.ReplaceAll(sf.Language, "-", "_")

	singleton.Conf.EnableIPChangeNotification = sf.EnableIPChangeNotification
	singleton.Conf.EnablePlainIPInNotification = sf.EnablePlainIPInNotification
	singleton.Conf.Cover = sf.Cover
	singleton.Conf.InstallHost = sf.InstallHost
	singleton.Conf.DashboardHost = sf.DashboardHost
	singleton.Conf.ReservedHosts = sf.ReservedHosts
	singleton.Conf.IgnoredIPNotification = sf.IgnoredIPNotification
	singleton.Conf.IPChangeNotificationGroupID = sf.IPChangeNotificationGroupID
	singleton.Conf.SiteName = sf.SiteName
	singleton.Conf.DNSServers = sf.DNSServers
	singleton.Conf.CustomCode = sf.CustomCode
	singleton.Conf.CustomCodeDashboard = sf.CustomCodeDashboard
	singleton.Conf.WebRealIPHeader = sf.WebRealIPHeader
	singleton.Conf.AgentRealIPHeader = sf.AgentRealIPHeader
	singleton.Conf.AgentTLS = sf.AgentTLS
	singleton.Conf.UserTemplate = sf.UserTemplate
	if sf.TerminalRecordingEnabled != nil {
		singleton.Conf.TerminalRecordingEnabled = *sf.TerminalRecordingEnabled
	}
	if sf.TerminalRecordingRetentionDays != nil {
		singleton.Conf.TerminalRecordingRetentionDays = *sf.TerminalRecordingRetentionDays
	}
	if sf.FMEnhancedEnabled != nil {
		singleton.Conf.FMEnhancedEnabled = *sf.FMEnhancedEnabled
	}
	if sf.TerminalIdleTimeoutSeconds != nil {
		singleton.Conf.TerminalIdleTimeoutSeconds = *sf.TerminalIdleTimeoutSeconds
	}
	// 二开：终端 AI 助手配置落地。ai_api_key 仅在用户显式填写时才覆盖，
	// 留空保持原值（避免一次普通保存把已配置的密钥清空）。
	if sf.AIEnabled != nil {
		singleton.Conf.AIEnabled = *sf.AIEnabled
	}
	if sf.AIBaseURL != "" {
		singleton.Conf.AIBaseURL = sf.AIBaseURL
	}
	if sf.AIApiKey != "" {
		singleton.Conf.AIApiKey = sf.AIApiKey
	}
	if sf.AIModel != "" {
		singleton.Conf.AIModel = sf.AIModel
	}
	if sf.AITemperature != nil {
		singleton.Conf.AITemperature = *sf.AITemperature
	}
	if sf.AIMaxTokens != nil {
		singleton.Conf.AIMaxTokens = *sf.AIMaxTokens
	}
	if sf.AITokenBudgetDaily != nil {
		singleton.Conf.AITokenBudgetDaily = *sf.AITokenBudgetDaily
	}
	mcpWasEnabled := singleton.Conf.MCPEnabled()
	mcpNext := resolveSettingEnableMCP(sf.EnableMCP, mcpWasEnabled)

	if err := applyEnableMCPTransition(
		mcpWasEnabled, mcpNext,
		singleton.Conf.SetMCPEnabled,
		singleton.Conf.Save,
		fireMCPKillSwitch,
	); err != nil {
		return nil, newGormError("%v", err)
	}

	// 二开修复：原先仅 MCP 开关变更会触发 Conf.Save()，其余设置（含终端录制/AI 等
	// 二开字段）改动后仅停留在内存、重启即丢失。这里统一写回 config.yaml。
	if err := singleton.Conf.Save(); err != nil {
		return nil, newGormError("%v", err)
	}

	singleton.OnUpdateLang(singleton.Conf.Language)
	return nil, nil
}

// applyEnableMCPTransition commits the new EnableMCP value and persists it,
// guaranteeing the in-memory flag and the kill-switch cleanup stay consistent
// with what actually reached durable storage:
//   - setVal(next) is applied so Save serialises the new value.
//   - If save fails, the flag is rolled back to prev and no cleanup runs, so a
//     failed disable cannot leave the dashboard half-disabled (new requests
//     rejected while in-flight RPC/streams/URLs are never revoked).
//   - cleanup runs only on a persisted enabled->disabled transition.
func applyEnableMCPTransition(prev, next bool, setVal func(bool), save func() error, cleanup func()) error {
	setVal(next)
	if err := save(); err != nil {
		setVal(prev)
		return err
	}
	if prev && !next {
		cleanup()
	}
	return nil
}

func fireMCPKillSwitch() {
	purgedURLs := PurgeTransferEntries()
	revokedStreams := rpc.NezhaHandlerSingleton.RevokeStreamsForPurpose(rpc.PurposeMCPTransfer)
	cancelledRPC := rpc.CancelAllMCPInflight()
	log.Printf("NEZHA>> MCP kill switch fired: purged=%d urls, revoked=%d streams, cancelled=%d rpc",
		purgedURLs, revokedStreams, cancelledRPC)
}

// resolveSettingEnableMCP picks the effective EnableMCP value for the
// update. A nil form pointer means "field absent" so we MUST preserve
// the current config to avoid accidentally tripping the kill switch on
// partial PATCH calls that omit enable_mcp.
func resolveSettingEnableMCP(formValue *bool, current bool) bool {
	if formValue == nil {
		return current
	}
	return *formValue
}

// Perform maintenance
// @Summary Perform maintenance
// @Security BearerAuth
// @Schemes
// @Description Perform system maintenance (SQLite VACUUM and TSDB maintenance)
// @Tags admin required
// @Produce json
// @Success 200 {object} model.CommonResponse[any]
// @Router /maintenance [post]
func runMaintenance(c *gin.Context) (any, error) {
	singleton.PerformMaintenance()
	return nil, nil
}

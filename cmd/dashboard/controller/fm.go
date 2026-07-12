package controller

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
	"github.com/gorilla/websocket"
	"github.com/hashicorp/go-uuid"

	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/pkg/websocketx"
	"github.com/nezhahq/nezha/proto"
	"github.com/nezhahq/nezha/service/rpc"
	"github.com/nezhahq/nezha/service/singleton"
)

// Create FM session
// @Summary Create FM session
// @Description Create an "attached" FM. It is advised to only call this within a terminal session.
// @Tags auth required
// @Accept json
// @Param id query uint true "Server ID"
// @Produce json
// @Success 200 {object} model.CreateFMResponse
// @Router /file [post]
func createFM(c *gin.Context) (*model.CreateFMResponse, error) {
	idStr := c.Query("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return nil, err
	}

	server, _ := singleton.ServerShared.Get(id)
	if server == nil {
		return nil, singleton.Localizer.ErrorT("server not found or not connected")
	}
	if server.GetTaskStream() == nil {
		return nil, singleton.Localizer.ErrorT("server not found or not connected")
	}

	if !server.HasPermission(c) {
		return nil, singleton.Localizer.ErrorT("permission denied")
	}

	streamId, err := uuid.GenerateUUID()
	if err != nil {
		return nil, err
	}

	if err := rpc.NezhaHandlerSingleton.CreateStream(streamId, getUid(c), server.ID); err != nil {
		return nil, err
	}

	fmData, _ := json.Marshal(&model.TaskFM{
		StreamID: streamId,
	})
	if err := server.SendTask(&proto.Task{
		Type: model.TaskTypeFM,
		Data: string(fmData),
	}); err != nil {
		return nil, err
	}

	singleton.WriteAuditLog(c, model.AuditActionFMCreate, "server", id, "", true)

	return &model.CreateFMResponse{
		SessionID: streamId,
	}, nil
}

// Start FM stream
// @Summary Start FM stream
// @Description Start FM stream
// @Tags auth required
// @Param id path string true "Stream UUID"
// @Success 200 {object} model.CommonResponse[any]
// @Router /ws/file/{id} [get]
func fmStream(c *gin.Context) (any, error) {
	streamId := c.Param("id")
	// GHSA-style fix: io_stream sessions must be reachable only by their creator
	// (or an admin). Without this, any authenticated user who learns a stream
	// UUID can hijack a live file-manager session on the target server.
	if !streamAttachAllowedForRequest(c, streamId) {
		return nil, singleton.Localizer.ErrorT("permission denied")
	}
	if _, err := rpc.NezhaHandlerSingleton.GetStream(streamId); err != nil {
		return nil, err
	}
	defer rpc.NezhaHandlerSingleton.CloseStream(streamId)

	wsConn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return nil, newWsError("%v", err)
	}
	defer wsConn.Close()
	conn := websocketx.NewConn(wsConn)

	deregisterPAT := registerPATConnection(c, func() { _ = wsConn.Close() })
	defer deregisterPAT()

	go func() {
		// PING 保活
		for {
			if err = conn.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
				return
			}
			time.Sleep(time.Second * 10)
		}
	}()

	if err = rpc.NezhaHandlerSingleton.UserConnected(streamId, conn); err != nil {
		return nil, newWsError("%v", err)
	}

	if err = rpc.NezhaHandlerSingleton.StartStream(streamId, time.Second*10); err != nil {
		return nil, newWsError("%v", err)
	}

	return nil, newWsError("")
}

// getFMEnhanced 返回文件管理器增强开关状态，供前端决定是否展示在线编辑/权限/压缩等操作入口。
//
// 增强操作的实现方式（无需在 nezhahq/agent 侧新增任何 opcode，官方 agent 直接可用）：
//   - 编辑写回：复用官方已支持的 Upload opcode（opcode 2），由前端经 FM WebSocket 流发送。
//   - chmod / chown / zip / unzip：后端 /api/v1/file/{chmod,chown,zip,unzip} 复用官方
//     TaskTypeExec 命令执行通道（参数化 argv 下发，不经过 shell，天然防注入）。
// 详见 cmd/dashboard/controller/fm_enhanced.go。
//
// @Summary Get FM enhanced flag
// @Tags auth required
// @Success 200 {object} model.CommonResponse[map[string]bool]
// @Router /file/enhanced [get]
func getFMEnhanced(c *gin.Context) (any, error) {
	return gin.H{"enabled": singleton.Conf.FMEnhancedEnabled}, nil
}

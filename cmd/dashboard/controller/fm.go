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
// 注意：这些操作的真实磁盘行为由外部 nezhahq/agent 实现对应 op 完成，dashboard 仅透传协议字节。
//
// 前端↔agent 二进制协议契约（dashboard FM 流是 user↔agent 的字节透传，见 io_stream.StartStream，
// 因此新 op 的下发与回包不经后端解析，完全由 agent 侧 handler 实现）：
//
//	Edit   = 3 : [3][u32 pathLen][path][content...]           回包 NZUP/NERR
//	Chmod  = 4 : [4][u32 pathLen][path][u32 mode]             mode 为八进制权限(如 0644)
//	Chown  = 5 : [5][u32 pathLen][path][u32 uid][u32 gid]
//	Zip    = 6 : [6][u32 srcLen][src][u32 dstLen][dst]         打包 src 到 dst
//	Unzip  = 7 : [7][u32 srcLen][src][u32 dstLen][dst]         解压 src 到 dst 目录
//
// 回包标识符（agent→前端，4 字节）：NZUP = 操作完成；NERR = 错误(其后为错误消息)。
// TODO(agent): 需在 nezhahq/agent 的 FM handler 实现上述 opcode，本题 dashboard 侧协议与 UI 已就绪。
//
// @Summary Get FM enhanced flag
// @Tags auth required
// @Success 200 {object} model.CommonResponse[map[string]bool]
// @Router /file/enhanced [get]
func getFMEnhanced(c *gin.Context) (any, error) {
	return gin.H{"enabled": singleton.Conf.FMEnhancedEnabled}, nil
}

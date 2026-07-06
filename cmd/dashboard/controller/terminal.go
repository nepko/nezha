package controller

import (
	"sync"
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

var terminalSessionStore = struct {
	sync.RWMutex
	sessions map[string]*model.TerminalSessionInfo
}{sessions: make(map[string]*model.TerminalSessionInfo)}

func recordTerminalSession(session *model.TerminalSessionInfo) {
	terminalSessionStore.Lock()
	defer terminalSessionStore.Unlock()
	terminalSessionStore.sessions[session.SessionID] = session
}

func closeTerminalSession(sessionID string) {
	terminalSessionStore.Lock()
	defer terminalSessionStore.Unlock()
	if session, ok := terminalSessionStore.sessions[sessionID]; ok {
		session.Active = false
		session.ClosedAt = time.Now().Unix()
	}
}

func listTerminalSessions(c *gin.Context) ([]*model.TerminalSessionInfo, error) {
	userID := getUid(c)
	isAdmin := callerIsAdmin(c)
	terminalSessionStore.RLock()
	defer terminalSessionStore.RUnlock()

	sessions := make([]*model.TerminalSessionInfo, 0, len(terminalSessionStore.sessions))
	for _, session := range terminalSessionStore.sessions {
		if !isAdmin && session.CreatorUserID != userID {
			continue
		}
		copySession := *session
		sessions = append(sessions, &copySession)
	}
	return sessions, nil
}

// Create web ssh terminal
// @Summary Create web ssh terminal
// @Description Create web ssh terminal
// @Tags auth required
// @Accept json
// @Param terminal body model.TerminalForm true "TerminalForm"
// @Produce json
// @Success 200 {object} model.CreateTerminalResponse
// @Router /terminal [post]
func createTerminal(c *gin.Context) (*model.CreateTerminalResponse, error) {
	var createTerminalReq model.TerminalForm
	if err := c.ShouldBind(&createTerminalReq); err != nil {
		return nil, err
	}

	server, _ := singleton.ServerShared.Get(createTerminalReq.ServerID)
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

	creatorUserID := getUid(c)
	if err := rpc.NezhaHandlerSingleton.CreateStream(streamId, creatorUserID, server.ID); err != nil {
		return nil, err
	}
	recordTerminalSession(&model.TerminalSessionInfo{
		SessionID:     streamId,
		ServerID:      server.ID,
		ServerName:    server.Name,
		CreatorUserID: creatorUserID,
		CreatedAt:     time.Now().Unix(),
		Active:        true,
	})

	terminalData, _ := json.Marshal(&model.TerminalTask{
		StreamID: streamId,
	})
	if err := server.SendTask(&proto.Task{
		Type: model.TaskTypeTerminalGRPC,
		Data: string(terminalData),
	}); err != nil {
		return nil, err
	}

	singleton.WriteAuditLog(c, model.AuditActionTerminalCreate, "server", server.ID, server.Name, true)

	return &model.CreateTerminalResponse{
		SessionID:  streamId,
		ServerID:   server.ID,
		ServerName: server.Name,
	}, nil
}

// TerminalStream web ssh terminal stream
// @Summary Terminal stream
// @Description Terminal stream
// @Tags auth required
// @Param id path string true "Stream UUID"
// @Success 200 {object} model.CommonResponse[any]
// @Router /ws/terminal/{id} [get]
func terminalStream(c *gin.Context) (any, error) {
	streamId := c.Param("id")
	// GHSA-style fix: io_stream sessions must be reachable only by their creator
	// (or an admin). Without this, any authenticated user who learns a stream
	// UUID — via Referer leak, access logs, browser history — can hijack a live
	// terminal and gain shell access to the target server.
	if !streamAttachAllowedForRequest(c, streamId) {
		return nil, singleton.Localizer.ErrorT("permission denied")
	}
	if _, err := rpc.NezhaHandlerSingleton.GetStream(streamId); err != nil {
		return nil, err
	}
	defer closeTerminalSession(streamId)
	defer rpc.NezhaHandlerSingleton.CloseStream(streamId)

	wsConn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return nil, newWsError("%v", err)
	}
	defer wsConn.Close()
	conn := websocketx.NewConn(wsConn)

	// 二开：终端空闲超时自动断开。仅统计用户输入活跃度，超时则关闭连接。
	if singleton.Conf != nil && singleton.Conf.TerminalIdleTimeoutSeconds > 0 {
		timeout := time.Duration(singleton.Conf.TerminalIdleTimeoutSeconds) * time.Second
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				idle := rpc.NezhaHandlerSingleton.StreamIdleSeconds(streamId)
				if idle < 0 {
					return // 流已结束
				}
				if idle >= int64(timeout.Seconds()) {
					// 二开：空闲超时断开，操作审计落库并向前端发送断开原因。
					singleton.WriteAuditLog(c, model.AuditActionTerminalIdleDisconnect, "terminal", 0, streamId, true)
					_ = wsConn.WriteMessage(websocket.CloseMessage,
						websocket.FormatCloseMessage(4000, "idle-timeout"))
					_ = wsConn.Close()
					return
				}
			}
		}()
	}

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

	// 二开：标记该流为终端会话，允许 StartStream 在配置开启时录制双向字节流。
	_ = rpc.NezhaHandlerSingleton.MarkRecording(streamId, true)

	if err = rpc.NezhaHandlerSingleton.StartStream(streamId, time.Second*10); err != nil {
		return nil, newWsError("%v", err)
	}

	return nil, newWsError("")
}

// listTerminalRecordings 列出已录制会话（聚合元信息），供回放审计页查询。
// @Summary List terminal recordings
// @Tags auth required
// @Success 200 {object} model.CommonResponse[[]model.RecordingSessionMeta]
// @Router /terminal/recordings [get]
func listTerminalRecordings(c *gin.Context) ([]*model.RecordingSessionMeta, error) {
	if !callerIsAdmin(c) {
		return nil, singleton.Localizer.ErrorT("permission denied")
	}

	type sessAgg struct {
		SessionID string
		ServerID  uint64
		Chunks    int64
		StartTs   int64
		EndTs     int64
	}
	var aggs []sessAgg
	if err := singleton.DB.Model(&model.TerminalRecordingChunk{}).
		Select("session_id, MAX(server_id) as server_id, COUNT(*) as chunks, MIN(ts) as start_ts, MAX(ts) as end_ts").
		Group("session_id").
		Order("end_ts desc").
		Scan(&aggs).Error; err != nil {
		return nil, err
	}

	result := make([]*model.RecordingSessionMeta, 0, len(aggs))
	for _, a := range aggs {
		name := ""
		if s, ok := singleton.ServerShared.Get(a.ServerID); ok {
			name = s.Name
		}
		result = append(result, &model.RecordingSessionMeta{
			SessionID:  a.SessionID,
			ServerID:   a.ServerID,
			ServerName: name,
			Chunks:     a.Chunks,
			StartTs:    a.StartTs,
			EndTs:      a.EndTs,
		})
	}
	return result, nil
}

// getTerminalRecording 返回某会话的全部录制分块（按 seq 顺序），供前端回放。
// @Summary Get terminal recording chunks
// @Tags auth required
// @Param id path string true "Session UUID"
// @Success 200 {object} model.CommonResponse[[]model.TerminalRecordingChunk]
// @Router /terminal/recordings/{id} [get]
func getTerminalRecording(c *gin.Context) ([]*model.TerminalRecordingChunk, error) {
	if !callerIsAdmin(c) {
		return nil, singleton.Localizer.ErrorT("permission denied")
	}
	sessionID := c.Param("id")
	var chunks []*model.TerminalRecordingChunk
	if err := singleton.DB.Where("session_id = ?", sessionID).Order("seq asc").Find(&chunks).Error; err != nil {
		return nil, err
	}
	return chunks, nil
}

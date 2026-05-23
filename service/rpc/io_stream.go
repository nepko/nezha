package rpc

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/service/singleton"
)

type ioStreamContext struct {
	userIo           io.ReadWriteCloser
	agentIo          io.ReadWriteCloser
	userIoConnectCh  chan struct{}
	agentIoConnectCh chan struct{}
	userIoChOnce     sync.Once
	agentIoChOnce    sync.Once

	// 断线重连支持
	agentConnected   atomic.Bool
	userConnected    atomic.Bool
	streamId         string
	serverID         uint64
	lastActivity     atomic.Int64

	// 审计日志支持
	recording        bool
	sessionID        string
	events           []model.TerminalSessionEvent
	eventsMu         sync.Mutex

	// 会话共享支持
	sharedUsers      map[string]io.ReadWriteCloser // userID -> connection
	sharedUsersMu    sync.RWMutex
	isShared         bool
}

type bp struct {
	buf []byte
}

var bufPool = sync.Pool{
	New: func() any {
		return &bp{
			buf: make([]byte, 1024*1024),
		}
	},
}

func (s *NezhaHandler) CreateStream(streamId string) {
	s.ioStreamMutex.Lock()
	defer s.ioStreamMutex.Unlock()

	s.ioStreams[streamId] = &ioStreamContext{
		userIoConnectCh:  make(chan struct{}),
		agentIoConnectCh: make(chan struct{}),
		streamId:         streamId,
		lastActivity:     atomic.Int64{},
	}
	s.ioStreams[streamId].lastActivity.Store(time.Now().Unix())
}

func (s *NezhaHandler) CreateStreamWithAudit(streamId string, serverID uint64, enableRecording bool) {
	s.ioStreamMutex.Lock()
	defer s.ioStreamMutex.Unlock()

	ctx := &ioStreamContext{
		userIoConnectCh:  make(chan struct{}),
		agentIoConnectCh: make(chan struct{}),
		streamId:         streamId,
		serverID:         serverID,
		recording:        enableRecording,
		lastActivity:     atomic.Int64{},
		sharedUsers:      make(map[string]io.ReadWriteCloser),
	}
	ctx.lastActivity.Store(time.Now().Unix())
	s.ioStreams[streamId] = ctx

	// 创建会话记录
	if enableRecording {
		session := model.TerminalSession{
			ServerID:  serverID,
			SessionID: streamId,
			StartedAt: time.Now().Unix(),
		}
		singleton.DB.Create(&session)
		ctx.sessionID = streamId
	}
}

func (s *NezhaHandler) GetStream(streamId string) (*ioStreamContext, error) {
	s.ioStreamMutex.RLock()
	defer s.ioStreamMutex.RUnlock()

	if ctx, ok := s.ioStreams[streamId]; ok {
		return ctx, nil
	}

	return nil, errors.New("stream not found")
}

func (s *NezhaHandler) CloseStream(streamId string) error {
	s.ioStreamMutex.Lock()
	defer s.ioStreamMutex.Unlock()

	if ctx, ok := s.ioStreams[streamId]; ok {
		if ctx.userIo != nil {
			ctx.userIo.Close()
		}
		if ctx.agentIo != nil {
			ctx.agentIo.Close()
		}
		// 关闭所有共享用户连接
		ctx.sharedUsersMu.Lock()
		for _, conn := range ctx.sharedUsers {
			conn.Close()
		}
		ctx.sharedUsersMu.Unlock()

		// 更新会话结束时间
		if ctx.sessionID != "" {
			singleton.DB.Model(&model.TerminalSession{}).
				Where("session_id = ?", ctx.sessionID).
				Update("ended_at", time.Now().Unix())
		}

		delete(s.ioStreams, streamId)
	}

	return nil
}

func (s *NezhaHandler) UserConnected(streamId string, userIo io.ReadWriteCloser) error {
	stream, err := s.GetStream(streamId)
	if err != nil {
		return err
	}

	// 保存第一个用户为主连接
	if !stream.userConnected.Load() {
		stream.userIo = userIo
		stream.userConnected.Store(true)
		stream.userIoChOnce.Do(func() {
			close(stream.userIoConnectCh)
		})
	} else {
		// 后续用户作为共享观察者
		stream.sharedUsersMu.Lock()
		stream.isShared = true
		stream.sharedUsers[userIo.(interface{ ID() string }).ID()] = userIo
		stream.sharedUsersMu.Unlock()
	}

	stream.lastActivity.Store(time.Now().Unix())
	return nil
}

// UserDisconnected 用户断开连接（用于断线重连）
func (s *NezhaHandler) UserDisconnected(streamId string) error {
	stream, err := s.GetStream(streamId)
	if err != nil {
		return err
	}

	stream.userConnected.Store(false)
	stream.lastActivity.Store(time.Now().Unix())

	// 重置连接通道，允许重连
	stream.userIoChOnce = sync.Once{}
	stream.userIoConnectCh = make(chan struct{})
	stream.userIo = nil

	return nil
}

// AgentDisconnected Agent 断开连接（用于断线重连）
func (s *NezhaHandler) AgentDisconnected(streamId string) error {
	stream, err := s.GetStream(streamId)
	if err != nil {
		return err
	}

	stream.agentConnected.Store(false)
	stream.lastActivity.Store(time.Now().Unix())

	// 重置连接通道，允许重连
	stream.agentIoChOnce = sync.Once{}
	stream.agentIoConnectCh = make(chan struct{})
	stream.agentIo = nil

	return nil
}

func (s *NezhaHandler) AgentConnected(streamId string, agentIo io.ReadWriteCloser) error {
	stream, err := s.GetStream(streamId)
	if err != nil {
		return err
	}

	if !stream.agentConnected.Load() {
		stream.agentIo = agentIo
		stream.agentConnected.Store(true)
		stream.agentIoChOnce.Do(func() {
			close(stream.agentIoConnectCh)
		})
	}

	stream.lastActivity.Store(time.Now().Unix())
	return nil
}

// IsStreamAlive 检查流是否存活
func (s *NezhaHandler) IsStreamAlive(streamId string) bool {
	s.ioStreamMutex.RLock()
	defer s.ioStreamMutex.RUnlock()

	if ctx, ok := s.ioStreams[streamId]; ok {
		return ctx.agentConnected.Load()
	}
	return false
}

// GetStreamActivity 获取流最后活跃时间
func (s *NezhaHandler) GetStreamActivity(streamId string) int64 {
	s.ioStreamMutex.RLock()
	defer s.ioStreamMutex.RUnlock()

	if ctx, ok := s.ioStreams[streamId]; ok {
		return ctx.lastActivity.Load()
	}
	return 0
}

// CleanupExpiredStreams 清理过期的流（超过30分钟无活动）
func (s *NezhaHandler) CleanupExpiredStreams(maxIdle time.Duration) {
	s.ioStreamMutex.Lock()
	defer s.ioStreamMutex.Unlock()

	now := time.Now().Unix()
	for id, ctx := range s.ioStreams {
		if now-ctx.lastActivity.Load() > int64(maxIdle.Seconds()) {
			if ctx.userIo != nil {
				ctx.userIo.Close()
			}
			if ctx.agentIo != nil {
				ctx.agentIo.Close()
			}
			ctx.sharedUsersMu.Lock()
			for _, conn := range ctx.sharedUsers {
				conn.Close()
			}
			ctx.sharedUsersMu.Unlock()

			if ctx.sessionID != "" {
				singleton.DB.Model(&model.TerminalSession{}).
					Where("session_id = ?", ctx.sessionID).
					Update("ended_at", now)
			}

			delete(s.ioStreams, id)
		}
	}
}

// RecordEvent 记录终端事件
func (ctx *ioStreamContext) RecordEvent(eventType uint8, data string) {
	if !ctx.recording || ctx.sessionID == "" {
		return
	}

	ctx.eventsMu.Lock()
	defer ctx.eventsMu.Unlock()

	event := model.TerminalSessionEvent{
		SessionID: ctx.sessionID,
		Type:      eventType,
		Data:      data,
		Timestamp: time.Now().UnixMilli(),
	}
	ctx.events = append(ctx.events, event)

	// 批量写入数据库（每50条或每5秒）
	if len(ctx.events) >= 50 {
		singleton.DB.CreateInBatches(ctx.events, 50)
		ctx.events = ctx.events[:0]
	}
}

// FlushEvents 刷新事件到数据库
func (ctx *ioStreamContext) FlushEvents() {
	if !ctx.recording || ctx.sessionID == "" {
		return
	}

	ctx.eventsMu.Lock()
	defer ctx.eventsMu.Unlock()

	if len(ctx.events) > 0 {
		singleton.DB.CreateInBatches(ctx.events, 50)
		ctx.events = ctx.events[:0]
	}
}

// BroadcastToSharedUsers 广播数据给所有共享用户
func (ctx *ioStreamContext) BroadcastToSharedUsers(data []byte) {
	ctx.sharedUsersMu.RLock()
	defer ctx.sharedUsersMu.RUnlock()

	for _, conn := range ctx.sharedUsers {
		go func(c io.Writer) {
			c.Write(data)
		}(conn)
	}
}

func (s *NezhaHandler) StartStream(streamId string, timeout time.Duration) error {
	stream, err := s.GetStream(streamId)
	if err != nil {
		return err
	}

	timeoutTimer := time.NewTimer(timeout)

LOOP:
	for {
		select {
		case <-stream.userIoConnectCh:
			if stream.agentIo != nil {
				timeoutTimer.Stop()
				break LOOP
			}
		case <-stream.agentIoConnectCh:
			if stream.userIo != nil {
				timeoutTimer.Stop()
				break LOOP
			}
		case <-time.After(timeout):
			break LOOP
		}
		time.Sleep(time.Millisecond * 500)
	}

	if stream.userIo == nil && stream.agentIo == nil {
		return singleton.Localizer.ErrorT("timeout: no connection established")
	}
	if stream.userIo == nil {
		return singleton.Localizer.ErrorT("timeout: user connection not established")
	}
	if stream.agentIo == nil {
		return singleton.Localizer.ErrorT("timeout: agent connection not established")
	}

	isDone := new(atomic.Bool)
	endCh := make(chan struct{})

	// Agent -> User (output)
	go func() {
		bp := bufPool.Get().(*bp)
		defer bufPool.Put(bp)
		buf := make([]byte, 4096)
		for {
			n, readErr := stream.agentIo.Read(buf)
			if n > 0 {
				outputData := buf[:n]
				stream.userIo.Write(outputData)
				stream.BroadcastToSharedUsers(outputData)
				stream.RecordEvent(model.TerminalEventOutput, string(outputData))
				stream.lastActivity.Store(time.Now().Unix())
			}
			if readErr != nil {
				err = readErr
				break
			}
		}
		if isDone.CompareAndSwap(false, true) {
			close(endCh)
		}
	}()

	// User -> Agent (input)
	go func() {
		bp := bufPool.Get().(*bp)
		defer bufPool.Put(bp)
		buf := make([]byte, 4096)
		for {
			n, readErr := stream.userIo.Read(buf)
			if n > 0 {
				inputData := buf[:n]
				stream.agentIo.Write(inputData)
				stream.RecordEvent(model.TerminalEventInput, string(inputData))
				stream.lastActivity.Store(time.Now().Unix())
			}
			if readErr != nil {
				err = readErr
				break
			}
		}
		if isDone.CompareAndSwap(false, true) {
			close(endCh)
		}
	}()

	<-endCh

	// 刷新审计事件
	stream.FlushEvents()

	return err
}

// StartStreamWithReconnect 支持断线重连的流启动
func (s *NezhaHandler) StartStreamWithReconnect(streamId string, timeout time.Duration) error {
	stream, err := s.GetStream(streamId)
	if err != nil {
		return err
	}

	timeoutTimer := time.NewTimer(timeout)

LOOP:
	for {
		select {
		case <-stream.userIoConnectCh:
			if stream.agentIo != nil {
				timeoutTimer.Stop()
				break LOOP
			}
		case <-stream.agentIoConnectCh:
			if stream.userIo != nil {
				timeoutTimer.Stop()
				break LOOP
			}
		case <-time.After(timeout):
			break LOOP
		}
		time.Sleep(time.Millisecond * 500)
	}

	if stream.userIo == nil && stream.agentIo == nil {
		return singleton.Localizer.ErrorT("timeout: no connection established")
	}
	if stream.userIo == nil {
		return singleton.Localizer.ErrorT("timeout: user connection not established")
	}
	if stream.agentIo == nil {
		return singleton.Localizer.ErrorT("timeout: agent connection not established")
	}

	isDone := new(atomic.Bool)
	endCh := make(chan struct{})

	// Agent -> User (output) with reconnect support
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := stream.agentIo.Read(buf)
			if n > 0 {
				outputData := buf[:n]
				if stream.userConnected.Load() {
					stream.userIo.Write(outputData)
				}
				stream.BroadcastToSharedUsers(outputData)
				stream.RecordEvent(model.TerminalEventOutput, string(outputData))
				stream.lastActivity.Store(time.Now().Unix())
			}
			if readErr != nil {
				// Agent 断开，标记状态但不退出
				stream.agentConnected.Store(false)
				stream.agentIoChOnce = sync.Once{}
				stream.agentIoConnectCh = make(chan struct{})
				stream.agentIo = nil

				if isDone.CompareAndSwap(false, true) {
					close(endCh)
				}
				return
			}
		}
	}()

	// User -> Agent (input) with reconnect support
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := stream.userIo.Read(buf)
			if n > 0 {
				inputData := buf[:n]
				if stream.agentConnected.Load() {
					stream.agentIo.Write(inputData)
				}
				stream.RecordEvent(model.TerminalEventInput, string(inputData))
				stream.lastActivity.Store(time.Now().Unix())
			}
			if readErr != nil {
				// 用户断开，标记状态但不退出（等待重连）
				stream.userConnected.Store(false)
				stream.userIoChOnce = sync.Once{}
				stream.userIoConnectCh = make(chan struct{})
				stream.userIo = nil

				if isDone.CompareAndSwap(false, true) {
					close(endCh)
				}
				return
			}
		}
	}()

	<-endCh
	stream.FlushEvents()
	return err
}

package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/service/rpc"
	"github.com/nezhahq/nezha/service/singleton"
)

// 二开：文件管理器增强操作（chmod / chown / zip / unzip）。
//
// 实现方式：复用官方 nezhahq/agent 已有的命令执行通道 TaskTypeExec
// （rpc.CallAgent），以「参数化 argv」方式下发，不经过 shell，
// 因此路径/参数中的特殊字符不会造成命令注入，也无需在 agent 侧新增任何 FM opcode。
// 这样官方 agent 装好即可直接用，避免维护 agent fork。
// （编辑写回则走官方已支持的 Upload opcode，见前端 fm.tsx。）

const fmExecTimeout = 60 * time.Second

// fmExec 通过 TaskTypeExec 在目标服务器上执行一条非交互命令，并返回结构化结果。
// 命令与参数以 argv 形式下发（不经 shell），天然防注入。
func fmExec(c *gin.Context, serverID uint64, cmd string, args []string, stdin string) (bool, error) {
	// FM 增强是独立开关（前端 getFMEnhanced 已据此决定是否展示入口），
	// 不受 EnableMCP 影响：即使管理员关闭 MCP 入口，chmod/chown/zip/unzip 仍可用。
	if !singleton.Conf.FMEnhancedEnabled {
		return false, errors.New("file manager enhanced is disabled")
	}
	req := model.ExecRequest{
		Cmd:            cmd,
		Args:           args,
		TimeoutSeconds: 60,
		Stdin:          stdin,
	}
	raw, err := rpc.CallAgentUnmanaged(c.Request.Context(), serverID, model.TaskTypeExec, req, fmExecTimeout+5*time.Second)
	if err != nil {
		return false, err
	}
	var res model.ExecResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return false, err
	}
	if res.Error != "" {
		return false, fmt.Errorf("%s", res.Error)
	}
	if res.ExitCode != 0 {
		msg := res.Stderr
		if msg == "" {
			msg = res.Stdout
		}
		return false, fmt.Errorf("exit %d: %s", res.ExitCode, msg)
	}
	return true, nil
}

type fmChmodRequest struct {
	ServerID uint64 `json:"server_id"`
	Path     string `json:"path"`
	Mode     int    `json:"mode"`
}

func handleFMChmod(c *gin.Context) (any, error) {
	var req fmChmodRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return nil, err
	}
	if req.ServerID == 0 || req.Path == "" {
		return nil, fmt.Errorf("invalid params")
	}
	if _, err := requireServerAccess(c, req.ServerID); err != nil {
		return nil, err
	}
	modeStr := fmt.Sprintf("%04o", req.Mode)
	ok, err := fmExec(c, req.ServerID, "chmod", []string{modeStr, req.Path}, "")
	if err != nil {
		return nil, err
	}
	singleton.WriteAuditLog(c, model.AuditActionFMCreate, "server", req.ServerID, fmt.Sprintf("chmod %s %s", modeStr, req.Path), ok)
	return gin.H{"success": true}, nil
}

type fmChownRequest struct {
	ServerID uint64 `json:"server_id"`
	Path     string `json:"path"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
}

func handleFMChown(c *gin.Context) (any, error) {
	var req fmChownRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return nil, err
	}
	if req.ServerID == 0 || req.Path == "" {
		return nil, fmt.Errorf("invalid params")
	}
	if _, err := requireServerAccess(c, req.ServerID); err != nil {
		return nil, err
	}
	owner := fmt.Sprintf("%d:%d", req.UID, req.GID)
	ok, err := fmExec(c, req.ServerID, "chown", []string{owner, req.Path}, "")
	if err != nil {
		return nil, err
	}
	singleton.WriteAuditLog(c, model.AuditActionFMCreate, "server", req.ServerID, fmt.Sprintf("chown %s %s", owner, req.Path), ok)
	return gin.H{"success": true}, nil
}

type fmArchiveRequest struct {
	ServerID uint64 `json:"server_id"`
	Src      string `json:"src"`
	Dst      string `json:"dst"`
}

func handleFMZip(c *gin.Context) (any, error) {
	var req fmArchiveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return nil, err
	}
	if req.ServerID == 0 || req.Src == "" || req.Dst == "" {
		return nil, fmt.Errorf("invalid params")
	}
	if _, err := requireServerAccess(c, req.ServerID); err != nil {
		return nil, err
	}
	ok, err := fmExec(c, req.ServerID, "zip", []string{"-r", req.Dst, req.Src}, "")
	if err != nil {
		return nil, err
	}
	singleton.WriteAuditLog(c, model.AuditActionFMCreate, "server", req.ServerID, fmt.Sprintf("zip %s -> %s", req.Src, req.Dst), ok)
	return gin.H{"success": true}, nil
}

func handleFMUnzip(c *gin.Context) (any, error) {
	var req fmArchiveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return nil, err
	}
	if req.ServerID == 0 || req.Src == "" || req.Dst == "" {
		return nil, fmt.Errorf("invalid params")
	}
	if _, err := requireServerAccess(c, req.ServerID); err != nil {
		return nil, err
	}
	ok, err := fmExec(c, req.ServerID, "unzip", []string{req.Src, "-d", req.Dst}, "")
	if err != nil {
		return nil, err
	}
	singleton.WriteAuditLog(c, model.AuditActionFMCreate, "server", req.ServerID, fmt.Sprintf("unzip %s -> %s", req.Src, req.Dst), ok)
	return gin.H{"success": true}, nil
}

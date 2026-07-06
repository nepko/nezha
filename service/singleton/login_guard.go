package singleton

import (
	"net"
	"sync"
	"time"

	"github.com/nezhahq/nezha/model"
)

// ipLoginState 记录某 IP 的登录失败计数与封禁截止时间（内存态，重启可恢复由 WAF 表兜底）。
type ipLoginState struct {
	fails   int
	banUntil int64
}

var (
	ipLoginMu    sync.RWMutex
	ipLoginStore = make(map[string]*ipLoginState)
)

// RecordIPLoginFailure 记录某 IP 一次登录失败，返回是否因此达到封禁阈值。
func RecordIPLoginFailure(ip string) (banned bool, until int64) {
	if ip == "" {
		return false, 0
	}
	ipLoginMu.Lock()
	defer ipLoginMu.Unlock()
	st, ok := ipLoginStore[ip]
	if !ok {
		st = &ipLoginState{}
		ipLoginStore[ip] = st
	}
	st.fails++
	if Conf.LoginProtectEnabled && Conf.LoginBanIPThreshold > 0 &&
		st.fails >= Conf.LoginBanIPThreshold {
		st.banUntil = time.Now().Unix() + int64(Conf.LoginBanMinutes)*60
		st.fails = 0
		return true, st.banUntil
	}
	return false, 0
}

// IsIPBanned 判断 IP 是否被封禁，返回剩余秒数（<=0 表示未封禁）。
func IsIPBanned(ip string) (bool, int64) {
	if ip == "" {
		return false, 0
	}
	ipLoginMu.RLock()
	st, ok := ipLoginStore[ip]
	ipLoginMu.RUnlock()
	if !ok || st.banUntil == 0 {
		return false, 0
	}
	remaining := st.banUntil - time.Now().Unix()
	if remaining <= 0 {
		// 过期，惰性清理
		ipLoginMu.Lock()
		st.banUntil = 0
		st.fails = 0
		ipLoginMu.Unlock()
		return false, 0
	}
	return true, remaining
}

// ClearIPBan 清除某 IP 的封禁与失败计数。
func ClearIPBan(ip string) {
	if ip == "" {
		return
	}
	ipLoginMu.Lock()
	delete(ipLoginStore, ip)
	ipLoginMu.Unlock()
}

// ListBannedIPs 返回当前所有处于封禁状态的 IP 视图。
func ListBannedIPs() []model.LoginLockEntry {
	now := time.Now().Unix()
	ipLoginMu.RLock()
	defer ipLoginMu.RUnlock()
	var out []model.LoginLockEntry
	for ip, st := range ipLoginStore {
		if st.banUntil > now {
			out = append(out, model.LoginLockEntry{
				Kind:      "ip",
				Target:    ip,
				Remaining: st.banUntil - now,
				Reason:    "brute_force_ban",
			})
		}
	}
	return out
}

// IPInCIDRs 判断 ip 是否落在逗号分隔的 CIDR 列表内（空列表返回 true）。
func IPInCIDRs(ip, cidrs string) bool {
	if cidrs == "" {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, raw := range splitComma(cidrs) {
		_, netObj, err := net.ParseCIDR(raw)
		if err != nil {
			// 也支持单个裸 IP
			if single := net.ParseIP(raw); single != nil && single.Equal(parsed) {
				return true
			}
			continue
		}
		if netObj.Contains(parsed) {
			return true
		}
	}
	return false
}

func splitComma(s string) []string {
	out := make([]string, 0)
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, trimSpace(cur))
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, trimSpace(cur))
	}
	return out
}

func trimSpace(s string) string {
	out := ""
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		out += string(r)
	}
	return out
}

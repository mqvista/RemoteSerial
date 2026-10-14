package web

import (
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// settingsUpdate 的每个字段都是可选的，只更新提供的项。
type settingsUpdate struct {
	LogLevel      *string   `json:"logLevel"`
	AllowedLogins *[]string `json:"allowedLogins"`
	Listen        *string   `json:"listen"`
	WebPort       *int      `json:"webPort"`
	UDPPort       *int      `json:"udpPort"`
	Hostname      *string   `json:"hostname"`
	ControlURL    *string   `json:"controlURL"`
	Tags          *[]string `json:"tags"`
	UpdateURL     *string   `json:"updateURL"`
	UpdateAuto    *bool     `json:"updateAutoCheck"`
}

type settingsResponse struct {
	Config          any      `json:"config"`
	RestartRequired bool     `json:"restartRequired"`
	Applied         []string `json:"applied"`
	Pending         []string `json:"pending"` // 需要重启才生效的项
}

// PUT /api/settings
func (s *Server) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	var u settingsUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&u); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
		return
	}
	cfg := s.deps.Config
	backup := *cfg
	backup.Access.AllowedLogins = append([]string(nil), cfg.Access.AllowedLogins...)
	resp := settingsResponse{Applied: []string{}, Pending: []string{}}

	if u.LogLevel != nil {
		cfg.LogLevel = strings.ToLower(strings.TrimSpace(*u.LogLevel))
	}
	if u.AllowedLogins != nil {
		clean := make([]string, 0, len(*u.AllowedLogins))
		for _, l := range *u.AllowedLogins {
			if l = strings.TrimSpace(l); l != "" {
				clean = append(clean, l)
			}
		}
		cfg.Access.AllowedLogins = clean
	}
	if u.Listen != nil && strings.TrimSpace(*u.Listen) != cfg.Listen {
		cfg.Listen = strings.TrimSpace(*u.Listen)
		resp.Pending = append(resp.Pending, "listen")
	}
	if u.WebPort != nil && *u.WebPort != cfg.Tailscale.WebPort {
		cfg.Tailscale.WebPort = *u.WebPort
		resp.Pending = append(resp.Pending, "webPort")
	}
	if u.UDPPort != nil && *u.UDPPort != cfg.Tailscale.UDPPort {
		cfg.Tailscale.UDPPort = *u.UDPPort
		resp.Pending = append(resp.Pending, "udpPort")
	}
	if u.Hostname != nil || u.ControlURL != nil {
		host := cfg.Tailscale.Hostname
		if u.Hostname != nil && strings.TrimSpace(*u.Hostname) != "" {
			host = strings.TrimSpace(*u.Hostname)
		}
		ctrl := cfg.Tailscale.ControlURL
		if u.ControlURL != nil {
			ctrl = strings.TrimSpace(*u.ControlURL)
		}
		if host != cfg.Tailscale.Hostname || ctrl != cfg.Tailscale.ControlURL {
			if s.deps.Node != nil {
				if err := s.deps.Node.UpdateConfig(host, ctrl); err != nil {
					*cfg = backup
					writeError(w, http.StatusConflict, err.Error())
					return
				}
			}
			cfg.Tailscale.Hostname = host
			cfg.Tailscale.ControlURL = ctrl
			resp.Applied = append(resp.Applied, "tailscale")
		}
	}

	if u.Tags != nil {
		clean := make([]string, 0, len(*u.Tags))
		for _, t := range *u.Tags {
			if t = strings.TrimSpace(t); t != "" {
				clean = append(clean, t)
			}
		}
		if strings.Join(clean, ",") != strings.Join(cfg.Tailscale.Tags, ",") {
			if s.deps.Node != nil {
				if err := s.deps.Node.SetTags(clean); err != nil {
					*cfg = backup
					writeError(w, http.StatusConflict, err.Error())
					return
				}
			}
			cfg.Tailscale.Tags = clean
			resp.Applied = append(resp.Applied, "tags")
		}
	}
	updateChanged := false
	if u.UpdateURL != nil && strings.TrimSpace(*u.UpdateURL) != cfg.Update.URL {
		cfg.Update.URL = strings.TrimSpace(*u.UpdateURL)
		updateChanged = true
	}
	if u.UpdateAuto != nil && *u.UpdateAuto != cfg.Update.AutoCheck {
		cfg.Update.AutoCheck = *u.UpdateAuto
		updateChanged = true
	}

	if err := cfg.Validate(); err != nil {
		*cfg = backup
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cfg.Save(s.deps.Paths.ConfigFile); err != nil {
		*cfg = backup
		writeError(w, http.StatusInternalServerError, "保存配置失败: "+err.Error())
		return
	}

	if u.LogLevel != nil && s.deps.SetLogLevel != nil {
		if err := s.deps.SetLogLevel(cfg.LogLevel); err == nil {
			resp.Applied = append(resp.Applied, "logLevel")
		}
	}
	if u.AllowedLogins != nil {
		resp.Applied = append(resp.Applied, "allowedLogins")
	}
	if updateChanged {
		if s.deps.Updater != nil {
			s.deps.Updater.SetConfig(cfg.Update.URL, cfg.Update.AutoCheck)
		}
		resp.Applied = append(resp.Applied, "update")
	}
	resp.RestartRequired = len(resp.Pending) > 0
	resp.Config = cfg
	s.deps.Log.Info("设置已更新", "applied", resp.Applied, "pending", resp.Pending, "by", requesterLabel(r))
	s.hub.Notify()
	writeJSON(w, http.StatusOK, resp)
}

// InterfaceInfo 是本机一个 IPv4 网段，供子网路由页一键填入。
type InterfaceInfo struct {
	Name   string `json:"name"`
	IP     string `json:"ip"`
	CIDR   string `json:"cidr"`
	Hidden bool   `json:"hidden"` // 虚拟网卡、tailscale 段等通常不该宣告的
}

// GET /api/network/interfaces
func (s *Server) handleInterfaces(w http.ResponseWriter, _ *http.Request) {
	ifaces, err := net.Interfaces()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []InterfaceInfo{}
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			ip, _ := netip.AddrFromSlice(ipn.IP.To4())
			ones, _ := ipn.Mask.Size()
			pfx := netip.PrefixFrom(ip, ones).Masked()
			info := InterfaceInfo{Name: ifc.Name, IP: ip.String(), CIDR: pfx.String()}
			lname := strings.ToLower(ifc.Name)
			if ip.IsLinkLocalUnicast() || cgnat.Contains(ip) ||
				strings.Contains(lname, "tailscale") || strings.Contains(lname, "virtualbox") ||
				strings.Contains(lname, "vmware") || strings.Contains(lname, "hyper-v") ||
				strings.Contains(lname, "wsl") || strings.Contains(lname, "docker") ||
				strings.Contains(lname, "vethernet") {
				info.Hidden = true
			}
			out = append(out, info)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"interfaces": out})
}

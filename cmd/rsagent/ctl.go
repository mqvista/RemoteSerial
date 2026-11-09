package main

// rsagent ctl：给 LuCI 的 rpcd 插件用的机器接口。总是在 stdout 输出一个 JSON 对象，出错时为
// {"error": "..."} 并以 1 退出。通过本机 127.0.0.1 API 操作正在运行的 rsagent。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"rsagent/internal/config"
	"rsagent/internal/deploy"
	"rsagent/internal/photonicat"
	"rsagent/internal/platform"
	"rsagent/internal/systs"
)

const ctlUsage = `usage: rsagent ctl <op> [args] [--data-dir dir]
  status                          service, console, Tailscale and bridges in one object
  service start|stop|restart|enable|disable
  bridge <id> start|stop
  ports                           serial ports on this device
  bridge-add --device /dev/ttyUSB0 [--name n] [--baud 115200] [--mode rfc2217|raw] [--tcp-port N] [--match-usb]
  bridge-remove <id>
  console [--port N] [--lan=true|false]   show or change the console port and LAN access
  token                           one-time console login token (valid 60 s)
  password [--clear]              set the LAN password (read from stdin) or clear it
  tailscale install|start|firewall|service   start a system Tailscale job; see tailscale-job
  tailscale-job                   progress of the last job
  tailscale-login                 start a browser login; the result has authURL
  screen install|remove           RemoteSerial page on the Photonicat 2 screen
  lang <code>                     use LuCI's current language when rsagent follows the system language
  update                          one-click update of a package install: check, download, install
  update-check                    check for a new version
  tailscale-authkey [--tags tag:a,tag:b]   log in with an auth key or OAuth client secret read from stdin`

type ctlClient struct {
	cfg  *config.Config
	base string
	http *http.Client
}

func (c *ctlClient) call(method, path string, body any) (json.RawMessage, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errAPIDown
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return nil, errors.New(e.Error)
		}
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return data, nil
}

var errAPIDown = errors.New("rsagent is not running")

func cmdCtl(args []string) {
	out, err := runCtl(args)
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err != nil {
		_ = enc.Encode(map[string]string{"error": err.Error()})
		os.Exit(1)
	}
	if raw, ok := out.(json.RawMessage); ok {
		os.Stdout.Write(bytes.TrimSpace(raw))
		os.Stdout.Write([]byte("\n"))
		return
	}
	_ = enc.Encode(out)
}

// ctlValueFlags 是带值的选项，可以写成 --x v 或 --x=v。
var ctlValueFlags = map[string]bool{"data-dir": true, "port": true, "lan": true, "tags": true, "device": true, "name": true, "baud": true, "mode": true, "tcp-port": true}

// splitCtlArgs 把位置参数与 --选项 分开，选项可以写在任何位置。
func splitCtlArgs(args []string) (pos, flags []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && ctlValueFlags[strings.TrimLeft(a, "-")] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	return pos, flags
}

func runCtl(args []string) (any, error) {
	pos, flags := splitCtlArgs(args)
	if len(pos) == 0 {
		return nil, errors.New(ctlUsage)
	}
	fs := flag.NewFlagSet("ctl", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataDir := fs.String("data-dir", os.Getenv("RSAGENT_DATA_DIR"), "")
	port := fs.Int("port", 0, "")
	lan := fs.String("lan", "", "")
	clear := fs.Bool("clear", false, "")
	tags := fs.String("tags", "", "")
	device := fs.String("device", "", "")
	name := fs.String("name", "", "")
	baud := fs.Int("baud", 115200, "")
	mode := fs.String("mode", "rfc2217", "")
	tcpPort := fs.Int("tcp-port", 0, "")
	matchUSB := fs.Bool("match-usb", false, "")
	if err := fs.Parse(flags); err != nil {
		return nil, fmt.Errorf("%v\n%s", err, ctlUsage)
	}
	paths := config.ResolvePaths(*dataDir)
	cfg, _, err := config.Load(paths.ConfigFile)
	if err != nil {
		return nil, err
	}
	c := &ctlClient{
		cfg:  cfg,
		base: "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.ConsolePort())),
		http: &http.Client{Timeout: 90 * time.Second},
	}

	op, rest := pos[0], pos[1:]
	switch op {
	case "status":
		return ctlStatus(c), nil
	case "service":
		if len(rest) != 1 {
			return nil, errors.New("usage: rsagent ctl service start|stop|restart|enable|disable")
		}
		return ctlService(rest[0])
	case "bridge":
		if len(rest) != 2 || (rest[1] != "start" && rest[1] != "stop") {
			return nil, errors.New("usage: rsagent ctl bridge <id> start|stop")
		}
		return c.call("POST", "/api/bridges/"+url.PathEscape(rest[0])+"/"+rest[1], nil)
	case "ports":
		return c.call("GET", "/api/serial/ports", nil)
	case "bridge-add":
		return ctlBridgeAdd(c, *device, *name, *baud, *mode, *tcpPort, *matchUSB)
	case "bridge-remove":
		if len(rest) != 1 {
			return nil, errors.New("usage: rsagent ctl bridge-remove <id>")
		}
		return ctlBridgeRemove(c, rest[0])
	case "console":
		body := map[string]any{}
		if *port != 0 {
			body["port"] = *port
		}
		if *lan != "" {
			v, err := strconv.ParseBool(*lan)
			if err != nil {
				return nil, fmt.Errorf("--lan must be true or false")
			}
			body["lan"] = v
		}
		if len(body) == 0 {
			if raw, err := c.call("GET", "/api/console", nil); !errors.Is(err, errAPIDown) {
				return raw, err
			}
			return ctlConsoleOffline(cfg), nil
		}
		raw, err := c.call("PUT", "/api/console", body)
		if errors.Is(err, errAPIDown) {
			return ctlConsoleSetOffline(cfg, paths.ConfigFile, body)
		}
		return raw, err
	case "token":
		return c.call("POST", "/api/session/token", nil)
	case "password":
		pw := ""
		if !*clear {
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil && line == "" {
				return nil, errors.New("write the new password to stdin, or use --clear")
			}
			pw = strings.TrimRight(line, "\r\n")
			if pw == "" {
				return nil, errors.New("empty password; use --clear to remove it")
			}
		}
		return c.call("PUT", "/api/session/password", map[string]string{"password": pw})
	case "tailscale":
		if len(rest) != 1 {
			return nil, errors.New("usage: rsagent ctl tailscale install|start|firewall|service")
		}
		switch rest[0] {
		case "install", "start", "firewall", "service":
		default:
			return nil, fmt.Errorf("unknown tailscale action %q", rest[0])
		}
		return c.call("POST", "/api/tailscale/system/"+rest[0], nil)
	case "screen":
		if len(rest) != 1 || (rest[0] != "install" && rest[0] != "remove") {
			return nil, errors.New("usage: rsagent ctl screen install|remove")
		}
		var log bytes.Buffer
		var err error
		if rest[0] == "install" {
			if err = photonicat.Install(&log); err == nil {
				err = setStatusFile(paths, photonicat.StatusFile)
			}
		} else if err = photonicat.Remove(&log); err == nil {
			err = setStatusFile(paths, "")
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "log": log.String(), "screen": photonicat.Status()}, nil
	case "lang":
		if len(rest) != 1 {
			return nil, errors.New("usage: rsagent ctl lang <code>, e.g. zh-tw or en")
		}
		return c.call("POST", "/api/language/follow", map[string]string{"lang": rest[0]})
	case "update":
		return c.call("POST", "/api/update/auto", nil)
	case "update-check":
		return c.call("POST", "/api/update/check", nil)
	case "tailscale-job":
		return c.call("GET", "/api/tailscale/system/job", nil)
	case "tailscale-login":
		return c.call("POST", "/api/tailscale/login-interactive", nil)
	case "tailscale-authkey":
		// key 从标准输入读，不出现在命令行参数（ps 可见）里
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		key := strings.TrimSpace(line)
		if key == "" {
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			return nil, errors.New("write the auth key to stdin")
		}
		list := []string{}
		for _, t := range strings.Split(*tags, ",") {
			if t = strings.TrimSpace(t); t != "" {
				list = append(list, t)
			}
		}
		if len(list) == 0 {
			list = cfg.Tailscale.Tags
		}
		if strings.HasPrefix(key, "tskey-client-") && len(list) == 0 {
			return nil, errors.New("an OAuth client secret needs at least one tag, e.g. tag:serial-agent")
		}
		return c.call("POST", "/api/tailscale/login", map[string]any{
			"authKey":    key,
			"controlURL": cfg.Tailscale.ControlURL,
			"tags":       list,
		})
	}
	return nil, fmt.Errorf("unknown ctl operation %q\n%s", op, ctlUsage)
}

func ctlStatus(c *ctlClient) any {
	out := map[string]any{"platform": platform.Name()}
	if platform.OpenWrt() {
		out["service"] = deploy.OpenWrtServiceStatus()
		if photonicat.Available() {
			out["photonicat"] = photonicat.Status()
		}
	}
	st, err := c.call("GET", "/api/status", nil)
	out["running"] = err == nil
	if err != nil {
		out["console"] = ctlConsoleOffline(c.cfg)
		// rsagent 停止时也检查系统 Tailscale，LuCI 照样能提示未安装
		if c.cfg.Tailscale.EffectiveBackend() == config.BackendSystem {
			out["tailscale"] = systs.Detect(c.cfg.Tailscale.Socket())
		}
		if !errors.Is(err, errAPIDown) {
			out["error"] = err.Error()
		}
		return out
	}
	out["status"] = st
	if ports, err := c.call("GET", "/api/serial/ports", nil); err == nil {
		var p struct {
			Ports json.RawMessage `json:"ports"`
		}
		if json.Unmarshal(ports, &p) == nil {
			out["ports"] = p.Ports
		}
	}
	if con, err := c.call("GET", "/api/console", nil); err == nil {
		out["console"] = con
	}
	if upd, err := c.call("GET", "/api/update/status", nil); err == nil {
		out["update"] = upd
	}
	return out
}

func ctlConsoleOffline(cfg *config.Config) map[string]any {
	return map[string]any{
		"port":        cfg.ConsolePort(),
		"lan":         cfg.Access.LAN,
		"listen":      cfg.Listen,
		"bind":        cfg.ConsoleBind(),
		"webPort":     cfg.Tailscale.WebPort,
		"passwordSet": cfg.Access.PasswordHash != "",
		"live":        false,
	}
}

// ctlConsoleSetOffline 在 rsagent 未运行时直接改配置文件，下次启动生效。
func ctlConsoleSetOffline(cfg *config.Config, file string, body map[string]any) (any, error) {
	oldPort := cfg.ConsolePort()
	port := oldPort
	if p, ok := body["port"].(int); ok {
		port = p
	}
	if v, ok := body["lan"].(bool); ok {
		cfg.Access.LAN = v
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("port must be between 1 and 65535: %d", port)
	}
	host, _, _ := net.SplitHostPort(cfg.Listen)
	cfg.Listen = net.JoinHostPort(host, strconv.Itoa(port))
	if cfg.Tailscale.WebPort == oldPort {
		cfg.Tailscale.WebPort = port
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.Save(file); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res := ctlConsoleOffline(cfg)
	if err := systs.SyncConsoleFirewall(ctx, port, cfg.Access.LAN); err != nil {
		res["warning"] = err.Error()
	}
	return res, nil
}

func ctlService(op string) (any, error) {
	switch op {
	case "start", "stop", "restart", "enable", "disable":
	default:
		return nil, fmt.Errorf("unknown service action %q", op)
	}
	if !platform.OpenWrt() {
		return nil, errors.New("rsagent ctl service is only available on OpenWrt; use rsagent service")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if b, err := exec.CommandContext(ctx, deploy.ProcdInitScript, op).CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(b))
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s %s: %s", deploy.ProcdInitScript, op, msg)
	}
	return map[string]any{"ok": true, "service": deploy.OpenWrtServiceStatus()}, nil
}

type ctlPort struct {
	Name         string `json:"name"`
	IsUSB        bool   `json:"isUSB"`
	VID          string `json:"vid"`
	PID          string `json:"pid"`
	SerialNumber string `json:"serialNumber"`
}

// ctlBridges 读出当前的桥接配置。
func ctlBridges(c *ctlClient) ([]config.Bridge, error) {
	raw, err := c.call("GET", "/api/bridges", nil)
	if err != nil {
		return nil, err
	}
	var v struct {
		Bridges []struct {
			Config config.Bridge `json:"config"`
		} `json:"bridges"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	out := make([]config.Bridge, 0, len(v.Bridges))
	for _, b := range v.Bridges {
		out = append(out, b.Config)
	}
	return out, nil
}

// ctlBridgeAdd 用与管理界面“新增桥接”相同的默认值新增一个桥接。
func ctlBridgeAdd(c *ctlClient, device, name string, baud int, mode string, tcpPort int, matchUSB bool) (any, error) {
	if device == "" {
		return nil, errors.New("--device is required, e.g. --device /dev/ttyUSB0")
	}
	if mode != "rfc2217" && mode != "raw" {
		return nil, fmt.Errorf("--mode must be rfc2217 or raw")
	}
	list, err := ctlBridges(c)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	ports := map[int]bool{c.cfg.ConsolePort(): true, c.cfg.Tailscale.WebPort: true}
	for _, b := range list {
		ids[b.ID] = true
		ports[b.TCPPort] = true
		if b.LocalPort > 0 {
			ports[b.LocalPort] = true
		}
	}
	id := ""
	for i := 1; ; i++ {
		if id = fmt.Sprintf("b%d", i); !ids[id] {
			break
		}
	}
	if tcpPort == 0 {
		for tcpPort = 2217; ports[tcpPort]; tcpPort++ {
		}
	}
	b := config.Bridge{
		ID: id, Name: strings.TrimSpace(name), Port: device, Baud: baud, DataBits: 8, Parity: "N", StopBits: 1,
		Mode: mode, TCPPort: tcpPort, ClientPolicy: "replace", Enabled: true,
	}
	if b.Name == "" {
		b.Name = strings.TrimPrefix(device, "/dev/")
	}
	if matchUSB {
		raw, err := c.call("GET", "/api/serial/ports", nil)
		if err != nil {
			return nil, err
		}
		var v struct {
			Ports []ctlPort `json:"ports"`
		}
		_ = json.Unmarshal(raw, &v)
		for _, p := range v.Ports {
			if p.Name == device && p.IsUSB && (p.VID != "" || p.SerialNumber != "") {
				b.Match = &config.PortMatch{VID: p.VID, PID: p.PID, Serial: p.SerialNumber}
			}
		}
		if b.Match == nil {
			return nil, fmt.Errorf("%s is not a USB serial port with a USB ID; add it without --match-usb", device)
		}
	}
	if _, err := c.call("PUT", "/api/bridges", map[string]any{"bridges": append(list, b)}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "bridge": b}, nil
}

func ctlBridgeRemove(c *ctlClient, id string) (any, error) {
	list, err := ctlBridges(c)
	if err != nil {
		return nil, err
	}
	keep := list[:0]
	found := false
	for _, b := range list {
		if b.ID == id {
			found = true
			continue
		}
		keep = append(keep, b)
	}
	if !found {
		return nil, fmt.Errorf("no bridge with id %q", id)
	}
	if _, err := c.call("PUT", "/api/bridges", map[string]any{"bridges": keep}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "removed": id}, nil
}

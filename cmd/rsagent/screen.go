package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"rsagent/internal/config"
	"rsagent/internal/photonicat"
)

// cmdScreen 管理外部显示屏上的 rsagent 状态页，目前支持 Photonicat 2 的小屏幕。
func cmdScreen(args []string) error {
	const usageLine = "usage: rsagent screen photonicat install|remove|status [--data-dir dir]"
	if len(args) < 2 || args[0] != "photonicat" {
		return errors.New(usageLine)
	}
	op := args[1]
	o, err := parseCommon("screen", args[2:], false)
	if err != nil {
		return err
	}
	paths := config.ResolvePaths(o.DataDir)
	switch op {
	case "status":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(photonicat.Status())
	case "install":
		if err := photonicat.Install(os.Stdout); err != nil {
			return err
		}
		if err := setStatusFile(paths, photonicat.StatusFile); err != nil {
			return fmt.Errorf("the screen page was added, but enabling the status file failed: %w", err)
		}
		fmt.Println("The RemoteSerial page is on the screen; press the screen button to reach it")
		return nil
	case "remove":
		if err := photonicat.Remove(os.Stdout); err != nil {
			return err
		}
		if err := setStatusFile(paths, ""); err != nil {
			return fmt.Errorf("the screen page was removed, but disabling the status file failed: %w", err)
		}
		fmt.Println("The RemoteSerial page was removed from the screen")
		return nil
	}
	return errors.New(usageLine)
}

// setStatusFile 设置 rsagent 的 statusFile：正在运行时经本机 API 立即生效，否则直接改配置文件。
func setStatusFile(paths config.Paths, file string) error {
	cfg, _, err := config.Load(paths.ConfigFile)
	if err != nil {
		return err
	}
	c := &ctlClient{cfg: cfg, base: "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.ConsolePort())), http: &http.Client{Timeout: 10 * time.Second}}
	if _, err := c.call("PUT", "/api/settings", map[string]any{"statusFile": file}); !errors.Is(err, errAPIDown) {
		return err
	}
	cfg.StatusFile = file
	return cfg.Save(paths.ConfigFile)
}

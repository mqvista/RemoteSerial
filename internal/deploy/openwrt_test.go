package deploy

import (
	"strings"
	"testing"
)

func TestProcdScript(t *testing.T) {
	s := ProcdScript(Options{DataDir: "/etc/rsagent", Listen: "0.0.0.0:7890"})
	for _, want := range []string{
		"#!/bin/sh /etc/rc.common\n",
		"USE_PROCD=1\n",
		"START=99\n",
		"procd_set_param command /usr/bin/rsagent service run --data-dir /etc/rsagent --listen 0.0.0.0:7890\n",
		"procd_set_param respawn 3600 5 0\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q:\n%s", want, s)
		}
	}
	s = ProcdScript(Options{DataDir: "/mnt/usb/rs agent's"})
	if !strings.Contains(s, `--data-dir '/mnt/usb/rs agent'\''s'`) {
		t.Errorf("bad quoting:\n%s", s)
	}
}

func TestParseProcdRunning(t *testing.T) {
	running := `{"rsagent":{"instances":{"instance1":{"running":true,"pid":1234,"command":["/usr/bin/rsagent"]}}}}`
	if !parseProcdRunning(running) {
		t.Error("running not detected")
	}
	for _, s := range []string{`{}`, `{"rsagent":{"instances":{"instance1":{"running":false}}}}`, `{"rsagent":{}}`, `not json`} {
		if parseProcdRunning(s) {
			t.Errorf("false positive for %s", s)
		}
	}
}

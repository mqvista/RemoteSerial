package app

import (
	"testing"

	"rsagent/internal/bridge"
)

func TestDiffPorts(t *testing.T) {
	old := map[string]bridge.PortInfo{"/dev/ttyS0": {Name: "/dev/ttyS0"}, "/dev/ttyUSB0": {Name: "/dev/ttyUSB0"}}
	cur := map[string]bridge.PortInfo{"/dev/ttyS0": {Name: "/dev/ttyS0"}, "/dev/ttyACM0": {Name: "/dev/ttyACM0"}}
	ev := diffPorts(old, cur)
	if len(ev) != 2 {
		t.Fatalf("events: %+v", ev)
	}
	for _, e := range ev {
		if e.added && e.port.Name != "/dev/ttyACM0" || !e.added && e.port.Name != "/dev/ttyUSB0" {
			t.Fatalf("wrong event %+v", e)
		}
	}
}

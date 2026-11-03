package pluginprotocol

import "testing"

func TestRetiredRewaitProtocolRejected(t *testing.T) {
	for _, input := range []struct {
		direction Direction
		raw       string
	}{
		{PluginToDaemon, `{"version":2,"type":"ready","role":"input","capabilities":["runtime_state_v2","rewait_v1"]}`},
		{DaemonToPlugin, `{"version":2,"type":"resume_watch","request_id":"r","subscription_id":"s","generation":"g","confirmation_position":"p"}`},
		{PluginToDaemon, `{"version":2,"type":"resume_result","request_id":"r","accepted":true}`},
	} {
		if _, err := ParsePayload([]byte(input.raw), input.direction); err == nil {
			t.Errorf("accepted retired frame %s", input.raw)
		}
	}
}

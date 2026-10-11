package pluginprocess

import (
	"context"

	"github.com/hashicorp/go-plugin"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"google.golang.org/grpc"
)

const PluginKey = "events"

var HandshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "AGENT_PULSE_HUB_PLUGIN",
	MagicCookieValue: "plugin-v1",
}

// FrameworkPlugin connects the application Session service to go-plugin.
// The host leaves Impl nil; a plugin executable supplies its service handler.
type FrameworkPlugin struct {
	plugin.NetRPCUnsupportedPlugin
	Impl   pluginprotocol.EventPluginServer
	onExit func()
}

var _ plugin.GRPCPlugin = (*FrameworkPlugin)(nil)

func (p *FrameworkPlugin) GRPCServer(_ *plugin.GRPCBroker, server *grpc.Server) error {
	if p.Impl != nil {
		pluginprotocol.RegisterEventPluginServer(server, p.Impl)
	}
	return nil
}

func (p *FrameworkPlugin) GRPCClient(ctx context.Context, _ *plugin.GRPCBroker, connection *grpc.ClientConn) (any, error) {
	if p.onExit != nil {
		go func() {
			<-ctx.Done()
			p.onExit()
		}()
	}
	return grpc.ClientConnInterface(connection), nil
}

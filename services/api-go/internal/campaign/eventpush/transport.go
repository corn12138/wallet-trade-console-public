package eventpush

import (
	"context"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/realtime"
)

// Transport adapts the hub to the realtime Socket.IO namespace.
type Transport struct{ Hub *Hub }

var _ realtime.CampaignPush = Transport{}

func (t Transport) Authenticate(ctx context.Context, auth any) (any, string) {
	identity, code := t.Hub.Authenticate(ctx, auth)
	if code != "" {
		return nil, code
	}
	return identity, ""
}

func (t Transport) Attach(identity any, conn realtime.PushConn) realtime.PushSession {
	verified, ok := identity.(*Identity)
	if !ok {
		return nil
	}
	if session := t.Hub.Attach(verified, conn); session != nil {
		return session
	}
	return nil
}

func (t Transport) AckTimeout() time.Duration { return t.Hub.cfg.AckTimeout }

package router_test

import (
	"context"
	"testing"

	. "github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	routing_session "github.com/xtls/xray-core/features/routing/session"
)

// monolit: правило nameless-* по адресу срабатывает, только если имя сайта неизвестно
func TestNamelessIP(t *testing.T) {
	build := func(tag string) Condition {
		geo := []*GeoIP{{Cidr: []*CIDR{{Ip: []byte{188, 114, 97, 0}, Prefix: 30}}}}
		c, err := (&RoutingRule{RuleTag: tag, Geoip: geo}).BuildCondition()
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	ctx := func(domain string) *routing_session.Context {
		ob := &session.Outbound{Target: net.TCPDestination(net.ParseAddress("188.114.97.1"), 443)}
		if domain != "" {
			ob.RouteTarget = net.TCPDestination(net.ParseAddress(domain), 443)
		}
		return routing_session.AsRoutingContext(session.ContextWithOutbounds(context.Background(), []*session.Outbound{ob})).(*routing_session.Context)
	}
	if !build("nameless-x").Apply(ctx("")) {
		t.Fatal("без имени адрес должен совпасть")
	}
	if build("nameless-x").Apply(ctx("check-host.net")) {
		t.Fatal("с именем адрес не должен решать")
	}
	if !build("plain").Apply(ctx("check-host.net")) {
		t.Fatal("обычное правило по адресу должно совпасть")
	}
}

package router_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	. "github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	routing_session "github.com/xtls/xray-core/features/routing/session"
)

// monolit: перезагрузка правил не должна оставлять окно без правил и терять старые при ошибке

func tcpRule(tag, ruleTag string) *RoutingRule {
	return &RoutingRule{
		TargetTag: &RoutingRule_Tag{Tag: tag},
		RuleTag:   ruleTag,
		Networks:  []net.Network{net.Network_TCP},
	}
}

func pickTag(t *testing.T, r *Router) string {
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{
		Target: net.TCPDestination(net.DomainAddress("example.com"), 80),
	}})
	route, err := r.PickRoute(routing_session.AsRoutingContext(ctx))
	if err != nil {
		return ""
	}
	return route.GetOutboundTag()
}

func TestReloadRulesConcurrentPick(t *testing.T) {
	r := new(Router)
	if err := r.Init(context.TODO(), &Config{Rule: []*RoutingRule{tcpRule("a", "")}}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	var stop atomic.Bool
	var misses atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if tag := pickTag(t, r); tag != "a" && tag != "b" {
					misses.Add(1)
				}
			}
		}()
	}

	for i := 0; i < 500; i++ {
		tag := "a"
		if i%2 == 0 {
			tag = "b"
		}
		// несколько правил, чтобы сборка шла не мгновенно
		cfg := &Config{Rule: []*RoutingRule{
			{TargetTag: &RoutingRule_Tag{Tag: "x"}, Networks: []net.Network{net.Network_UDP}},
			{TargetTag: &RoutingRule_Tag{Tag: "y"}, Networks: []net.Network{net.Network_UDP}},
			tcpRule(tag, ""),
		}}
		if err := r.ReloadRules(cfg, false); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	wg.Wait()

	if n := misses.Load(); n > 0 {
		t.Fatalf("во время перезагрузки %d раз не нашлось правила", n)
	}
}

func TestReloadRulesFailureKeepsOld(t *testing.T) {
	r := new(Router)
	if err := r.Init(context.TODO(), &Config{Rule: []*RoutingRule{tcpRule("old", "keep")}}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	// балансировщика нет — сборка падает на втором правиле
	bad := &Config{Rule: []*RoutingRule{
		tcpRule("new", ""),
		{TargetTag: &RoutingRule_BalancingTag{BalancingTag: "missing"}, Networks: []net.Network{net.Network_TCP}},
	}}
	if err := r.ReloadRules(bad, false); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if tag := pickTag(t, r); tag != "old" {
		t.Fatalf("после неудачной перезагрузки правило %q, ожидалось old", tag)
	}

	// повтор ruleTag при добавлении тоже не должен ломать набор
	dup := &Config{Rule: []*RoutingRule{tcpRule("dup", "keep")}}
	if err := r.ReloadRules(dup, true); err == nil {
		t.Fatal("ожидалась ошибка повтора ruleTag")
	}
	if n := len(r.ListRule()); n != 1 {
		t.Fatalf("правил %d, ожидалось 1", n)
	}

	// удачная замена
	if err := r.ReloadRules(&Config{Rule: []*RoutingRule{tcpRule("new", "")}}, false); err != nil {
		t.Fatal(err)
	}
	if tag := pickTag(t, r); tag != "new" {
		t.Fatalf("правило %q, ожидалось new", tag)
	}
}

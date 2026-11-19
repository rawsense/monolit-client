package router

import (
	"context"
	"sync"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/features/routing"
	routing_dns "github.com/xtls/xray-core/features/routing/dns"
)

// Router is an implementation of routing.Router.
type Router struct {
	domainStrategy Config_DomainStrategy
	rules          []*Rule
	balancers      map[string]*Balancer
	dns            dns.Client

	ctx        context.Context
	ohm        outbound.Manager
	dispatcher routing.Dispatcher
	// monolit: mu упорядочивает изменения правил (сборка может быть долгой),
	// rmu защищает только подмену готовых rules/balancers. Срезы правил после
	// публикации не меняются, читатели берут снимок под RLock
	mu  sync.Mutex
	rmu sync.RWMutex
}

// Route is an implementation of routing.Route.
type Route struct {
	routing.Context
	outboundGroupTags []string
	outboundTag       string
	ruleTag           string
}

// Init initializes the Router.
func (r *Router) Init(ctx context.Context, config *Config, d dns.Client, ohm outbound.Manager, dispatcher routing.Dispatcher) error {
	r.domainStrategy = config.DomainStrategy
	r.dns = d
	r.ctx = ctx
	r.ohm = ohm
	r.dispatcher = dispatcher

	r.balancers = make(map[string]*Balancer, len(config.BalancingRule))
	for _, rule := range config.BalancingRule {
		balancer, err := rule.Build(ohm, dispatcher)
		if err != nil {
			return err
		}
		balancer.InjectContext(ctx)
		r.balancers[rule.Tag] = balancer
	}

	r.rules = make([]*Rule, 0, len(config.Rule))
	for _, rule := range config.Rule {
		cond, err := rule.BuildCondition()
		if err != nil {
			r.closeWebhooks()
			return err
		}
		rr := &Rule{
			Condition: cond,
			Tag:       rule.GetTag(),
			RuleTag:   rule.GetRuleTag(),
		}
		if wh := rule.GetWebhook(); wh != nil {
			notifier, err := NewWebhookNotifier(wh)
			if err != nil {
				r.closeWebhooks()
				return err
			}
			rr.Webhook = notifier
		}
		btag := rule.GetBalancingTag()
		if len(btag) > 0 {
			brule, found := r.balancers[btag]
			if !found {
				if rr.Webhook != nil {
					rr.Webhook.Close()
				}
				r.closeWebhooks()
				return errors.New("balancer ", btag, " not found")
			}
			rr.Balancer = brule
		}
		r.rules = append(r.rules, rr)
	}

	return nil
}

// PickRoute implements routing.Router.
func (r *Router) PickRoute(ctx routing.Context) (routing.Route, error) {
	originalCtx := ctx
	rule, ctx, err := r.pickRouteInternal(ctx)
	if err != nil {
		return nil, err
	}
	tag, err := rule.GetTag()
	if err != nil {
		return nil, err
	}
	if rule.Webhook != nil {
		rule.Webhook.Fire(originalCtx, tag)
	}
	return &Route{Context: ctx, outboundTag: tag, ruleTag: rule.RuleTag}, nil
}

// AddRule implements routing.Router.
func (r *Router) AddRule(config *serial.TypedMessage, shouldAppend bool) error {

	inst, err := config.GetInstance()
	if err != nil {
		return err
	}
	if c, ok := inst.(*Config); ok {
		return r.ReloadRules(c, shouldAppend)
	}
	return errors.New("AddRule: config type error")
}

func (r *Router) ReloadRules(config *Config, shouldAppend bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// monolit: новый набор собираем в локальных переменных, текущий не трогаем
	// до полного успеха — иначе во время сборки новые соединения видят пустые правила,
	// а при ошибке роутер остаётся совсем без правил
	oldRules, oldBalancers := r.snapshot()

	balancers := make(map[string]*Balancer, len(config.BalancingRule))
	base := 0
	if shouldAppend {
		for tag, b := range oldBalancers {
			balancers[tag] = b
		}
		base = len(oldRules)
	}
	for _, rule := range config.BalancingRule {
		if _, found := balancers[rule.Tag]; found {
			return errors.New("duplicate balancer tag")
		}
		balancer, err := rule.Build(r.ohm, r.dispatcher)
		if err != nil {
			return err
		}
		balancer.InjectContext(r.ctx)
		balancers[rule.Tag] = balancer
	}

	rules := make([]*Rule, 0, base+len(config.Rule))
	rules = append(rules, oldRules[:base]...)
	// при ошибке закрываем только вебхуки новых правил
	fail := func(err error) error {
		closeRuleWebhooks(rules[base:])
		return err
	}

	for _, rule := range config.Rule {
		if ruleExists(rules, rule.GetRuleTag()) {
			return fail(errors.New("duplicate ruleTag ", rule.GetRuleTag()))
		}
		cond, err := rule.BuildCondition()
		if err != nil {
			return fail(err)
		}
		rr := &Rule{
			Condition: cond,
			Tag:       rule.GetTag(),
			RuleTag:   rule.GetRuleTag(),
		}
		if wh := rule.GetWebhook(); wh != nil {
			notifier, err := NewWebhookNotifier(wh)
			if err != nil {
				return fail(err)
			}
			rr.Webhook = notifier
		}
		btag := rule.GetBalancingTag()
		if len(btag) > 0 {
			brule, found := balancers[btag]
			if !found {
				if rr.Webhook != nil {
					rr.Webhook.Close()
				}
				return fail(errors.New("balancer ", btag, " not found"))
			}
			rr.Balancer = brule
		}
		rules = append(rules, rr)
	}

	r.publish(rules, balancers)
	if !shouldAppend {
		// старые правила заменены целиком — их вебхуки больше не нужны
		closeRuleWebhooks(oldRules)
	}
	return nil
}

// snapshot возвращает текущие правила и балансировщики; менять их нельзя
func (r *Router) snapshot() ([]*Rule, map[string]*Balancer) {
	r.rmu.RLock()
	defer r.rmu.RUnlock()
	return r.rules, r.balancers
}

// publish подменяет набор правил одним действием
func (r *Router) publish(rules []*Rule, balancers map[string]*Balancer) {
	r.rmu.Lock()
	r.rules = rules
	r.balancers = balancers
	r.rmu.Unlock()
}

func ruleExists(rules []*Rule, tag string) bool {
	if tag != "" {
		for _, rule := range rules {
			if rule.RuleTag == tag {
				return true
			}
		}
	}
	return false
}

func closeRuleWebhooks(rules []*Rule) {
	for _, rule := range rules {
		if rule.Webhook != nil {
			rule.Webhook.Close()
		}
	}
}

func (r *Router) RuleExists(tag string) bool {
	rules, _ := r.snapshot()
	return ruleExists(rules, tag)
}

// RemoveRule implements routing.Router.
func (r *Router) RemoveRule(tag string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if tag == "" {
		return errors.New("empty tag name!")
	}
	oldRules, balancers := r.snapshot()
	newRules := make([]*Rule, 0, len(oldRules))
	var removed []*Rule
	for _, rule := range oldRules {
		if rule.RuleTag != tag {
			newRules = append(newRules, rule)
		} else {
			removed = append(removed, rule)
		}
	}
	r.publish(newRules, balancers)
	closeRuleWebhooks(removed)
	return nil
}

// ListRule implements routing.Router
func (r *Router) ListRule() []routing.Route {
	r.mu.Lock()
	defer r.mu.Unlock()
	rules, _ := r.snapshot()
	ruleList := make([]routing.Route, 0, len(rules))
	for _, rule := range rules {
		ruleList = append(ruleList, &Route{
			outboundTag: rule.Tag,
			ruleTag:     rule.RuleTag,
		})
	}
	return ruleList
}

func (r *Router) pickRouteInternal(ctx routing.Context) (*Rule, routing.Context, error) {
	// SkipDNSResolve is set from DNS module.
	// the DOH remote server maybe a domain name,
	// this prevents cycle resolving dead loop
	skipDNSResolve := ctx.GetSkipDNSResolve()

	if r.domainStrategy == Config_IpOnDemand && !skipDNSResolve {
		ctx = routing_dns.ContextWithDNSClient(ctx, r.dns)
	}

	// monolit: снимок правил — перезагрузка не должна менять их посреди выбора
	rules, _ := r.snapshot()
	for _, rule := range rules {
		if rule.Apply(ctx) {
			return rule, ctx, nil
		}
	}

	if r.domainStrategy != Config_IpIfNonMatch || len(ctx.GetTargetDomain()) == 0 || skipDNSResolve {
		return nil, ctx, common.ErrNoClue
	}

	ctx = routing_dns.ContextWithDNSClient(ctx, r.dns)

	// Try applying rules again if we have IPs.
	for _, rule := range rules {
		if rule.Apply(ctx) {
			return rule, ctx, nil
		}
	}

	return nil, ctx, common.ErrNoClue
}

// Start implements common.Runnable.
func (r *Router) Start() error {
	return nil
}

// closeWebhooks closes all webhook notifiers in the current rule set.
func (r *Router) closeWebhooks() {
	rules, _ := r.snapshot()
	closeRuleWebhooks(rules)
}

// Close implements common.Closable.
func (r *Router) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeWebhooks()
	return nil
}

// Type implements common.HasType.
func (*Router) Type() interface{} {
	return routing.RouterType()
}

// GetOutboundGroupTags implements routing.Route.
func (r *Route) GetOutboundGroupTags() []string {
	return r.outboundGroupTags
}

// GetOutboundTag implements routing.Route.
func (r *Route) GetOutboundTag() string {
	return r.outboundTag
}

func (r *Route) GetRuleTag() string {
	return r.ruleTag
}

func init() {
	common.Must(common.RegisterConfig((*Config)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		r := new(Router)
		if err := core.RequireFeatures(ctx, func(d dns.Client, ohm outbound.Manager, dispatcher routing.Dispatcher) error {
			return r.Init(ctx, config.(*Config), d, ohm, dispatcher)
		}); err != nil {
			return nil, err
		}
		return r, nil
	}))
}

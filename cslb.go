package cslb

import (
	"math"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	NodeFailedKey = "node-failed."
	RefreshKey    = "refresh"
)

type LoadBalancer struct {
	service  Service
	strategy Strategy
	option   LoadBalancerOption

	sf       *singleflight.Group
	nodes    *Group
	ttlTimer *time.Timer
}

func NewLoadBalancer(service Service, strategy Strategy, option ...LoadBalancerOption) *LoadBalancer {
	opt := DefaultLoadBalancerOption
	if len(option) > 0 {
		opt = option[0]
	}

	lb := &LoadBalancer{
		service:  service,
		strategy: strategy,
		option:   opt,
		sf:       new(singleflight.Group),
		nodes:    NewGroup(opt.MaxNodeCount),
		ttlTimer: nil,
	}
	<-lb.refresh()

	if lb.option.TTL != TTLUnlimited {
		lb.ttlTimer = time.NewTimer(lb.option.TTL)
	}

	return lb
}

func (lb *LoadBalancer) Next() (Node, error) {
	next, err := lb.strategy.Next()
	if err != nil {
		// Refresh and retry
		<-lb.refresh()
		next, err = lb.strategy.Next()
	}

	// Check TTL
	if lb.ttlTimer != nil {
		select {
		case <-lb.ttlTimer.C:
			// Background refresh
			lb.refresh()
		default:
		}
	}

	return next, err
}

func (lb *LoadBalancer) NodeFailed(node Node) {
	lb.sf.Do(NodeFailedKey+node.String(), func() (interface{}, error) {
		// TODO: allow fail several times before exile
		lb.nodes.Exile(node)
		if fn := lb.service.NodeFailedCallbackFunc(); fn != nil {
			go fn(node)
		}
		nodes := lb.nodes.Get()
		if len(nodes) <= 0 ||
			math.Round(float64(lb.nodes.GetOriginalCount())*lb.option.MinHealthyNodeRatio) > float64(lb.nodes.GetCurrentCount()) {
			<-lb.refresh()
		} else {
			lb.strategy.SetNodes(nodes)
		}
		return nil, nil
	})
}

func (lb *LoadBalancer) refresh() <-chan singleflight.Result {
	return lb.sf.DoChan(RefreshKey, func() (interface{}, error) {
		lb.service.Refresh()

		if lb.ttlTimer != nil {
			select {
			case <-lb.ttlTimer.C:
			default:
			}
			lb.ttlTimer.Reset(lb.option.TTL)
		}

		lb.nodes.Set(lb.service.Nodes())
		lb.strategy.SetNodes(lb.nodes.Get())
		return nil, nil
	})
}

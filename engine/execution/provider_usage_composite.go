package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// CombineProviderUsage admits in order, rolling back earlier reservations when
// a later owner denies. The last owner supplies the wire identity (Goal last).
func CombineProviderUsage(first, second ProviderUsageAdmitter) ProviderUsageAdmitter {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	return &combinedProviderUsage{first: first, second: second}
}

type combinedProviderUsage struct{ first, second ProviderUsageAdmitter }

func (c *combinedProviderUsage) NewLogicalRoundID() string { return c.second.NewLogicalRoundID() }
func (c *combinedProviderUsage) AdmitProviderUsage(ctx context.Context, d ProviderUsageDescriptor) (ProviderUsageCall, error) {
	a, err := c.first.AdmitProviderUsage(ctx, d)
	if err != nil {
		return nil, err
	}
	if a == nil || strings.TrimSpace(a.ProviderCallID()) == "" {
		return nil, ReleaseProviderUsageBeforeDispatch(a, &ProviderUsageTerminalError{Err: fmt.Errorf("provider usage admission returned no call identity")})
	}
	b, err := c.second.AdmitProviderUsage(ctx, d)
	if err != nil {
		return nil, ReleaseProviderUsageBeforeDispatch(a, err)
	}
	if b == nil || strings.TrimSpace(b.ProviderCallID()) == "" {
		err := ReleaseProviderUsageBeforeDispatch(b, &ProviderUsageTerminalError{Err: fmt.Errorf("provider usage admission returned no call identity")})
		return nil, ReleaseProviderUsageBeforeDispatch(a, err)
	}
	return &combinedProviderUsageCall{first: a, second: b}, nil
}

type combinedProviderUsageCall struct{ first, second ProviderUsageCall }

func (c *combinedProviderUsageCall) ProviderCallID() string { return c.second.ProviderCallID() }
func (c *combinedProviderUsageCall) CompleteProviderUsage(u *schema.TokenUsage) error {
	return errors.Join(c.first.CompleteProviderUsage(u), c.second.CompleteProviderUsage(u))
}

func (c *combinedProviderUsageCall) ReleaseProviderUsageBeforeDispatch() error {
	return errors.Join(c.first.ReleaseProviderUsageBeforeDispatch(), c.second.ReleaseProviderUsageBeforeDispatch())
}

func (c *combinedProviderUsageCall) MarkProviderUsageAmbiguous(err error) error {
	return errors.Join(c.first.MarkProviderUsageAmbiguous(err), c.second.MarkProviderUsageAmbiguous(err))
}

func (c *combinedProviderUsageCall) FailClosedOnAmbiguousUsage() bool {
	for _, call := range []ProviderUsageCall{c.first, c.second} {
		policy, ok := call.(interface{ FailClosedOnAmbiguousUsage() bool })
		if !ok || policy.FailClosedOnAmbiguousUsage() {
			return true
		}
	}
	return false
}

func (c *combinedProviderUsageCall) ObserveProviderResponseModel(model string) {
	for _, call := range []ProviderUsageCall{c.first, c.second} {
		if observer, ok := call.(ProviderResponseModelObserver); ok {
			observer.ObserveProviderResponseModel(model)
		}
	}
}

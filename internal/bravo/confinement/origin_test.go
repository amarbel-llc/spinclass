package confinement

import (
	"context"
	"testing"
)

func TestClassifyOriginBridgeIsConfinedWithoutAskingTheServe(t *testing.T) {
	ctx := WithOrigin(context.Background(), CallerOrigin{Transport: TransportBridge, SessionKey: "repo/branch", Unit: "u.service"})
	asked := false
	confined, why := ClassifyOrigin(ctx, func() (bool, string) { asked = true; return false, "serve says unconfined" })
	if !confined || why != BridgedWhy {
		t.Errorf("bridged: got (%v, %q), want confined with %q", confined, why, BridgedWhy)
	}
	if asked {
		t.Error("the serve's own classification was consulted for a bridged call")
	}
}

func TestClassifyOriginStdioAndNoOriginDelegate(t *testing.T) {
	stdio := WithOrigin(context.Background(), CallerOrigin{Transport: TransportStdio})
	for name, ctx := range map[string]context.Context{"none": context.Background(), "stdio": stdio, "nil": nil} {
		for _, want := range []bool{true, false} {
			confined, why := ClassifyOrigin(ctx, func() (bool, string) { return want, "from the serve" })
			if confined != want || why != "from the serve" {
				t.Errorf("%s: got (%v, %q), want (%v, from the serve)", name, confined, why, want)
			}
		}
	}
}

func TestOriginFromIsNotForgeable(t *testing.T) {
	// A context key of the same string value from another package must not
	// read as an origin.
	type foreign struct{}
	ctx := context.WithValue(context.Background(), foreign{}, CallerOrigin{Transport: TransportBridge})
	if _, ok := OriginFrom(ctx); ok {
		t.Error("a foreign context value read as an origin")
	}
}

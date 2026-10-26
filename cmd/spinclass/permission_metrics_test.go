package main

import (
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

func TestPermissionStatsdClientDisabledAndUnset(t *testing.T) {
	t.Setenv("SPINCLASS_DISABLE_STATSD", "")
	t.Run("sweatfile disable beats address", func(t *testing.T) {
		addr, off := "127.0.0.1:9", true
		sf := sweatfile.Sweatfile{Metrics: &sweatfile.Metrics{StatsdAddress: &addr, DisableStatsd: &off}}
		if permissionStatsdClient(sf, true).Enabled() {
			t.Error("client enabled despite disable-statsd")
		}
	})
	t.Run("sweatfile address enables", func(t *testing.T) {
		addr := "127.0.0.1:9"
		sf := sweatfile.Sweatfile{Metrics: &sweatfile.Metrics{StatsdAddress: &addr}}
		if !permissionStatsdClient(sf, true).Enabled() {
			t.Error("client disabled despite statsd-address")
		}
	})
	t.Run("env kill switch", func(t *testing.T) {
		t.Setenv("SPINCLASS_DISABLE_STATSD", "1")
		if permissionStatsdClient(sweatfile.Sweatfile{}, false).Enabled() {
			t.Error("client enabled despite SPINCLASS_DISABLE_STATSD=1")
		}
	})
}

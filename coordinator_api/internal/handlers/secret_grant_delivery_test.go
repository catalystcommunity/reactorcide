package handlers

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestSecretGrantDelivery(t *testing.T) {
	base := SecretGrantRequest{Name: "gh", SecretPathPattern: "org/gh"}

	var g models.SecretGrant
	if err := applySecretGrantRequest(&g, base); err != nil || g.Delivery != models.SecretDeliveryEnv {
		t.Fatalf("default delivery is env: %q %v", g.Delivery, err)
	}

	g = models.SecretGrant{}
	req := base
	req.Delivery = "proxy"
	req.ProxyHosts = []string{" API.GitHub.com ", "raw.githubusercontent.com"}
	req.ProxyHeaderFormat = strPtr("Bearer {}")
	if err := applySecretGrantRequest(&g, req); err != nil {
		t.Fatal(err)
	}
	if g.Delivery != "proxy" || g.ProxyHosts[0] != "api.github.com" || g.ProxyHeaderFormat != "Bearer {}" {
		t.Fatalf("grant %+v", g)
	}

	// A later update without delivery fields keeps them.
	if err := applySecretGrantRequest(&g, SecretGrantRequest{Description: "x"}); err != nil || g.Delivery != "proxy" || len(g.ProxyHosts) != 2 {
		t.Fatalf("update kept %+v %v", g, err)
	}

	bad := []SecretGrantRequest{
		{Name: "a", SecretPathPattern: "p", Delivery: "file"},
		{Name: "a", SecretPathPattern: "p", Delivery: "proxy"},
		{Name: "a", SecretPathPattern: "p", Delivery: "proxy", ProxyHosts: []string{"*"}},
		// A public suffix (Public Suffix List, private section too).
		{Name: "a", SecretPathPattern: "p", Delivery: "proxy", ProxyHosts: []string{"*.githubusercontent.com"}},
		{Name: "a", SecretPathPattern: "p", Delivery: "proxy", ProxyHosts: []string{"localhost"}},
		{Name: "a", SecretPathPattern: "p", Delivery: "proxy", ProxyHosts: []string{"x.com"}, ProxyHeader: strPtr("Host")},
		{Name: "a", SecretPathPattern: "p", Delivery: "proxy", ProxyHosts: []string{"x.com"}, ProxyHeaderFormat: strPtr("Bearer")},
	}
	for i, r := range bad {
		var g models.SecretGrant
		if err := applySecretGrantRequest(&g, r); err == nil {
			t.Errorf("case %d must be refused: %+v", i, g)
		}
	}

	a := models.SecretGrant{Name: "x", Delivery: "env"}
	b := models.SecretGrant{Name: "x"}
	if !secretGrantEquivalent(a, b) {
		t.Fatal("empty delivery equals env")
	}
	b.Delivery, b.ProxyHosts = "proxy", []string{"x.com"}
	if secretGrantEquivalent(a, b) {
		t.Fatal("a delivery change is a change")
	}
}

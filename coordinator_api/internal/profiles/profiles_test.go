package profiles

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestWeakerOrEqual(t *testing.T) {
	parent := &models.ExecutionProfile{DenySecrets: true, RuntimeCapabilities: []string{"gpu"}, AllowedWorkerClasses: []string{"default"}}
	child := &models.ExecutionProfile{DenySecrets: true, RuntimeCapabilities: []string{}, AllowedWorkerClasses: []string{"default"}}
	if err := WeakerOrEqual(child, parent); err != nil {
		t.Fatal(err)
	}
	child.MayRunAsRoot = true
	if err := WeakerOrEqual(child, parent); err == nil {
		t.Fatal("root escalation was accepted")
	}
}

func TestWeakerOrEqualResourceCeilings(t *testing.T) {
	parent := &models.ExecutionProfile{ResourceCeilings: models.JSONB{"cpu_limit": "2", "memory_limit": "2Gi"}}
	child := &models.ExecutionProfile{ResourceCeilings: models.JSONB{"cpu_limit": "1", "memory_limit": "1Gi"}}
	if err := WeakerOrEqual(child, parent); err != nil {
		t.Fatal(err)
	}
	child.ResourceCeilings["memory_limit"] = "3Gi"
	if err := WeakerOrEqual(child, parent); err == nil {
		t.Fatal("a child profile raised its memory ceiling")
	}
	delete(child.ResourceCeilings, "cpu_limit")
	if err := WeakerOrEqual(child, parent); err == nil {
		t.Fatal("a child profile removed its CPU ceiling")
	}
}

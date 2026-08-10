// Package profiles enforces monotonic execution-profile authority.
package profiles

import (
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resources"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func WeakerOrEqual(child, parent *models.ExecutionProfile) error {
	if child == nil || parent == nil {
		return fmt.Errorf("both execution profiles are required")
	}
	if parent.DenySecrets && !child.DenySecrets {
		return fmt.Errorf("child profile enables secrets")
	}
	if !parent.MayRunAsRoot && child.MayRunAsRoot {
		return fmt.Errorf("child profile enables root")
	}
	if !parent.TrustedCacheWrites && child.TrustedCacheWrites {
		return fmt.Errorf("child profile enables trusted cache writes")
	}
	if !subset(child.RuntimeCapabilities, parent.RuntimeCapabilities) {
		return fmt.Errorf("child profile adds runtime capabilities")
	}
	if !subset(child.AllowedWorkerClasses, parent.AllowedWorkerClasses) {
		return fmt.Errorf("child profile adds worker classes")
	}
	if !subset(child.SecretPathAllowlist, parent.SecretPathAllowlist) {
		return fmt.Errorf("child profile adds secret paths")
	}
	if parent.TimeoutCeilingSeconds != nil && (child.TimeoutCeilingSeconds == nil || *child.TimeoutCeilingSeconds > *parent.TimeoutCeilingSeconds) {
		return fmt.Errorf("child profile raises the timeout ceiling")
	}
	if parent.CacheNamespace != nil && (child.CacheNamespace == nil || *child.CacheNamespace != *parent.CacheNamespace) {
		return fmt.Errorf("child profile changes the cache namespace")
	}
	if parent.ArtifactNamespace != nil && (child.ArtifactNamespace == nil || *child.ArtifactNamespace != *parent.ArtifactNamespace) {
		return fmt.Errorf("child profile changes the artifact namespace")
	}
	for _, ceiling := range []struct {
		key   string
		parse func(string) (int64, error)
	}{
		{key: "cpu_request", parse: resources.ParseCPU},
		{key: "cpu_limit", parse: resources.ParseCPU},
		{key: "memory_limit", parse: resources.ParseMemory},
	} {
		parentValue, limited := parent.ResourceCeilings[ceiling.key]
		if !limited {
			continue
		}
		childValue, present := child.ResourceCeilings[ceiling.key]
		if !present {
			return fmt.Errorf("child removes the %s ceiling", ceiling.key)
		}
		parentLimit, err := ceiling.parse(fmt.Sprint(parentValue))
		if err != nil {
			return fmt.Errorf("parent has an invalid %s ceiling", ceiling.key)
		}
		childLimit, err := ceiling.parse(fmt.Sprint(childValue))
		if err != nil {
			return fmt.Errorf("child has an invalid %s ceiling", ceiling.key)
		}
		if childLimit > parentLimit {
			return fmt.Errorf("child raises the %s ceiling", ceiling.key)
		}
	}
	return nil
}

// A nil parent list means that the parent does not apply a limit.
func subset(child, parent []string) bool {
	if parent == nil {
		return true
	}
	allowed := make(map[string]bool, len(parent))
	for _, value := range parent {
		allowed[value] = true
	}
	for _, value := range child {
		if !allowed[value] {
			return false
		}
	}
	return true
}

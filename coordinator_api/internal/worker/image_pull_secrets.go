package worker

import (
	"fmt"
	"regexp"
)

// Job-level image pull secrets are NAMES of Kubernetes Secrets in the job
// namespace, never secret values. Nothing in this file (or anywhere else)
// reads Secret data; the names are only attached to the pod spec's
// imagePullSecrets so the kubelet resolves them.

// k8sSecretNameRe is the RFC 1123 subdomain grammar Kubernetes applies to
// Secret names: lowercase alphanumeric labels separated by dots, hyphens
// allowed inside a label.
var k8sSecretNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

const maxK8sSecretNameLength = 253

// ValidateImagePullSecretNames rejects empty, over-length, non-DNS-subdomain,
// and duplicate names. It runs at every parse/submit boundary; the worker
// re-validates before Kubernetes Job creation so coordinator validation is
// not load-bearing.
func ValidateImagePullSecretNames(names []string) error {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if name == "" {
			return fmt.Errorf("image_pull_secrets must not contain an empty name")
		}
		if len(name) > maxK8sSecretNameLength || !k8sSecretNameRe.MatchString(name) {
			return fmt.Errorf("image_pull_secrets name %q is not a valid Kubernetes Secret name", name)
		}
		if seen[name] {
			return fmt.Errorf("image_pull_secrets contains duplicate name %q", name)
		}
		seen[name] = true
	}
	return nil
}

// EnforceImagePullSecretAllowlist rejects any requested name that is in
// neither the operator's global list (applied to every job pod) nor the
// job-level allowlist. Secure default: with both lists empty, every request
// is rejected.
func EnforceImagePullSecretAllowlist(requested, global, allowed []string) error {
	if len(requested) == 0 {
		return nil
	}
	approved := make(map[string]bool, len(global)+len(allowed))
	for _, name := range global {
		approved[name] = true
	}
	for _, name := range allowed {
		approved[name] = true
	}
	for _, name := range requested {
		if !approved[name] {
			return fmt.Errorf("image pull secret %q is not permitted by the worker allowlist", name)
		}
	}
	return nil
}

// CombineImagePullSecrets returns the operator's global list followed by the
// job-level names, first occurrence wins, order preserved.
func CombineImagePullSecrets(global, jobLevel []string) []string {
	combined := make([]string, 0, len(global)+len(jobLevel))
	seen := make(map[string]bool, len(global)+len(jobLevel))
	for _, name := range append(append([]string{}, global...), jobLevel...) {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		combined = append(combined, name)
	}
	return combined
}

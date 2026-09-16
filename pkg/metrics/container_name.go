package metrics

import (
	"sort"
	"strings"
)

// ShortContainerName is the label used on resource charts. Docker names like
// `peer0.org1.example.com` or the Fabric chaincode `dev-peer…` hash are too
// long for a legend; the peer / role is what you need to see who is hot.
func ShortContainerName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}
	if strings.HasPrefix(name, "dev-peer") {
		rest := strings.TrimPrefix(name, "dev-")
		peer := rest
		if i := strings.Index(rest, "-"); i > 0 {
			peer = rest[:i]
		}
		return "chaincode." + strings.TrimSuffix(peer, ".example.com")
	}
	name = strings.TrimSuffix(name, ".example.com")
	for _, p := range []string{"bench-fabricx-fabricx-", "bench-fabricx-", "bench-neuchain-", "bench-"} {
		if strings.HasPrefix(name, p) {
			return strings.TrimPrefix(name, p)
		}
	}
	return name
}

// SampledContainerNames is the union of ByContainer names, sorted. Empty when
// the samples predate per-container recording (only the summed fields exist).
func SampledContainerNames(samples []SystemSample) []string {
	seen := map[string]struct{}{}
	for _, s := range samples {
		for _, u := range s.ByContainer {
			if u.Name != "" {
				seen[u.Name] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

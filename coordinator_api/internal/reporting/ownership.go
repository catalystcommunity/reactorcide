package reporting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/codeowners"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/subjects"
)

// FileReader reads one file of a repository at a commit. found is false when
// the file does not exist at the commit.
type FileReader interface {
	GetFileContent(ctx context.Context, repo, filePath, ref string) (content []byte, found bool, err error)
}

// LoadedCodeowners is a parsed code owners file of one commit.
type LoadedCodeowners struct {
	// Path is the location the file came from, or "" when the commit has no
	// code owners file.
	Path string
	File *codeowners.File
}

type codeownersEntry struct {
	value   *LoadedCodeowners
	expires time.Time
}

// CodeownersCache caches the parsed code owners file per (repository, base
// SHA). A SHA names fixed content, so the TTL only limits memory and the age
// of a "no file" answer. It is safe for concurrent use.
type CodeownersCache struct {
	mu      sync.Mutex
	entries map[string]codeownersEntry
	now     func() time.Time
	// MaxEntries bounds the cache. 0 means 2000.
	MaxEntries int
}

// DefaultCodeownersCache is the cache of the coordinator process.
var DefaultCodeownersCache = &CodeownersCache{}

func (c *CodeownersCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Load returns the code owners file of the commit, from the cache or from
// the VCS. The first file found in codeowners.Locations wins. A commit with
// no file gives a LoadedCodeowners with Path "" and no error.
func (c *CodeownersCache) Load(ctx context.Context, reader FileReader, provider, repo, sha string, ttl time.Duration) (*LoadedCodeowners, error) {
	if reader == nil {
		return nil, errors.New("the VCS client cannot read repository files")
	}
	if strings.TrimSpace(sha) == "" || strings.TrimSpace(repo) == "" {
		return nil, errors.New("the trusted base SHA is not known")
	}
	if ttl <= 0 {
		ttl = time.Duration(models.DefaultCodeownersCacheTTLSeconds) * time.Second
	}
	key := strings.ToLower(provider) + "\x00" + strings.ToLower(repo) + "\x00" + sha
	now := c.clock()
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && now.Before(entry.expires) {
		c.mu.Unlock()
		return entry.value, nil
	}
	c.mu.Unlock()
	loaded := &LoadedCodeowners{File: &codeowners.File{}}
	for _, location := range codeowners.Locations {
		content, found, err := reader.GetFileContent(ctx, repo, location, sha)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", location, err)
		}
		if found {
			loaded = &LoadedCodeowners{Path: location, File: codeowners.Parse(content)}
			break
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]codeownersEntry{}
	}
	limit := c.MaxEntries
	if limit <= 0 {
		limit = 2000
	}
	if len(c.entries) >= limit {
		for k, entry := range c.entries {
			if !now.Before(entry.expires) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries {
			if len(c.entries) < limit {
				break
			}
			delete(c.entries, k)
		}
	}
	c.entries[key] = codeownersEntry{value: loaded, expires: now.Add(ttl)}
	return loaded, nil
}

// SubjectMapper turns code owners into Reactorcide subjects.
type SubjectMapper struct {
	// Provider is the VCS provider ("github").
	Provider string
	// Repo is "OWNER/NAME". A team of the repository owner gets the owner
	// spelling of the repository, the same as a live team lookup.
	Repo string
	// Store resolves an email owner to a principal (WP2 resolver). nil
	// ignores email owners.
	Store any
	// Warn receives an owner that has no subject.
	Warn func(owner codeowners.Owner, reason string)
}

// Subject returns the subject of one owner. ok is false for an owner that
// has no subject (an email that no principal has).
func (m SubjectMapper) Subject(ctx context.Context, owner codeowners.Owner) (string, bool) {
	provider := strings.ToLower(strings.TrimSpace(m.Provider))
	if provider == "" {
		provider = "github"
	}
	switch owner.Kind {
	case codeowners.OwnerUser:
		return subjects.PrefixVCSUser + provider + "/" + strings.ToLower(owner.Value), true
	case codeowners.OwnerTeam:
		org, slug, _ := strings.Cut(owner.Value, "/")
		if repoOwner, _, ok := strings.Cut(m.Repo, "/"); ok && strings.EqualFold(repoOwner, org) {
			org = repoOwner
		}
		return subjects.PrefixVCSTeam + org + "/" + strings.ToLower(slug), true
	case codeowners.OwnerEmail:
		if subject, ok := m.emailSubject(ctx, owner.Value); ok {
			return subject, true
		}
		if m.Warn != nil {
			m.Warn(owner, "no principal has this email address; the owner is ignored")
		}
	}
	return "", false
}

// emailSubject resolves an email address through the principal resolver.
// The address can be a LinkKeys identity of a principal, or the email of a
// Reactorcide user.
func (m SubjectMapper) emailSubject(ctx context.Context, email string) (string, bool) {
	principalStore, ok := m.Store.(principals.Store)
	if !ok {
		return "", false
	}
	resolver := principals.NewResolver(principalStore, 0)
	email = strings.ToLower(strings.TrimSpace(email))
	resolution, err := resolver.Resolve(ctx, models.IdentityTypeLinkKeys, email)
	if err == nil && resolution != nil && !resolution.Inactive {
		return subjects.Principal(resolution.Principal.PrincipalID), true
	}
	if users, ok := m.Store.(interface {
		GetUserByEmail(ctx context.Context, email string) (*models.User, error)
	}); ok {
		if user, err := users.GetUserByEmail(ctx, email); err == nil && user != nil {
			if resolution, err := resolver.Resolve(ctx, models.IdentityTypeUser, user.UserID); err == nil && resolution != nil && !resolution.Inactive {
				return subjects.Principal(resolution.Principal.PrincipalID), true
			}
		}
	}
	return "", false
}

// Area is the owner set of a path: the pattern of the rule that decided it
// and its owner subjects. No owners means that nobody owns the path.
type Area struct {
	Pattern string   `json:"pattern"`
	Owners  []string `json:"owners"`
	// Tokens are the owners as the code owners file writes them, for display.
	Tokens []string `json:"tokens,omitempty"`
	Source string   `json:"source"`
}

// OwnershipMap finds the owner area of a path. ok is false when no rule
// matches.
type OwnershipMap interface {
	AreaOf(path string) (Area, bool)
}

type codeownersMap struct {
	file  *codeowners.File
	areas []Area
}

// CodeownersMap maps the rules of a code owners file to subjects. Each rule
// maps one time.
func CodeownersMap(ctx context.Context, file *codeowners.File, mapper SubjectMapper) OwnershipMap {
	result := &codeownersMap{file: file}
	if file == nil {
		return result
	}
	cache := map[codeowners.Owner]string{}
	missing := map[codeowners.Owner]bool{}
	for _, rule := range file.Rules {
		area := Area{Pattern: rule.Pattern, Owners: []string{}, Source: "codeowners"}
		for _, owner := range rule.Owners {
			area.Tokens = append(area.Tokens, owner.String())
			if missing[owner] {
				continue
			}
			subject, ok := cache[owner]
			if !ok {
				if subject, ok = mapper.Subject(ctx, owner); !ok {
					missing[owner] = true
					continue
				}
				cache[owner] = subject
			}
			area.Owners = appendUnique(area.Owners, subject)
		}
		result.areas = append(result.areas, area)
	}
	return result
}

func (m *codeownersMap) AreaOf(path string) (Area, bool) {
	if m.file == nil {
		return Area{}, false
	}
	for i := len(m.file.Rules) - 1; i >= 0; i-- {
		if m.file.Rules[i].Matches(path) {
			return m.areas[i], true
		}
	}
	return Area{}, false
}

type registryRule struct {
	area    Area
	matches func(string) bool
}

type registryMap struct{ rules []registryRule }

// RegistryMap is the coordinator path ownership registry. The last matching
// rule wins, the same as in a code owners file.
func RegistryMap(rules []models.PathOwnerRule) OwnershipMap {
	result := &registryMap{}
	for _, rule := range rules {
		matches, err := CompilePattern(rule.Pattern)
		if err != nil {
			continue
		}
		owners := append([]string{}, rule.Owners...)
		result.rules = append(result.rules, registryRule{matches: matches,
			area: Area{Pattern: rule.Pattern, Owners: owners, Tokens: owners, Source: "registry"}})
	}
	return result
}

func (m *registryMap) AreaOf(path string) (Area, bool) {
	for i := len(m.rules) - 1; i >= 0; i-- {
		if m.rules[i].matches(path) {
			return m.rules[i].area, true
		}
	}
	return Area{}, false
}

func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

// OwnerGroup is one set of owners of changed files, for the derived
// required reviews.
type OwnerGroup struct {
	Owners   []string
	Tokens   []string
	Patterns []string
	Files    []string
}

// GroupFilesByOwners groups changed files by the owners of their area. A
// file with no area, or an area with no owners, needs no review.
func GroupFilesByOwners(ownership OwnershipMap, files []string) []OwnerGroup {
	groups := map[string]*OwnerGroup{}
	order := []string{}
	for _, file := range files {
		area, ok := ownership.AreaOf(file)
		if !ok || len(area.Owners) == 0 {
			continue
		}
		owners := append([]string{}, area.Owners...)
		sort.Strings(owners)
		key := strings.Join(owners, "\x00")
		group, exists := groups[key]
		if !exists {
			group = &OwnerGroup{Owners: owners}
			groups[key] = group
			order = append(order, key)
		}
		for _, token := range area.Tokens {
			group.Tokens = appendUnique(group.Tokens, token)
		}
		group.Patterns = appendUnique(group.Patterns, area.Pattern)
		group.Files = append(group.Files, file)
	}
	sort.Strings(order)
	out := make([]OwnerGroup, 0, len(order))
	for _, key := range order {
		out = append(out, *groups[key])
	}
	return out
}

// AllOwnerGroups returns every owner set of a code owners map. It is the
// fail-closed answer when the changed files are not known.
func AllOwnerGroups(ownership OwnershipMap) []OwnerGroup {
	m, ok := ownership.(*codeownersMap)
	if !ok {
		return nil
	}
	groups := map[string]*OwnerGroup{}
	order := []string{}
	for _, area := range m.areas {
		if len(area.Owners) == 0 {
			continue
		}
		owners := append([]string{}, area.Owners...)
		sort.Strings(owners)
		key := strings.Join(owners, "\x00")
		group, exists := groups[key]
		if !exists {
			group = &OwnerGroup{Owners: owners}
			groups[key] = group
			order = append(order, key)
		}
		for _, token := range area.Tokens {
			group.Tokens = appendUnique(group.Tokens, token)
		}
		group.Patterns = appendUnique(group.Patterns, area.Pattern)
	}
	sort.Strings(order)
	out := make([]OwnerGroup, 0, len(order))
	for _, key := range order {
		out = append(out, *groups[key])
	}
	return out
}

// CodeownersReviewPrefix starts the ID of each derived review.
const CodeownersReviewPrefix = "codeowners-"

// ScopeCodeowners is the scope of a derived review.
const ScopeCodeowners = "codeowners"

// DerivedReviewID returns a stable review ID for an owner set. The ID is
// also the label of "/reactorcide accept LABEL": lowercase letters, digits,
// and hyphens, at most 63 characters.
//
// The readable part is a slug of the owner names. Many owner sets have the
// same slug ("acme/a-b", "acme/a.b", and "acme/a_b" all give "acme-a-b"), so
// the ID always ends with a short hash of the full owner set. Two owner sets
// never share an ID unless the hash collides.
func DerivedReviewID(owners []string) string {
	var slug strings.Builder
	for _, owner := range owners {
		value := owner
		if _, rest, ok := strings.Cut(owner, ":"); ok {
			value = rest
		}
		if _, login, ok := strings.Cut(value, "/"); ok && strings.HasPrefix(owner, subjects.PrefixVCSUser) {
			value = login
		}
		if slug.Len() > 0 {
			slug.WriteByte('-')
		}
		for _, c := range strings.ToLower(value) {
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
				slug.WriteRune(c)
			default:
				slug.WriteByte('-')
			}
		}
	}
	text := strings.Trim(collapseHyphens(slug.String()), "-")
	sum := sha256.Sum256([]byte(ownerSetKey(owners)))
	short := hex.EncodeToString(sum[:])[:8]
	if limit := 63 - len(CodeownersReviewPrefix) - 9; len(text) > limit {
		text = strings.Trim(text[:limit], "-")
	}
	if text == "" {
		return CodeownersReviewPrefix + short
	}
	return CodeownersReviewPrefix + text + "-" + short
}

// ownerSetKey is the identity of an owner set: the full subjects, joined.
func ownerSetKey(owners []string) string {
	return strings.Join(owners, "\x00")
}

func collapseHyphens(value string) string {
	for strings.Contains(value, "--") {
		value = strings.ReplaceAll(value, "--", "-")
	}
	return value
}

// DerivedReviews turns owner groups into required reviews. Each review
// applies (no paths): it exists only because a changed file needs it.
func DerivedReviews(groups []OwnerGroup) []models.EffectiveRequiredReview {
	out := make([]models.EffectiveRequiredReview, 0, len(groups))
	seen := map[string]bool{}
	for _, group := range groups {
		key := ownerSetKey(group.Owners)
		if seen[key] {
			continue
		}
		seen[key] = true
		id := DerivedReviewID(group.Owners)
		description := "Code owners " + strings.Join(group.Tokens, ", ") + " for " + strings.Join(limitList(group.Patterns, 5), ", ")
		out = append(out, models.EffectiveRequiredReview{Scope: ScopeCodeowners, RequiredReview: models.RequiredReview{
			ID: id, Description: truncate(description, 300), Accept: append([]string{}, group.Owners...), Count: 1}})
	}
	return out
}

func limitList(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	out := append([]string{}, values[:limit]...)
	return append(out, fmt.Sprintf("%d more", len(values)-limit))
}

// UnreadableCodeownersReview is the review when the code owners file could
// not be read. Nobody can accept it, so the merge stays blocked until a
// later evaluation reads the file (fail closed).
func UnreadableCodeownersReview(reason string) models.EffectiveRequiredReview {
	return models.EffectiveRequiredReview{Scope: ScopeCodeowners, RequiredReview: models.RequiredReview{
		ID: CodeownersReviewPrefix + "unavailable", Description: truncate("The code owners file could not be read: "+reason, 300),
		Accept: []string{}, Count: 1}}
}

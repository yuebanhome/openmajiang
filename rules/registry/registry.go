// Package registry explicitly assembles audited rules into the application.
package registry

import (
	"fmt"
	"regexp"
	"sort"
	"sync"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/mcr"
)

type Registry struct {
	mu    sync.RWMutex
	rules map[string]rulesdk.Rule
}

func New() *Registry { return &Registry{rules: make(map[string]rulesdk.Rule)} }

var artifactPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func Key(id, version string) string { return id + "@" + version }
func (r *Registry) Register(rule rulesdk.Rule) error {
	m := rule.Manifest()
	if m.ID == "" || m.Version == "" || m.APIVersion != "1" || len(m.SeatCounts) == 0 || !artifactPattern.MatchString(m.ArtifactHash) {
		return fmt.Errorf("invalid or incompatible rule manifest")
	}
	for _, n := range m.SeatCounts {
		if n < 2 || n > 16 {
			return fmt.Errorf("unsupported participant count in manifest")
		}
	}
	watch := false
	for _, c := range m.Capabilities {
		if c == "spectator_discard_only@1" {
			watch = true
		}
	}
	if !watch {
		return fmt.Errorf("rule %s must implement discard-only spectator policy", m.ID)
	}
	compiled, err := bindArtifact(rule, m)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := Key(m.ID, m.Version)
	if _, ok := r.rules[k]; ok {
		return fmt.Errorf("rule version already registered: %s", k)
	}
	r.rules[k] = compiled
	return nil
}
func (r *Registry) Get(id, version string) (rulesdk.Rule, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.rules[Key(id, version)]
	return v, ok
}
func (r *Registry) List() []rulesdk.Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]rulesdk.Manifest, 0, len(r.rules))
	for _, v := range r.rules {
		out = append(out, v.Manifest())
	}
	sort.Slice(out, func(i, j int) bool { return Key(out[i].ID, out[i].Version) < Key(out[j].ID, out[j].Version) })
	return out
}
func (r *Registry) RuleMap() map[string]rulesdk.Rule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]rulesdk.Rule, len(r.rules))
	for k, v := range r.rules {
		out[k] = v
	}
	return out
}
func Default() (*Registry, error) {
	r := New()
	if err := r.Register(mcr.New()); err != nil {
		return nil, err
	}
	return r, nil
}

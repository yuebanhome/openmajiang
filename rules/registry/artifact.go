package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

// Rule implementations remain deterministic and do no filesystem I/O. The
// host registry binds them to the exact executable which includes Go and CGO.
// This deliberately refuses recovery after ANY binary change; deploy by drain.
var executableHash = sync.OnceValues(func() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate rule host artifact: %w", err)
	}
	if runtime.GOOS == "linux" {
		path = "/proc/self/exe"
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open rule host artifact: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash rule host artifact: %w", err)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
})

type compiledRule struct {
	rulesdk.Rule
	manifest rulesdk.Manifest
}

func (r compiledRule) Manifest() rulesdk.Manifest { return r.manifest }

func bindArtifact(rule rulesdk.Rule, m rulesdk.Manifest) (rulesdk.Rule, error) {
	digest, err := executableHash()
	if err != nil {
		return nil, err
	}
	if m.SourceHash == "" {
		m.SourceHash = m.ArtifactHash
	}
	m.ArtifactHash = digest
	m.Build = &rulesdk.BuildIdentity{GoVersion: runtime.Version(), OS: runtime.GOOS, Architecture: runtime.GOARCH}
	return compiledRule{Rule: rule, manifest: m}, nil
}

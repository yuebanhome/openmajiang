package mcr

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"sort"
	"sync"
)

// Include the evaluator, its patches and the rule implementation in the build.
// Persisting this digest prevents a restarted host from silently loading changed
// adjudication code under an existing ruleset version. Tests and source notices
// are deliberately included, making the compatibility check conservative.
//
//go:embed *.go scoring
var ruleSource embed.FS

var fingerprint = sync.OnceValue(func() string {
	var paths []string
	if err := fs.WalkDir(ruleSource, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		panic(err)
	}
	sort.Strings(paths)
	h := sha256.New()
	h.Write([]byte("openmajiang.rule-sdk@1\x00"))
	for _, path := range paths {
		b, err := ruleSource.ReadFile(path)
		if err != nil {
			panic(err)
		}
		h.Write([]byte(path))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
})

func ruleFingerprint() string { return fingerprint() }

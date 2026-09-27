package mcr

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	pathutil "path"
	"sort"
	"strings"
	"sync"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

// Include the evaluator, its patches and the rule implementation in the build.
// The source identity includes the actual SDK contract. Tests, documentation
// and generated measurement reports are excluded. The host registry separately
// binds the rule to the SHA256 of the loaded Go/CGO executable.
//
//go:embed *.go scoring
var ruleSource embed.FS

var fingerprint = sync.OnceValue(func() string {
	var paths []string
	if err := fs.WalkDir(ruleSource, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		ext := pathutil.Ext(d.Name())
		if !d.IsDir() && !strings.HasSuffix(d.Name(), "_test.go") && d.Name() != "unit_test.cpp" && (ext == ".go" || ext == ".cpp" || ext == ".h" || ext == ".c" || ext == ".hpp") {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		panic(err)
	}
	sort.Strings(paths)
	h := sha256.New()
	h.Write([]byte("openmajiang.rule-sdk@1\x00"))
	h.Write([]byte(rulesdk.ContractHash() + "\x00"))
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

package rulesdk

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"strings"
	"sync"
)

//go:embed *.go
var contractSource embed.FS

var contractHash = sync.OnceValue(func() string {
	h := sha256.New()
	entries, err := contractSource.ReadDir(".")
	if err != nil {
		panic(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		body, err := contractSource.ReadFile(entry.Name())
		if err != nil {
			panic(err)
		}
		h.Write([]byte(entry.Name() + "\x00"))
		h.Write(body)
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
})

// ContractHash fingerprints the source contract used to compile a rule.
func ContractHash() string { return contractHash() }

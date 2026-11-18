package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"regexp"
)

// buildID fingerprints everything embedded under static/. Pages add it to their script, style and icon URLs,
// so a new deploy always loads matching files, and those files can be cached for a year.
var buildID = func() string {
	h := sha256.New()
	fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, _ := staticFS.ReadFile(p)
		h.Write([]byte(p))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:10]
}()

var assetRe = regexp.MustCompile(`(/static/[A-Za-z0-9._-]+\.(?:js|css|svg))(["'])`)

// versioned adds ?v=buildID to every /static asset URL that sits inside quotes.
func versioned(b []byte) []byte {
	return assetRe.ReplaceAll(b, []byte("${1}?v="+buildID+"${2}"))
}

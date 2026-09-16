package fields

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// update regenerates docs/FIELDS.md instead of comparing against it:
//
//	go test ./internal/fields -update
var update = flag.Bool("update", false, "rewrite docs/FIELDS.md from the field table")

// docPath is the generated reference.
var docPath = filepath.Join("..", "..", "docs", "FIELDS.md")

// TestFieldDocumentationIsCurrent keeps the documentation and the parser in
// lockstep: a field added to the table without regenerating the reference
// fails here rather than silently going undocumented.
func TestFieldDocumentationIsCurrent(t *testing.T) {
	want := Markdown()
	if *update {
		if err := os.WriteFile(docPath, []byte(want), 0o600); err != nil {
			t.Fatalf("write %s: %v", docPath, err)
		}
		t.Logf("wrote %s", docPath)
		return
	}

	got, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v (run: go test ./internal/fields -update)", docPath, err)
	}
	if string(got) != want {
		t.Errorf("%s is out of date; run: go test ./internal/fields -update", docPath)
	}
}

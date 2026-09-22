package mockups

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTwinFixture lays down one 3b-form set: manifest + html + twin per option.
func writeTwinFixture(t *testing.T, proj, slug string, withTwin bool) string {
	t.Helper()
	setDir := filepath.Join(proj, "mockups", slug)
	require.NoError(t, os.MkdirAll(setDir, 0o750))
	opts := []map[string]any{
		{"n": 1, "name": "One", "file": "01-a.html"},
	}
	require.NoError(t, os.WriteFile(filepath.Join(setDir, "01-a.html"), []byte("<html>a</html>"), 0o600))
	if withTwin {
		opts[0]["component"] = "01-a.tsx"
		require.NoError(t, os.WriteFile(filepath.Join(setDir, "01-a.tsx"), []byte("export default function A() { return null }"), 0o600))
	}
	manifest := map[string]any{"slug": slug, "feature": "F", "created": "2026-09-22T09:00:00Z", "options": opts}
	data, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(setDir, "index.json"), data, 0o600))
	return setDir
}

func TestReadSetFile(t *testing.T) {
	proj := t.TempDir()
	writeTwinFixture(t, proj, "twins", true)

	projects := []Project{{Name: "p", Dir: proj}}

	// Listed files read back verbatim, by either manifest field.
	for _, name := range []string{"01-a.html", "01-a.tsx"} {
		b, err := ReadSetFile(projects, "twins", name)
		require.NoError(t, err, name)
		assert.NotEmpty(t, b, name)
	}

	// Unlisted-on-disk, traversal, nesting, empty: all ErrFileNotFound.
	require.NoError(t, os.WriteFile(filepath.Join(proj, "mockups", "twins", "extra.txt"), []byte("x"), 0o600))
	for _, name := range []string{"extra.txt", "../index.json", "sub/01-a.tsx", "..", ""} {
		_, err := ReadSetFile(projects, "twins", name)
		assert.True(t, errors.Is(err, ErrFileNotFound), "%q: want ErrFileNotFound, got %v", name, err)
	}

	// Unknown slug surfaces the set-level error.
	_, err := ReadSetFile(projects, "nope", "01-a.tsx")
	assert.True(t, errors.Is(err, ErrSetNotFound))
}

func TestReadSetFile_NoTwinOptionsHTMLFileOnly(t *testing.T) {
	proj := t.TempDir()
	writeTwinFixture(t, proj, "htmlonly", false)

	// The listed HTML reads; the tsx does not exist and is not listed.
	b, err := ReadSetFile([]Project{{Name: "p", Dir: proj}}, "htmlonly", "01-a.html")
	require.NoError(t, err)
	assert.NotEmpty(t, b)

	_, err = ReadSetFile([]Project{{Name: "p", Dir: proj}}, "htmlonly", "01-a.tsx")
	assert.True(t, errors.Is(err, ErrFileNotFound))
}

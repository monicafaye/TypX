package action

import (
	"testing"

	"github.com/monicafaye/TypX/internal/config"
	"github.com/stretchr/testify/assert"
)

type stubRuntimeFile struct {
	name string
	data []byte
}

func (s stubRuntimeFile) Name() string          { return s.name }
func (s stubRuntimeFile) Data() ([]byte, error) { return s.data, nil }

func TestFiletypeCompleteFallsBackToEmbeddedYAMLWithoutHeaders(t *testing.T) {
	// Simulate a `go install` binary where .hdr files were never generated.
	embeddedYAML := []config.RuntimeFile{
		stubRuntimeFile{
			name: "typst",
			data: []byte("filetype: typst\ndetect:\n  filename: \"\\\\.typ$\"\n"),
		},
		stubRuntimeFile{
			name: "bibtex",
			data: []byte("filetype: bibtex\ndetect:\n  filename: \"\\\\.bib$\"\n"),
		},
		stubRuntimeFile{
			name: "default",
			data: []byte("filetype: unknown\n"),
		},
	}

	chosen, suggestions := completeFiletypes("typ", nil, nil, embeddedYAML)
	assert.Equal(t, "typst", chosen)
	assert.Equal(t, []string{"typst"}, suggestions)

	_, all := completeFiletypes("", nil, nil, embeddedYAML)
	assert.Contains(t, all, "typst")
	assert.Contains(t, all, "bibtex")
	assert.Contains(t, all, "off")
	assert.NotContains(t, all, "unknown")
}

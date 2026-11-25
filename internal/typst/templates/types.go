package templates

import "context"

// SourceKind identifies where a template was discovered.
type SourceKind int

const (
	SourceProject SourceKind = iota
	SourcePersonal
	SourceBuiltin
	SourceLocalPkg
	SourceCachedPkg
	SourceUniverse
)

// String returns a human-readable label for SourceKind.
func (s SourceKind) String() string {
	switch s {
	case SourceProject:
		return "Project"
	case SourcePersonal:
		return "Personal"
	case SourceBuiltin:
		return "Built-in"
	case SourceLocalPkg:
		return "@local"
	case SourceCachedPkg:
		return "Cached @preview"
	case SourceUniverse:
		return "Typst Universe"
	default:
		return "Unknown"
	}
}

// SectionTitle returns the grouped UI header label for SourceKind.
func (s SourceKind) SectionTitle() string {
	switch s {
	case SourceProject, SourcePersonal:
		return "Project & Personal"
	case SourceBuiltin:
		return "Built-in Starters"
	case SourceLocalPkg, SourceCachedPkg:
		return "Local & Cached"
	case SourceUniverse:
		return "Typst Universe"
	default:
		return "Other"
	}
}

// Template describes an offline starter, user template, or Typst package template.
type Template struct {
	ID          string
	Name        string
	Title       string
	Description string
	Category    string
	Version     string
	Source      SourceKind
	IsMultiFile bool
	DirPath     string
	FilePath    string
	Entrypoint  string
	PackageSpec string
	AssetName   string
}

// DisplayTitle returns Title if non-empty, falling back to Name or ID.
func (t Template) DisplayTitle() string {
	if t.Title != "" {
		return t.Title
	}
	if t.Name != "" {
		return t.Name
	}
	return t.ID
}

// IsPackage reports whether this template comes from a Typst @local or @preview package.
func (t Template) IsPackage() bool {
	return t.Source == SourceLocalPkg || t.Source == SourceCachedPkg || t.Source == SourceUniverse
}

// TemplateVars holds placeholder substitutions applied to starter/personal templates.
type TemplateVars struct {
	Title  string
	Author string
	Date   string
}

// DiscoverOptions configures offline template discovery locations.
// Empty fields fall back to default environment/config paths (except ProjectRoot).
type DiscoverOptions struct {
	ProjectRoot      string
	ConfigDir        string
	LocalPackagesDir string
	CachePackagesDir string
}

// ExternalBackend abstracts tinymist template RPCs without importing internal/lsp.
type ExternalBackend interface {
	InitTemplate(ctx context.Context, spec, targetDir string) (entryPath string, err error)
	GetTemplateEntry(ctx context.Context, spec string) (content string, err error)
}

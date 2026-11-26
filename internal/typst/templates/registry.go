package templates

import (
	"os"
	"path/filepath"
	stdruntime "runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/monicafaye/TypX/internal/config"
)

// PackageDirs returns Typst's (localDataDir, cacheDir) honoring environment
// overrides TYPST_PACKAGE_PATH and TYPST_PACKAGE_CACHE_PATH and OS defaults.
func PackageDirs() (localDir, cacheDir string) {
	home, _ := os.UserHomeDir()
	return packageDirsForOS(stdruntime.GOOS, os.Getenv, home)
}

func packageDirsForOS(goos string, getenv func(string) string, home string) (localDir, cacheDir string) {
	switch goos {
	case "windows":
		if appData := getenv("APPDATA"); appData != "" {
			localDir = filepath.Join(appData, "typst", "packages")
		} else if home != "" {
			localDir = filepath.Join(home, "AppData", "Roaming", "typst", "packages")
		}
		if localAppData := getenv("LOCALAPPDATA"); localAppData != "" {
			cacheDir = filepath.Join(localAppData, "typst", "packages")
		} else if home != "" {
			cacheDir = filepath.Join(home, "AppData", "Local", "typst", "packages")
		}
	case "darwin":
		if home != "" {
			localDir = filepath.Join(home, "Library", "Application Support", "typst", "packages")
			cacheDir = filepath.Join(home, "Library", "Caches", "typst", "packages")
		}
	default:
		dataHome := getenv("XDG_DATA_HOME")
		if dataHome == "" && home != "" {
			dataHome = filepath.Join(home, ".local", "share")
		}
		if dataHome != "" {
			localDir = filepath.Join(dataHome, "typst", "packages")
		}
		cacheHome := getenv("XDG_CACHE_HOME")
		if cacheHome == "" && home != "" {
			cacheHome = filepath.Join(home, ".cache")
		}
		if cacheHome != "" {
			cacheDir = filepath.Join(cacheHome, "typst", "packages")
		}
	}

	if envLocal := strings.TrimSpace(getenv("TYPST_PACKAGE_PATH")); envLocal != "" {
		localDir = envLocal
	}
	if envCache := strings.TrimSpace(getenv("TYPST_PACKAGE_CACHE_PATH")); envCache != "" {
		cacheDir = envCache
	}
	return localDir, cacheDir
}

// DiscoverOffline scans project-local, personal, @local, and
// cached @preview templates in precedence order without any network calls.
func DiscoverOffline(opts DiscoverOptions) []Template {
	cfgDir := opts.ConfigDir
	if cfgDir == "" {
		cfgDir = config.ConfigDir
	}
	localPkgDir, cachePkgDir := PackageDirs()
	if opts.LocalPackagesDir != "" {
		localPkgDir = opts.LocalPackagesDir
	}
	if opts.CachePackagesDir != "" {
		cachePkgDir = opts.CachePackagesDir
	}

	var out []Template

	// 1. Project-local templates (.typx/templates/).
	if opts.ProjectRoot != "" {
		projDir := filepath.Join(opts.ProjectRoot, ".typx", "templates")
		out = append(out, scanTemplateDir(projDir, SourceProject, "project/")...)
	}

	// 2. Personal templates (<ConfigDir>/templates/).
	if cfgDir != "" {
		persDir := filepath.Join(cfgDir, "templates")
		out = append(out, scanTemplateDir(persDir, SourcePersonal, "personal/")...)
	}

	// 3. Local @local packages.
	if localPkgDir != "" {
		out = append(out, scanPackageNamespace(filepath.Join(localPkgDir, "local"), "local", SourceLocalPkg)...)
	}

	// 5. Cached @preview packages.
	if cachePkgDir != "" {
		out = append(out, scanPackageNamespace(filepath.Join(cachePkgDir, "preview"), "preview", SourceCachedPkg)...)
	}

	return out
}

func scanTemplateDir(dir string, source SourceKind, idPrefix string) []Template {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var out []Template
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		if !e.IsDir() {
			if strings.ToLower(filepath.Ext(name)) != ".typ" {
				continue
			}
			stem := strings.TrimSuffix(name, filepath.Ext(name))
			raw, err := os.ReadFile(full)
			if err != nil {
				continue
			}
			meta := ParseCommentMetadataFull(string(raw))
			title := meta.Title
			if title == "" {
				title = humanizeSlug(stem)
			}
			cat := meta.Category
			if cat == "" {
				cat = source.String()
			}
			out = append(out, Template{
				ID:          idPrefix + stem,
				Name:        stem,
				Title:       title,
				Description: meta.Description,
				Category:    cat,
				Keywords:    meta.Keywords,
				Source:      source,
				FilePath:    full,
				Entrypoint:  name,
			})
			continue
		}

		// Multi-file template subdirectory containing typst.toml or main.typ.
		tomlPath := filepath.Join(full, "typst.toml")
		if rawToml, err := os.ReadFile(tomlPath); err == nil {
			if man, err := ParseTypstTOML(rawToml); err == nil && man.HasTemplate() {
				entryRel := filepath.ToSlash(man.Template.Entrypoint)
				entryAbs := filepath.Join(full, filepath.FromSlash(man.Template.Path), filepath.FromSlash(man.Template.Entrypoint))
				if _, statErr := os.Stat(entryAbs); statErr == nil {
					title := man.Package.Name
					if title == "" {
						title = humanizeSlug(name)
					}
					cat := source.String()
					if len(man.Package.Categories) > 0 {
						cat = man.Package.Categories[0]
					}
					out = append(out, Template{
						ID:          idPrefix + name,
						Name:        name,
						Title:       title,
						Description: man.Package.Description,
						Category:    cat,
						Categories:  man.Package.Categories,
						Keywords:    man.Package.Keywords,
						Disciplines: man.Package.Disciplines,
						Authors:     man.Package.Authors,
						Version:     man.Package.Version,
						Source:      source,
						IsMultiFile: true,
						DirPath:     filepath.Join(full, filepath.FromSlash(man.Template.Path)),
						FilePath:    entryAbs,
						Entrypoint:  entryRel,
					})
					continue
				}
			}
		}

		mainPath := filepath.Join(full, "main.typ")
		if raw, err := os.ReadFile(mainPath); err == nil {
			meta := ParseCommentMetadataFull(string(raw))
			title := meta.Title
			if title == "" {
				title = humanizeSlug(name)
			}
			cat := meta.Category
			if cat == "" {
				cat = source.String()
			}
			out = append(out, Template{
				ID:          idPrefix + name,
				Name:        name,
				Title:       title,
				Description: meta.Description,
				Category:    cat,
				Keywords:    meta.Keywords,
				Source:      source,
				IsMultiFile: true,
				DirPath:     full,
				FilePath:    mainPath,
				Entrypoint:  "main.typ",
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func scanPackageNamespace(nsDir, nsName string, source SourceKind) []Template {
	pkgEntries, err := os.ReadDir(nsDir)
	if err != nil {
		return nil
	}

	var out []Template
	for _, pkg := range pkgEntries {
		if !pkg.IsDir() || strings.HasPrefix(pkg.Name(), ".") {
			continue
		}
		pkgDir := filepath.Join(nsDir, pkg.Name())
		verEntries, err := os.ReadDir(pkgDir)
		if err != nil {
			continue
		}

		var best *Template
		for _, ver := range verEntries {
			if !ver.IsDir() {
				continue
			}
			verDir := filepath.Join(pkgDir, ver.Name())
			rawToml, err := os.ReadFile(filepath.Join(verDir, "typst.toml"))
			if err != nil {
				continue
			}
			man, err := ParseTypstTOML(rawToml)
			if err != nil || !man.HasTemplate() {
				continue
			}
			pkgName := man.Package.Name
			if pkgName == "" {
				pkgName = pkg.Name()
			}
			pkgVer := man.Package.Version
			if pkgVer == "" {
				pkgVer = ver.Name()
			}
			tmplRoot := filepath.Clean(filepath.Join(verDir, filepath.FromSlash(man.Template.Path)))
			if !isSubpath(verDir, tmplRoot) {
				continue
			}
			entryAbs := filepath.Clean(filepath.Join(tmplRoot, filepath.FromSlash(man.Template.Entrypoint)))
			if !isSubpath(tmplRoot, entryAbs) {
				continue
			}
			if _, err := os.Stat(entryAbs); err != nil {
				continue
			}

			if best != nil && compareSemver(pkgVer, best.Version) <= 0 {
				continue
			}
			cat := nsName
			if len(man.Package.Categories) > 0 {
				cat = man.Package.Categories[0]
			}
			var thumbPath string
			if man.Template.Thumbnail != "" {
				cand := filepath.Clean(filepath.Join(verDir, filepath.FromSlash(man.Template.Thumbnail)))
				if isSubpath(verDir, cand) {
					if fi, err := os.Stat(cand); err == nil && fi.Mode().IsRegular() {
						thumbPath = cand
					}
				}
				if thumbPath == "" {
					cand = filepath.Clean(filepath.Join(tmplRoot, filepath.FromSlash(man.Template.Thumbnail)))
					if isSubpath(tmplRoot, cand) {
						if fi, err := os.Stat(cand); err == nil && fi.Mode().IsRegular() {
							thumbPath = cand
						}
					}
				}
			}
			spec := "@" + nsName + "/" + pkgName + ":" + pkgVer
			best = &Template{
				ID:            spec,
				Name:          pkgName,
				Title:         pkgName + " (" + pkgVer + ")",
				Description:   man.Package.Description,
				Category:      cat,
				Categories:    man.Package.Categories,
				Keywords:      man.Package.Keywords,
				Disciplines:   man.Package.Disciplines,
				Authors:       man.Package.Authors,
				Version:       pkgVer,
				Source:        source,
				IsMultiFile:   true,
				DirPath:       tmplRoot,
				FilePath:      entryAbs,
				Entrypoint:    filepath.ToSlash(man.Template.Entrypoint),
				PackageSpec:   spec,
				ThumbnailPath: thumbPath,
				ThumbnailName: filepath.ToSlash(man.Template.Thumbnail),
			}
		}
		if best != nil {
			out = append(out, *best)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func isSubpath(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func compareSemver(a, b string) int {
	pa := parseSemverParts(a)
	pb := parseSemverParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] > pb[i] {
				return 1
			}
			return -1
		}
	}
	return strings.Compare(a, b)
}

func parseSemverParts(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if dash := strings.IndexByte(v, '-'); dash >= 0 {
		v = v[:dash]
	}
	parts := strings.Split(v, ".")
	var out [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out
}

// ResolveByName finds the best matching template for query following precedence:
// Project > Personal > @local > Cached @preview > Typst Universe.
func ResolveByName(all []Template, query string) (Template, bool) {
	q := strings.TrimSpace(query)
	if q == "" {
		return Template{}, false
	}
	qLower := strings.ToLower(q)

	// Sort copy by Source precedence without mutating caller slice.
	ordered := make([]Template, len(all))
	copy(ordered, all)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Source < ordered[j].Source
	})

	// 1. Exact ID or PackageSpec match when qualified (contains '/' or ':') or checking by precedence.
	if strings.ContainsAny(qLower, "/:") && !strings.HasPrefix(qLower, "@") {
		for _, t := range ordered {
			if strings.ToLower(t.ID) == qLower {
				return t, true
			}
		}
		return Template{}, false
	}

	// 2. Typst @preview/<name>[:ver] or @local/<name>[:ver].
	if strings.HasPrefix(qLower, "@preview/") || strings.HasPrefix(qLower, "@local/") {
		ns := "preview"
		rest := strings.TrimPrefix(qLower, "@preview/")
		if strings.HasPrefix(qLower, "@local/") {
			ns = "local"
			rest = strings.TrimPrefix(qLower, "@local/")
		}
		if colon := strings.IndexByte(rest, ':'); colon >= 0 {
			pkgName := rest[:colon]
			ver := rest[colon+1:]
			for _, t := range ordered {
				if strings.ToLower(t.Name) == pkgName && t.Version == ver &&
					((ns == "local" && t.Source == SourceLocalPkg) || (ns == "preview" && (t.Source == SourceCachedPkg || t.Source == SourceUniverse))) {
					return t, true
				}
			}
			return Template{}, false
		}
		for _, t := range ordered {
			if strings.ToLower(t.Name) == rest &&
				((ns == "local" && t.Source == SourceLocalPkg) || (ns == "preview" && (t.Source == SourceCachedPkg || t.Source == SourceUniverse))) {
				return t, true
			}
		}
		return Template{}, false
	}

	// 3. Unqualified match respecting Source precedence (Project > Personal > @local > ...).
	for _, t := range ordered {
		if strings.ToLower(t.ID) == qLower || strings.ToLower(t.Name) == qLower ||
			(t.PackageSpec != "" && strings.ToLower(t.PackageSpec) == qLower) {
			return t, true
		}
	}

	return Template{}, false
}

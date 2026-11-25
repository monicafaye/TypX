package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/typst/templates"
)

func handleNewFlag(templateName string, args []string, out io.Writer, universeClient *templates.UniverseClient) (openArgs []string, exitNow bool, err error) {
	query := strings.TrimSpace(templateName)
	if query == "" {
		return args, false, nil
	}

	cwd, _ := os.Getwd()
	projectRoot := ""
	if cwd != "" {
		projectRoot = cwd
	}
	offline := templates.DiscoverOffline(templates.DiscoverOptions{
		ProjectRoot: projectRoot,
		ConfigDir:   config.ConfigDir,
	})

	if strings.EqualFold(query, "list") {
		printTemplateList(out, offline)
		return nil, true, nil
	}

	tmpl, ok := templates.ResolveByName(offline, query)
	if !ok {
		if strings.HasPrefix(strings.ToLower(query), "@preview/") {
			if universeClient == nil {
				universeClient = templates.NewUniverseClient()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			pkgName, pkgVer, resErr := templates.ResolveUniverseVersion(ctx, universeClient, query)
			if resErr == nil {
				_, cacheDir := templates.PackageDirs()
				pkgDir, dlErr := universeClient.DownloadAndExtractPreviewPackage(ctx, pkgName, pkgVer, cacheDir)
				if dlErr == nil {
					tmpl, ok = templateFromPackageDir(pkgDir, pkgName, pkgVer)
				} else {
					resErr = dlErr
				}
			}
			cancel()
			if !ok {
				if resErr != nil {
					return nil, false, resErr
				}
				return nil, false, fmt.Errorf("failed to resolve @preview template %q", query)
			}
		} else {
			return nil, false, fmt.Errorf("unknown template %q (run `typx --new list` to view available templates)", query)
		}
	}

	var targetArg string
	var tailArgs []string
	if len(args) > 0 && !strings.HasPrefix(args[0], "+") {
		targetArg = args[0]
		tailArgs = args[1:]
	} else {
		tailArgs = args
		if tmpl.IsMultiFile || tmpl.IsPackage() {
			targetArg = defaultNewSlug(tmpl.Name)
		} else {
			targetArg = defaultNewSlug(tmpl.Name) + ".typ"
		}
	}

	var createdEntry string
	if tmpl.IsMultiFile || tmpl.IsPackage() {
		createdEntry, err = templates.ScaffoldToDir(tmpl, targetArg, templates.TemplateVars{}, false)
		if err != nil {
			return nil, false, err
		}
	} else {
		if info, statErr := os.Stat(targetArg); statErr == nil && info.IsDir() {
			createdEntry, err = templates.ScaffoldToDir(tmpl, targetArg, templates.TemplateVars{}, false)
			if err != nil {
				return nil, false, err
			}
		} else {
			if filepath.Ext(targetArg) == "" {
				targetArg += ".typ"
			}
			if err = templates.ScaffoldToFile(tmpl, targetArg, templates.TemplateVars{}, false); err != nil {
				return nil, false, err
			}
			createdEntry = targetArg
		}
	}

	openArgs = make([]string, 0, 1+len(tailArgs))
	openArgs = append(openArgs, createdEntry)
	openArgs = append(openArgs, tailArgs...)
	return openArgs, false, nil
}

func templateFromPackageDir(pkgDir, pkgName, pkgVer string) (templates.Template, bool) {
	raw, err := os.ReadFile(filepath.Join(pkgDir, "typst.toml"))
	if err != nil {
		return templates.Template{}, false
	}
	man, err := templates.ParseTypstTOML(raw)
	if err != nil || !man.HasTemplate() {
		return templates.Template{}, false
	}
	tmplRoot := filepath.Clean(filepath.Join(pkgDir, filepath.FromSlash(man.Template.Path)))
	entryAbs := filepath.Clean(filepath.Join(tmplRoot, filepath.FromSlash(man.Template.Entrypoint)))
	spec := "@preview/" + pkgName + ":" + pkgVer
	return templates.Template{
		ID:          spec,
		Name:        pkgName,
		Title:       pkgName + " (" + pkgVer + ")",
		Description: man.Package.Description,
		Version:     pkgVer,
		Source:      templates.SourceCachedPkg,
		IsMultiFile: true,
		DirPath:     tmplRoot,
		FilePath:    entryAbs,
		Entrypoint:  filepath.ToSlash(man.Template.Entrypoint),
		PackageSpec: spec,
	}, true
}

func defaultNewSlug(name string) string {
	s := strings.TrimSpace(name)
	if s == "" {
		return "document"
	}
	return s
}

func printTemplateList(out io.Writer, items []templates.Template) {
	if out == nil {
		return
	}
	sections := []string{
		"Project & Personal",
		"Built-in Starters",
		"Local & Cached",
	}
	bySec := make(map[string][]templates.Template)
	for _, it := range items {
		sec := it.Source.SectionTitle()
		bySec[sec] = append(bySec[sec], it)
	}
	printed := false
	for _, sec := range sections {
		list := bySec[sec]
		if len(list) == 0 {
			continue
		}
		if printed {
			fmt.Fprintln(out)
		}
		printed = true
		fmt.Fprintf(out, "%s:\n", sec)
		for _, it := range list {
			key := it.Name
			if it.IsPackage() && it.PackageSpec != "" {
				key = it.PackageSpec
			}
			desc := it.Description
			if desc == "" {
				desc = it.Title
			}
			fmt.Fprintf(out, "  %-28s %s\n", key, desc)
		}
	}
}

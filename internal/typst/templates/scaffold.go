package templates

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrTargetExists is returned when scaffolding or saving would overwrite a
// non-empty destination without force=true.
var ErrTargetExists = errors.New("destination already exists")

var validTemplateNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidateTemplateName checks whether name is a safe file stem for personal templates.
func ValidateTemplateName(name string) error {
	if !validTemplateNameRe.MatchString(name) {
		return fmt.Errorf("invalid template name %q: use letters, digits, '-' or '_' (max 64 chars)", name)
	}
	return nil
}

// ReadEntryContent returns the template's entry Typst source with leading
// TypX metadata comments stripped.
func ReadEntryContent(t Template) (string, error) {
	if t.FilePath == "" {
		return "", fmt.Errorf("template %q has no readable source", t.DisplayTitle())
	}
	raw, err := os.ReadFile(t.FilePath)
	if err != nil {
		return "", err
	}
	_, _, _, body := ParseCommentMetadata(string(raw))
	return body, nil
}

// RenderEntry renders t's entry file with variable substitution (for personal
// and project templates). Package templates are returned verbatim.
func RenderEntry(t Template, vars TemplateVars) (string, error) {
	body, err := ReadEntryContent(t)
	if err != nil {
		return "", err
	}
	if t.IsPackage() {
		return body, nil
	}
	return ApplyVars(body, vars), nil
}

// ScaffoldToFile writes a template's entry file to targetFile.
// Refuses to overwrite an existing non-empty file unless force is true.
func ScaffoldToFile(t Template, targetFile string, vars TemplateVars, force bool) error {
	if targetFile == "" {
		return errors.New("target file path is empty")
	}
	if !force {
		if info, err := os.Stat(targetFile); err == nil {
			if info.IsDir() {
				return fmt.Errorf("%w: %s is a directory", ErrTargetExists, targetFile)
			}
			if info.Size() > 0 {
				return fmt.Errorf("%w: %s already exists and is non-empty", ErrTargetExists, targetFile)
			}
		}
	}

	if vars.Title == "" {
		vars.Title = DeriveTitle(targetFile)
	}
	rendered, err := RenderEntry(t, vars)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(targetFile, []byte(rendered), 0o644)
}

// ScaffoldToDir scaffolds t (single-file or multi-file directory) into targetDir
// and returns the absolute path to the main entrypoint file.
// Refuses if targetDir already exists and contains files unless force is true.
func ScaffoldToDir(t Template, targetDir string, vars TemplateVars, force bool) (string, error) {
	if targetDir == "" {
		return "", errors.New("target directory path is empty")
	}
	absDir, err := filepath.Abs(targetDir)
	if err != nil {
		return "", err
	}

	if !force {
		if info, statErr := os.Stat(absDir); statErr == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("%w: %s exists and is not a directory", ErrTargetExists, absDir)
			}
			entries, readErr := os.ReadDir(absDir)
			if readErr == nil && len(entries) > 0 {
				return "", fmt.Errorf("%w: %s already exists and is non-empty", ErrTargetExists, absDir)
			}
		}
	}

	if vars.Title == "" {
		vars.Title = DeriveTitle(filepath.Join(absDir, "main.typ"))
	}

	if t.IsMultiFile && t.DirPath != "" {
		if err := copyDirContents(t.DirPath, absDir); err != nil {
			return "", err
		}
		entryRel := t.Entrypoint
		if entryRel == "" {
			entryRel = "main.typ"
		}
		entryAbs := filepath.Clean(filepath.Join(absDir, filepath.FromSlash(entryRel)))
		if !isSubpath(absDir, entryAbs) {
			return "", fmt.Errorf("invalid template entrypoint %q", entryRel)
		}
		if !t.IsPackage() {
			if raw, readErr := os.ReadFile(entryAbs); readErr == nil {
				_, _, _, body := ParseCommentMetadata(string(raw))
				rendered := ApplyVars(body, vars)
				if writeErr := os.WriteFile(entryAbs, []byte(rendered), 0o644); writeErr != nil {
					return "", writeErr
				}
			}
		}
		return entryAbs, nil
	}

	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return "", err
	}
	entryRel := t.Entrypoint
	if entryRel == "" || !strings.HasSuffix(strings.ToLower(entryRel), ".typ") {
		entryRel = "main.typ"
	} else if t.Source == SourcePersonal || t.Source == SourceProject {
		entryRel = "main.typ"
	}
	entryAbs := filepath.Join(absDir, filepath.FromSlash(entryRel))
	if err := ScaffoldToFile(t, entryAbs, vars, force); err != nil {
		return "", err
	}
	return entryAbs, nil
}

// SavePersonalTemplate writes content (or only its preamble when preambleOnly is
// true) to <configDir>/templates/<name>.typ.
func SavePersonalTemplate(configDir, name, content string, preambleOnly, force bool) (string, error) {
	if configDir == "" {
		return "", errors.New("configuration directory is not initialized")
	}
	if err := ValidateTemplateName(name); err != nil {
		return "", err
	}

	dir := filepath.Join(configDir, "templates")
	target := filepath.Join(dir, name+".typ")
	if !force {
		if _, err := os.Stat(target); err == nil {
			return "", fmt.Errorf("%w: %s", ErrTargetExists, target)
		}
	}

	body := strings.ReplaceAll(content, "\r\n", "\n")
	if preambleOnly {
		body = ExtractPreamble(body)
	}
	if strings.TrimSpace(body) == "" {
		return "", errors.New("cannot save an empty template")
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		return "", err
	}
	return target, nil
}

func copyDirContents(srcDir, dstDir string) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(srcDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dstDir, rel)
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink inside template directory: %s", rel)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

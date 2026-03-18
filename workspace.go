package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Workspace struct {
	path    string
	initGit bool
}

func NewWorkspace(cfg WorkspaceConfig) *Workspace {
	return &Workspace{
		path:    cfg.Path,
		initGit: cfg.InitGit,
	}
}

func (w *Workspace) Path() string {
	return w.path
}

func (w *Workspace) Init() error {
	if err := os.MkdirAll(w.path, 0o755); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(w.path, ".agentbridge"), 0o755); err != nil {
		return fmt.Errorf("create metadata dir: %w", err)
	}

	if !w.initGit {
		return nil
	}

	if w.isGitRepo() {
		return nil
	}

	if _, err := w.runGit("init"); err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	if _, err := w.runGit("config", "user.name", "AgentBridge"); err != nil {
		return fmt.Errorf("git config user.name: %w", err)
	}
	if _, err := w.runGit("config", "user.email", "agentbridge@local"); err != nil {
		return fmt.Errorf("git config user.email: %w", err)
	}
	keepFile := filepath.Join(w.path, ".agentbridge", ".gitkeep")
	if err := os.WriteFile(keepFile, []byte(""), 0o644); err != nil {
		return fmt.Errorf("write metadata keep file: %w", err)
	}
	if _, err := w.runGit("add", ".agentbridge/.gitkeep"); err != nil {
		return fmt.Errorf("git add initial file: %w", err)
	}
	if _, err := w.runGit("commit", "-m", "Initial workspace"); err != nil {
		return fmt.Errorf("git initial commit: %w", err)
	}
	return nil
}

func (w *Workspace) isGitRepo() bool {
	_, err := w.runGit("rev-parse", "--is-inside-work-tree")
	return err == nil
}

func (w *Workspace) runGit(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = w.path
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (w *Workspace) CaptureChangedFiles() ([]string, error) {
	if !w.isGitRepo() {
		return nil, nil
	}
	output, err := w.runGit("status", "--porcelain")
	if err != nil {
		return nil, err
	}
	if output == "" {
		return nil, nil
	}
	lines := strings.Split(output, "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(line) < 4 {
			continue
		}
		files = append(files, strings.TrimSpace(line[3:]))
	}
	sort.Strings(files)
	return files, nil
}

func (w *Workspace) CommitTask(agentName, taskTitle, taskID string, allowedFiles []string) (string, []string, error) {
	if !w.isGitRepo() {
		return "", nil, nil
	}
	pathspec := normalizeWorkspacePaths(allowedFiles)
	if len(pathspec) == 0 {
		return "", nil, nil
	}
	addArgs := append([]string{"add", "-A", "--"}, pathspec...)
	if _, err := w.runGit(addArgs...); err != nil {
		return "", nil, err
	}
	diffArgs := append([]string{"diff", "--cached", "--name-only", "--"}, pathspec...)
	cachedFilesOutput, err := w.runGit(diffArgs...)
	if err != nil {
		return "", nil, err
	}
	cachedFiles := splitNonEmptyLines(cachedFilesOutput)
	if len(cachedFiles) == 0 {
		return "", nil, nil
	}
	message := fmt.Sprintf("[agentbridge] %s: %s (task:%s)", agentName, taskTitle, taskID)
	commitArgs := append([]string{"commit", "-m", message, "--"}, cachedFiles...)
	if _, err := w.runGit(commitArgs...); err != nil {
		return "", nil, err
	}
	hash, err := w.runGit("rev-parse", "HEAD")
	if err != nil {
		return "", nil, err
	}
	return hash, cachedFiles, nil
}

func (w *Workspace) Diff() (string, error) {
	if !w.isGitRepo() {
		return "", nil
	}
	return w.runGit("diff", "HEAD")
}

func (w *Workspace) ListFiles() ([]string, error) {
	files := make([]string, 0)
	err := filepath.WalkDir(w.path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(w.path, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == ".agentbridge" || strings.HasPrefix(rel, ".agentbridge"+string(filepath.Separator)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func (w *Workspace) ReadFile(relPath string) ([]byte, error) {
	fullPath, err := w.resolvePath(relPath)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(fullPath)
}

func (w *Workspace) WriteFile(relPath string, content io.Reader) (string, error) {
	fullPath, err := w.resolvePath(relPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return "", fmt.Errorf("create parent dir: %w", err)
	}
	data, err := io.ReadAll(content)
	if err != nil {
		return "", fmt.Errorf("read upload: %w", err)
	}
	if err := os.WriteFile(fullPath, data, 0o644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	rel, err := filepath.Rel(w.path, fullPath)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func (w *Workspace) resolvePath(relPath string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(relPath))
	if cleaned == "." || cleaned == "" {
		return "", errors.New("file path is required")
	}
	fullPath := filepath.Join(w.path, cleaned)
	rel, err := filepath.Rel(w.path, fullPath)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid path")
	}
	return fullPath, nil
}

func splitNonEmptyLines(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, "\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func normalizeWorkspacePaths(paths []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path == "" || path == "." {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

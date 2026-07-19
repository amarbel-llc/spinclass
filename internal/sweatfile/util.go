package sweatfile

import (
	"os"
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/git"
)

func fileExists(path string) (os.FileInfo, bool) {
	info, err := os.Stat(path)
	return info, err == nil
}

func gitCommonDir(worktreePath string) (string, error) {
	rel, err := git.Run(worktreePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(rel) {
		rel = filepath.Join(worktreePath, rel)
	}
	return filepath.Clean(rel), nil
}

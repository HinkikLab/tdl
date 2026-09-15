package up

import (
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/iyear/tdl/core/util/fsutil"
	"github.com/iyear/tdl/pkg/consts"
	"github.com/iyear/tdl/pkg/filterMap"
)

func walk(paths, includes, excludes []string) ([]*File, error) {
	files := make([]*File, 0)
	seen := make(map[string]struct{})

	normalizeExt := func(ext string) string { return strings.ToLower(fsutil.AddPrefixDot(ext)) }
	includesMap := filterMap.New(includes, normalizeExt)
	excludesMap := filterMap.New(excludes, normalizeExt)
	excludesMap[consts.UploadThumbExt] = struct{}{} // ignore thumbnail files

	for _, path := range paths {
		err := filepath.WalkDir(path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}

			// process include and exclude
			ext := strings.ToLower(filepath.Ext(path))
			if _, ok := includesMap[ext]; len(includesMap) > 0 && !ok {
				return nil
			}
			if _, ok := excludesMap[ext]; len(excludesMap) > 0 && ok {
				return nil
			}

			absolute, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			key := filepath.Clean(absolute)
			if runtime.GOOS == "windows" {
				key = strings.ToLower(key)
			}
			if _, ok := seen[key]; ok {
				return nil
			}
			seen[key] = struct{}{}

			f := File{File: path}
			t := strings.TrimSuffix(path, filepath.Ext(path)) + consts.UploadThumbExt
			if fsutil.PathExists(t) {
				f.Thumb = t
			}

			files = append(files, &f)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return files, nil
}

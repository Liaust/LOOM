package applicationbuild

import (
	"context"
	"io"
	"os"
	"path"
	"strings"
)

func discoverRuntime(ctx context.Context, root *os.Root, module string, maxFiles int) ([]string, error) {
	top, _, _ := strings.Cut(module, ".")
	if !safeRuntimePath(top + ".py") {
		return nil, ErrUnsafeSource
	}
	info, err := root.Lstat(top)
	if os.IsNotExist(err) {
		if strings.Contains(module, ".") {
			return nil, ErrInvalidInput
		}
		return []string{top + ".py"}, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !safeRuntimePath(top+"/runtime.py") {
		return nil, ErrUnsafeSource
	}
	var result []string
	entries := 0
	var walk func(*os.Root, string, string) error
	walk = func(parent *os.Root, leaf, relative string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		before, err := parent.Lstat(leaf)
		if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafeSource
		}
		directory, err := parent.OpenRoot(leaf)
		if err != nil {
			return ErrUnsafeSource
		}
		defer directory.Close()
		after, err := directory.Stat(".")
		if err != nil || !os.SameFile(before, after) {
			return ErrSourceChanged
		}
		listing, err := directory.Open(".")
		if err != nil {
			return ErrUnsafeSource
		}
		defer listing.Close()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			batch, err := listing.ReadDir(128)
			if err != nil && err != io.EOF {
				return ErrUnsafeSource
			}
			for _, entry := range batch {
				entries++
				if entries > maxFiles*8+128 {
					return ErrUnsafeSource
				}
				name := path.Join(relative, entry.Name())
				if entry.Type()&os.ModeSymlink != 0 {
					if safeRuntimePath(name) {
						return ErrUnsafeSource
					}
					continue
				}
				if entry.IsDir() {
					if safeRuntimePath(name + "/runtime.py") {
						if err := walk(directory, entry.Name(), name); err != nil {
							return err
						}
					}
				} else if safeRuntimePath(name) {
					if !entry.Type().IsRegular() {
						return ErrUnsafeSource
					}
					result = append(result, name)
					if len(result) > maxFiles {
						return ErrUnsafeSource
					}
				}
			}
			if err == io.EOF {
				break
			}
		}
		return nil
	}
	if err := walk(root, top, top); err != nil {
		return nil, err
	}
	return result, nil
}

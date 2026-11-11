package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"loom.local/loom/internal/applicationlaunch"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	action, config := "", ""
	if len(os.Args) == 2 {
		config = os.Args[1]
	} else if len(os.Args) == 4 && os.Args[1] == "--action" {
		action, config = os.Args[2], os.Args[3]
	} else {
		return fmt.Errorf("expected resolved-config file or --action name resolved-config file")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return err
	}
	root := filepath.Dir(filepath.Dir(self))
	f, err := os.Open(filepath.Join(root, "share/loom/launch.json"))
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 512*1024 {
		return fmt.Errorf("invalid launch metadata file")
	}
	raw := make([]byte, info.Size())
	if _, err := f.ReadAt(raw, 0); err != nil {
		return err
	}
	m, err := applicationlaunch.Decode(raw)
	if err != nil {
		return err
	}
	program, argv, err := applicationlaunch.Resolve(m, action, config)
	if err != nil {
		return err
	}
	if info, err := os.Stat(config); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("resolved-config file is unavailable")
	}
	env := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "LOOM_CONFIG_FILE=") {
			env = append(env, value)
		}
	}
	env = append(env, "LOOM_CONFIG_FILE="+config)
	return syscall.Exec(program, argv, env)
}

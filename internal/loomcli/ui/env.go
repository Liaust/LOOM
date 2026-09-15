package ui

import "os"

type Env map[string]string

func FromOS() Env {
	env := Env{}
	for _, key := range []string{
		"CI",
		"COLORTERM",
		"HOME",
		"LOOM_NO_ANIMATION",
		"LOOM_NO_COLOR",
		"LOOM_NONINTERACTIVE",
		"LOOM_THEME",
		"NO_COLOR",
		"TERM",
		"XDG_CONFIG_HOME",
	} {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	return env
}

func (e Env) Get(key string) string {
	if e == nil {
		return ""
	}
	return e[key]
}

func (e Env) IsSet(key string) bool {
	if e == nil {
		return false
	}
	_, ok := e[key]
	return ok
}

package session

import (
	"os"
	"strings"
)

// EnvVarManager manages session-scoped environment variables.
// Hooks can set environment variables that persist for the duration
// of the session and are inherited by subprocesses.
//
// Reference: src/utils/sessionEnvVars.ts (22 lines) +
//
//	src/utils/sessionEnvironment.ts (166 lines)
type EnvVarManager struct {
	vars     map[string]string
	original map[string]string
}

// NewEnvVarManager creates a new session env var manager.
func NewEnvVarManager() *EnvVarManager {
	return &EnvVarManager{
		vars:     make(map[string]string),
		original: make(map[string]string),
	}
}

// Set sets a session-scoped environment variable.
func (m *EnvVarManager) Set(key, value string) {
	if _, exists := m.original[key]; !exists {
		m.original[key] = os.Getenv(key)
	}
	m.vars[key] = value
	_ = os.Setenv(key, value)
}

// Get returns a session env var value.
func (m *EnvVarManager) Get(key string) string {
	if v, ok := m.vars[key]; ok {
		return v
	}
	return os.Getenv(key)
}

// Restore restores all modified environment variables to their original values.
func (m *EnvVarManager) Restore() {
	for key, orig := range m.original {
		if orig == "" {
			_ = os.Unsetenv(key)
		} else {
			_ = os.Setenv(key, orig)
		}
	}
	m.vars = make(map[string]string)
	m.original = make(map[string]string)
}

// Environ returns the session environment as a slice of "KEY=VALUE" strings,
// suitable for exec.Cmd.Env.
func (m *EnvVarManager) Environ() []string {
	env := os.Environ()
	for key, val := range m.vars {
		found := false
		prefix := key + "="
		for i, e := range env {
			if strings.HasPrefix(e, prefix) {
				env[i] = key + "=" + val
				found = true
				break
			}
		}
		if !found {
			env = append(env, key+"="+val)
		}
	}
	return env
}

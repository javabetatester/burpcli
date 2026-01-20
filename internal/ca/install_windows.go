package ca

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func installRootCAWindows(st *Store, scope InstallScope) (string, error) {
	thumb := st.RootThumbprintSHA1()
	if thumb == "" {
		return "", fmt.Errorf("thumbprint vazio")
	}

	der := st.RootCertDER()
	if len(der) == 0 {
		return "", fmt.Errorf("cert der vazio")
	}

	path := filepath.Join(st.Dir, "ca.cer")
	if err := os.WriteFile(path, der, 0o644); err != nil {
		return "", err
	}

	args := []string{"-user", "-addstore", "Root", path}
	if scope != ScopeCurrentUser {
		args = []string{"-addstore", "Root", path}
	}

	cmd := exec.Command("certutil", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return thumb, fmt.Errorf("certutil falhou: %w\n%s", err, string(out))
	}
	return thumb, nil
}

func uninstallRootCAWindows(st *Store, scope InstallScope) (string, error) {
	thumb := st.RootThumbprintSHA1()
	if thumb == "" {
		return "", fmt.Errorf("thumbprint vazio")
	}

	args := []string{"-user", "-delstore", "Root", thumb}
	if scope != ScopeCurrentUser {
		args = []string{"-delstore", "Root", thumb}
	}

	cmd := exec.Command("certutil", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return thumb, fmt.Errorf("certutil falhou: %w\n%s", err, string(out))
	}
	return thumb, nil
}

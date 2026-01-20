package ca

import (
	"fmt"
	"runtime"
)

type InstallScope int

const (
	ScopeCurrentUser InstallScope = iota
)

func InstallRootCA(dir string, scope InstallScope) (thumbprint string, err error) {
	st, err := LoadOrCreate(dir)
	if err != nil {
		return "", err
	}

	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("auto-instalação suportada só no Windows por enquanto")
	}
	return installRootCAWindows(st, scope)
}

func UninstallRootCA(dir string, scope InstallScope) (thumbprint string, err error) {
	st, err := LoadOrCreate(dir)
	if err != nil {
		return "", err
	}

	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("auto-desinstalação suportada só no Windows por enquanto")
	}
	return uninstallRootCAWindows(st, scope)
}

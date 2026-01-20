package app

import (
	"fmt"
	"os"

	"burpui/internal/ca"
)

func ExportCA(caDir string, outPath string) error {
	st, err := ca.LoadOrCreate(caDir)
	if err != nil {
		return err
	}
	if outPath == "" {
		return fmt.Errorf("caminho vazio")
	}
	return os.WriteFile(outPath, st.RootCertPEM(), 0o644)
}

func InstallCA(caDir string) (string, error) {
	return ca.InstallRootCA(caDir, ca.ScopeCurrentUser)
}

func UninstallCA(caDir string) (string, error) {
	return ca.UninstallRootCA(caDir, ca.ScopeCurrentUser)
}

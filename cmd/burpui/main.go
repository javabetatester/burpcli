package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"burpui/internal/app"
)

func main() {
	var listenAddr string
	var maxBodyBytes int
	var mitm bool
	var caDir string
	var exportCA string
	var installCA bool
	var uninstallCA bool

	flag.StringVar(&listenAddr, "listen", ":8080", "endereço do proxy (ex: :8080)")
	flag.IntVar(&maxBodyBytes, "max-body", 4<<20, "máximo de bytes capturados por body")
	flag.BoolVar(&mitm, "mitm", false, "habilita MITM HTTPS (requer instalar o CA)")
	flag.StringVar(&caDir, "ca-dir", filepath.Join(".", "ca"), "diretório para armazenar o CA")
	flag.StringVar(&exportCA, "export-ca", "", "exporta o certificado raiz (PEM) e sai")
	flag.BoolVar(&installCA, "install-ca", false, "instala o CA no Trusted Root (CurrentUser) e sai")
	flag.BoolVar(&uninstallCA, "uninstall-ca", false, "remove o CA do Trusted Root (CurrentUser) e sai")
	flag.Parse()

	if exportCA != "" {
		if err := app.ExportCA(caDir, exportCA); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		fmt.Fprintln(os.Stdout, exportCA)
		return
	}

	if installCA {
		thumb, err := app.InstallCA(caDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		fmt.Fprintf(os.Stdout, "instalado (thumbprint): %s\n", thumb)
		return
	}

	if uninstallCA {
		thumb, err := app.UninstallCA(caDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		fmt.Fprintf(os.Stdout, "removido (thumbprint): %s\n", thumb)
		return
	}

	if err := app.Run(app.Config{ListenAddr: listenAddr, MaxBodyBytes: maxBodyBytes, MITM: mitm, CADir: caDir}); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

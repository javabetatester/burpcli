package main

import (
	"flag"
	"fmt"
	"os"

	"burpui/internal/app"
)

func main() {
	var listenAddr string
	var maxBodyBytes int

	flag.StringVar(&listenAddr, "listen", ":8080", "endereço do proxy (ex: :8080)")
	flag.IntVar(&maxBodyBytes, "max-body", 4<<20, "máximo de bytes capturados por body")
	flag.Parse()

	if err := app.Run(app.Config{ListenAddr: listenAddr, MaxBodyBytes: maxBodyBytes}); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

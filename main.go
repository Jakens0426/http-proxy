package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"

	"http-proxy/core"
	"http-proxy/server"
)

var Version = "1.0.0"

//go:embed webui/dist
var webUI embed.FS

func main() {
	versionFlag := flag.Bool("version", false, "print version and exit")
	helpFlag := flag.Bool("help", false, "print usage")
	flag.Parse()

	if *versionFlag {
		fmt.Println("http-proxy version", Version)
		os.Exit(0)
	}
	if *helpFlag {
		fmt.Println("代理中转器 - embedded sing-box proxy pool")
		fmt.Println()
		fmt.Println("Usage: http-proxy [options]")
		fmt.Println()
		flag.PrintDefaults()
		fmt.Println()
		fmt.Println("代理中转器 UI: http://127.0.0.1:9090")
		os.Exit(0)
	}

	store := core.NewStore(core.DatabaseFile)
	defer store.Close()
	if err := store.Load(); err != nil {
		log.Fatalf("could not load database %s: %v", core.DatabaseFile, err)
	}
	log.Printf("Loaded %d subscriptions from %s", len(store.GetSubscriptions()), core.DatabaseFile)

	subMgr := core.NewSubscriptionManager(store)
	svc := NewService(subMgr)

	totalProxies := len(subMgr.GetAllProxies())
	log.Printf("Total proxies from all subscriptions: %d", totalProxies)

	distFS, err := fs.Sub(webUI, "webui/dist")
	if err != nil {
		log.Fatalf("could not load embedded web UI: %v", err)
	}

	srv := server.NewServer(svc, distFS)
	addr := "127.0.0.1:9090"
	log.Printf("代理中转器 UI: http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, srv))
}

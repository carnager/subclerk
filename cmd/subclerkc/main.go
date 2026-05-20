package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/carnager/subclerk/internal/shared"
)

const usage = `Usage: subclerkc <command>

Commands:
  prev      Previous track
  toggle    Toggle play/pause
  stop      Stop playback
  next      Next track
  update    Rebuild library cache
  status    Show current playback status
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	cmd := os.Args[1]

	var method, path string
	switch cmd {
	case "prev":
		method, path = "POST", "playback/prev"
	case "toggle":
		method, path = "POST", "playback/play"
	case "stop":
		method, path = "POST", "playback/stop"
	case "next":
		method, path = "POST", "playback/next"
	case "update":
		method, path = "POST", "cache/update"
	case "status":
		method, path = "GET", "playback/status"
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	baseURL, useLocal, socketPath, err := shared.APIBaseURLFromAddress("local")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var client *http.Client
	if useLocal {
		client = shared.NewLocalHTTPClient(5*time.Second, socketPath)
	} else {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	url := baseURL + "/" + path
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if cmd == "status" {
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		fmt.Print(string(buf[:n]))
	}
}

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/carnager/subclerk/internal/shared"
)

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

type agentConfig struct {
	Agent struct {
		Name       string `toml:"name"`
		Bind       string `toml:"bind"`
		Master     string `toml:"master"`
		Format     string `toml:"format"`
		MaxBitRate int    `toml:"max_bitrate"`
	} `toml:"agent"`
	MPV struct {
		Socket     string `toml:"socket"`
		Executable string `toml:"executable"`
	} `toml:"mpv"`
}

// ---------------------------------------------------------------------------
// mpv IPC (duplicated from subclerkd for standalone binary)
// ---------------------------------------------------------------------------

type mpvRequest struct {
	Command   []any `json:"command"`
	RequestID int   `json:"request_id"`
}

type mpvResponse struct {
	Data      any    `json:"data"`
	Error     string `json:"error"`
	RequestID int    `json:"request_id"`
}

type mpvClient struct {
	socketPath string
	executable string
	mu         sync.Mutex
	process    *exec.Cmd
	reqID      int
}

func (m *mpvClient) isRunning() bool {
	conn, err := net.DialTimeout("unix", m.socketPath, 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (m *mpvClient) start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(m.socketPath), 0o755); err != nil {
		return err
	}
	_ = os.Remove(m.socketPath)

	cmd := exec.Command(m.executable, "--idle", "--no-video", "--no-terminal",
		"--input-ipc-server="+m.socketPath)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	m.process = cmd

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(m.socketPath); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("mpv socket did not appear at %s", m.socketPath)
}

func (m *mpvClient) command(args ...any) (*mpvResponse, error) {
	m.mu.Lock()
	m.reqID++
	reqID := m.reqID
	m.mu.Unlock()

	conn, err := net.DialTimeout("unix", m.socketPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("mpv connect: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	req := mpvRequest{Command: args, RequestID: reqID}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("mpv write: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var resp mpvResponse
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			continue
		}
		if resp.RequestID == reqID {
			if resp.Error != "" && resp.Error != "success" {
				return nil, fmt.Errorf("mpv: %s", resp.Error)
			}
			return &resp, nil
		}
	}
	return nil, fmt.Errorf("mpv: no response")
}

func (m *mpvClient) getProperty(name string) (any, error) {
	resp, err := m.command("get_property", name)
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (m *mpvClient) setProperty(name string, value any) error {
	_, err := m.command("set_property", name, value)
	return err
}

func (m *mpvClient) loadFile(url string, mode string) error {
	_, err := m.command("loadfile", url, mode)
	return err
}

func (m *mpvClient) playlistClear() error {
	_, err := m.command("playlist-clear")
	return err
}

func (m *mpvClient) playlistRemove(index int) error {
	_, err := m.command("playlist-remove", index)
	return err
}

// ---------------------------------------------------------------------------
// Agent
// ---------------------------------------------------------------------------

type agent struct {
	cfg    agentConfig
	logger *log.Logger
	mpv    *mpvClient
}

func main() {
	logger := log.New(os.Stdout, "subclerk-agent: ", log.LstdFlags)

	cfg, err := loadAgentConfig()
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}

	a := &agent{
		cfg:    cfg,
		logger: logger,
		mpv: &mpvClient{
			socketPath: cfg.MPV.Socket,
			executable: cfg.MPV.Executable,
		},
	}

	go a.ensureMPV()
	go a.registerAndHeartbeat()

	logger.Printf("starting agent %q on %s (master: %s)", cfg.Agent.Name, cfg.Agent.Bind, cfg.Agent.Master)
	if err := http.ListenAndServe(cfg.Agent.Bind, a.routes()); err != nil {
		logger.Fatalf("listen: %v", err)
	}
}

func loadAgentConfig() (agentConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return agentConfig{}, err
	}
	xdgConfig := shared.Getenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	configPath := filepath.Join(xdgConfig, "subclerk", "subclerk-agent.toml")

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return agentConfig{}, err
	}

	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(configPath, []byte(defaultAgentConfig()), 0o644); err != nil {
			return agentConfig{}, err
		}
	}

	var cfg agentConfig
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		return agentConfig{}, err
	}

	// Apply defaults
	if cfg.Agent.Name == "" {
		hostname, _ := os.Hostname()
		cfg.Agent.Name = hostname
	}
	if cfg.Agent.Bind == "" {
		cfg.Agent.Bind = "0.0.0.0:6702"
	}
	if cfg.Agent.Master == "" {
		cfg.Agent.Master = "localhost:6701"
	}
	if cfg.MPV.Socket == "" {
		runtimeDir := shared.Getenv("XDG_RUNTIME_DIR", filepath.Join(os.TempDir(), fmt.Sprintf("subclerk-%d", os.Getuid())))
		cfg.MPV.Socket = filepath.Join(runtimeDir, "subclerk", "agent-mpv.sock")
	}
	if cfg.MPV.Executable == "" {
		cfg.MPV.Executable = "mpv"
	}

	return cfg, nil
}

func defaultAgentConfig() string {
	hostname, _ := os.Hostname()
	return `[agent]
name = "` + hostname + `"
bind = "0.0.0.0:6702"
master = "localhost:6701"
format = ""
max_bitrate = 0

[mpv]
socket = ""
executable = "mpv"
`
}

func (a *agent) ensureMPV() {
	for {
		if a.mpv.isRunning() {
			time.Sleep(5 * time.Second)
			continue
		}
		a.logger.Printf("mpv: starting idle instance")
		if err := a.mpv.start(); err != nil {
			a.logger.Printf("mpv: start failed: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		a.logger.Printf("mpv: started, ipc at %s", a.mpv.socketPath)
		time.Sleep(5 * time.Second)
	}
}

func (a *agent) registerAndHeartbeat() {
	// Wait a bit for the HTTP server to start
	time.Sleep(2 * time.Second)

	client := &http.Client{Timeout: 5 * time.Second}

	for {
		agentID := a.register(client)
		a.heartbeatLoop(client, agentID)
		// If heartbeatLoop returns, the server is gone — re-register
		a.logger.Printf("lost connection to master, re-registering in 5s")
		time.Sleep(5 * time.Second)
	}
}

func (a *agent) register(client *http.Client) string {
	regBody, _ := json.Marshal(map[string]any{
		"name":        a.cfg.Agent.Name,
		"address":     a.cfg.Agent.Bind,
		"format":      a.cfg.Agent.Format,
		"max_bitrate": a.cfg.Agent.MaxBitRate,
	})

	for {
		resp, err := client.Post(
			"http://"+a.cfg.Agent.Master+"/api/v1/devices/register",
			"application/json",
			bytes.NewReader(regBody),
		)
		if err != nil {
			a.logger.Printf("registration failed: %v (retrying in 5s)", err)
			time.Sleep(5 * time.Second)
			continue
		}
		var result map[string]string
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		agentID := result["id"]
		a.logger.Printf("registered with master as %q (id=%s)", a.cfg.Agent.Name, agentID)
		return agentID
	}
}

func (a *agent) heartbeatLoop(client *http.Client, agentID string) {
	failures := 0
	for {
		time.Sleep(10 * time.Second)
		hbBody, _ := json.Marshal(map[string]string{"id": agentID})
		resp, err := client.Post(
			"http://"+a.cfg.Agent.Master+"/api/v1/devices/heartbeat",
			"application/json",
			bytes.NewReader(hbBody),
		)
		if err != nil {
			failures++
			a.logger.Printf("heartbeat failed (%d/3): %v", failures, err)
			if failures >= 3 {
				return // trigger re-registration
			}
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			// Server doesn't know us anymore (restarted) — re-register
			a.logger.Printf("server lost our registration, re-registering")
			return
		}
		failures = 0
	}
}

// ---------------------------------------------------------------------------
// Routes
// ---------------------------------------------------------------------------

func (a *agent) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /agent/v1/health", a.handleHealth)
	mux.HandleFunc("POST /agent/v1/load", a.handleLoad)
	mux.HandleFunc("POST /agent/v1/play", a.handlePlay)
	mux.HandleFunc("POST /agent/v1/pause", a.handlePause)
	mux.HandleFunc("POST /agent/v1/stop", a.handleStop)
	mux.HandleFunc("POST /agent/v1/seek", a.handleSeek)
	mux.HandleFunc("POST /agent/v1/next", a.handleNext)
	mux.HandleFunc("POST /agent/v1/prev", a.handlePrev)
	mux.HandleFunc("POST /agent/v1/playlist-clear", a.handlePlaylistClear)
	mux.HandleFunc("POST /agent/v1/playlist-move", a.handlePlaylistMove)
	mux.HandleFunc("POST /agent/v1/playlist-remove", a.handlePlaylistRemove)
	mux.HandleFunc("POST /agent/v1/set-property", a.handleSetProperty)
	mux.HandleFunc("POST /agent/v1/handoff", a.handleHandoff)
	mux.HandleFunc("GET /agent/v1/status", a.handleStatus)
	return mux
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (a *agent) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":   a.cfg.Agent.Name,
		"online": a.mpv.isRunning(),
	})
}

func (a *agent) handleLoad(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	url := stringVal(body["url"])
	mode := stringVal(body["mode"])
	if url == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url is required"})
		return
	}
	if mode == "" {
		mode = "replace"
	}
	if err := a.mpv.loadFile(url, mode); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *agent) handlePlay(w http.ResponseWriter, r *http.Request) {
	if err := a.mpv.setProperty("pause", false); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *agent) handlePause(w http.ResponseWriter, r *http.Request) {
	if err := a.mpv.setProperty("pause", true); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *agent) handleStop(w http.ResponseWriter, r *http.Request) {
	_ = a.mpv.setProperty("pause", true)
	_ = a.mpv.setProperty("time-pos", 0)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *agent) handleSeek(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	pos := shared.FloatFromAny(body["position"], -1)
	if pos < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid position"})
		return
	}
	if err := a.mpv.setProperty("time-pos", pos); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *agent) handleNext(w http.ResponseWriter, r *http.Request) {
	if _, err := a.mpv.command("playlist-next"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *agent) handlePrev(w http.ResponseWriter, r *http.Request) {
	if _, err := a.mpv.command("playlist-prev"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *agent) handlePlaylistClear(w http.ResponseWriter, r *http.Request) {
	if err := a.mpv.playlistClear(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *agent) handlePlaylistMove(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	from := shared.IntFromAny(body["from"], -1)
	to := shared.IntFromAny(body["to"], -1)
	if from < 0 || to < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid from/to"})
		return
	}
	if _, err := a.mpv.command("playlist-move", from, to); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *agent) handlePlaylistRemove(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	index := shared.IntFromAny(body["index"], -1)
	if index < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid index"})
		return
	}
	if err := a.mpv.playlistRemove(index); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *agent) handleSetProperty(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	name := stringVal(body["name"])
	value := body["value"]
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if err := a.mpv.setProperty(name, value); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *agent) handleHandoff(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}

	playlistPos := shared.IntFromAny(body["playlist_pos"], 0)
	timePos := shared.FloatFromAny(body["time_pos"], 0)
	paused, _ := body["paused"].(bool)

	// Set playlist position (starts loading that track)
	if err := a.mpv.setProperty("playlist-pos", playlistPos); err != nil {
		a.logger.Printf("handoff: set playlist-pos failed: %v", err)
	}

	// Wait for mpv to actually load the file before seeking
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if v, err := a.mpv.getProperty("duration"); err == nil {
			if d, ok := v.(float64); ok && d > 0 {
				break
			}
		}
	}

	// Seek to the correct position
	if timePos > 0 {
		if err := a.mpv.setProperty("time-pos", timePos); err != nil {
			a.logger.Printf("handoff: seek failed: %v", err)
		}
	}

	// Set pause state
	if err := a.mpv.setProperty("pause", paused); err != nil {
		a.logger.Printf("handoff: set pause failed: %v", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *agent) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]any{}

	if v, err := a.mpv.getProperty("pause"); err == nil {
		status["pause"] = v
	}
	if v, err := a.mpv.getProperty("time-pos"); err == nil {
		status["time_pos"] = v
	}
	if v, err := a.mpv.getProperty("duration"); err == nil {
		status["duration"] = v
	}
	if v, err := a.mpv.getProperty("playlist-pos"); err == nil {
		status["playlist_pos"] = v
	}

	writeJSON(w, http.StatusOK, status)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func decodeBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	if r.Body == nil {
		return map[string]any{}, true
	}
	defer r.Body.Close()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return nil, false
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]any{}, true
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return nil, false
	}
	if body == nil {
		body = map[string]any{}
	}
	return body, true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func stringVal(v any) string {
	return shared.Stringify(v)
}

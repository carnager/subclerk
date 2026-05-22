package main

import (
	"bufio"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	mrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/carnager/subclerk/internal/shared"
	"github.com/vmihailenco/msgpack/v5"
)

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

type config struct {
	Server struct {
		BindToAddress []string `toml:"bind_to_address"`
		APISecret     string   `toml:"api_secret"`
	} `toml:"server"`
	Navidrome struct {
		URL      string `toml:"url"`
		Username string `toml:"username"`
		Password string `toml:"password"`
	} `toml:"navidrome"`
	MPV struct {
		Socket     string `toml:"socket"`
		Executable string `toml:"executable"`
		ReplayGain string `toml:"replaygain"` // "off", "track", "album"
	} `toml:"mpv"`
	Random struct {
		Tracks int `toml:"tracks"`
	} `toml:"random"`
	Cache struct {
		PollInterval int `toml:"poll_interval"`
	} `toml:"cache"`
	Scrobble struct {
		Enabled bool `toml:"enabled"`
	} `toml:"scrobble"`
}

type paths struct {
	DataDir          string
	ConfigPath       string
	AlbumCacheFile   string
	TracksCacheFile  string
	RatingsCacheFile string
	CacheStateFile   string
	ActiveDeviceFile string
	PlayQueueFile    string
}

type cacheState struct {
	Version   int64  `json:"version" msgpack:"version"`
	UpdatedAt string `json:"updated_at" msgpack:"updated_at"`
}

type cacheStatus struct {
	Version              int64  `json:"version"`
	UpdatedAt            string `json:"updated_at"`
	Stale                bool   `json:"stale"`
	NavidromeConnected   bool   `json:"navidrome_connected"`
	NavidromeScanning    bool   `json:"navidrome_scanning"`
	NavidromeLastScanned string `json:"navidrome_last_scanned,omitempty"`
}

// ---------------------------------------------------------------------------
// Subsonic API types
// ---------------------------------------------------------------------------

type subsonicResponse struct {
	SubsonicResponse struct {
		Status    string          `json:"status"`
		Error     *subsonicError  `json:"error,omitempty"`
		AlbumList *subAlbumList   `json:"albumList2,omitempty"`
		Album     *subAlbumDetail `json:"album,omitempty"`
		ScanStatus *subScanStatus `json:"scanStatus,omitempty"`
		NowPlaying *subNowPlaying `json:"nowPlaying,omitempty"`
		Playlists  *subPlaylists  `json:"playlists,omitempty"`
		Playlist   *subPlaylistDetail `json:"playlist,omitempty"`
	} `json:"subsonic-response"`
}

type subsonicError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type subAlbumList struct {
	Albums []subAlbum `json:"album"`
}

type subAlbum struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Artist    string `json:"artist"`
	ArtistID  string `json:"artistId"`
	Year      int    `json:"year"`
	SongCount int    `json:"songCount"`
	Duration  int    `json:"duration"`
	Created   string `json:"created"`
}

type subAlbumDetail struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Artist string    `json:"artist"`
	Year   int       `json:"year"`
	Songs  []subSong `json:"song"`
}

type subReplayGain struct {
	TrackGain float64 `json:"trackGain"`
	AlbumGain float64 `json:"albumGain"`
	TrackPeak float64 `json:"trackPeak"`
	AlbumPeak float64 `json:"albumPeak"`
}

type subSong struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Track      int    `json:"track"`
	DiscNumber int    `json:"discNumber"`
	Artist     string `json:"artist"`
	Album      string `json:"album"`
	AlbumID    string `json:"albumId"`
	Year       int    `json:"year"`
	Duration   int    `json:"duration"`
	Path       string `json:"path"`
	UserRating int    `json:"userRating,omitempty"`
	Created    string `json:"created"`
	ReplayGain subReplayGain `json:"replayGain"`
}

type subScanStatus struct {
	Scanning  bool   `json:"scanning"`
	Count     int64  `json:"count"`
	LastScan  string `json:"lastScan,omitempty"`
}

type subPlaylists struct {
	Playlists []subPlaylist `json:"playlist"`
}

type subPlaylist struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SongCount int    `json:"songCount"`
	Duration  int    `json:"duration"`
	Owner     string `json:"owner"`
	Public    bool   `json:"public"`
	Created   string `json:"created"`
	Changed   string `json:"changed"`
	CoverArt  string `json:"coverArt"`
}

type subPlaylistDetail struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Songs  []subSong `json:"entry"`
}

type subNowPlaying struct {
	Entries []subSong `json:"entry"`
}

// ---------------------------------------------------------------------------
// mpv IPC
// ---------------------------------------------------------------------------

type mpvClient struct {
	socketPath string
	executable string
	replaygain string
	mu         sync.Mutex
	process    *exec.Cmd
	reqID      int
}

type mpvRequest struct {
	Command   []any `json:"command"`
	RequestID int   `json:"request_id"`
}

type mpvResponse struct {
	Data      any    `json:"data"`
	Error     string `json:"error"`
	RequestID int    `json:"request_id"`
}

// ---------------------------------------------------------------------------
// Playback target interface
// ---------------------------------------------------------------------------

type playbackTarget interface {
	loadFile(url, mode string, meta map[string]any) error
	playlistClear() error
	playlistRemove(index int) error
	playlistMove(from, to int) error
	getProperty(name string) (any, error)
	setProperty(name string, value any) error
	command(args ...any) (*mpvResponse, error)
	isRunning() bool
}

// remoteTarget talks to a subclerk-agent over HTTP.
type remoteTarget struct {
	address string
	secret  string
	client  *http.Client
}

func (rt *remoteTarget) doRequest(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, "http://"+rt.address+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if rt.secret != "" {
		req.Header.Set("Authorization", "Bearer "+rt.secret)
	}
	return rt.client.Do(req)
}

func (rt *remoteTarget) loadFile(url, mode string, meta map[string]any) error {
	body, _ := json.Marshal(map[string]string{"url": url, "mode": mode})
	resp, err := rt.doRequest("POST", "/agent/v1/load", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent load: http %d", resp.StatusCode)
	}
	return nil
}

func (rt *remoteTarget) playlistClear() error {
	resp, err := rt.doRequest("POST", "/agent/v1/playlist-clear", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent playlist-clear: http %d", resp.StatusCode)
	}
	return nil
}

func (rt *remoteTarget) playlistRemove(index int) error {
	body, _ := json.Marshal(map[string]int{"index": index})
	resp, err := rt.doRequest("POST", "/agent/v1/playlist-remove", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent playlist-remove: http %d", resp.StatusCode)
	}
	return nil
}

func (rt *remoteTarget) playlistMove(from, to int) error {
	body, _ := json.Marshal(map[string]int{"from": from, "to": to})
	resp, err := rt.doRequest("POST", "/agent/v1/playlist-move", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent playlist-move: http %d", resp.StatusCode)
	}
	return nil
}

func (rt *remoteTarget) getProperty(name string) (any, error) {
	resp, err := rt.doRequest("GET", "/agent/v1/status", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent status: http %d", resp.StatusCode)
	}
	var status map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, err
	}
	// Map mpv property names to agent status keys
	keyMap := map[string]string{
		"pause":        "pause",
		"time-pos":     "time_pos",
		"duration":     "duration",
		"playlist-pos": "playlist_pos",
	}
	if mapped, ok := keyMap[name]; ok {
		return status[mapped], nil
	}
	return status[name], nil
}

func (rt *remoteTarget) setProperty(name string, value any) error {
	// Map specific properties to dedicated endpoints for play/pause
	if name == "pause" {
		endpoint := "/agent/v1/pause"
		if v, ok := value.(bool); ok && !v {
			endpoint = "/agent/v1/play"
		}
		resp, err := rt.doRequest("POST", endpoint, nil)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("agent set-property: http %d", resp.StatusCode)
		}
		return nil
	}
	if name == "time-pos" {
		body, _ := json.Marshal(map[string]any{"position": value})
		resp, err := rt.doRequest("POST", "/agent/v1/seek", strings.NewReader(string(body)))
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("agent seek: http %d", resp.StatusCode)
		}
		return nil
	}
	body, _ := json.Marshal(map[string]any{"name": name, "value": value})
	resp, err := rt.doRequest("POST", "/agent/v1/set-property", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent set-property: http %d", resp.StatusCode)
	}
	return nil
}

func (rt *remoteTarget) command(args ...any) (*mpvResponse, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	cmd := fmt.Sprintf("%v", args[0])
	switch cmd {
	case "playlist-next":
		resp, err := rt.doRequest("POST", "/agent/v1/next", nil)
		if err != nil {
			return nil, err
		}
		resp.Body.Close()
		return &mpvResponse{}, nil
	case "playlist-prev":
		resp, err := rt.doRequest("POST", "/agent/v1/prev", nil)
		if err != nil {
			return nil, err
		}
		resp.Body.Close()
		return &mpvResponse{}, nil
	case "playlist-clear":
		if err := rt.playlistClear(); err != nil {
			return nil, err
		}
		return &mpvResponse{}, nil
	case "playlist-move":
		if len(args) >= 3 {
			from := shared.IntFromAny(args[1], 0)
			to := shared.IntFromAny(args[2], 0)
			if err := rt.playlistMove(from, to); err != nil {
				return nil, err
			}
		}
		return &mpvResponse{}, nil
	default:
		return nil, fmt.Errorf("unsupported remote command: %s", cmd)
	}
}

func (rt *remoteTarget) handoff(playlistPos int, timePos float64, paused bool) error {
	body, _ := json.Marshal(map[string]any{
		"playlist_pos": playlistPos,
		"time_pos":     timePos,
		"paused":       paused,
	})
	resp, err := rt.doRequest("POST", "/agent/v1/handoff", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent handoff: http %d", resp.StatusCode)
	}
	return nil
}

func (rt *remoteTarget) isRunning() bool {
	resp, err := rt.doRequest("GET", "/agent/v1/health", nil)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ---------------------------------------------------------------------------
// Device management
// ---------------------------------------------------------------------------

type device struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Address    string    `json:"address"`
	IsLocal    bool      `json:"is_local"`
	Type       string    `json:"type"` // "local", "agent", "browser"
	Format     string    `json:"format"`
	MaxBitRate    int       `json:"max_bitrate"`
	NavidromeURL  string    `json:"navidrome_url,omitempty"`
	ReplayGain    string    `json:"replaygain,omitempty"` // "off", "track", "album"
	LastSeen      time.Time `json:"last_seen"`
	// For browser devices: SSE command channel
	cmdCh chan browserCmd `json:"-"`
	// For browser devices: playback state reported by browser
	browserState   map[string]any `json:"-"`
	browserStateMu sync.RWMutex  `json:"-"`
}

type browserCmd struct {
	Action string         `json:"action"` // "load", "play", "pause", "stop", "seek", "clear", "set-property"
	Data   map[string]any `json:"data,omitempty"`
}

// browserTarget pushes commands via SSE channel to a browser device.
type browserTarget struct {
	dev *device
}

func (bt *browserTarget) loadFile(url, mode string, meta map[string]any) error {
	data := map[string]any{"url": url, "mode": mode}
	if rg, ok := meta["replay_gain"]; ok && rg != nil {
		data["replay_gain"] = rg
	}
	if songID, ok := meta["song_id"]; ok && songID != nil {
		data["song_id"] = songID
	}
	return bt.send(browserCmd{Action: "load", Data: data})
}

func (bt *browserTarget) playlistClear() error {
	return bt.send(browserCmd{Action: "clear"})
}

func (bt *browserTarget) playlistRemove(index int) error {
	return bt.send(browserCmd{Action: "playlist-remove", Data: map[string]any{"index": index}})
}

func (bt *browserTarget) playlistMove(from, to int) error {
	return bt.send(browserCmd{Action: "playlist-move", Data: map[string]any{"from": from, "to": to}})
}

func (bt *browserTarget) getProperty(name string) (any, error) {
	bt.dev.browserStateMu.RLock()
	defer bt.dev.browserStateMu.RUnlock()
	if bt.dev.browserState == nil {
		return nil, fmt.Errorf("no browser state")
	}
	keyMap := map[string]string{
		"pause":        "pause",
		"time-pos":     "time_pos",
		"duration":     "duration",
		"playlist-pos": "playlist_pos",
	}
	if mapped, ok := keyMap[name]; ok {
		return bt.dev.browserState[mapped], nil
	}
	return bt.dev.browserState[name], nil
}

func (bt *browserTarget) setProperty(name string, value any) error {
	if name == "pause" {
		if v, ok := value.(bool); ok && !v {
			return bt.send(browserCmd{Action: "play"})
		}
		return bt.send(browserCmd{Action: "pause"})
	}
	if name == "time-pos" {
		return bt.send(browserCmd{Action: "seek", Data: map[string]any{"position": value}})
	}
	if name == "playlist-pos" {
		return bt.send(browserCmd{Action: "set-property", Data: map[string]any{"name": name, "value": value}})
	}
	return bt.send(browserCmd{Action: "set-property", Data: map[string]any{"name": name, "value": value}})
}

func (bt *browserTarget) command(args ...any) (*mpvResponse, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	cmd := fmt.Sprintf("%v", args[0])
	switch cmd {
	case "playlist-next":
		return &mpvResponse{}, bt.send(browserCmd{Action: "next"})
	case "playlist-prev":
		return &mpvResponse{}, bt.send(browserCmd{Action: "prev"})
	case "playlist-clear":
		return &mpvResponse{}, bt.send(browserCmd{Action: "clear"})
	case "playlist-move":
		if len(args) >= 3 {
			return &mpvResponse{}, bt.send(browserCmd{Action: "playlist-move", Data: map[string]any{"from": args[1], "to": args[2]}})
		}
		return &mpvResponse{}, nil
	default:
		return nil, fmt.Errorf("unsupported browser command: %s", cmd)
	}
}

func (bt *browserTarget) isRunning() bool {
	return bt.dev.cmdCh != nil
}

func (bt *browserTarget) send(cmd browserCmd) error {
	if bt.dev.cmdCh == nil {
		return fmt.Errorf("browser device not connected")
	}
	select {
	case bt.dev.cmdCh <- cmd:
		return nil
	case <-time.After(2 * time.Second):
		return fmt.Errorf("browser device timeout")
	}
}

// ---------------------------------------------------------------------------
// App
// ---------------------------------------------------------------------------

type app struct {
	cfg       config
	paths     paths
	logger    *log.Logger
	cacheLock sync.Mutex
	mpv       *mpvClient
	// playQueue tracks the Navidrome song IDs in current mpv playlist order
	playQueue   []string
	playQueueMu sync.Mutex
	// in-memory track index: songID -> track map
	trackIndex   map[string]map[string]any
	trackIndexMu sync.RWMutex
	// scrobble tracking
	scrobbleMu         sync.Mutex
	scrobbleLastSongID string
	scrobbleStartTime  time.Time
	scrobbleSubmitted  bool
	// device management
	devices      map[string]*device
	devicesMu    sync.RWMutex
	activeDevice string // device ID, "" = local
}

func main() {
	logger := log.New(os.Stdout, "subclerkd: ", log.LstdFlags)
	cfg, pathCfg, err := loadConfig()
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}

	a := &app{
		cfg:    cfg,
		paths:  pathCfg,
		logger: logger,
		mpv: &mpvClient{
			socketPath: cfg.MPV.Socket,
			executable: cfg.MPV.Executable,
			replaygain: cfg.MPV.ReplayGain,
		},
		playQueue: []string{},
		devices: map[string]*device{
			"local": {
				ID:       "local",
				Name:     "local",
				IsLocal:  true,
				Type:     "local",
				LastSeen: time.Now(),
			},
		},
		activeDevice: "local",
	}

	if err := a.ensureStartupState(); err != nil {
		logger.Fatalf("startup failed: %v", err)
	}
	a.restorePlayQueue()

	go a.ensureMPV()
	go a.watchNavidromeUpdates()
	go a.deviceCleanup()
	if a.cfg.Scrobble.Enabled {
		go a.scrobbleLoop()
	}

	if err := a.serve(); err != nil {
		logger.Fatalf("listen and serve: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Config loading
// ---------------------------------------------------------------------------

func loadConfig() (config, paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return config{}, paths{}, err
	}
	xdgData := getenvDefault("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	xdgConfig := getenvDefault("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	pathCfg := paths{
		DataDir:          filepath.Join(xdgData, "subclerk"),
		ConfigPath:       filepath.Join(xdgConfig, "subclerk", "subclerkd.toml"),
		AlbumCacheFile:   filepath.Join(xdgData, "subclerk", "album.cache"),
		TracksCacheFile:  filepath.Join(xdgData, "subclerk", "tracks.cache"),
		RatingsCacheFile: filepath.Join(xdgData, "subclerk", "ratings.cache"),
		CacheStateFile:   filepath.Join(xdgData, "subclerk", "cache.state"),
		ActiveDeviceFile: filepath.Join(xdgData, "subclerk", "active_device"),
		PlayQueueFile:    filepath.Join(xdgData, "subclerk", "playqueue.json"),
	}

	if err := os.MkdirAll(pathCfg.DataDir, 0o755); err != nil {
		return config{}, paths{}, err
	}
	if err := os.MkdirAll(filepath.Dir(pathCfg.ConfigPath), 0o755); err != nil {
		return config{}, paths{}, err
	}

	if _, err := os.Stat(pathCfg.ConfigPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(pathCfg.ConfigPath, []byte(defaultDaemonConfig()), 0o644); err != nil {
			return config{}, paths{}, err
		}
	}

	var raw map[string]any
	if _, err := toml.DecodeFile(pathCfg.ConfigPath, &raw); err != nil {
		return config{}, paths{}, err
	}
	var cfg config
	server, _ := raw["server"].(map[string]any)
	navidrome, _ := raw["navidrome"].(map[string]any)
	mpvSection, _ := raw["mpv"].(map[string]any)
	random, _ := raw["random"].(map[string]any)
	cache, _ := raw["cache"].(map[string]any)
	cfg.Server.BindToAddress = stringSlice(server["bind_to_address"])
	cfg.Server.APISecret = stringify(server["api_secret"])
	cfg.Navidrome.URL = stringify(navidrome["url"])
	cfg.Navidrome.Username = stringify(navidrome["username"])
	cfg.Navidrome.Password = stringify(navidrome["password"])
	cfg.MPV.Socket = stringify(mpvSection["socket"])
	cfg.MPV.Executable = stringify(mpvSection["executable"])
	cfg.MPV.ReplayGain = stringify(mpvSection["replaygain"])
	cfg.Random.Tracks = intFromAny(random["tracks"], 20)
	cfg.Cache.PollInterval = intFromAny(cache["poll_interval"], 300)
	scrobble, _ := raw["scrobble"].(map[string]any)
	cfg.Scrobble.Enabled = boolFromAny(scrobble["enabled"], true)
	applyDefaults(&cfg)
	return cfg, pathCfg, nil
}

func defaultDaemonConfig() string {
	return `[server]
bind_to_address = ["0.0.0.0:6701", "` + shared.DefaultSocketPath() + `"]

[navidrome]
url = "http://localhost:4533"
username = "admin"
password = "password"

[mpv]
socket = "` + defaultMPVSocket() + `"
executable = "mpv"
replaygain = ""

[random]
tracks = 20

[cache]
poll_interval = 300

[scrobble]
enabled = true
`
}

func defaultMPVSocket() string {
	runtimeDir := shared.Getenv("XDG_RUNTIME_DIR", filepath.Join(os.TempDir(), fmt.Sprintf("subclerk-%d", os.Getuid())))
	return filepath.Join(runtimeDir, "subclerk", "mpv.sock")
}

func applyDefaults(cfg *config) {
	if cfg.Navidrome.URL == "" {
		cfg.Navidrome.URL = "http://localhost:4533"
	}
	if cfg.Navidrome.Username == "" {
		cfg.Navidrome.Username = "admin"
	}
	if cfg.MPV.Socket == "" {
		cfg.MPV.Socket = defaultMPVSocket()
	}
	if cfg.MPV.Executable == "" {
		cfg.MPV.Executable = "mpv"
	}
	if cfg.Random.Tracks <= 0 {
		cfg.Random.Tracks = 20
	}
	if cfg.Cache.PollInterval <= 0 {
		cfg.Cache.PollInterval = 300
	}
	if envBind := os.Getenv("SUBCLERKD_BIND_TO_ADDRESS"); envBind != "" {
		cfg.Server.BindToAddress = splitAndTrim(envBind, ",")
	}
	if len(cfg.Server.BindToAddress) == 0 {
		cfg.Server.BindToAddress = defaultBindToAddress()
	}
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

func (a *app) serve() error {
	handler := a.routes()
	listeners, err := a.listenConfigured()
	if err != nil {
		return err
	}

	errCh := make(chan error, len(listeners))
	for _, listener := range listeners {
		l := listener
		go func() {
			errCh <- http.Serve(l, handler)
		}()
	}

	err = <-errCh
	for _, listener := range listeners {
		_ = listener.Close()
	}
	return err
}

func (a *app) listenConfigured() ([]net.Listener, error) {
	listeners := make([]net.Listener, 0, len(a.cfg.Server.BindToAddress))
	for _, bind := range a.cfg.Server.BindToAddress {
		listener, err := a.listenAddress(bind)
		if err != nil {
			for _, existing := range listeners {
				_ = existing.Close()
			}
			return nil, err
		}
		listeners = append(listeners, listener)
	}
	return listeners, nil
}

func (a *app) listenAddress(bind string) (net.Listener, error) {
	bind = strings.TrimSpace(bind)
	if bind == "" {
		return nil, fmt.Errorf("empty bind_to_address entry")
	}
	if isUnixBindAddress(bind) {
		listener, err := listenUnixSocket(bind)
		if err != nil {
			return nil, err
		}
		a.logger.Printf("serving unix socket on %s", bind)
		return listener, nil
	}
	listener, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, err
	}
	a.logger.Printf("serving tcp on %s", bind)
	return listener, nil
}

func listenUnixSocket(socketPath string) (net.Listener, error) {
	if socketPath == "" {
		return nil, fmt.Errorf("empty socket path")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

func isUnixBindAddress(bind string) bool {
	return strings.Contains(bind, "/")
}

func defaultBindToAddress() []string {
	return []string{
		"0.0.0.0:6701",
		shared.DefaultSocketPath(),
	}
}

// ---------------------------------------------------------------------------
// Routes
// ---------------------------------------------------------------------------

func (a *app) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for unix socket connections (already secured by filesystem permissions)
		isUnix := !strings.Contains(r.RemoteAddr, ":")
		if a.cfg.Server.APISecret != "" && strings.HasPrefix(r.URL.Path, "/api/") && !isUnix {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token == "" {
				token = r.URL.Query().Get("secret")
			}
			if token != a.cfg.Server.APISecret {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/cover/{id}", a.handleCoverArt)
	mux.HandleFunc("GET /api/v1/health", a.handleHealth)
	mux.HandleFunc("GET /api/v1/albums", a.handleAlbums)
	mux.HandleFunc("GET /api/v1/latest_albums", a.handleLatestAlbums)
	mux.HandleFunc("GET /api/v1/tracks", a.handleTracks)
	mux.HandleFunc("GET /api/v1/cache/status", a.handleCacheStatus)
	mux.HandleFunc("GET /api/v1/albums/{album_id}/rating", a.handleAlbumRatingGet)
	mux.HandleFunc("POST /api/v1/albums/{album_id}/rating", a.handleAlbumRatingPost)
	mux.HandleFunc("POST /api/v1/tracks/{track_id}/rating", a.handleTrackRatingPost)
	mux.HandleFunc("POST /api/v1/playlist/add/album/{album_id}", a.handleAddAlbum)
	mux.HandleFunc("POST /api/v1/playlist/add/track/{track_id}", a.handleAddTrack)
	mux.HandleFunc("POST /api/v1/playlist/add/albums", a.handleAddAlbums)
	mux.HandleFunc("POST /api/v1/playlist/add/tracks", a.handleAddTracks)
	mux.HandleFunc("POST /api/v1/playback/random/album", a.handleRandomAlbum)
	mux.HandleFunc("POST /api/v1/playback/random/tracks", a.handleRandomTracks)
	mux.HandleFunc("POST /api/v1/cache/update", a.handleCacheUpdate)
	mux.HandleFunc("GET /api/v1/current_album/rating", a.handleCurrentAlbumRatingGet)
	mux.HandleFunc("POST /api/v1/current_album/rating", a.handleCurrentAlbumRatingPost)
	mux.HandleFunc("GET /api/v1/current_track/rating", a.handleCurrentTrackRatingGet)
	mux.HandleFunc("POST /api/v1/current_track/rating", a.handleCurrentTrackRatingPost)

	// Playback control
	mux.HandleFunc("POST /api/v1/playback/play", a.handlePlay)
	mux.HandleFunc("POST /api/v1/playback/pause", a.handlePause)
	mux.HandleFunc("POST /api/v1/playback/stop", a.handleStop)
	mux.HandleFunc("POST /api/v1/playback/next", a.handleNext)
	mux.HandleFunc("POST /api/v1/playback/prev", a.handlePrev)
	mux.HandleFunc("GET /api/v1/playback/status", a.handlePlaybackStatus)
	mux.HandleFunc("GET /api/v1/playback/queue", a.handleQueueGet)
	mux.HandleFunc("DELETE /api/v1/playback/queue/{position}", a.handleQueueRemove)
	mux.HandleFunc("POST /api/v1/playback/queue/move", a.handleQueueMove)
	mux.HandleFunc("POST /api/v1/playback/seek", a.handleSeek)
	mux.HandleFunc("POST /api/v1/playback/queue/play/{position}", a.handleQueuePlay)
	mux.HandleFunc("DELETE /api/v1/playback/queue", a.handleQueueClear)

	// Browse & Search
	mux.HandleFunc("GET /api/v1/browse/artists", a.handleBrowseArtists)
	mux.HandleFunc("GET /api/v1/browse/albums", a.handleBrowseAlbums)
	mux.HandleFunc("GET /api/v1/browse/tracks", a.handleBrowseTracks)
	mux.HandleFunc("GET /api/v1/search", a.handleSearch)

	// Navidrome playlists
	mux.HandleFunc("GET /api/v1/playlists", a.handlePlaylists)
	mux.HandleFunc("GET /api/v1/playlists/tracks", a.handlePlaylistTracks)
	mux.HandleFunc("POST /api/v1/playlists/add/{id}", a.handlePlaylistAdd)
	mux.HandleFunc("POST /api/v1/playlists/add-track/{id}", a.handlePlaylistAddTrack)

	// Device management
	mux.HandleFunc("GET /api/v1/devices", a.handleDevicesList)
	mux.HandleFunc("POST /api/v1/devices/register", a.handleDeviceRegister)
	mux.HandleFunc("POST /api/v1/devices/heartbeat", a.handleDeviceHeartbeat)
	mux.HandleFunc("POST /api/v1/devices/active", a.handleDeviceSetActive)
	mux.HandleFunc("GET /api/v1/devices/active", a.handleDeviceGetActive)
	mux.HandleFunc("GET /api/v1/devices/stream", a.handleDeviceSSE)
	mux.HandleFunc("POST /api/v1/devices/status", a.handleDeviceBrowserStatus)

	// Stream URL (for offline downloads)
	mux.HandleFunc("GET /api/v1/stream/url", a.handleStreamURL)

	// Scrobble toggle
	mux.HandleFunc("GET /api/v1/scrobble/status", a.handleScrobbleStatus)
	mux.HandleFunc("POST /api/v1/scrobble/toggle", a.handleScrobbleToggle)

	// Web UI
	mux.HandleFunc("GET /ui", a.handleWebUI)
	mux.HandleFunc("GET /ui/", a.handleWebUI)

	return a.authMiddleware(mux)
}

// ---------------------------------------------------------------------------
// Startup
// ---------------------------------------------------------------------------

func (a *app) ensureStartupState() error {
	if _, err := os.Stat(a.paths.RatingsCacheFile); errors.Is(err, os.ErrNotExist) {
		if err := a.saveRatings(map[string]string{}); err != nil {
			return err
		}
	}
	if a.allCachesExist() {
		if err := a.ensureCacheState(); err != nil {
			return err
		}
		a.loadTrackIndex()
		return nil
	}
	return a.rebuildCache("startup")
}

func (a *app) allCachesExist() bool {
	required := []string{a.paths.AlbumCacheFile, a.paths.TracksCacheFile, a.paths.RatingsCacheFile}
	for _, path := range required {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}

func (a *app) ensureCacheState() error {
	if _, err := os.Stat(a.paths.CacheStateFile); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	state, err := a.deriveCacheState()
	if err != nil {
		return err
	}
	return a.saveCacheState(state)
}

// ---------------------------------------------------------------------------
// Subsonic API client
// ---------------------------------------------------------------------------

func (a *app) subsonicURL(endpoint string, extra url.Values) string {
	salt := randomSalt()
	token := md5Token(a.cfg.Navidrome.Password, salt)
	base := strings.TrimRight(a.cfg.Navidrome.URL, "/") + "/rest/" + endpoint
	params := url.Values{}
	params.Set("u", a.cfg.Navidrome.Username)
	params.Set("t", token)
	params.Set("s", salt)
	params.Set("v", "1.16.1")
	params.Set("c", "subclerk")
	params.Set("f", "json")
	for k, vals := range extra {
		for _, v := range vals {
			params.Add(k, v)
		}
	}
	return base + "?" + params.Encode()
}

func (a *app) streamURL(songID string) string {
	salt := randomSalt()
	token := md5Token(a.cfg.Navidrome.Password, salt)
	base := strings.TrimRight(a.cfg.Navidrome.URL, "/") + "/rest/stream"
	params := url.Values{}
	params.Set("id", songID)
	params.Set("u", a.cfg.Navidrome.Username)
	params.Set("t", token)
	params.Set("s", salt)
	params.Set("v", "1.16.1")
	params.Set("c", "subclerk")
	return base + "?" + params.Encode()
}

func md5Token(password, salt string) string {
	h := md5.Sum([]byte(password + salt))
	return hex.EncodeToString(h[:])
}

func randomSalt() string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 12)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		b[i] = letters[n.Int64()]
	}
	return string(b)
}

func (a *app) subsonicGet(endpoint string, extra url.Values) (*subsonicResponse, error) {
	apiURL := a.subsonicURL(endpoint, extra)
	resp, err := http.Get(apiURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("subsonic %s: http %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result subsonicResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("subsonic %s: decode: %w", endpoint, err)
	}
	if result.SubsonicResponse.Status != "ok" {
		if result.SubsonicResponse.Error != nil {
			return nil, fmt.Errorf("subsonic %s: %s", endpoint, result.SubsonicResponse.Error.Message)
		}
		return nil, fmt.Errorf("subsonic %s: status=%s", endpoint, result.SubsonicResponse.Status)
	}
	return &result, nil
}

func (a *app) subsonicPing() error {
	_, err := a.subsonicGet("ping", nil)
	return err
}

func (a *app) subsonicGetAlbumList(offset, size int) ([]subAlbum, error) {
	params := url.Values{}
	params.Set("type", "alphabeticalByArtist")
	params.Set("size", strconv.Itoa(size))
	params.Set("offset", strconv.Itoa(offset))
	resp, err := a.subsonicGet("getAlbumList2", params)
	if err != nil {
		return nil, err
	}
	if resp.SubsonicResponse.AlbumList == nil {
		return nil, nil
	}
	return resp.SubsonicResponse.AlbumList.Albums, nil
}

func (a *app) subsonicGetAlbum(id string) (*subAlbumDetail, error) {
	params := url.Values{}
	params.Set("id", id)
	resp, err := a.subsonicGet("getAlbum", params)
	if err != nil {
		return nil, err
	}
	return resp.SubsonicResponse.Album, nil
}

func (a *app) subsonicGetScanStatus() (*subScanStatus, error) {
	resp, err := a.subsonicGet("getScanStatus", nil)
	if err != nil {
		return nil, err
	}
	return resp.SubsonicResponse.ScanStatus, nil
}

// ---------------------------------------------------------------------------
// mpv management
// ---------------------------------------------------------------------------

func (a *app) ensureMPV() {
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

	args := []string{"--idle", "--no-video", "--no-terminal", "--input-ipc-server=" + m.socketPath}
	if m.replaygain != "" && m.replaygain != "off" {
		args = append(args, "--replaygain="+m.replaygain)
	}
	cmd := exec.Command(m.executable, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	m.process = cmd

	// Wait for socket to appear
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

func (m *mpvClient) loadFile(url string, mode string, meta map[string]any) error {
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

func (m *mpvClient) playlistMove(from, to int) error {
	_, err := m.command("playlist-move", from, to)
	return err
}

// ---------------------------------------------------------------------------
// Playback target / device helpers
// ---------------------------------------------------------------------------

func (a *app) target() playbackTarget {
	a.devicesMu.RLock()
	defer a.devicesMu.RUnlock()
	if a.activeDevice == "" || a.activeDevice == "local" {
		return a.mpv
	}
	dev := a.devices[a.activeDevice]
	if dev == nil {
		return a.mpv
	}
	if dev.Type == "browser" {
		return &browserTarget{dev: dev}
	}
	return &remoteTarget{address: dev.Address, secret: a.cfg.Server.APISecret, client: &http.Client{Timeout: 5 * time.Second}}
}

func (a *app) activeDeviceInfo() *device {
	a.devicesMu.RLock()
	defer a.devicesMu.RUnlock()
	if a.activeDevice == "" || a.activeDevice == "local" {
		if d, ok := a.devices["local"]; ok {
			return d
		}
		return &device{ID: "local", Name: "local", IsLocal: true, LastSeen: time.Now()}
	}
	return a.devices[a.activeDevice]
}

func (a *app) streamURLForDevice(songID, format string, maxBitRate int, navidromeURL string) string {
	salt := randomSalt()
	token := md5Token(a.cfg.Navidrome.Password, salt)
	baseURL := a.cfg.Navidrome.URL
	if navidromeURL != "" {
		baseURL = navidromeURL
	}
	base := strings.TrimRight(baseURL, "/") + "/rest/stream"
	params := url.Values{}
	params.Set("id", songID)
	params.Set("u", a.cfg.Navidrome.Username)
	params.Set("t", token)
	params.Set("s", salt)
	params.Set("v", "1.16.1")
	params.Set("c", "subclerk")
	if format != "" {
		params.Set("format", format)
	}
	if maxBitRate > 0 {
		params.Set("maxBitRate", strconv.Itoa(maxBitRate))
	}
	return base + "?" + params.Encode()
}

func (a *app) streamURLForActiveDevice(songID string) string {
	dev := a.activeDeviceInfo()
	if dev == nil || dev.IsLocal {
		return a.streamURL(songID)
	}
	return a.streamURLForDevice(songID, dev.Format, dev.MaxBitRate, dev.NavidromeURL)
}

// ---------------------------------------------------------------------------
// Playback helpers
// ---------------------------------------------------------------------------

func (a *app) replayGainMeta(songID string) map[string]any {
	meta := map[string]any{"song_id": songID}
	if track := a.findTrackBySongID(songID); track != nil {
		if rg, ok := track["replay_gain"].(map[string]any); ok {
			meta["replay_gain"] = rg
		}
	}
	return meta
}

func (a *app) addSongsToPlaylist(songIDs []string, mode string) error {
	if len(songIDs) == 0 {
		return nil
	}

	a.playQueueMu.Lock()
	defer a.playQueueMu.Unlock()

	t := a.target()
	switch mode {
	case "replace":
		if err := t.playlistClear(); err != nil {
			return err
		}
		a.playQueue = nil
		for i, id := range songIDs {
			loadMode := "append"
			if i == 0 {
				loadMode = "replace"
			}
			if err := t.loadFile(a.streamURLForActiveDevice(id), loadMode, a.replayGainMeta(id)); err != nil {
				return err
			}
			a.playQueue = append(a.playQueue, id)
		}
		a.savePlayQueue()
		return t.setProperty("pause", false)

	case "insert":
		posRaw, _ := t.getProperty("playlist-pos")
		pos := 0
		if f, ok := posRaw.(float64); ok && f >= 0 {
			pos = int(f) + 1
		}
		for i, id := range songIDs {
			if err := t.loadFile(a.streamURLForActiveDevice(id), "append", a.replayGainMeta(id)); err != nil {
				return err
			}
			// Move from end to insert position
			endIdx := len(a.playQueue) + i
			targetIdx := pos + i
			if endIdx > targetIdx {
				t.playlistMove(endIdx, targetIdx)
			}
		}
		// Update our tracking
		newQueue := make([]string, 0, len(a.playQueue)+len(songIDs))
		newQueue = append(newQueue, a.playQueue[:pos]...)
		newQueue = append(newQueue, songIDs...)
		if pos < len(a.playQueue) {
			newQueue = append(newQueue, a.playQueue[pos:]...)
		}
		a.playQueue = newQueue
		a.savePlayQueue()
		return t.setProperty("pause", false)

	default: // "add"
		for _, id := range songIDs {
			if err := t.loadFile(a.streamURLForActiveDevice(id), "append", a.replayGainMeta(id)); err != nil {
				return err
			}
			a.playQueue = append(a.playQueue, id)
		}
		a.savePlayQueue()
		return nil
	}
}

// savePlayQueue persists the current play queue to disk (caller must hold playQueueMu or be safe).
func (a *app) savePlayQueue() {
	data, _ := json.Marshal(a.playQueue)
	_ = os.WriteFile(a.paths.PlayQueueFile, data, 0o644)
}

// restorePlayQueue loads the saved play queue from disk and reloads into
// the local mpv target so that clients see a consistent queue on reconnect.
func (a *app) restorePlayQueue() {
	data, err := os.ReadFile(a.paths.PlayQueueFile)
	if err != nil {
		return
	}
	var queue []string
	if json.Unmarshal(data, &queue) != nil || len(queue) == 0 {
		return
	}
	a.playQueue = queue
	a.logger.Printf("restored play queue: %d tracks", len(queue))

	// For local target, reload into mpv (paused) so clients can browse/click
	if a.activeDevice == "" || a.activeDevice == "local" {
		go a.reloadQueueIntoTarget()
	}
}

// reloadQueueIntoTarget loads the saved playQueue into the current target (paused).
func (a *app) reloadQueueIntoTarget() {
	// Wait for mpv to be ready
	for i := 0; i < 20; i++ {
		if a.mpv.isRunning() {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !a.mpv.isRunning() {
		a.logger.Printf("restore: mpv not ready, skipping queue reload")
		return
	}

	a.playQueueMu.Lock()
	queue := make([]string, len(a.playQueue))
	copy(queue, a.playQueue)
	a.playQueueMu.Unlock()

	t := a.target()
	for i, songID := range queue {
		mode := "append"
		if i == 0 {
			mode = "replace"
		}
		if err := t.loadFile(a.streamURLForActiveDevice(songID), mode, a.replayGainMeta(songID)); err != nil {
			a.logger.Printf("restore: failed to load track %d: %v", i, err)
			return
		}
	}
	// Pause immediately — we're just restoring state, not starting playback
	_ = t.setProperty("pause", true)
	a.logger.Printf("restored %d tracks into mpv (paused)", len(queue))
}

func (a *app) currentPlayingSongID() string {
	a.playQueueMu.Lock()
	defer a.playQueueMu.Unlock()

	posRaw, err := a.target().getProperty("playlist-pos")
	if err != nil {
		return ""
	}
	pos, ok := posRaw.(float64)
	if !ok || int(pos) < 0 || int(pos) >= len(a.playQueue) {
		return ""
	}
	return a.playQueue[int(pos)]
}

func (a *app) loadTrackIndex() {
	tracks, err := a.readMapSlice(a.paths.TracksCacheFile)
	if err != nil {
		a.logger.Printf("loadTrackIndex: %v", err)
		return
	}
	idx := make(map[string]map[string]any, len(tracks))
	for _, track := range tracks {
		sid := stringify(track["song_id"])
		if sid != "" {
			idx[sid] = track
		}
	}
	a.trackIndexMu.Lock()
	a.trackIndex = idx
	a.trackIndexMu.Unlock()
	a.logger.Printf("track index loaded: %d entries", len(idx))
}

func (a *app) findTrackBySongID(songID string) map[string]any {
	a.trackIndexMu.RLock()
	track := a.trackIndex[songID]
	a.trackIndexMu.RUnlock()
	return track
}

// ---------------------------------------------------------------------------
// Handlers: Health & Cache
// ---------------------------------------------------------------------------

func (a *app) handleHealth(w http.ResponseWriter, r *http.Request) {
	err := a.subsonicPing()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":              "error",
			"navidrome_connected": false,
			"mpv_running":         a.mpv.isRunning(),
			"error":               err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":              "ok",
		"navidrome_connected": true,
		"mpv_running":         a.mpv.isRunning(),
	})
}

func (a *app) handleCoverArt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	size := r.URL.Query().Get("size")
	params := url.Values{"id": {id}}
	if size != "" {
		params.Set("size", size)
	}
	apiURL := a.subsonicURL("getCoverArt", params)
	resp, err := http.Get(apiURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream error", resp.StatusCode)
		return
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	io.Copy(w, resp.Body)
}

func (a *app) handlePlaylists(w http.ResponseWriter, r *http.Request) {
	resp, err := a.subsonicGet("getPlaylists", nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if resp.SubsonicResponse.Playlists == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	result := make([]map[string]any, 0, len(resp.SubsonicResponse.Playlists.Playlists))
	for _, pl := range resp.SubsonicResponse.Playlists.Playlists {
		result = append(result, map[string]any{
			"id":         pl.ID,
			"name":       pl.Name,
			"song_count": pl.SongCount,
			"duration":   pl.Duration,
			"cover_art":  pl.CoverArt,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *app) handlePlaylistTracks(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}
	resp, err := a.subsonicGet("getPlaylist", url.Values{"id": {id}})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if resp.SubsonicResponse.Playlist == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	tracks := make([]map[string]any, 0, len(resp.SubsonicResponse.Playlist.Songs))
	for i, song := range resp.SubsonicResponse.Playlist.Songs {
		tracks = append(tracks, map[string]any{
			"id":                 strconv.Itoa(i),
			"song_id":            song.ID,
			"title":              song.Title,
			"artist":             song.Artist,
			"album":              song.Album,
			"tracknumber":        song.Track,
			"navidrome_album_id": song.AlbumID,
			"duration":           song.Duration,
		})
	}
	writeJSON(w, http.StatusOK, tracks)
}

func (a *app) handlePlaylistAdd(w http.ResponseWriter, r *http.Request) {
	playlistID := r.PathValue("id")
	if playlistID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	mode := stringify(body["mode"])
	if mode == "" {
		mode = "add"
	}

	// Fetch playlist tracks from Navidrome
	resp, err := a.subsonicGet("getPlaylist", url.Values{"id": {playlistID}})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if resp.SubsonicResponse.Playlist == nil || len(resp.SubsonicResponse.Playlist.Songs) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"status": "empty playlist"})
		return
	}

	songIDs := make([]string, 0, len(resp.SubsonicResponse.Playlist.Songs))
	for _, song := range resp.SubsonicResponse.Playlist.Songs {
		songIDs = append(songIDs, song.ID)
	}

	if err := a.addSongsToPlaylist(songIDs, mode); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *app) handlePlaylistAddTrack(w http.ResponseWriter, r *http.Request) {
	playlistID := r.PathValue("id")
	if playlistID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	songID := stringify(body["song_id"])
	if songID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "song_id required"})
		return
	}
	_, err := a.subsonicGet("updatePlaylist", url.Values{
		"playlistId":  {playlistID},
		"songIdToAdd": {songID},
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *app) handleAlbums(w http.ResponseWriter, r *http.Request) {
	albums, err := a.readMapSlice(a.paths.AlbumCacheFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if r.URL.Query().Get("sort") == "latest" {
		slices.SortFunc(albums, func(a1, a2 map[string]any) int {
			return strings.Compare(stringify(a2["last_modified"]), stringify(a1["last_modified"]))
		})
	}
	ratings, err := a.loadRatings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	a.applyCacheStateHeaders(w)
	writeJSON(w, http.StatusOK, attachAlbumRatings(albums, ratings))
}

func (a *app) handleLatestAlbums(w http.ResponseWriter, r *http.Request) {
	r.URL.RawQuery = "sort=latest"
	a.handleAlbums(w, r)
}

func (a *app) handleTracks(w http.ResponseWriter, r *http.Request) {
	tracks, err := a.readMapSlice(a.paths.TracksCacheFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	a.applyCacheStateHeaders(w)
	writeJSON(w, http.StatusOK, tracks)
}

func (a *app) handleCacheStatus(w http.ResponseWriter, r *http.Request) {
	status, err := a.loadCacheStatusFull()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	a.writeCacheStateHeaders(w, cacheState{
		Version:   status.Version,
		UpdatedAt: status.UpdatedAt,
	})
	writeJSON(w, http.StatusOK, status)
}

func (a *app) handleCacheUpdate(w http.ResponseWriter, r *http.Request) {
	go func() {
		if err := a.rebuildCache("api request"); err != nil {
			log.Printf("background cache update failed: %v", err)
		}
	}()
	writeJSON(w, http.StatusOK, map[string]string{"message": "Cache update started"})
}

// ---------------------------------------------------------------------------
// Handlers: Album ratings
// ---------------------------------------------------------------------------

func (a *app) handleAlbumRatingGet(w http.ResponseWriter, r *http.Request) {
	albums, err := a.readMapSlice(a.paths.AlbumCacheFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	album := findByID(albums, r.PathValue("album_id"))
	if album == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Album not found"})
		return
	}
	ratings, err := a.loadRatings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"album_id": r.PathValue("album_id"),
		"rating":   ratings[albumKey(album)],
	})
}

func (a *app) handleAlbumRatingPost(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	rating := stringify(body["rating"])
	if rating == "" {
		rating = "---"
	}
	if !validRating(rating) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid rating"})
		return
	}
	albums, err := a.readMapSlice(a.paths.AlbumCacheFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	album := findByID(albums, r.PathValue("album_id"))
	if album == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Album not found"})
		return
	}
	changed, err := a.updateAlbumRating(album, rating)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"changed": changed})
}

// ---------------------------------------------------------------------------
// Handlers: Track ratings
// ---------------------------------------------------------------------------

func (a *app) handleTrackRatingPost(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	rating := stringify(body["rating"])
	if rating == "" {
		rating = "---"
	}
	if !validRating(rating) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid rating. Must be '1'-'10', '---', or 'Delete'."})
		return
	}
	tracks, err := a.readMapSlice(a.paths.TracksCacheFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	track := findByID(tracks, r.PathValue("track_id"))
	if track == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Track not found"})
		return
	}
	changed, err := a.updateTrackRating(track, rating)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": changed})
}

// ---------------------------------------------------------------------------
// Handlers: Playlist / Queue
// ---------------------------------------------------------------------------

func (a *app) handleAddAlbum(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBodyOptional(w, r)
	if !ok {
		return
	}
	mode := normalizePlaylistMode(stringify(body["mode"]))
	albums, err := a.readMapSlice(a.paths.AlbumCacheFile)
	if err != nil {
		a.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	album := findByID(albums, r.PathValue("album_id"))
	if album == nil {
		a.writeError(w, r, http.StatusNotFound, "Album not found")
		return
	}
	songIDs, err := a.albumTrackSongIDs(album)
	if err != nil {
		a.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.addSongsToPlaylist(songIDs, mode); err != nil {
		a.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Album added to playlist successfully."})
}

func (a *app) handleAddTrack(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBodyOptional(w, r)
	if !ok {
		return
	}
	mode := normalizePlaylistMode(stringify(body["mode"]))
	tracks, err := a.readMapSlice(a.paths.TracksCacheFile)
	if err != nil {
		a.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	track := findByID(tracks, r.PathValue("track_id"))
	if track == nil {
		a.writeError(w, r, http.StatusNotFound, "Track not found")
		return
	}
	songID := stringify(track["song_id"])
	if songID == "" {
		a.writeError(w, r, http.StatusUnprocessableEntity, "Track has no song_id")
		return
	}
	if err := a.addSongsToPlaylist([]string{songID}, mode); err != nil {
		a.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Track added to playlist successfully."})
}

func (a *app) handleAddAlbums(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	ids := stringSlice(body["album_ids"])
	if len(ids) == 0 {
		a.writeError(w, r, http.StatusBadRequest, "album_ids must be a non-empty list")
		return
	}
	mode := normalizePlaylistMode(stringify(body["mode"]))
	albums, err := a.readMapSlice(a.paths.AlbumCacheFile)
	if err != nil {
		a.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	selected := findManyByID(albums, ids)
	if len(selected) != len(ids) {
		a.writeError(w, r, http.StatusNotFound, "Some albums not found")
		return
	}
	var allSongIDs []string
	for _, album := range selected {
		songIDs, err := a.albumTrackSongIDs(album)
		if err != nil {
			a.writeError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		allSongIDs = append(allSongIDs, songIDs...)
	}
	if err := a.addSongsToPlaylist(allSongIDs, mode); err != nil {
		a.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": fmt.Sprintf("%d albums added to playlist successfully", len(selected))})
}

func (a *app) handleAddTracks(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	ids := stringSlice(body["track_ids"])
	if len(ids) == 0 {
		a.writeError(w, r, http.StatusBadRequest, "track_ids must be a non-empty list")
		return
	}
	mode := normalizePlaylistMode(stringify(body["mode"]))
	tracks, err := a.readMapSlice(a.paths.TracksCacheFile)
	if err != nil {
		a.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	selected := findManyByID(tracks, ids)
	if len(selected) != len(ids) {
		a.writeError(w, r, http.StatusNotFound, "Some tracks not found")
		return
	}
	songIDs := make([]string, 0, len(selected))
	for _, track := range selected {
		sid := stringify(track["song_id"])
		if sid != "" {
			songIDs = append(songIDs, sid)
		}
	}
	if err := a.addSongsToPlaylist(songIDs, mode); err != nil {
		a.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": fmt.Sprintf("%d tracks added to playlist successfully", len(selected))})
}

// ---------------------------------------------------------------------------
// Handlers: Random
// ---------------------------------------------------------------------------

func (a *app) handleRandomAlbum(w http.ResponseWriter, r *http.Request) {
	albums, err := a.readMapSlice(a.paths.AlbumCacheFile)
	if err != nil || len(albums) == 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no albums available"})
		return
	}
	rng := mrand.New(mrand.NewSource(time.Now().UnixNano()))
	album := albums[rng.Intn(len(albums))]
	songIDs, err := a.albumTrackSongIDs(album)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := a.addSongsToPlaylist(songIDs, "replace"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Random album playback started"})
}

func (a *app) handleRandomTracks(w http.ResponseWriter, r *http.Request) {
	tracks, err := a.readMapSlice(a.paths.TracksCacheFile)
	if err != nil || len(tracks) == 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no tracks available"})
		return
	}
	count := a.cfg.Random.Tracks
	if count > len(tracks) {
		count = len(tracks)
	}
	rng := mrand.New(mrand.NewSource(time.Now().UnixNano()))
	rng.Shuffle(len(tracks), func(i, j int) { tracks[i], tracks[j] = tracks[j], tracks[i] })
	songIDs := make([]string, 0, count)
	for _, track := range tracks[:count] {
		sid := stringify(track["song_id"])
		if sid != "" {
			songIDs = append(songIDs, sid)
		}
	}
	if err := a.addSongsToPlaylist(songIDs, "replace"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Random tracks playback started"})
}

// ---------------------------------------------------------------------------
// Handlers: Playback control
// ---------------------------------------------------------------------------

func (a *app) handlePlay(w http.ResponseWriter, r *http.Request) {
	if err := a.target().setProperty("pause", false); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Playing"})
}

func (a *app) handlePause(w http.ResponseWriter, r *http.Request) {
	if err := a.target().setProperty("pause", true); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Paused"})
}

func (a *app) handleStop(w http.ResponseWriter, r *http.Request) {
	t := a.target()
	_ = t.setProperty("pause", true)
	_ = t.setProperty("time-pos", 0)
	writeJSON(w, http.StatusOK, map[string]string{"message": "Stopped"})
}

func (a *app) handleNext(w http.ResponseWriter, r *http.Request) {
	if _, err := a.target().command("playlist-next"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Next track"})
}

func (a *app) handlePrev(w http.ResponseWriter, r *http.Request) {
	if _, err := a.target().command("playlist-prev"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Previous track"})
}

func (a *app) handlePlaybackStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]any{
		"state": "stopped",
	}

	t := a.target()
	pauseRaw, err := t.getProperty("pause")
	if err == nil {
		if paused, ok := pauseRaw.(bool); ok {
			if paused {
				status["state"] = "paused"
			} else {
				status["state"] = "playing"
			}
		}
	}

	if posRaw, err := t.getProperty("playlist-pos"); err == nil {
		if pos, ok := posRaw.(float64); ok {
			status["playlist_pos"] = int(pos)
		}
	}

	if timeRaw, err := t.getProperty("time-pos"); err == nil {
		status["time_pos"] = timeRaw
	}
	if durRaw, err := t.getProperty("duration"); err == nil {
		status["duration"] = durRaw
	}

	songID := a.currentPlayingSongID()
	// If we can't get songID from the target (no status report yet), try queue position 0
	if songID == "" {
		a.playQueueMu.Lock()
		if len(a.playQueue) > 0 {
			pos := 0
			if p, ok := status["playlist_pos"].(int); ok && p >= 0 && p < len(a.playQueue) {
				pos = p
			}
			songID = a.playQueue[pos]
		}
		a.playQueueMu.Unlock()
	}
	if songID != "" {
		status["song_id"] = songID
		if track := a.findTrackBySongID(songID); track != nil {
			status["title"] = track["title"]
			status["artist"] = track["artist"]
			status["album"] = track["album"]
			status["date"] = track["date"]
			if albumID, ok := track["navidrome_album_id"].(string); ok && albumID != "" {
				status["album_id"] = albumID
			}
			if rg, ok := track["replay_gain"].(map[string]any); ok {
				status["replay_gain"] = rg
			}
			// Fall back to track metadata duration if device reports 0
			// (happens with transcoded streams where ExoPlayer can't determine duration)
			dur, _ := status["duration"].(float64)
			if dur <= 0 {
				rawDur := track["duration"]
				switch d := rawDur.(type) {
				case float64:
					if d > 0 { status["duration"] = d }
				case int:
					if d > 0 { status["duration"] = float64(d) }
				case int8:
					if d > 0 { status["duration"] = float64(d) }
				case int16:
					if d > 0 { status["duration"] = float64(d) }
				case int32:
					if d > 0 { status["duration"] = float64(d) }
				case int64:
					if d > 0 { status["duration"] = float64(d) }
				case uint:
					if d > 0 { status["duration"] = float64(d) }
				case uint8:
					if d > 0 { status["duration"] = float64(d) }
				case uint16:
					if d > 0 { status["duration"] = float64(d) }
				case uint32:
					if d > 0 { status["duration"] = float64(d) }
				case uint64:
					if d > 0 { status["duration"] = float64(d) }
				default:
					a.logger.Printf("duration fallback: unknown type %T value=%v", rawDur, rawDur)
				}
			}
		}
	}

	// Include active device info
	if dev := a.activeDeviceInfo(); dev != nil {
		status["active_device"] = map[string]any{
			"id":       dev.ID,
			"name":     dev.Name,
			"is_local": dev.IsLocal,
		}
	}

	writeJSON(w, http.StatusOK, status)
}

func (a *app) handleQueueGet(w http.ResponseWriter, r *http.Request) {
	a.playQueueMu.Lock()
	queue := make([]string, len(a.playQueue))
	copy(queue, a.playQueue)
	a.playQueueMu.Unlock()

	posRaw, _ := a.target().getProperty("playlist-pos")
	currentPos := -1
	if f, ok := posRaw.(float64); ok {
		currentPos = int(f)
	}

	entries := make([]map[string]any, 0, len(queue))
	for i, songID := range queue {
		entry := map[string]any{
			"position": i,
			"song_id":  songID,
			"current":  i == currentPos,
		}
		if track := a.findTrackBySongID(songID); track != nil {
			entry["title"] = track["title"]
			entry["artist"] = track["artist"]
			entry["album"] = track["album"]
			entry["date"] = track["date"]
			entry["duration"] = track["duration"]
			if albumID, ok := track["navidrome_album_id"].(string); ok && albumID != "" {
				entry["album_id"] = albumID
			}
		}
		entries = append(entries, entry)
	}
	writeJSON(w, http.StatusOK, entries)
}

func (a *app) handleQueueRemove(w http.ResponseWriter, r *http.Request) {
	posStr := r.PathValue("position")
	pos, err := strconv.Atoi(posStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid position"})
		return
	}
	a.playQueueMu.Lock()
	defer a.playQueueMu.Unlock()

	if pos < 0 || pos >= len(a.playQueue) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "position out of range"})
		return
	}
	if err := a.target().playlistRemove(pos); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	a.playQueue = append(a.playQueue[:pos], a.playQueue[pos+1:]...)
	a.savePlayQueue()
	writeJSON(w, http.StatusOK, map[string]string{"message": "Removed from queue"})
}

// ---------------------------------------------------------------------------
// Handlers: Current track/album rating
// ---------------------------------------------------------------------------

func (a *app) handleCurrentAlbumRatingGet(w http.ResponseWriter, r *http.Request) {
	songID := a.currentPlayingSongID()
	if songID == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "No song playing"})
		return
	}
	track := a.findTrackBySongID(songID)
	if track == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Current track not in cache"})
		return
	}
	ratings, err := a.loadRatings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	album := map[string]any{
		"albumartist": firstNonEmpty(stringify(track["albumartist"]), stringify(track["artist"])),
		"album":       track["album"],
		"date":        firstNonEmpty(stringify(track["date"]), "0000"),
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rating":      ratings[albumKey(album)],
		"albumartist": album["albumartist"],
		"album":       album["album"],
		"date":        album["date"],
	})
}

func (a *app) handleCurrentAlbumRatingPost(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	rating := stringify(body["rating"])
	if rating == "" {
		rating = "---"
	}
	if !validRating(rating) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid rating"})
		return
	}
	songID := a.currentPlayingSongID()
	if songID == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "No song playing"})
		return
	}
	track := a.findTrackBySongID(songID)
	if track == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Current track not in cache"})
		return
	}
	album := map[string]any{
		"albumartist": firstNonEmpty(stringify(track["albumartist"]), stringify(track["artist"])),
		"album":       track["album"],
		"date":        firstNonEmpty(stringify(track["date"]), "0000"),
	}
	changed, err := a.updateAlbumRating(album, rating)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"changed": changed})
}

func (a *app) handleCurrentTrackRatingGet(w http.ResponseWriter, r *http.Request) {
	songID := a.currentPlayingSongID()
	if songID == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "No song playing"})
		return
	}
	track := a.findTrackBySongID(songID)
	if track == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Current track not in cache"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rating": track["rating"],
		"title":  track["title"],
		"artist": track["artist"],
		"album":  track["album"],
		"date":   track["date"],
	})
}

func (a *app) handleCurrentTrackRatingPost(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	rating := stringify(body["rating"])
	if rating == "" {
		rating = "---"
	}
	if !validRating(rating) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid rating. Must be '1'-'10', '---', or 'Delete'."})
		return
	}
	songID := a.currentPlayingSongID()
	if songID == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "No song playing"})
		return
	}
	track := a.findTrackBySongID(songID)
	if track == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Current track not in cache"})
		return
	}
	changed, err := a.updateTrackRating(track, rating)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": changed})
}

// ---------------------------------------------------------------------------
// Handlers: Seek & Queue Move
// ---------------------------------------------------------------------------

func (a *app) handleSeek(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	posRaw, exists := body["position"]
	if !exists {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing position"})
		return
	}
	pos := shared.FloatFromAny(posRaw, -1)
	if pos < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid position"})
		return
	}
	if err := a.target().setProperty("time-pos", pos); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Seeked"})
}

func (a *app) handleQueueMove(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	from := intFromAny(body["from"], -1)
	to := intFromAny(body["to"], -1)

	a.playQueueMu.Lock()
	defer a.playQueueMu.Unlock()

	if from < 0 || from >= len(a.playQueue) || to < 0 || to >= len(a.playQueue) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "position out of range"})
		return
	}
	if from == to {
		writeJSON(w, http.StatusOK, map[string]string{"message": "No move needed"})
		return
	}
	// mpv playlist-move inserts BEFORE target, so moving down needs +1
	mpvTo := to
	if to > from {
		mpvTo = to + 1
	}
	if err := a.target().playlistMove(from, mpvTo); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Update local tracking
	item := a.playQueue[from]
	newQueue := make([]string, 0, len(a.playQueue))
	for i, s := range a.playQueue {
		if i == from {
			continue
		}
		if i == to && to < from {
			newQueue = append(newQueue, item)
		}
		newQueue = append(newQueue, s)
		if i == to && to > from {
			newQueue = append(newQueue, item)
		}
	}
	a.playQueue = newQueue
	a.savePlayQueue()
	writeJSON(w, http.StatusOK, map[string]string{"message": "Moved"})
}

func (a *app) handleQueuePlay(w http.ResponseWriter, r *http.Request) {
	posStr := r.PathValue("position")
	pos, err := strconv.Atoi(posStr)
	if err != nil || pos < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid position"})
		return
	}
	a.playQueueMu.Lock()
	qLen := len(a.playQueue)
	a.playQueueMu.Unlock()
	if pos >= qLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "position out of range"})
		return
	}
	t := a.target()
	if err := t.setProperty("playlist-pos", pos); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_ = t.setProperty("pause", false)
	writeJSON(w, http.StatusOK, map[string]string{"message": "Playing"})
}

func (a *app) handleQueueClear(w http.ResponseWriter, r *http.Request) {
	t := a.target()
	_ = t.setProperty("pause", true)
	_ = t.playlistClear()
	a.playQueueMu.Lock()
	a.playQueue = nil
	a.savePlayQueue()
	a.playQueueMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"message": "Queue cleared"})
}

// ---------------------------------------------------------------------------
// Handlers: Browse & Search
// ---------------------------------------------------------------------------

func (a *app) handleBrowseArtists(w http.ResponseWriter, r *http.Request) {
	albums, err := a.readMapSlice(a.paths.AlbumCacheFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	seen := map[string]struct{}{}
	artists := make([]string, 0)
	for _, album := range albums {
		artist := stringify(album["albumartist"])
		if artist == "" {
			continue
		}
		if _, ok := seen[artist]; !ok {
			seen[artist] = struct{}{}
			artists = append(artists, artist)
		}
	}
	slices.Sort(artists)
	writeJSON(w, http.StatusOK, artists)
}

func (a *app) handleBrowseAlbums(w http.ResponseWriter, r *http.Request) {
	artistFilter := strings.TrimSpace(r.URL.Query().Get("artist"))
	if artistFilter == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "artist parameter required"})
		return
	}
	albums, err := a.readMapSlice(a.paths.AlbumCacheFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	ratings, err := a.loadRatings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	filtered := make([]map[string]any, 0)
	for _, album := range albums {
		if stringify(album["albumartist"]) == artistFilter {
			entry := cloneMap(album)
			entry["rating"] = ratings[albumKey(album)]
			filtered = append(filtered, entry)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

func (a *app) handleBrowseTracks(w http.ResponseWriter, r *http.Request) {
	albumID := strings.TrimSpace(r.URL.Query().Get("album_id"))
	if albumID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "album_id parameter required"})
		return
	}
	albums, err := a.readMapSlice(a.paths.AlbumCacheFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	album := findByID(albums, albumID)
	if album == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Album not found"})
		return
	}
	albumArtist := stringify(album["albumartist"])
	albumName := stringify(album["album"])
	date := stringify(album["date"])

	tracks, err := a.readMapSlice(a.paths.TracksCacheFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	filtered := make([]map[string]any, 0)
	for _, track := range tracks {
		if stringify(track["albumartist"]) == albumArtist &&
			stringify(track["album"]) == albumName &&
			stringify(track["date"]) == date {
			filtered = append(filtered, track)
		}
	}
	slices.SortFunc(filtered, func(a1, a2 map[string]any) int {
		d1 := intFromAny(a1["discnumber"], 0)
		d2 := intFromAny(a2["discnumber"], 0)
		if d1 != d2 {
			return d1 - d2
		}
		return intFromAny(a1["tracknumber"], 0) - intFromAny(a2["tracknumber"], 0)
	})
	writeJSON(w, http.StatusOK, filtered)
}

func (a *app) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if query == "" {
		writeJSON(w, http.StatusOK, map[string]any{"albums": []any{}, "tracks": []any{}})
		return
	}

	albums, _ := a.readMapSlice(a.paths.AlbumCacheFile)
	tracks, _ := a.readMapSlice(a.paths.TracksCacheFile)
	ratings, _ := a.loadRatings()

	terms := strings.Fields(query)

	matchedAlbums := make([]map[string]any, 0)
	for _, album := range albums {
		text := strings.ToLower(stringify(album["albumartist"]) + " " + stringify(album["album"]) + " " + stringify(album["date"]))
		if matchesAll(text, terms) {
			entry := cloneMap(album)
			entry["rating"] = ratings[albumKey(album)]
			matchedAlbums = append(matchedAlbums, entry)
			if len(matchedAlbums) >= 50 {
				break
			}
		}
	}

	matchedTracks := make([]map[string]any, 0)
	for _, track := range tracks {
		text := strings.ToLower(stringify(track["title"]) + " " + stringify(track["artist"]) + " " + stringify(track["album"]) + " " + stringify(track["albumartist"]))
		if matchesAll(text, terms) {
			matchedTracks = append(matchedTracks, track)
			if len(matchedTracks) >= 50 {
				break
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"albums": matchedAlbums,
		"tracks": matchedTracks,
	})
}

func matchesAll(text string, terms []string) bool {
	for _, term := range terms {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Handlers: Stream URL
// ---------------------------------------------------------------------------

func (a *app) handleStreamURL(w http.ResponseWriter, r *http.Request) {
	songID := r.URL.Query().Get("song_id")
	deviceID := r.URL.Query().Get("device_id")
	if songID == "" {
		http.Error(w, "song_id required", http.StatusBadRequest)
		return
	}
	var streamURL string
	if deviceID != "" {
		a.devicesMu.RLock()
		dev := a.devices[deviceID]
		a.devicesMu.RUnlock()
		if dev != nil {
			streamURL = a.streamURLForDevice(songID, dev.Format, dev.MaxBitRate, dev.NavidromeURL)
		} else {
			streamURL = a.streamURL(songID)
		}
	} else {
		streamURL = a.streamURL(songID)
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": streamURL})
}

// Handlers: Scrobble toggle
// ---------------------------------------------------------------------------

func (a *app) handleScrobbleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": a.cfg.Scrobble.Enabled})
}

func (a *app) handleScrobbleToggle(w http.ResponseWriter, r *http.Request) {
	a.cfg.Scrobble.Enabled = !a.cfg.Scrobble.Enabled
	if a.cfg.Scrobble.Enabled {
		go a.scrobbleLoop()
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": a.cfg.Scrobble.Enabled})
}

// ---------------------------------------------------------------------------
// Handlers: Device management
// ---------------------------------------------------------------------------

func (a *app) handleDevicesList(w http.ResponseWriter, r *http.Request) {
	a.devicesMu.RLock()
	defer a.devicesMu.RUnlock()

	list := make([]map[string]any, 0, len(a.devices))
	for _, dev := range a.devices {
		online := dev.IsLocal || time.Since(dev.LastSeen) < 60*time.Second
		if !online {
			continue
		}
		list = append(list, map[string]any{
			"id":             dev.ID,
			"name":           dev.Name,
			"address":        dev.Address,
			"is_local":       dev.IsLocal,
			"type":           dev.Type,
			"format":         dev.Format,
			"max_bitrate":    dev.MaxBitRate,
			"navidrome_url":  dev.NavidromeURL,
			"replaygain":     dev.ReplayGain,
			"last_seen":      dev.LastSeen,
			"online":         online,
			"active":         dev.ID == a.activeDevice,
		})
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *app) handleDeviceRegister(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	name := stringify(body["name"])
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	devType := stringify(body["type"])
	if devType == "" {
		devType = "agent"
	}
	address := stringify(body["address"])
	if devType == "agent" && address == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "address is required"})
		return
	}
	format := stringify(body["format"])
	maxBitRate := intFromAny(body["max_bitrate"], 0)
	navidromeURL := stringify(body["navidrome_url"])
	replaygain := stringify(body["replaygain"])

	id := name // use name as ID for simplicity

	a.devicesMu.Lock()
	// Close old channel if re-registering a browser device
	if old, exists := a.devices[id]; exists && old.cmdCh != nil {
		close(old.cmdCh)
	}
	dev := &device{
		ID:           id,
		Name:         name,
		Address:      address,
		IsLocal:      false,
		Type:         devType,
		Format:       format,
		MaxBitRate:   maxBitRate,
		NavidromeURL: navidromeURL,
		ReplayGain:   replaygain,
		LastSeen:     time.Now(),
	}
	if devType == "browser" {
		dev.cmdCh = make(chan browserCmd, 32)
		dev.browserState = map[string]any{}
	}
	a.devices[id] = dev
	// Restore saved active device on re-registration
	if saved, err := os.ReadFile(a.paths.ActiveDeviceFile); err == nil && string(saved) == id {
		a.activeDevice = id
		a.logger.Printf("restored active device: %s", id)
	}
	a.devicesMu.Unlock()

	a.logger.Printf("device registered: %s (type=%s)", name, devType)
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (a *app) handleDeviceHeartbeat(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	id := stringify(body["id"])
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}

	a.devicesMu.Lock()
	dev, exists := a.devices[id]
	if exists {
		dev.LastSeen = time.Now()
	}
	a.devicesMu.Unlock()

	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *app) handleDeviceSetActive(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	newID := stringify(body["device_id"])
	if newID == "" {
		newID = stringify(body["id"])
	}
	if newID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "device_id is required"})
		return
	}

	a.devicesMu.Lock()
	newDev, exists := a.devices[newID]
	if !exists {
		a.devicesMu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}

	oldID := a.activeDevice
	if oldID == newID {
		a.devicesMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"message": "already active"})
		return
	}

	// Build old target while holding devicesMu (to get the correct target)
	var oldTarget playbackTarget
	if oldID == "" || oldID == "local" {
		oldTarget = a.mpv
	} else if oldDev := a.devices[oldID]; oldDev != nil {
		if oldDev.Type == "browser" {
			oldTarget = &browserTarget{dev: oldDev}
		} else {
			oldTarget = &remoteTarget{address: oldDev.Address, secret: a.cfg.Server.APISecret, client: &http.Client{Timeout: 5 * time.Second}}
		}
	} else {
		oldTarget = a.mpv
	}

	// Build new target
	var newTarget playbackTarget
	if newDev.IsLocal {
		newTarget = a.mpv
	} else if newDev.Type == "browser" {
		newTarget = &browserTarget{dev: newDev}
	} else {
		newTarget = &remoteTarget{address: newDev.Address, secret: a.cfg.Server.APISecret, client: &http.Client{Timeout: 5 * time.Second}}
	}

	a.devicesMu.Unlock()

	// Capture state from old target
	var playlistPos int
	var timePos float64
	var wasPaused bool

	if posRaw, err := oldTarget.getProperty("playlist-pos"); err == nil {
		if f, ok := posRaw.(float64); ok {
			playlistPos = int(f)
		}
	}
	if tpRaw, err := oldTarget.getProperty("time-pos"); err == nil {
		if f, ok := tpRaw.(float64); ok {
			timePos = f
		}
	}
	if pauseRaw, err := oldTarget.getProperty("pause"); err == nil {
		if p, ok := pauseRaw.(bool); ok {
			wasPaused = p
		}
	}

	// Pause old target and clear its playlist
	_ = oldTarget.setProperty("pause", true)
	_ = oldTarget.playlistClear()

	// Load playQueue into new target with device-specific stream URLs
	a.playQueueMu.Lock()
	queue := make([]string, len(a.playQueue))
	copy(queue, a.playQueue)
	a.playQueueMu.Unlock()

	if newDev.Type == "browser" {
		// Send entire queue + position in a single handoff command
		bt := &browserTarget{dev: newDev}
		tracks := make([]map[string]any, 0, len(queue))
		for _, songID := range queue {
			track := map[string]any{
				"url":     a.streamURLForDevice(songID, newDev.Format, newDev.MaxBitRate, newDev.NavidromeURL),
				"song_id": songID,
			}
			if meta := a.replayGainMeta(songID); meta != nil {
				if rgVal, ok := meta["replay_gain"]; ok {
					track["replay_gain"] = rgVal
				}
			}
			tracks = append(tracks, track)
		}
		_ = bt.send(browserCmd{
			Action: "handoff",
			Data: map[string]any{
				"tracks":       tracks,
				"playlist_pos": playlistPos,
				"time_pos":     timePos,
				"paused":       wasPaused,
			},
		})
	} else {
		for i, songID := range queue {
			loadMode := "append"
			if i == 0 {
				loadMode = "replace"
			}
			streamURL := a.streamURLForDevice(songID, newDev.Format, newDev.MaxBitRate, newDev.NavidromeURL)
			if err := newTarget.loadFile(streamURL, loadMode, a.replayGainMeta(songID)); err != nil {
				a.logger.Printf("device handoff: failed to load song %s: %v", songID, err)
			}
		}
	}

	// Set playlist position, seek, and resume — use atomic handoff where available
	if len(queue) > 0 && playlistPos >= 0 && playlistPos < len(queue) {
		switch {
		case newDev.Type == "browser":
			// handled above in single handoff command
		case newDev.Type == "agent":
			rt := newTarget.(*remoteTarget)
			if err := rt.handoff(playlistPos, timePos, wasPaused); err != nil {
				a.logger.Printf("device handoff: agent handoff failed: %v", err)
			}
		default: // local mpv
			_ = newTarget.setProperty("playlist-pos", playlistPos)
			// Wait for mpv to load the track
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
				if v, err := newTarget.getProperty("duration"); err == nil {
					if d, ok := v.(float64); ok && d > 0 {
						break
					}
				}
			}
			if timePos > 0 {
				_ = newTarget.setProperty("time-pos", timePos)
			}
			if !wasPaused {
				_ = newTarget.setProperty("pause", false)
			}
		}
	} else if !wasPaused {
		_ = newTarget.setProperty("pause", false)
	}

	// Update active device
	a.devicesMu.Lock()
	a.activeDevice = newID
	a.devicesMu.Unlock()
	_ = os.WriteFile(a.paths.ActiveDeviceFile, []byte(newID), 0o644)

	a.logger.Printf("active device switched: %s -> %s", oldID, newID)
	writeJSON(w, http.StatusOK, map[string]string{"message": "Device switched", "active_device": newID})
}

func (a *app) handleDeviceGetActive(w http.ResponseWriter, r *http.Request) {
	dev := a.activeDeviceInfo()
	if dev == nil {
		writeJSON(w, http.StatusOK, map[string]any{"id": "local", "name": "local", "is_local": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          dev.ID,
		"name":        dev.Name,
		"address":     dev.Address,
		"is_local":    dev.IsLocal,
		"format":      dev.Format,
		"max_bitrate": dev.MaxBitRate,
	})
}

func (a *app) deviceCleanup() {
	for {
		time.Sleep(30 * time.Second)
		a.devicesMu.RLock()
		for id, dev := range a.devices {
			if dev.IsLocal {
				continue
			}
			if time.Since(dev.LastSeen) > 60*time.Second {
				a.logger.Printf("device offline: %s (last seen %s)", id, dev.LastSeen.Format(time.RFC3339))
			}
		}
		a.devicesMu.RUnlock()
	}
}

// ---------------------------------------------------------------------------
// Browser device SSE + status
// ---------------------------------------------------------------------------

func (a *app) handleDeviceSSE(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}

	a.devicesMu.RLock()
	dev, exists := a.devices[id]
	a.devicesMu.RUnlock()

	if !exists || dev.Type != "browser" {
		http.Error(w, "device not found or not a browser device", http.StatusNotFound)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case cmd, ok := <-dev.cmdCh:
			if !ok {
				return
			}
			data, _ := json.Marshal(cmd)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (a *app) handleDeviceBrowserStatus(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	id := stringify(body["id"])
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}

	a.devicesMu.RLock()
	dev, exists := a.devices[id]
	a.devicesMu.RUnlock()

	if !exists || dev.Type != "browser" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}

	dev.browserStateMu.Lock()
	dev.browserState = map[string]any{
		"pause":        body["pause"],
		"time_pos":     body["time_pos"],
		"duration":     body["duration"],
		"playlist_pos": body["playlist_pos"],
	}
	dev.browserStateMu.Unlock()

	// Also update last seen
	a.devicesMu.Lock()
	dev.LastSeen = time.Now()
	a.devicesMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// Scrobble loop
// ---------------------------------------------------------------------------

func (a *app) scrobbleLoop() {
	a.logger.Printf("scrobble: started")
	for a.cfg.Scrobble.Enabled {
		time.Sleep(3 * time.Second)
		a.scrobbleTick()
	}
	a.logger.Printf("scrobble: stopped")
}

func (a *app) scrobbleTick() {
	songID := a.currentPlayingSongID()
	if songID == "" {
		return
	}

	t := a.target()
	pauseRaw, err := t.getProperty("pause")
	if err != nil {
		return
	}
	if paused, ok := pauseRaw.(bool); ok && paused {
		return
	}

	a.scrobbleMu.Lock()
	defer a.scrobbleMu.Unlock()

	if songID != a.scrobbleLastSongID {
		// New song started
		a.scrobbleLastSongID = songID
		a.scrobbleStartTime = time.Now()
		a.scrobbleSubmitted = false
		// Send "now playing"
		a.subsonicScrobble(songID, false)
		a.logger.Printf("scrobble: now playing %s", songID)
		return
	}

	if a.scrobbleSubmitted {
		return
	}

	elapsed := time.Since(a.scrobbleStartTime).Seconds()
	durRaw, err := t.getProperty("duration")
	if err != nil {
		return
	}
	duration := 0.0
	if f, ok := durRaw.(float64); ok {
		duration = f
	}

	// Scrobble after 50% of track or 4 minutes, whichever is first
	threshold := duration / 2
	if threshold > 240 {
		threshold = 240
	}
	if threshold < 30 {
		threshold = 30
	}

	if elapsed >= threshold {
		a.subsonicScrobble(songID, true)
		a.scrobbleSubmitted = true
		a.logger.Printf("scrobble: submitted %s (%.0fs elapsed, %.0fs threshold)", songID, elapsed, threshold)
	}
}

func (a *app) subsonicScrobble(songID string, submission bool) {
	params := url.Values{}
	params.Set("id", songID)
	if submission {
		params.Set("submission", "true")
	} else {
		params.Set("submission", "false")
	}
	_, _ = a.subsonicGet("scrobble", params)
}

// ---------------------------------------------------------------------------
// Album track resolution (cached tracks -> Navidrome song IDs)
// ---------------------------------------------------------------------------

func (a *app) albumTrackSongIDs(album map[string]any) ([]string, error) {
	navidromeAlbumID := stringify(album["navidrome_album_id"])
	if navidromeAlbumID == "" {
		return nil, fmt.Errorf("album has no navidrome_album_id")
	}
	detail, err := a.subsonicGetAlbum(navidromeAlbumID)
	if err != nil {
		return nil, err
	}
	if detail == nil || len(detail.Songs) == 0 {
		return nil, fmt.Errorf("no tracks found for album")
	}
	// Sort by disc then track
	slices.SortFunc(detail.Songs, func(a, b subSong) int {
		if a.DiscNumber != b.DiscNumber {
			return a.DiscNumber - b.DiscNumber
		}
		if a.Track != b.Track {
			return a.Track - b.Track
		}
		return strings.Compare(a.Title, b.Title)
	})
	ids := make([]string, 0, len(detail.Songs))
	for _, song := range detail.Songs {
		ids = append(ids, song.ID)
	}
	return ids, nil
}

// ---------------------------------------------------------------------------
// Cache building
// ---------------------------------------------------------------------------

func (a *app) rebuildCache(reason string) error {
	a.cacheLock.Lock()
	defer a.cacheLock.Unlock()

	a.logger.Printf("cache rebuild: started (%s)", reason)
	if err := a.createCache(); err != nil {
		a.logger.Printf("cache rebuild: failed (%s): %v", reason, err)
		return err
	}
	if err := a.saveCacheState(newCacheState(time.Now())); err != nil {
		a.logger.Printf("cache rebuild: failed to save state (%s): %v", reason, err)
		return err
	}
	a.logger.Printf("cache rebuild: finished (%s)", reason)
	a.loadTrackIndex()
	return nil
}

func (a *app) createCache() error {
	// Fetch all albums from Navidrome
	var allSubAlbums []subAlbum
	batchSize := 500
	for offset := 0; ; offset += batchSize {
		batch, err := a.subsonicGetAlbumList(offset, batchSize)
		if err != nil {
			return fmt.Errorf("fetch album list at offset %d: %w", offset, err)
		}
		if len(batch) == 0 {
			break
		}
		allSubAlbums = append(allSubAlbums, batch...)
		a.logger.Printf("cache rebuild: fetched %d albums so far", len(allSubAlbums))
		if len(batch) < batchSize {
			break
		}
	}

	a.logger.Printf("cache rebuild: fetching tracks for %d albums concurrently", len(allSubAlbums))

	// Fetch album details concurrently with a worker pool
	type albumResult struct {
		index  int
		album  subAlbum
		detail *subAlbumDetail
	}

	results := make([]albumResult, len(allSubAlbums))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16) // limit to 16 concurrent requests

	for i, sa := range allSubAlbums {
		results[i] = albumResult{index: i, album: sa}
		wg.Add(1)
		go func(idx int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			detail, err := a.subsonicGetAlbum(id)
			if err != nil {
				a.logger.Printf("cache rebuild: skip album %s: %v", id, err)
				return
			}
			results[idx].detail = detail
		}(i, sa.ID)
	}
	wg.Wait()

	albums := make([]map[string]any, 0, len(allSubAlbums))
	tracks := make([]map[string]any, 0)
	trackIndex := 0

	for i, res := range results {
		subAlbum := res.album
		albumArtist := subAlbum.Artist
		albumName := subAlbum.Name
		date := "0000"
		if subAlbum.Year > 0 {
			date = strconv.Itoa(subAlbum.Year)
		}

		albumEntry := map[string]any{
			"id":                 subAlbum.ID,
			"albumartist":        albumArtist,
			"album":              albumName,
			"date":               date,
			"navidrome_album_id": subAlbum.ID,
		}
		albumIdx := len(albums)
		albums = append(albums, albumEntry)

		if res.detail == nil {
			continue
		}

		var maxCreated string
		for _, song := range res.detail.Songs {
			tracks = append(tracks, map[string]any{
				"track":              strconv.Itoa(song.Track),
				"tracknumber":        song.Track,
				"discnumber":         song.DiscNumber,
				"title":              song.Title,
				"artist":             song.Artist,
				"albumartist":        albumArtist,
				"album":              albumName,
				"date":               date,
				"song_id":            song.ID,
				"duration":           song.Duration,
				"rating":             valueOrNil(""),
				"id":                 strconv.Itoa(trackIndex),
				"navidrome_album_id": subAlbum.ID,
				"replay_gain": map[string]any{
					"track_gain": song.ReplayGain.TrackGain,
					"album_gain": song.ReplayGain.AlbumGain,
					"track_peak": song.ReplayGain.TrackPeak,
					"album_peak": song.ReplayGain.AlbumPeak,
				},
			})
			trackIndex++
			if song.Created > maxCreated {
				maxCreated = song.Created
			}
		}
		if maxCreated != "" {
			albums[albumIdx]["last_modified"] = maxCreated
		}

		if (i+1)%100 == 0 {
			a.logger.Printf("cache rebuild: processed %d/%d albums, %d tracks", i+1, len(allSubAlbums), trackIndex)
		}
	}

	// Sort albums alphabetically by default
	slices.SortFunc(albums, func(a1, a2 map[string]any) int {
		if c := strings.Compare(strings.ToLower(stringify(a1["albumartist"])), strings.ToLower(stringify(a2["albumartist"]))); c != 0 {
			return c
		}
		if c := strings.Compare(stringify(a1["date"]), stringify(a2["date"])); c != 0 {
			return c
		}
		return strings.Compare(strings.ToLower(stringify(a1["album"])), strings.ToLower(stringify(a2["album"])))
	})

	if err := a.writeMapSlice(a.paths.AlbumCacheFile, albums); err != nil {
		return err
	}
	if err := a.writeMapSlice(a.paths.TracksCacheFile, tracks); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Watch for Navidrome library updates
// ---------------------------------------------------------------------------

func (a *app) watchNavidromeUpdates() {
	interval := time.Duration(a.cfg.Cache.PollInterval) * time.Second
	var lastScan string

	for {
		time.Sleep(interval)
		status, err := a.subsonicGetScanStatus()
		if err != nil {
			a.logger.Printf("navidrome watcher: scan status failed: %v", err)
			continue
		}
		if status.Scanning {
			continue
		}
		if status.LastScan != "" && status.LastScan != lastScan {
			if lastScan != "" {
				a.logger.Printf("navidrome watcher: new scan detected (%s), rebuilding cache", status.LastScan)
				if err := a.rebuildCache("navidrome scan update"); err != nil {
					a.logger.Printf("navidrome watcher: cache rebuild failed: %v", err)
				}
			}
			lastScan = status.LastScan
		}
	}
}

// ---------------------------------------------------------------------------
// Rating logic
// ---------------------------------------------------------------------------

func (a *app) updateAlbumRating(album map[string]any, rating string) (bool, error) {
	key := albumKey(album)
	if key == "" {
		return false, fmt.Errorf("cannot generate album key")
	}
	ratings, err := a.loadRatings()
	if err != nil {
		return false, err
	}
	current, exists := ratings[key]
	changed := false

	switch rating {
	case "Delete":
		if exists {
			delete(ratings, key)
			changed = true
		}
	case "---":
		return false, nil
	default:
		if current != rating {
			ratings[key] = rating
			changed = true
		}
	}

	if !changed {
		return false, nil
	}
	if err := a.saveRatings(ratings); err != nil {
		return false, err
	}
	return true, nil
}

func (a *app) updateTrackRating(track map[string]any, rating string) (bool, error) {
	songID := stringify(track["song_id"])
	if songID == "" {
		return false, fmt.Errorf("missing song_id")
	}

	switch rating {
	case "Delete":
		if err := a.updateTrackCacheRating(songID, ""); err != nil {
			return false, err
		}
		return true, nil
	case "---":
		return false, nil
	default:
		if err := a.updateTrackCacheRating(songID, rating); err != nil {
			return false, err
		}
		return true, nil
	}
}

func (a *app) updateTrackCacheRating(songID, rating string) error {
	tracks, err := a.readMapSlice(a.paths.TracksCacheFile)
	if err != nil {
		return err
	}
	changed := false
	for _, track := range tracks {
		if stringify(track["song_id"]) != songID {
			continue
		}
		track["rating"] = valueOrNil(rating)
		changed = true
		break
	}
	if !changed {
		return nil
	}
	return a.writeMapSlice(a.paths.TracksCacheFile, tracks)
}

// ---------------------------------------------------------------------------
// Cache state
// ---------------------------------------------------------------------------

func newCacheState(updatedAt time.Time) cacheState {
	updatedAt = updatedAt.UTC()
	return cacheState{
		Version:   updatedAt.UnixNano(),
		UpdatedAt: updatedAt.Format(time.RFC3339Nano),
	}
}

func (a *app) deriveCacheState() (cacheState, error) {
	paths := []string{a.paths.AlbumCacheFile, a.paths.TracksCacheFile}
	var newest time.Time
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return cacheState{}, err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		newest = time.Now()
	}
	return newCacheState(newest), nil
}

func (a *app) loadCacheState() (cacheState, error) {
	data, err := os.ReadFile(a.paths.CacheStateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return a.deriveCacheState()
		}
		return cacheState{}, err
	}
	if len(data) == 0 {
		return a.deriveCacheState()
	}
	var state cacheState
	if err := msgpack.Unmarshal(data, &state); err != nil {
		return cacheState{}, err
	}
	if state.Version == 0 || strings.TrimSpace(state.UpdatedAt) == "" {
		return a.deriveCacheState()
	}
	return state, nil
}

func (a *app) loadCacheStatusFull() (cacheStatus, error) {
	state, err := a.loadCacheState()
	if err != nil {
		return cacheStatus{}, err
	}

	status := cacheStatus{
		Version:   state.Version,
		UpdatedAt: state.UpdatedAt,
	}

	scanStatus, err := a.subsonicGetScanStatus()
	if err != nil {
		return status, nil
	}

	status.NavidromeConnected = true
	status.NavidromeScanning = scanStatus.Scanning
	if scanStatus.LastScan != "" {
		status.NavidromeLastScanned = scanStatus.LastScan
		t, err := time.Parse(time.RFC3339, scanStatus.LastScan)
		if err == nil {
			status.Stale = state.Version < t.UTC().UnixNano()
		}
	}

	return status, nil
}

func (a *app) saveCacheState(state cacheState) error {
	data, err := msgpack.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(a.paths.CacheStateFile, data, 0o644)
}

func (a *app) applyCacheStateHeaders(w http.ResponseWriter) {
	state, err := a.loadCacheState()
	if err != nil {
		return
	}
	a.writeCacheStateHeaders(w, state)
}

func (a *app) writeCacheStateHeaders(w http.ResponseWriter, state cacheState) {
	w.Header().Set("X-Clerk-Cache-Version", strconv.FormatInt(state.Version, 10))
	w.Header().Set("X-Clerk-Cache-Updated-At", state.UpdatedAt)
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", state.Version))
}

// ---------------------------------------------------------------------------
// Cache path & rating helpers
// ---------------------------------------------------------------------------

func (a *app) loadRatings() (map[string]string, error) {
	data, err := os.ReadFile(a.paths.RatingsCacheFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]string{}, nil
	}
	var ratings map[string]string
	if err := msgpack.Unmarshal(data, &ratings); err != nil {
		return nil, err
	}
	if ratings == nil {
		ratings = map[string]string{}
	}
	return ratings, nil
}

func (a *app) saveRatings(ratings map[string]string) error {
	data, err := msgpack.Marshal(ratings)
	if err != nil {
		return err
	}
	return os.WriteFile(a.paths.RatingsCacheFile, data, 0o644)
}

func (a *app) readMapSlice(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return []map[string]any{}, nil
	}
	var items []map[string]any
	if err := msgpack.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	if items == nil {
		items = []map[string]any{}
	}
	return items, nil
}

func (a *app) writeMapSlice(path string, items []map[string]any) error {
	data, err := msgpack.Marshal(items)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func attachAlbumRatings(items []map[string]any, ratings map[string]string) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		entry := cloneMap(item)
		entry["rating"] = ratings[albumKey(item)]
		out = append(out, entry)
	}
	return out
}

// ---------------------------------------------------------------------------
// Web UI handler
// ---------------------------------------------------------------------------

func (a *app) handleWebUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, webUIHTML)
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func decodeBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Request Content-Type is not 'application/json'"})
		return nil, false
	}
	return decodeBodyOptional(w, r)
}

func decodeBodyOptional(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	if r.Body == nil {
		return map[string]any{}, true
	}
	defer r.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if errors.Is(err, http.ErrBodyNotAllowed) {
			return map[string]any{}, true
		}
		if strings.Contains(err.Error(), "EOF") {
			return map[string]any{}, true
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Error parsing request body: " + err.Error()})
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

func (a *app) writeError(w http.ResponseWriter, r *http.Request, status int, message string) {
	a.logger.Printf("%s %s -> %d: %s", r.Method, r.URL.Path, status, message)
	writeJSON(w, status, map[string]string{"error": message})
}

// ---------------------------------------------------------------------------
// Generic helpers
// ---------------------------------------------------------------------------

func albumKey(item map[string]any) string {
	artist := stringify(item["albumartist"])
	if artist == "" {
		artist = stringify(item["artist"])
	}
	album := stringify(item["album"])
	date := stringify(item["date"])
	if artist == "" || album == "" || date == "" {
		return ""
	}
	return artist + "|||" + album + "|||" + date
}

func findByID(items []map[string]any, id string) map[string]any {
	for _, item := range items {
		if stringify(item["id"]) == id {
			return item
		}
	}
	return nil
}

func findManyByID(items []map[string]any, ids []string) []map[string]any {
	index := map[string]map[string]any{}
	for _, item := range items {
		index[stringify(item["id"])] = item
	}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		if item, ok := index[id]; ok {
			out = append(out, item)
		}
	}
	return out
}

func stringSlice(value any) []string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := stringify(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func normalizePlaylistMode(mode string) string {
	switch mode {
	case "insert", "replace":
		return mode
	default:
		return "add"
	}
}

func validRating(value string) bool {
	if value == "Delete" || value == "---" {
		return true
	}
	for i := 1; i <= 10; i++ {
		if value == strconv.Itoa(i) {
			return true
		}
	}
	return false
}

func valueOrNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func stringify(value any) string {
	return shared.Stringify(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func cloneMap(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func getenvDefault(key, fallback string) string {
	return shared.Getenv(key, fallback)
}

func intFromAny(value any, fallback int) int {
	return shared.IntFromAny(value, fallback)
}

func boolFromAny(value any, fallback bool) bool {
	return shared.BoolFromAny(value, fallback)
}

func splitAndTrim(value, sep string) []string {
	parts := strings.Split(value, sep)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

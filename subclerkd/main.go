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
	} `toml:"server"`
	Navidrome struct {
		URL      string `toml:"url"`
		Username string `toml:"username"`
		Password string `toml:"password"`
	} `toml:"navidrome"`
	MPV struct {
		Socket     string `toml:"socket"`
		Executable string `toml:"executable"`
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
	LatestCacheFile  string
	RatingsCacheFile string
	CacheStateFile   string
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
}

type subScanStatus struct {
	Scanning  bool   `json:"scanning"`
	Count     int64  `json:"count"`
	LastScan  string `json:"lastScan,omitempty"`
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
		},
		playQueue: []string{},
	}

	if err := a.ensureStartupState(); err != nil {
		logger.Fatalf("startup failed: %v", err)
	}

	go a.ensureMPV()
	go a.watchNavidromeUpdates()
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
		LatestCacheFile:  filepath.Join(xdgData, "subclerk", "latest.cache"),
		RatingsCacheFile: filepath.Join(xdgData, "subclerk", "ratings.cache"),
		CacheStateFile:   filepath.Join(xdgData, "subclerk", "cache.state"),
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
	cfg.Navidrome.URL = stringify(navidrome["url"])
	cfg.Navidrome.Username = stringify(navidrome["username"])
	cfg.Navidrome.Password = stringify(navidrome["password"])
	cfg.MPV.Socket = stringify(mpvSection["socket"])
	cfg.MPV.Executable = stringify(mpvSection["executable"])
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

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()

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

	// Browse & Search
	mux.HandleFunc("GET /api/v1/browse/artists", a.handleBrowseArtists)
	mux.HandleFunc("GET /api/v1/browse/albums", a.handleBrowseAlbums)
	mux.HandleFunc("GET /api/v1/browse/tracks", a.handleBrowseTracks)
	mux.HandleFunc("GET /api/v1/search", a.handleSearch)

	// Scrobble toggle
	mux.HandleFunc("GET /api/v1/scrobble/status", a.handleScrobbleStatus)
	mux.HandleFunc("POST /api/v1/scrobble/toggle", a.handleScrobbleToggle)

	// Web UI
	mux.HandleFunc("GET /ui", a.handleWebUI)
	mux.HandleFunc("GET /ui/", a.handleWebUI)

	return mux
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
	required := []string{a.paths.AlbumCacheFile, a.paths.TracksCacheFile, a.paths.LatestCacheFile, a.paths.RatingsCacheFile}
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

	cmd := exec.Command(m.executable, "--idle", "--no-video", "--no-terminal",
		"--input-ipc-server="+m.socketPath)
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
// Playback helpers
// ---------------------------------------------------------------------------

func (a *app) addSongsToPlaylist(songIDs []string, mode string) error {
	if len(songIDs) == 0 {
		return nil
	}

	a.playQueueMu.Lock()
	defer a.playQueueMu.Unlock()

	switch mode {
	case "replace":
		if err := a.mpv.playlistClear(); err != nil {
			return err
		}
		a.playQueue = nil
		for i, id := range songIDs {
			loadMode := "append"
			if i == 0 {
				loadMode = "replace"
			}
			if err := a.mpv.loadFile(a.streamURL(id), loadMode); err != nil {
				return err
			}
			a.playQueue = append(a.playQueue, id)
		}
		return a.mpv.setProperty("pause", false)

	case "insert":
		posRaw, _ := a.mpv.getProperty("playlist-pos")
		pos := 0
		if f, ok := posRaw.(float64); ok && f >= 0 {
			pos = int(f) + 1
		}
		for i, id := range songIDs {
			if err := a.mpv.loadFile(a.streamURL(id), "append"); err != nil {
				return err
			}
			// Move from end to insert position
			endIdx := len(a.playQueue) + i
			targetIdx := pos + i
			if endIdx > targetIdx {
				a.mpv.command("playlist-move", endIdx, targetIdx)
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
		return a.mpv.setProperty("pause", false)

	default: // "add"
		for _, id := range songIDs {
			if err := a.mpv.loadFile(a.streamURL(id), "append"); err != nil {
				return err
			}
			a.playQueue = append(a.playQueue, id)
		}
		return nil
	}
}

func (a *app) currentPlayingSongID() string {
	a.playQueueMu.Lock()
	defer a.playQueueMu.Unlock()

	posRaw, err := a.mpv.getProperty("playlist-pos")
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

func (a *app) handleAlbums(w http.ResponseWriter, r *http.Request) {
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
	a.applyCacheStateHeaders(w)
	writeJSON(w, http.StatusOK, attachAlbumRatings(albums, ratings))
}

func (a *app) handleLatestAlbums(w http.ResponseWriter, r *http.Request) {
	albums, err := a.readMapSlice(a.paths.LatestCacheFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	ratings, err := a.loadRatings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	a.applyCacheStateHeaders(w)
	writeJSON(w, http.StatusOK, attachAlbumRatings(albums, ratings))
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
	cachePath, err := a.albumCachePath(strings.TrimSpace(r.URL.Query().Get("list_mode")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	albums, err := a.readMapSlice(cachePath)
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
	listMode := stringify(body["list_mode"])
	if listMode == "" {
		listMode = strings.TrimSpace(r.URL.Query().Get("list_mode"))
	}
	cachePath, err := a.albumCachePath(listMode)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	albums, err := a.readMapSlice(cachePath)
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
	listMode := stringify(body["list_mode"])
	if listMode == "" {
		listMode = "album"
	}
	cachePath, err := a.albumCachePath(listMode)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	albums, err := a.readMapSlice(cachePath)
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
	listMode := stringify(body["list_mode"])
	if listMode == "" {
		listMode = "album"
	}
	cachePath, err := a.albumCachePath(listMode)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	albums, err := a.readMapSlice(cachePath)
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
	if err := a.mpv.setProperty("pause", false); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Playing"})
}

func (a *app) handlePause(w http.ResponseWriter, r *http.Request) {
	if err := a.mpv.setProperty("pause", true); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Paused"})
}

func (a *app) handleStop(w http.ResponseWriter, r *http.Request) {
	_ = a.mpv.setProperty("pause", true)
	_, _ = a.mpv.command("stop")
	a.playQueueMu.Lock()
	a.playQueue = nil
	a.playQueueMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"message": "Stopped"})
}

func (a *app) handleNext(w http.ResponseWriter, r *http.Request) {
	if _, err := a.mpv.command("playlist-next"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Next track"})
}

func (a *app) handlePrev(w http.ResponseWriter, r *http.Request) {
	if _, err := a.mpv.command("playlist-prev"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Previous track"})
}

func (a *app) handlePlaybackStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]any{
		"state": "stopped",
	}

	pauseRaw, err := a.mpv.getProperty("pause")
	if err == nil {
		if paused, ok := pauseRaw.(bool); ok {
			if paused {
				status["state"] = "paused"
			} else {
				status["state"] = "playing"
			}
		}
	}

	if posRaw, err := a.mpv.getProperty("playlist-pos"); err == nil {
		if pos, ok := posRaw.(float64); ok {
			status["playlist_pos"] = int(pos)
		}
	}

	if timeRaw, err := a.mpv.getProperty("time-pos"); err == nil {
		status["time_pos"] = timeRaw
	}
	if durRaw, err := a.mpv.getProperty("duration"); err == nil {
		status["duration"] = durRaw
	}

	songID := a.currentPlayingSongID()
	if songID != "" {
		status["song_id"] = songID
		if track := a.findTrackBySongID(songID); track != nil {
			status["title"] = track["title"]
			status["artist"] = track["artist"]
			status["album"] = track["album"]
			status["date"] = track["date"]
		}
	}

	writeJSON(w, http.StatusOK, status)
}

func (a *app) handleQueueGet(w http.ResponseWriter, r *http.Request) {
	a.playQueueMu.Lock()
	queue := make([]string, len(a.playQueue))
	copy(queue, a.playQueue)
	a.playQueueMu.Unlock()

	posRaw, _ := a.mpv.getProperty("playlist-pos")
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
	if err := a.mpv.playlistRemove(pos); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	a.playQueue = append(a.playQueue[:pos], a.playQueue[pos+1:]...)
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
	if err := a.mpv.setProperty("time-pos", pos); err != nil {
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
	if _, err := a.mpv.command("playlist-move", from, mpvTo); err != nil {
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
	if err := a.mpv.setProperty("playlist-pos", pos); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Playing"})
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

	pauseRaw, err := a.mpv.getProperty("pause")
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
	durRaw, err := a.mpv.getProperty("duration")
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
	latestMap := map[string]map[string]any{}
	trackIndex := 0

	for i, res := range results {
		subAlbum := res.album
		albumArtist := subAlbum.Artist
		albumName := subAlbum.Name
		date := "0000"
		if subAlbum.Year > 0 {
			date = strconv.Itoa(subAlbum.Year)
		}

		key := albumArtist + "|||" + albumName + "|||" + date

		albums = append(albums, map[string]any{
			"albumartist":        albumArtist,
			"album":              albumName,
			"date":               date,
			"navidrome_album_id": subAlbum.ID,
		})

		if res.detail == nil {
			continue
		}

		for _, song := range res.detail.Songs {
			tracks = append(tracks, map[string]any{
				"track":       strconv.Itoa(song.Track),
				"tracknumber": song.Track,
				"discnumber":  song.DiscNumber,
				"title":       song.Title,
				"artist":      song.Artist,
				"albumartist": albumArtist,
				"album":       albumName,
				"date":        date,
				"song_id":     song.ID,
				"duration":    song.Duration,
				"rating":      valueOrNil(""),
				"id":          strconv.Itoa(trackIndex),
			})
			trackIndex++

			prev := latestMap[key]
			if prev == nil || strings.Compare(song.Created, stringify(prev["last-modified"])) > 0 {
				latestMap[key] = map[string]any{
					"albumartist":        albumArtist,
					"album":              albumName,
					"date":               date,
					"last-modified":      song.Created,
					"navidrome_album_id": subAlbum.ID,
				}
			}
		}

		if (i+1)%100 == 0 {
			a.logger.Printf("cache rebuild: processed %d/%d albums, %d tracks", i+1, len(allSubAlbums), trackIndex)
		}
	}

	// Sort albums
	slices.SortFunc(albums, func(a1, a2 map[string]any) int {
		if c := strings.Compare(strings.ToLower(stringify(a1["albumartist"])), strings.ToLower(stringify(a2["albumartist"]))); c != 0 {
			return c
		}
		if c := strings.Compare(stringify(a1["date"]), stringify(a2["date"])); c != 0 {
			return c
		}
		return strings.Compare(strings.ToLower(stringify(a1["album"])), strings.ToLower(stringify(a2["album"])))
	})
	for i := range albums {
		albums[i]["id"] = strconv.Itoa(i)
	}

	// Build latest list
	latest := make([]map[string]any, 0, len(latestMap))
	for _, album := range latestMap {
		latest = append(latest, album)
	}
	slices.SortFunc(latest, func(a1, a2 map[string]any) int {
		return strings.Compare(stringify(a2["last-modified"]), stringify(a1["last-modified"]))
	})
	for i := range latest {
		latest[i]["id"] = strconv.Itoa(i)
	}

	if err := a.writeMapSlice(a.paths.AlbumCacheFile, albums); err != nil {
		return err
	}
	if err := a.writeMapSlice(a.paths.TracksCacheFile, tracks); err != nil {
		return err
	}
	if err := a.writeMapSlice(a.paths.LatestCacheFile, latest); err != nil {
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
	paths := []string{a.paths.AlbumCacheFile, a.paths.TracksCacheFile, a.paths.LatestCacheFile}
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

func (a *app) albumCachePath(mode string) (string, error) {
	if mode == "" {
		mode = "album"
	}
	switch mode {
	case "album":
		return a.paths.AlbumCacheFile, nil
	case "latest":
		return a.paths.LatestCacheFile, nil
	default:
		return "", fmt.Errorf("invalid list mode")
	}
}

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

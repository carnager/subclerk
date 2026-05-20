package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/carnager/subclerk/internal/shared"
	"github.com/mattn/go-runewidth"
)

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

type tuiConfig struct {
	Master string `toml:"master"` // e.g. "local", "192.168.1.10:6701"
}

var cfg tuiConfig

func loadTUIConfig() tuiConfig {
	home, err := os.UserHomeDir()
	if err != nil {
		return tuiConfig{}
	}
	xdgConfig := shared.Getenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	configPath := filepath.Join(xdgConfig, "subclerk", "subclerk-tui.toml")

	_ = os.MkdirAll(filepath.Dir(configPath), 0o755)

	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		_ = os.WriteFile(configPath, []byte("master = \"local\"\n"), 0o644)
	}

	var c tuiConfig
	if _, err := toml.DecodeFile(configPath, &c); err != nil {
		fmt.Fprintf(os.Stderr, "warning: config: %v\n", err)
	}
	if c.Master == "" {
		c.Master = "local"
	}
	return c
}

// ---------------------------------------------------------------------------
// Ensure agent is running (spawn detached if not)
// ---------------------------------------------------------------------------

func ensureAgent() {
	// Check if agent is reachable by reading its config for the bind address
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	xdgConfig := shared.Getenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	agentConfigPath := filepath.Join(xdgConfig, "subclerk", "subclerk-agent.toml")

	// Read agent bind address from config (default 0.0.0.0:6703)
	agentAddr := "127.0.0.1:6702"
	if _, err := os.Stat(agentConfigPath); err == nil {
		var raw map[string]any
		if _, err := toml.DecodeFile(agentConfigPath, &raw); err == nil {
			if agent, ok := raw["agent"].(map[string]any); ok {
				if bind, ok := agent["bind"].(string); ok && bind != "" {
					// Replace 0.0.0.0 with 127.0.0.1 for health check
					agentAddr = strings.Replace(bind, "0.0.0.0", "127.0.0.1", 1)
				}
			}
		}
	}

	// Try to reach the agent health endpoint
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + agentAddr + "/agent/v1/health")
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return // agent already running
		}
	}

	// Agent not running — find the binary and spawn it detached
	agentBin := findAgentBinary()
	if agentBin == "" {
		fmt.Fprintln(os.Stderr, "warning: subclerk-agent not found in PATH or next to this binary")
		return
	}

	cmd := exec.Command(agentBin)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	// Detach from parent process
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to start agent: %v\n", err)
		return
	}
	// Release so the agent lives on after TUI exits
	_ = cmd.Process.Release()
}

func findAgentBinary() string {
	// Check PATH first
	if p, err := exec.LookPath("subclerk-agent"); err == nil {
		return p
	}
	// Check next to our own binary
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	candidate := filepath.Join(filepath.Dir(self), "subclerk-agent")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// ---------------------------------------------------------------------------
// API client (talks to master)
// ---------------------------------------------------------------------------

var httpClient *http.Client
var apiBase string

func initAPI() {
	base, useLocal, sock, err := shared.APIBaseURLFromAddress(cfg.Master)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	apiBase = base
	if useLocal {
		httpClient = shared.NewLocalHTTPClient(5*time.Second, sock)
	} else {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
}

func apiCall(method, path string, body string) ([]byte, error) {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, apiBase+"/"+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func apiJSON(method, path, body string, out any) error {
	data, err := apiCall(method, path, body)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// ---------------------------------------------------------------------------
// API types
// ---------------------------------------------------------------------------

type playbackStatus struct {
	State   string  `json:"state"`
	Title   string  `json:"title"`
	Artist  string  `json:"artist"`
	Album   string  `json:"album"`
	Date    string  `json:"date"`
	TimePos float64 `json:"time_pos"`
	Dur     float64 `json:"duration"`
}

type queueItem struct {
	Position int     `json:"position"`
	SongID   string  `json:"song_id"`
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Album    string  `json:"album"`
	Duration float64 `json:"duration"`
	Current  bool    `json:"current"`
}

type albumEntry struct {
	ID          string `json:"id"`
	AlbumArtist string `json:"albumartist"`
	Album       string `json:"album"`
	Date        string `json:"date"`
}

type deviceInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsLocal  bool   `json:"is_local"`
	Online   bool   `json:"online"`
	Format   string `json:"format"`
	BitRate  int    `json:"max_bitrate"`
}

type activeDeviceInfo struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
}

type trackEntry struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	TrackNumber int    `json:"tracknumber"`
}

type searchResult struct {
	Albums []albumEntry `json:"albums"`
	Tracks []trackEntry `json:"tracks"`
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

type tickMsg time.Time

type statusMsg struct {
	status playbackStatus
	queue  []queueItem
}

type artistsMsg []string

type albumsMsg []albumEntry

type tracksMsg []trackEntry

type searchMsg searchResult

type devicesMsg struct {
	devices []deviceInfo
	active  string // active device ID
}

type ratingInfo struct {
	Rating      string `json:"rating"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	AlbumArtist string `json:"albumartist"`
	Album       string `json:"album"`
	Date        string `json:"date"`
}

type ratingMsg ratingInfo

// ---------------------------------------------------------------------------
// Focus / panel
// ---------------------------------------------------------------------------

type panel int

const (
	panelLibrary panel = iota
	panelQueue
)

type libView int

const (
	libArtists libView = iota
	libAlbums
	libTracks
)

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

type model struct {
	width, height int

	focus panel

	// playback
	status playbackStatus
	queue  []queueItem

	// library
	libMode    libView
	artists    []string
	albums     []albumEntry
	tracks     []trackEntry
	curArtist  string
	libCursor  int
	libOffset  int

	// queue
	qCursor      int
	qOffset      int
	qSelected    map[int]bool // selected queue positions
	confirmClear bool
	qVisited     bool   // has the user tabbed to queue at least once
	qFirstSongID string // first song ID to detect queue replacement

	// search
	searching   bool
	searchInput textinput.Model
	searchRes   searchResult
	srCursor    int
	srOffset    int
	srTotal     int

	// action menu
	showMenu   bool
	menuCursor int
	menuSource string // "library" or "search"

	// help
	showHelp bool

	// devices
	showDevices  bool
	devices      []deviceInfo
	activeDevice string // active device ID
	devCursor    int

	// rating
	showRating   bool
	ratingMode   string // "track" or "album"
	ratingLabel  string
	ratingCur    string
	ratingCursor int

	err string
}

func newModel() model {
	ti := textinput.New()
	ti.Placeholder = "Search albums and tracks..."
	ti.CharLimit = 100

	return model{
		focus:       panelLibrary,
		libMode:     libArtists,
		searchInput: ti,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		fetchArtists,
		fetchStatus,
		tickCmd(),
	)
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

func tickCmd() tea.Cmd {
	return tea.Tick(800*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func fetchStatus() tea.Msg {
	var st playbackStatus
	var q []queueItem
	_ = apiJSON("GET", "playback/status", "", &st)
	_ = apiJSON("GET", "playback/queue", "", &q)
	return statusMsg{status: st, queue: q}
}

func fetchArtists() tea.Msg {
	var artists []string
	_ = apiJSON("GET", "browse/artists", "", &artists)
	return artistsMsg(artists)
}

func fetchAlbums(artist string) tea.Cmd {
	return func() tea.Msg {
		var albums []albumEntry
		_ = apiJSON("GET", "browse/albums?artist="+url.QueryEscape(artist), "", &albums)
		return albumsMsg(albums)
	}
}

func fetchTracks(albumID string) tea.Cmd {
	return func() tea.Msg {
		var tracks []trackEntry
		_ = apiJSON("GET", "browse/tracks?album_id="+albumID, "", &tracks)
		return tracksMsg(tracks)
	}
}

func doSearch(q string) tea.Cmd {
	return func() tea.Msg {
		var res searchResult
		_ = apiJSON("GET", "search?q="+url.QueryEscape(q), "", &res)
		return searchMsg(res)
	}
}

func fetchDevices() tea.Msg {
	var devs []deviceInfo
	_ = apiJSON("GET", "devices", "", &devs)
	var act activeDeviceInfo
	_ = apiJSON("GET", "devices/active", "", &act)
	return devicesMsg{devices: devs, active: act.DeviceID}
}

func setActiveDevice(id string) tea.Cmd {
	return func() tea.Msg {
		apiCall("POST", "devices/active", fmt.Sprintf(`{"device_id":"%s"}`, id))
		return fetchDevices()
	}
}

func doPost(path, body string) tea.Cmd {
	return func() tea.Msg {
		apiCall("POST", path, body)
		return fetchStatus()
	}
}

func doDelete(path string) tea.Cmd {
	return func() tea.Msg {
		apiCall("DELETE", path, "")
		return fetchStatus()
	}
}

func doDeleteMultiple(positions []int) tea.Cmd {
	return func() tea.Msg {
		for _, pos := range positions {
			apiCall("DELETE", fmt.Sprintf("playback/queue/%d", pos), "")
		}
		return fetchStatus()
	}
}

type movePair struct{ from, to int }

func doMoveMultiple(moves []movePair) tea.Cmd {
	return func() tea.Msg {
		for _, mv := range moves {
			body := fmt.Sprintf(`{"from":%d,"to":%d}`, mv.from, mv.to)
			apiCall("POST", "playback/queue/move", body)
		}
		return fetchStatus()
	}
}

func sortedSelected(sel map[int]bool) []int {
	positions := make([]int, 0, len(sel))
	for p := range sel {
		positions = append(positions, p)
	}
	sort.Ints(positions)
	return positions
}

func fetchRating(mode string) tea.Cmd {
	return func() tea.Msg {
		endpoint := "current_track/rating"
		if mode == "album" {
			endpoint = "current_album/rating"
		}
		var info ratingInfo
		_ = apiJSON("GET", endpoint, "", &info)
		return ratingMsg(info)
	}
}

func submitRating(mode, value string) tea.Cmd {
	return func() tea.Msg {
		endpoint := "current_track/rating"
		if mode == "album" {
			endpoint = "current_album/rating"
		}
		apiCall("POST", endpoint, fmt.Sprintf(`{"rating":"%s"}`, value))
		return fetchStatus()
	}
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		return m, tea.Batch(tea.Cmd(fetchStatus), tickCmd())

	case statusMsg:
		m.status = msg.status
		// Detect queue replacement
		newFirstID := ""
		if len(msg.queue) > 0 {
			newFirstID = msg.queue[0].SongID
		}
		if newFirstID != m.qFirstSongID {
			m.qCursor = 0
			m.qOffset = 0
			m.qSelected = nil
			m.qFirstSongID = newFirstID
		}
		m.queue = msg.queue
		return m, nil

	case artistsMsg:
		m.artists = msg
		m.libCursor = 0
		m.libOffset = 0
		return m, nil

	case albumsMsg:
		m.albums = msg
		m.libMode = libAlbums
		m.libCursor = 0
		m.libOffset = 0
		return m, nil

	case tracksMsg:
		m.tracks = msg
		m.libMode = libTracks
		m.libCursor = 0
		m.libOffset = 0
		return m, nil

	case searchMsg:
		m.searchRes = searchResult(msg)
		m.srTotal = len(m.searchRes.Albums) + len(m.searchRes.Tracks)
		m.srCursor = 0
		return m, nil

	case devicesMsg:
		m.devices = msg.devices
		m.activeDevice = msg.active
		return m, nil

	case ratingMsg:
		info := ratingInfo(msg)
		m.showRating = true
		m.ratingCur = info.Rating
		m.ratingCursor = 0
		if m.ratingMode == "track" {
			m.ratingLabel = info.Title
			if info.Artist != "" {
				m.ratingLabel += " \u2014 " + info.Artist
			}
		} else {
			m.ratingLabel = info.Album
			if info.AlbumArtist != "" {
				m.ratingLabel += " \u2014 " + info.AlbumArtist
			}
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			// Seekbar is on the second-to-last line of the screen
			seekY := m.height - 2
			if msg.Y == seekY && m.status.Dur > 0 {
				posStr := fmtTime(m.status.TimePos)
				durStr := fmtTime(m.status.Dur)
				barStart := len(posStr) + 1
				barW := m.width - len(posStr) - len(durStr) - 6
				if barW < 5 {
					barW = 5
				}
				x := msg.X - barStart
				if x >= 0 && x <= barW {
					pos := float64(x) / float64(barW) * m.status.Dur
					body := fmt.Sprintf(`{"position":%.1f}`, pos)
					return m, doPost("playback/seek", body)
				}
			}
		}
	}

	if m.searching {
		var cmd tea.Cmd
		m.searchInput, cmd = m.searchInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Quit
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if key == "q" && !m.searching && !m.showMenu && !m.showRating && !m.showHelp {
		return m, tea.Quit
	}

	// Help mode
	if m.showHelp {
		m.showHelp = false
		return m, nil
	}

	// Device picker mode
	if m.showDevices {
		return m.handleDeviceKey(key)
	}

	// Rating mode
	if m.showRating {
		return m.handleRatingKey(key)
	}

	// Action menu mode
	if m.showMenu {
		return m.handleMenuKey(key)
	}

	// Search mode
	if m.searching {
		return m.handleSearchKey(msg, key)
	}

	// Global hotkeys
	switch key {
	case "/":
		m.searching = true
		m.searchInput.SetValue("")
		m.searchInput.Focus()
		m.searchRes = searchResult{}
		m.srCursor = 0
		m.srTotal = 0
		return m, textinput.Blink
	case " ":
		path := "playback/play"
		if m.status.State == "playing" {
			path = "playback/pause"
		}
		return m, doPost(path, "")
	case ">":
		return m, doPost("playback/next", "")
	case "<":
		return m, doPost("playback/prev", "")
	case "s":
		return m, doPost("playback/stop", "")
	case "r":
		return m, doPost("playback/random/album", "")
	case "R":
		return m, doPost("playback/random/tracks", "")
	case "u":
		return m, doPost("cache/update", "")
	case "T":
		m.ratingMode = "track"
		return m, fetchRating("track")
	case "A":
		m.ratingMode = "album"
		return m, fetchRating("album")
	case "?":
		m.showHelp = true
		return m, nil
	case "D":
		m.showDevices = true
		m.devCursor = 0
		return m, tea.Cmd(fetchDevices)
	case "tab":
		if m.focus == panelLibrary {
			m.focus = panelQueue
		} else {
			m.focus = panelLibrary
		}
		return m, nil
	}

	// Panel-specific
	if m.focus == panelLibrary {
		return m.handleLibKey(key)
	}
	return m.handleQueueKey(key)
}

var menuOptions = []string{"Add to queue", "Insert after current", "Replace queue", "Browse into"}

func (m model) menuOptionCount() int {
	if m.menuSource == "search" {
		idx := m.srCursor
		nAlbums := len(m.searchRes.Albums)
		if idx < nAlbums {
			return 4 // albums can browse into
		}
		return 3 // tracks: no browse
	}
	if m.libMode == libTracks {
		return 3 // no "Browse into" for tracks
	}
	return len(menuOptions)
}

func (m model) handleMenuKey(key string) (tea.Model, tea.Cmd) {
	maxIdx := m.menuOptionCount() - 1
	switch key {
	case "esc", "q":
		m.showMenu = false
		return m, nil
	case "j", "down":
		if m.menuCursor < maxIdx {
			m.menuCursor++
		}
		return m, nil
	case "k", "up":
		if m.menuCursor > 0 {
			m.menuCursor--
		}
		return m, nil
	case "enter":
		m.showMenu = false
		if m.menuSource == "search" {
			switch m.menuCursor {
			case 0:
				return m.searchAction("add")
			case 1:
				return m.searchAction("insert")
			case 2:
				return m.searchAction("replace")
			case 3:
				return m.searchDrillIn()
			}
		} else {
			switch m.menuCursor {
			case 0:
				return m.libAction("add")
			case 1:
				return m.libAction("insert")
			case 2:
				return m.libAction("replace")
			case 3:
				return m.libDrillIn()
			}
		}
	}
	return m, nil
}

var ratingValues = []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "Delete"}

func (m model) handleRatingKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q":
		m.showRating = false
		return m, nil
	case "j", "down", "right":
		if m.ratingCursor < len(ratingValues)-1 {
			m.ratingCursor++
		}
		return m, nil
	case "k", "up", "left":
		if m.ratingCursor > 0 {
			m.ratingCursor--
		}
		return m, nil
	case "enter":
		m.showRating = false
		return m, submitRating(m.ratingMode, ratingValues[m.ratingCursor])
	case "delete", "backspace":
		m.showRating = false
		return m, submitRating(m.ratingMode, "Delete")
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		m.showRating = false
		return m, submitRating(m.ratingMode, key)
	case "0":
		m.showRating = false
		return m, submitRating(m.ratingMode, "10")
	}
	return m, nil
}

func (m model) handleLibKey(key string) (tea.Model, tea.Cmd) {
	listLen := m.libListLen()

	switch key {
	case "j", "down":
		if m.libCursor < listLen-1 {
			m.libCursor++
		}
		return m, nil
	case "k", "up":
		if m.libCursor > 0 {
			m.libCursor--
		}
		return m, nil
	case "g", "home":
		m.libCursor = 0
		return m, nil
	case "G", "end":
		if listLen > 0 {
			m.libCursor = listLen - 1
		}
		return m, nil
	case "pgdown":
		m.libCursor += 20
		if m.libCursor >= listLen {
			m.libCursor = listLen - 1
		}
		return m, nil
	case "pgup":
		m.libCursor -= 20
		if m.libCursor < 0 {
			m.libCursor = 0
		}
		return m, nil
	case "enter":
		di := m.dataIndex()
		if di < 0 {
			return m.libBack()
		}
		m.showMenu = true
		m.menuCursor = 0
		m.menuSource = "library"
		return m, nil
	case "l", "right":
		return m.libDrillIn()
	case "h", "left", "backspace":
		return m.libBack()
	case "a": // add
		return m.libAction("add")
	case "A": // replace
		return m.libAction("replace")
	case "i": // insert
		return m.libAction("insert")
	}
	return m, nil
}

func (m model) libListLen() int {
	switch m.libMode {
	case libArtists:
		return len(m.artists)
	case libAlbums:
		return len(m.albums) + 1 // +1 for back row
	case libTracks:
		return len(m.tracks) + 1 // +1 for back row
	}
	return 0
}

// dataIndex returns the index into the data slice, accounting for the back row.
// Returns -1 if the cursor is on the back row.
func (m model) dataIndex() int {
	if m.libMode == libArtists {
		return m.libCursor
	}
	return m.libCursor - 1 // subtract back row
}

func (m model) libDrillIn() (tea.Model, tea.Cmd) {
	di := m.dataIndex()
	if di < 0 {
		return m.libBack()
	}
	switch m.libMode {
	case libArtists:
		if di < len(m.artists) {
			m.curArtist = m.artists[di]
			return m, fetchAlbums(m.curArtist)
		}
	case libAlbums:
		if di < len(m.albums) {
			return m, fetchTracks(m.albums[di].ID)
		}
	case libTracks:
		// Tracks are leaf level, drill-in adds to queue
		if di < len(m.tracks) {
			return m, doPost("playlist/add/track/"+m.tracks[di].ID, `{"mode":"add"}`)
		}
	}
	return m, nil
}

func (m model) libBack() (tea.Model, tea.Cmd) {
	switch m.libMode {
	case libAlbums:
		m.libMode = libArtists
		m.libCursor = 0
		m.libOffset = 0
	case libTracks:
		m.libMode = libAlbums
		m.libCursor = 0
		m.libOffset = 0
	}
	return m, nil
}

func (m model) libAction(mode string) (tea.Model, tea.Cmd) {
	di := m.dataIndex()
	if di < 0 {
		return m, nil
	}
	body := fmt.Sprintf(`{"mode":"%s"}`, mode)
	switch m.libMode {
	case libArtists:
		if di < len(m.artists) {
			name := m.artists[di]
			return m, func() tea.Msg {
				var albums []albumEntry
				_ = apiJSON("GET", "browse/albums?artist="+url.QueryEscape(name), "", &albums)
				if len(albums) == 0 {
					return fetchStatus()
				}
				ids := make([]string, len(albums))
				for i, a := range albums {
					ids[i] = a.ID
				}
				idsJSON, _ := json.Marshal(ids)
				apiCall("POST", "playlist/add/albums", fmt.Sprintf(`{"album_ids":%s,"mode":"%s"}`, idsJSON, mode))
				return fetchStatus()
			}
		}
	case libAlbums:
		if di < len(m.albums) {
			return m, doPost("playlist/add/album/"+m.albums[di].ID, body)
		}
	case libTracks:
		if di < len(m.tracks) {
			return m, doPost("playlist/add/track/"+m.tracks[di].ID, body)
		}
	}
	return m, nil
}

func (m model) handleQueueKey(key string) (tea.Model, tea.Cmd) {
	if m.confirmClear {
		m.confirmClear = false
		if key == "y" || key == "Y" {
			return m, doDelete("playback/queue")
		}
		return m, nil
	}
	qLen := len(m.queue)
	switch key {
	case "j", "down":
		if m.qCursor < qLen-1 {
			m.qCursor++
		}
	case "k", "up":
		if m.qCursor > 0 {
			m.qCursor--
		}
	case "g", "home":
		m.qCursor = 0
	case "G", "end":
		if qLen > 0 {
			m.qCursor = qLen - 1
		}
	case "pgdown":
		m.qCursor += 20
		if m.qCursor >= qLen {
			m.qCursor = qLen - 1
		}
		if m.qCursor < 0 {
			m.qCursor = 0
		}
	case "pgup":
		m.qCursor -= 20
		if m.qCursor < 0 {
			m.qCursor = 0
		}
	case "enter":
		if m.qCursor < qLen {
			m.qSelected = nil
			return m, doPost(fmt.Sprintf("playback/queue/play/%d", m.qCursor), "")
		}
	case "v": // toggle select
		if m.qCursor < qLen {
			if m.qSelected == nil {
				m.qSelected = map[int]bool{}
			}
			if m.qSelected[m.qCursor] {
				delete(m.qSelected, m.qCursor)
			} else {
				m.qSelected[m.qCursor] = true
			}
			if m.qCursor < qLen-1 {
				m.qCursor++
			}
		}
	case "V": // select range from last selected to cursor
		if m.qCursor < qLen {
			if m.qSelected == nil {
				m.qSelected = map[int]bool{}
			}
			// Find nearest selected item
			from := m.qCursor
			for i := m.qCursor - 1; i >= 0; i-- {
				if m.qSelected[i] {
					from = i
					break
				}
			}
			lo, hi := from, m.qCursor
			if lo > hi {
				lo, hi = hi, lo
			}
			for i := lo; i <= hi; i++ {
				m.qSelected[i] = true
			}
		}
	case "escape", "esc":
		m.qSelected = nil
	case "d", "delete", "x":
		if len(m.qSelected) > 0 {
			// Delete selected items (highest first to preserve indices)
			positions := make([]int, 0, len(m.qSelected))
			for pos := range m.qSelected {
				positions = append(positions, pos)
			}
			sort.Sort(sort.Reverse(sort.IntSlice(positions)))
			m.qSelected = nil
			return m, doDeleteMultiple(positions)
		}
		if m.qCursor < qLen {
			return m, doDelete(fmt.Sprintf("playback/queue/%d", m.qCursor))
		}
	case "J": // move down
		if len(m.qSelected) > 0 {
			positions := sortedSelected(m.qSelected)
			// Check if any selected item is at the bottom
			if positions[len(positions)-1] >= qLen-1 {
				return m, nil
			}
			// Move from bottom to top to preserve indices
			cmds := make([]movePair, 0, len(positions))
			for i := len(positions) - 1; i >= 0; i-- {
				cmds = append(cmds, movePair{positions[i], positions[i] + 1})
			}
			// Update selection
			newSel := map[int]bool{}
			for _, p := range positions {
				newSel[p+1] = true
			}
			m.qSelected = newSel
			m.qCursor++
			return m, doMoveMultiple(cmds)
		}
		if m.qCursor < qLen-1 {
			body := fmt.Sprintf(`{"from":%d,"to":%d}`, m.qCursor, m.qCursor+1)
			m.qCursor++
			return m, doPost("playback/queue/move", body)
		}
	case "K": // move up
		if len(m.qSelected) > 0 {
			positions := sortedSelected(m.qSelected)
			// Check if any selected item is at the top
			if positions[0] <= 0 {
				return m, nil
			}
			// Move from top to bottom to preserve indices
			cmds := make([]movePair, 0, len(positions))
			for _, p := range positions {
				cmds = append(cmds, movePair{p, p - 1})
			}
			// Update selection
			newSel := map[int]bool{}
			for _, p := range positions {
				newSel[p-1] = true
			}
			m.qSelected = newSel
			m.qCursor--
			return m, doMoveMultiple(cmds)
		}
		if m.qCursor > 0 {
			body := fmt.Sprintf(`{"from":%d,"to":%d}`, m.qCursor, m.qCursor-1)
			m.qCursor--
			return m, doPost("playback/queue/move", body)
		}
	case "c":
		m.confirmClear = true
		return m, nil
	}
	return m, nil
}

func (m model) handleSearchKey(msg tea.KeyMsg, key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.searching = false
		return m, nil
	case "up":
		if m.srCursor > 0 {
			m.srCursor--
		}
		return m, nil
	case "down":
		if m.srCursor < m.srTotal-1 {
			m.srCursor++
		}
		return m, nil
	case "enter":
		if m.srTotal > 0 {
			m.showMenu = true
			m.menuCursor = 0
			m.menuSource = "search"
		}
		return m, nil
	}

	// Forward to text input
	var cmd tea.Cmd
	prev := m.searchInput.Value()
	m.searchInput, cmd = m.searchInput.Update(msg)
	cur := m.searchInput.Value()
	if cur != prev && strings.TrimSpace(cur) != "" {
		return m, tea.Batch(cmd, doSearch(strings.TrimSpace(cur)))
	}
	return m, cmd
}

func (m model) searchAction(mode string) (tea.Model, tea.Cmd) {
	if m.srTotal == 0 {
		return m, nil
	}
	idx := m.srCursor
	nAlbums := len(m.searchRes.Albums)
	body := fmt.Sprintf(`{"mode":"%s"}`, mode)
	var cmd tea.Cmd
	if idx < nAlbums {
		a := m.searchRes.Albums[idx]
		cmd = doPost("playlist/add/album/"+a.ID, body)
	} else {
		t := m.searchRes.Tracks[idx-nAlbums]
		cmd = doPost("playlist/add/track/"+t.ID, body)
	}
	m.searching = false
	return m, cmd
}

func (m model) searchDrillIn() (tea.Model, tea.Cmd) {
	if m.srTotal == 0 {
		return m, nil
	}
	idx := m.srCursor
	nAlbums := len(m.searchRes.Albums)
	if idx < nAlbums {
		a := m.searchRes.Albums[idx]
		m.searching = false
		return m, fetchTracks(a.ID)
	}
	return m, nil
}

func (m model) handleDeviceKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q", "D":
		m.showDevices = false
		return m, nil
	case "j", "down":
		if m.devCursor < len(m.devices)-1 {
			m.devCursor++
		}
		return m, nil
	case "k", "up":
		if m.devCursor > 0 {
			m.devCursor--
		}
		return m, nil
	case "enter":
		if m.devCursor < len(m.devices) {
			dev := m.devices[m.devCursor]
			m.showDevices = false
			return m, setActiveDevice(dev.ID)
		}
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

var (
	accentColor  = lipgloss.Color("#3b82f6")
	dimColor     = lipgloss.Color("#6b7280")
	dangerColor  = lipgloss.Color("#ef4444")
	borderColor  = lipgloss.Color("#374151")
	selectedBg   = lipgloss.Color("#1e3a5f")
	playingBg    = lipgloss.Color("#1a2744")

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	dimStyle   = lipgloss.NewStyle().Foreground(dimColor)
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#9ca3af")).
			Background(lipgloss.Color("#1f2937")).
			Padding(0, 1)
	panelBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor)
	focusBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accentColor)
)

func (m model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	if m.showHelp {
		return m.helpView()
	}
	if m.showDevices {
		return m.deviceView()
	}
	if m.showMenu {
		return m.menuView()
	}
	if m.searching {
		return m.searchView()
	}
	if m.showRating {
		return m.ratingView()
	}

	playerH := 3
	mainH := m.height - playerH
	if mainH < 3 {
		mainH = 3
	}

	libW := m.width * 35 / 100
	if libW < 20 {
		libW = 20
	}
	queueW := m.width - libW
	if queueW < 20 {
		queueW = 20
	}

	lib := m.libraryView(libW-2, mainH-2)
	que := m.queueView(queueW-2, mainH-2)

	libBorder := panelBorder
	queBorder := panelBorder
	if m.focus == panelLibrary {
		libBorder = focusBorder
	} else {
		queBorder = focusBorder
	}

	libPanel := libBorder.Width(libW - 2).Height(mainH - 2).Render(lib)
	quePanel := queBorder.Width(queueW - 2).Height(mainH - 2).Render(que)

	main := lipgloss.JoinHorizontal(lipgloss.Top, libPanel, quePanel)
	player := m.playerView()

	return main + "\n" + player
}

func (m model) libraryView(w, h int) string {
	var title string
	var items []string

	switch m.libMode {
	case libArtists:
		title = fmt.Sprintf("Artists (%d)", len(m.artists))
		for i, a := range m.artists {
			items = append(items, m.libRow(i, a, "", w))
		}
	case libAlbums:
		title = fmt.Sprintf("Albums \u2014 %s", m.curArtist)
		items = append(items, m.backRow(w, "\u2190 Artists"))
		for i, a := range m.albums {
			label := a.Album
			if a.Date != "" && a.Date != "0000" {
				label = a.Date + " " + a.Album
			}
			items = append(items, m.libRow(i+1, label, "", w))
		}
	case libTracks:
		albumName := ""
		if len(m.tracks) > 0 {
			albumName = m.tracks[0].Album
		}
		title = fmt.Sprintf("Tracks \u2014 %s", albumName)
		items = append(items, m.backRow(w, "\u2190 Albums"))
		for i, t := range m.tracks {
			num := fmt.Sprintf("%2d", t.TrackNumber)
			items = append(items, m.libRow(i+1, t.Title, num, w))
		}
	}

	hdr := headerStyle.Width(w).Render(title)
	visH := h - 1
	if visH < 1 {
		visH = 1
	}

	// Scroll
	m.libOffset = scrollOffset(m.libCursor, m.libOffset, visH, len(items))

	end := m.libOffset + visH
	if end > len(items) {
		end = len(items)
	}
	visible := items[m.libOffset:end]

	body := strings.Join(visible, "\n")
	// Pad
	for len(visible) < visH {
		body += "\n"
		visible = append(visible, "")
	}

	return hdr + "\n" + body
}

func (m model) libRow(idx int, text, prefix string, w int) string {
	selected := m.focus == panelLibrary && idx == m.libCursor
	s := lipgloss.NewStyle().Width(w)
	if selected {
		s = s.Background(selectedBg).Foreground(lipgloss.Color("#ffffff")).Bold(true)
	}
	label := text
	if prefix != "" {
		label = dimStyle.Render(prefix) + " " + text
	}
	return s.Render(truncate(label, w))
}

func (m model) backRow(w int, label string) string {
	selected := m.focus == panelLibrary && m.libCursor == 0
	s := lipgloss.NewStyle().Width(w).Foreground(accentColor)
	if selected {
		s = s.Background(selectedBg).Bold(true)
	}
	return s.Render(label)
}

func (m model) queueView(w, h int) string {
	var title string
	if m.confirmClear {
		title = lipgloss.NewStyle().Bold(true).Foreground(dangerColor).Render("Clear queue? [y/N]")
	} else {
		title = fmt.Sprintf("Queue (%d)", len(m.queue))
	}
	hdr := headerStyle.Width(w).Render(title)
	visH := h - 1
	if visH < 1 {
		visH = 1
	}

	if len(m.queue) == 0 {
		return hdr + "\n" + dimStyle.Render("  Empty queue")
	}

	// Column widths: num(4) + artist(30%) + title(40%) + album(rest) + time(6)
	numW := 4
	timeW := 6
	innerW := w - numW - timeW - 4 // 4 for separators
	artistW := innerW * 30 / 100
	titleW := innerW * 40 / 100
	albumW := innerW - artistW - titleW
	if artistW < 5 { artistW = 5 }
	if titleW < 5 { titleW = 5 }
	if albumW < 5 { albumW = 5 }

	var items []string
	for i, q := range m.queue {
		num := fmt.Sprintf("%3d", q.Position+1)
		dur := fmtTime(q.Duration)
		dur = strings.Repeat(" ", timeW-len(dur)) + dur

		artist := truncate(q.Artist, artistW)
		title := truncate(q.Title, titleW)
		album := truncate(q.Album, albumW)

		// Pad columns
		artist = padRight(artist, artistW)
		title = padRight(title, titleW)
		album = padRight(album, albumW)

		isCursor := m.focus == panelQueue && i == m.qCursor
		isSelected := m.qSelected[i]
		s := lipgloss.NewStyle().Width(w)
		if q.Current {
			s = s.Background(playingBg)
		}
		if isSelected {
			s = s.Background(lipgloss.Color("#2d1f4e"))
		}
		if isCursor {
			s = s.Background(selectedBg).Foreground(lipgloss.Color("#ffffff")).Bold(true)
		}
		marker := " "
		if q.Current && isCursor {
			marker = "\u25b6"
		} else if q.Current {
			marker = lipgloss.NewStyle().Foreground(accentColor).Render("\u25b6")
		} else if isSelected && !isCursor {
			marker = lipgloss.NewStyle().Foreground(accentColor).Render("*")
		} else if isSelected {
			marker = "*"
		}
		var row string
		if isCursor {
			// Plain text — let outer style handle all coloring
			row = marker + num + " " + artist + " " + title + " " + album + " " + dur
		} else {
			row = marker + dimStyle.Render(num) + " " + artist + " " + title + " " + dimStyle.Render(album) + " " + dimStyle.Render(dur)
		}
		items = append(items, s.Render(row))
	}

	m.qOffset = scrollOffset(m.qCursor, m.qOffset, visH, len(items))

	end := m.qOffset + visH
	if end > len(items) {
		end = len(items)
	}
	visible := items[m.qOffset:end]
	body := strings.Join(visible, "\n")
	for len(visible) < visH {
		body += "\n"
		visible = append(visible, "")
	}

	return hdr + "\n" + body
}

func (m model) playerView() string {
	w := m.width
	if w < 10 {
		w = 10
	}

	// Now playing
	np := "\u2014"
	if m.status.Title != "" {
		np = m.status.Title
		if m.status.Artist != "" {
			np += " \u2014 " + m.status.Artist
		}
		if m.status.Album != "" {
			np += " \u2014 " + m.status.Album
		}
	}

	stateIcon := "\u25b6"
	if m.status.State == "playing" {
		stateIcon = "\u23f8"
	} else if m.status.State == "stopped" {
		stateIcon = "\u25a0"
	}

	// Seekbar
	posStr := fmtTime(m.status.TimePos)
	durStr := fmtTime(m.status.Dur)
	barW := w - len(posStr) - len(durStr) - 6
	if barW < 5 {
		barW = 5
	}
	filled := 0
	if m.status.Dur > 0 {
		filled = int(m.status.TimePos / m.status.Dur * float64(barW))
	}
	if filled > barW {
		filled = barW
	}
	if filled < 0 {
		filled = 0
	}

	bar := lipgloss.NewStyle().Foreground(accentColor).Render(strings.Repeat("\u2501", filled))
	bar += lipgloss.NewStyle().Foreground(accentColor).Render("\u25cf")
	bar += dimStyle.Render(strings.Repeat("\u2500", barW-filled))

	timeL := dimStyle.Render(posStr)
	timeR := dimStyle.Render(durStr)

	line1 := titleStyle.Render(stateIcon) + " " + truncate(np, w-4)
	line2 := timeL + " " + bar + " " + timeR

	// Hotkey hints
	hints := dimStyle.Render("[/]search [?]help [space]play [<>]prev/next [s]stop [r]album [R]tracks [T]rate [A]rate album [D]devices [q]quit")

	return line1 + "\n" + line2 + "\n" + hints
}

func (m model) helpView() string {
	title := titleStyle.Render("Hotkeys")
	sections := []struct{ header, body string }{
		{"Global", strings.Join([]string{
			"  /          Search",
			"  ?          This help screen",
			"  Space      Play / Pause",
			"  >          Next track",
			"  <          Previous track",
			"  s          Stop",
			"  r          Random album",
			"  R          Random tracks",
			"  u          Update library cache",
			"  T          Rate current track",
			"  A          Rate current album",
			"  D          Device picker",
			"  Tab        Switch panel focus",
			"  q          Quit",
		}, "\n")},
		{"Library", strings.Join([]string{
			"  j/k        Navigate up/down",
			"  Enter      Action menu (Add/Insert/Replace/Browse)",
			"  PgUp/PgDn  Jump 20 items",
			"  g/G        Go to first/last",
		}, "\n")},
		{"Queue", strings.Join([]string{
			"  j/k        Navigate up/down",
			"  Enter      Play selected track",
			"  d/x/Del    Delete track (or selection)",
			"  v          Toggle select",
			"  V          Select range",
			"  Esc        Clear selection",
			"  J/K        Move track down/up",
			"  c          Clear queue (confirm)",
			"  PgUp/PgDn  Jump 20 items",
			"  g/G        Go to first/last",
		}, "\n")},
		{"Seekbar", strings.Join([]string{
			"  Click      Seek to position",
		}, "\n")},
	}

	var lines []string
	lines = append(lines, title, "")
	for _, s := range sections {
		lines = append(lines, titleStyle.Render(s.header))
		lines = append(lines, s.body, "")
	}
	lines = append(lines, dimStyle.Render("Press any key to close"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accentColor).
		Padding(1, 2).
		Render(strings.Join(lines, "\n"))

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m model) ratingView() string {
	modeLabel := "Track"
	if m.ratingMode == "album" {
		modeLabel = "Album"
	}
	header := titleStyle.Render("Rate "+modeLabel) + "\n"
	header += dimStyle.Render(truncate(m.ratingLabel, m.width-10)) + "\n"
	cur := m.ratingCur
	if cur == "" {
		cur = "none"
	}
	header += dimStyle.Render("Current: "+cur) + "\n\n"

	var items []string
	for i, v := range ratingValues {
		prefix := "  "
		if i == m.ratingCursor {
			prefix = "\u25b8 "
			s := lipgloss.NewStyle().Background(selectedBg).Foreground(lipgloss.Color("#ffffff")).Bold(true)
			if v == cur {
				s = s.Foreground(accentColor)
			}
			items = append(items, s.Render(prefix+v))
		} else {
			label := prefix + v
			if v == cur {
				label = prefix + titleStyle.Render(v)
			}
			items = append(items, label)
		}
	}

	hints := "\n" + dimStyle.Render("[1-0]rate [del]remove [\u2191\u2193]select [enter]confirm [esc]cancel")
	content := header + strings.Join(items, "\n") + hints

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accentColor).
		Padding(1, 3).
		Render(content)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m model) deviceView() string {
	header := titleStyle.Render("Devices") + "\n\n"

	if len(m.devices) == 0 {
		header += dimStyle.Render("No devices found")
	}

	var items []string
	for i, d := range m.devices {
		status := dimStyle.Render("\u25cb") // offline circle
		if d.Online {
			status = lipgloss.NewStyle().Foreground(lipgloss.Color("#22c55e")).Render("\u25cf") // green dot
		}
		active := "  "
		if d.ID == m.activeDevice {
			active = lipgloss.NewStyle().Foreground(accentColor).Render("\u25b6 ")
		}

		name := d.Name
		if d.IsLocal {
			name += " (local)"
		}
		detail := ""
		if d.Format != "" {
			detail = d.Format
			if d.BitRate > 0 {
				detail += fmt.Sprintf(" %dkbps", d.BitRate)
			}
		}
		if detail != "" {
			name += "  " + dimStyle.Render(detail)
		}

		isCursor := i == m.devCursor
		s := lipgloss.NewStyle()
		if isCursor {
			s = s.Background(selectedBg).Foreground(lipgloss.Color("#ffffff")).Bold(true)
			line := " " + active + status + " " + d.Name
			if d.IsLocal {
				line += " (local)"
			}
			if detail != "" {
				line += "  " + detail
			}
			items = append(items, s.Render(line))
		} else {
			items = append(items, " "+active+status+" "+name)
		}
	}

	hints := "\n\n" + dimStyle.Render("[\u2191\u2193]navigate [enter]switch [esc]close")
	content := header + strings.Join(items, "\n") + hints

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accentColor).
		Padding(1, 3).
		Render(content)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m model) menuView() string {
	// Determine what we're acting on
	di := m.dataIndex()
	var label string
	switch m.libMode {
	case libArtists:
		if di >= 0 && di < len(m.artists) {
			label = m.artists[di]
		}
	case libAlbums:
		if di >= 0 && di < len(m.albums) {
			a := m.albums[di]
			label = a.Album
			if a.Date != "" && a.Date != "0000" {
				label = a.Date + " " + a.Album
			}
		}
	case libTracks:
		if di >= 0 && di < len(m.tracks) {
			label = m.tracks[di].Title
		}
	}

	header := titleStyle.Render("Action") + "  " + label + "\n\n"
	var items []string
	// Hide "Browse into" for tracks (leaf level)
	optCount := len(menuOptions)
	if m.libMode == libTracks {
		optCount = 3
	}
	for i := 0; i < optCount; i++ {
		prefix := "  "
		if i == m.menuCursor {
			prefix = "\u25b8 "
			items = append(items, lipgloss.NewStyle().Background(selectedBg).Foreground(lipgloss.Color("#ffffff")).Bold(true).Render(prefix+menuOptions[i]))
		} else {
			items = append(items, prefix+menuOptions[i])
		}
	}

	hints := "\n\n" + dimStyle.Render("[\u2191\u2193]navigate [enter]confirm [esc]cancel")
	content := header + strings.Join(items, "\n") + hints

	// Center it
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accentColor).
		Padding(1, 3).
		Render(content)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m model) searchView() string {
	w := m.width
	h := m.height

	// Input line at top
	prompt := titleStyle.Render("> ") + m.searchInput.View()
	hints := dimStyle.Render("[esc]close [\u2191\u2193]navigate [enter]action menu")

	// Available height for results (minus prompt line and hints line)
	resH := h - 2
	if resH < 1 {
		resH = 1
	}

	// Build flat result list with cursor visual index tracking
	var items []string
	cursorVisual := 0
	nAlbums := len(m.searchRes.Albums)
	if nAlbums > 0 {
		items = append(items, dimStyle.Render(fmt.Sprintf(" Albums (%d)", nAlbums)))
		for i, a := range m.searchRes.Albums {
			if i == m.srCursor {
				cursorVisual = len(items)
			}
			label := a.AlbumArtist + " \u2014 " + a.Album
			if a.Date != "" {
				label += " (" + a.Date + ")"
			}
			items = append(items, m.srRow(i, label, w))
		}
	}
	if len(m.searchRes.Tracks) > 0 {
		items = append(items, dimStyle.Render(fmt.Sprintf(" Tracks (%d)", len(m.searchRes.Tracks))))
		for i, t := range m.searchRes.Tracks {
			if nAlbums+i == m.srCursor {
				cursorVisual = len(items)
			}
			label := t.Title + " \u2014 " + t.Artist
			items = append(items, m.srRow(nAlbums+i, label, w))
		}
	}

	// Scroll to keep cursor visible
	m.srOffset = scrollOffset(cursorVisual, m.srOffset, resH, len(items))
	end := m.srOffset + resH
	if end > len(items) {
		end = len(items)
	}

	var body string
	if m.srTotal == 0 && strings.TrimSpace(m.searchInput.Value()) != "" {
		body = dimStyle.Render(" No results")
		for i := 1; i < resH; i++ {
			body += "\n"
		}
	} else if len(items) > 0 {
		visible := items[m.srOffset:end]
		body = strings.Join(visible, "\n")
		for i := len(visible); i < resH; i++ {
			body += "\n"
		}
	} else {
		for i := 0; i < resH; i++ {
			if i > 0 {
				body += "\n"
			}
		}
	}

	return prompt + "\n" + body + "\n" + hints
}

func (m model) srRow(idx int, text string, w int) string {
	s := lipgloss.NewStyle().Width(w)
	if idx == m.srCursor {
		s = s.Background(selectedBg).Foreground(lipgloss.Color("#ffffff")).Bold(true)
		return s.Render(" " + truncate(text, w-2))
	}
	return s.Render(" " + truncate(text, w-2))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func fmtTime(s float64) string {
	if s < 0 {
		s = 0
	}
	m := int(s) / 60
	sec := int(s) % 60
	return fmt.Sprintf("%d:%02d", m, sec)
}

func truncate(s string, max int) string {
	if max < 1 {
		return ""
	}
	if runewidth.StringWidth(s) <= max {
		return s
	}
	if max <= 1 {
		return "\u2026"
	}
	return runewidth.Truncate(s, max-1, "") + "\u2026"
}

func strWidth(s string) int {
	return runewidth.StringWidth(s)
}

func padRight(s string, w int) string {
	sw := runewidth.StringWidth(s)
	if sw >= w {
		return s
	}
	return s + strings.Repeat(" ", w-sw)
}

func scrollOffset(cursor, offset, visible, total int) int {
	if total <= visible {
		return 0
	}
	// Keep cursor centered
	o := cursor - visible/2
	if o < 0 {
		o = 0
	}
	if o > total-visible {
		o = total - visible
	}
	return o
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	cfg = loadTUIConfig()
	initAPI()
	ensureAgent()

	p := tea.NewProgram(newModel(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

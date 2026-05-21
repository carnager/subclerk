package com.subclerk.app

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

enum class LibView { Artists, Albums, Tracks }

class MainViewModel : ViewModel() {
    private val api get() = SubclerkApp.instance.api
    private val offline = SubclerkApp.instance.offlineManager

    // Playback status
    var status by mutableStateOf<PlaybackStatus?>(null); private set
    var queue by mutableStateOf<List<QueueItem>>(emptyList()); private set
    var currentTrackOffline by mutableStateOf(false); private set

    // Library
    var libView by mutableStateOf(LibView.Artists); private set
    var artists by mutableStateOf<List<String>>(emptyList()); private set
    var albums by mutableStateOf<List<Album>>(emptyList()); private set
    var tracks by mutableStateOf<List<Track>>(emptyList()); private set
    var curArtist by mutableStateOf(""); private set
    var curAlbum by mutableStateOf<Album?>(null); private set

    // Search
    var searchQuery by mutableStateOf("")
    var searchResult by mutableStateOf(SearchResult(emptyList(), emptyList())); private set
    private var searchJob: Job? = null

    // Devices
    var devices by mutableStateOf<List<DeviceInfo>>(emptyList()); private set

    // Action menu
    var showActionMenu by mutableStateOf(false)
    var actionTarget by mutableStateOf<ActionTarget?>(null); private set

    // Offline downloads
    var downloadProgress by mutableStateOf<OfflineManager.DownloadProgress?>(null); private set
    var downloadedAlbums by mutableStateOf<Set<String>>(emptySet()); private set

    private var pollJob: Job? = null
    private var downloadJob: Job? = null

    init {
        downloadedAlbums = offline.getDownloadedAlbumIds()
        startPolling()
        loadArtists()
    }

    private fun startPolling() {
        pollJob?.cancel()
        pollJob = viewModelScope.launch {
            while (true) {
                refresh()
                delay(800)
            }
        }
    }

    private suspend fun refresh() {
        status = api.getStatus()
        queue = api.getQueue()
        currentTrackOffline = PlaybackService.instance?.isCurrentTrackOffline ?: false
    }

    // --- Library ---

    fun loadArtists() {
        viewModelScope.launch {
            artists = api.getArtists()
            libView = LibView.Artists
        }
    }

    fun loadAlbums(artist: String) {
        viewModelScope.launch {
            curArtist = artist
            albums = api.getAlbums(artist)
            libView = LibView.Albums
        }
    }

    fun loadTracks(album: Album) {
        viewModelScope.launch {
            curAlbum = album
            tracks = api.getTracks(album.id)
            libView = LibView.Tracks
        }
    }

    fun libBack() {
        when (libView) {
            LibView.Tracks -> {
                libView = LibView.Albums
                tracks = emptyList()
            }
            LibView.Albums -> {
                libView = LibView.Artists
                albums = emptyList()
            }
            LibView.Artists -> {}
        }
    }

    // --- Action menu ---

    sealed class ActionTarget {
        data class ArtistTarget(val name: String) : ActionTarget()
        data class AlbumTarget(val album: Album) : ActionTarget()
        data class TrackTarget(val track: Track) : ActionTarget()
        data class SearchAlbumTarget(val album: Album) : ActionTarget()
        data class SearchTrackTarget(val track: Track) : ActionTarget()
    }

    fun showAction(target: ActionTarget) {
        actionTarget = target
        showActionMenu = true
    }

    fun dismissAction() {
        showActionMenu = false
        actionTarget = null
    }

    fun executeAction(mode: String) {
        val t = actionTarget ?: return
        viewModelScope.launch {
            when (t) {
                is ActionTarget.ArtistTarget -> {
                    val artistAlbums = api.getAlbums(t.name)
                    if (artistAlbums.isNotEmpty()) {
                        api.addAlbums(artistAlbums.map { it.id }, mode)
                    }
                }
                is ActionTarget.AlbumTarget -> api.addAlbum(t.album.id, mode)
                is ActionTarget.TrackTarget -> api.addTrack(t.track.id, mode)
                is ActionTarget.SearchAlbumTarget -> api.addAlbum(t.album.id, mode)
                is ActionTarget.SearchTrackTarget -> api.addTrack(t.track.id, mode)
            }
        }
        dismissAction()
    }

    fun browseIntoAction() {
        val t = actionTarget ?: return
        when (t) {
            is ActionTarget.ArtistTarget -> loadAlbums(t.name)
            is ActionTarget.AlbumTarget -> loadTracks(t.album)
            is ActionTarget.SearchAlbumTarget -> loadTracks(t.album)
            else -> {}
        }
        dismissAction()
    }

    // --- Search ---

    fun updateSearch(query: String) {
        searchQuery = query
        searchJob?.cancel()
        if (query.isBlank()) {
            searchResult = SearchResult(emptyList(), emptyList())
            return
        }
        searchJob = viewModelScope.launch {
            delay(200)
            searchResult = api.search(query)
        }
    }

    // --- Playback ---

    fun togglePlay() {
        viewModelScope.launch {
            if (status?.state == "playing") api.pause() else api.play()
        }
    }

    fun playNext() { viewModelScope.launch { api.next() } }
    fun playPrev() { viewModelScope.launch { api.prev() } }
    fun stopPlayback() { viewModelScope.launch { api.stop() } }
    fun seek(pos: Double) { viewModelScope.launch { api.seek(pos) } }
    fun randomAlbum() { viewModelScope.launch { api.randomAlbum() } }
    fun randomTracks() { viewModelScope.launch { api.randomTracks() } }

    // --- Queue ---

    fun queuePlay(position: Int) { viewModelScope.launch { api.queuePlay(position) } }
    fun queueRemove(position: Int) { viewModelScope.launch { api.queueRemove(position) } }
    fun queueMove(from: Int, to: Int) { viewModelScope.launch { api.queueMove(from, to) } }
    fun queueClear() { viewModelScope.launch { api.queueClear() } }

    // --- Offline downloads ---

    fun downloadAlbum(album: Album) {
        if (downloadJob?.isActive == true) return
        downloadJob = viewModelScope.launch {
            val albumTracks = api.getTracks(album.id)
            if (albumTracks.isEmpty()) return@launch
            val deviceId = PlaybackService.instance?.deviceId ?: ""
            offline.downloadAlbum(album.id, albumTracks, api, deviceId) { progress ->
                downloadProgress = progress
            }
            downloadProgress = null
            downloadedAlbums = offline.getDownloadedAlbumIds()
        }
    }

    fun removeOfflineAlbum(albumId: String) {
        offline.removeAlbum(albumId)
        downloadedAlbums = offline.getDownloadedAlbumIds()
    }

    fun isAlbumDownloaded(albumId: String): Boolean = offline.isAlbumDownloaded(albumId)

    // --- Devices ---

    fun loadDevices() {
        viewModelScope.launch {
            devices = api.getDevices()
        }
    }

    fun setActiveDevice(id: String) {
        viewModelScope.launch {
            api.setActiveDevice(id)
            delay(300)
            devices = api.getDevices()
        }
    }
}

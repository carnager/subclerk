package com.subclerk.app

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.TimeUnit

data class Album(val id: String, val albumArtist: String, val album: String, val date: String)
data class Track(val id: String, val songId: String, val title: String, val artist: String, val album: String, val trackNumber: Int)
data class QueueItem(val position: Int, val songId: String, val title: String, val artist: String, val album: String, val albumId: String, val duration: Double, val current: Boolean)
data class PlaybackStatus(val state: String, val title: String, val artist: String, val album: String, val date: String, val albumId: String, val timePos: Double, val duration: Double)
data class DeviceInfo(val id: String, val name: String, val isLocal: Boolean, val type: String, val online: Boolean, val format: String, val maxBitrate: Int, val active: Boolean)
data class SearchResult(val albums: List<Album>, val tracks: List<Track>)
data class PlaylistInfo(val id: String, val name: String, val songCount: Int, val duration: Int, val coverArt: String)

class SubclerkApi(private val server: String) {
    private val client = OkHttpClient.Builder()
        .connectTimeout(5, TimeUnit.SECONDS)
        .readTimeout(10, TimeUnit.SECONDS)
        .build()

    private val json = "application/json".toMediaType()

    var deviceSecret: String = ""

    val baseUrl: String
        get() = if (server.isBlank()) "" else {
            val scheme = if (server.startsWith("https://") || server.startsWith("http://")) "" else "http://"
            val base = if (scheme.isNotEmpty()) "$scheme$server" else server
            "$base/api/v1"
        }

    val isConfigured: Boolean
        get() = server.isNotBlank()

    private fun Request.Builder.withAuth(): Request.Builder {
        if (deviceSecret.isNotBlank()) header("Authorization", "Bearer $deviceSecret")
        return this
    }

    private suspend fun get(path: String): String? = withContext(Dispatchers.IO) {
        if (!isConfigured) return@withContext null
        try {
            val req = Request.Builder().url("$baseUrl/$path").withAuth().build()
            client.newCall(req).execute().use { it.body?.string() }
        } catch (e: Exception) {
            null
        }
    }

    private suspend fun post(path: String, body: String = ""): String? = withContext(Dispatchers.IO) {
        if (!isConfigured) return@withContext null
        try {
            val reqBody = body.toRequestBody(json)
            val req = Request.Builder().url("$baseUrl/$path").withAuth().post(reqBody).build()
            client.newCall(req).execute().use { it.body?.string() }
        } catch (e: Exception) {
            null
        }
    }

    private suspend fun delete(path: String): String? = withContext(Dispatchers.IO) {
        if (!isConfigured) return@withContext null
        try {
            val req = Request.Builder().url("$baseUrl/$path").withAuth().delete().build()
            client.newCall(req).execute().use { it.body?.string() }
        } catch (e: Exception) {
            null
        }
    }

    // --- Browse ---

    suspend fun getArtists(): List<String> {
        val data = get("browse/artists") ?: return emptyList()
        return try {
            val arr = JSONArray(data)
            (0 until arr.length()).map { arr.getString(it) }
        } catch (e: Exception) { emptyList() }
    }

    suspend fun getAlbums(artist: String): List<Album> {
        val data = get("browse/albums?artist=${java.net.URLEncoder.encode(artist, "UTF-8")}") ?: return emptyList()
        return parseAlbums(data)
    }

    suspend fun getLatestAlbums(): List<Album> {
        val data = get("albums?sort=latest") ?: return emptyList()
        return parseAlbums(data)
    }

    suspend fun getTracks(albumId: String): List<Track> {
        val data = get("browse/tracks?album_id=$albumId") ?: return emptyList()
        return try {
            val arr = JSONArray(data)
            (0 until arr.length()).map { i ->
                val o = arr.getJSONObject(i)
                Track(
                    id = o.optString("id"),
                    songId = o.optString("song_id", o.optString("id")),
                    title = o.optString("title"),
                    artist = o.optString("artist"),
                    album = o.optString("album"),
                    trackNumber = o.optInt("tracknumber", 0)
                )
            }
        } catch (e: Exception) { emptyList() }
    }

    // --- Search ---

    suspend fun search(query: String): SearchResult {
        val data = get("search?q=${java.net.URLEncoder.encode(query, "UTF-8")}") ?: return SearchResult(emptyList(), emptyList())
        return try {
            val obj = JSONObject(data)
            val albums = parseAlbums(obj.optJSONArray("albums")?.toString() ?: "[]")
            val tracks = try {
                val arr = obj.optJSONArray("tracks") ?: JSONArray()
                (0 until arr.length()).map { i ->
                    val o = arr.getJSONObject(i)
                    Track(o.optString("id"), o.optString("song_id", o.optString("id")), o.optString("title"), o.optString("artist"), o.optString("album"), o.optInt("tracknumber", 0))
                }
            } catch (e: Exception) { emptyList() }
            SearchResult(albums, tracks)
        } catch (e: Exception) { SearchResult(emptyList(), emptyList()) }
    }

    // --- Playback ---

    suspend fun getStatus(): PlaybackStatus? {
        val data = get("playback/status") ?: return null
        return try {
            val o = JSONObject(data)
            PlaybackStatus(
                state = o.optString("state", "stopped"),
                title = o.optString("title"),
                artist = o.optString("artist"),
                album = o.optString("album"),
                date = o.optString("date"),
                albumId = o.optString("album_id"),
                timePos = o.optDouble("time_pos", 0.0),
                duration = o.optDouble("duration", 0.0)
            )
        } catch (e: Exception) { null }
    }

    suspend fun play() { post("playback/play") }
    suspend fun pause() { post("playback/pause") }
    suspend fun stop() { post("playback/stop") }
    suspend fun next() { post("playback/next") }
    suspend fun prev() { post("playback/prev") }
    suspend fun seek(position: Double) { post("playback/seek", """{"position":$position}""") }
    suspend fun randomAlbum() { post("playback/random/album") }
    suspend fun randomTracks() { post("playback/random/tracks") }

    // --- Queue ---

    suspend fun getQueue(): List<QueueItem> {
        val data = get("playback/queue") ?: return emptyList()
        return try {
            val arr = JSONArray(data)
            (0 until arr.length()).map { i ->
                val o = arr.getJSONObject(i)
                QueueItem(
                    position = o.optInt("position"),
                    songId = o.optString("song_id"),
                    title = o.optString("title"),
                    artist = o.optString("artist"),
                    album = o.optString("album"),
                    albumId = o.optString("album_id"),
                    duration = o.optDouble("duration", 0.0),
                    current = o.optBoolean("current", false)
                )
            }
        } catch (e: Exception) { emptyList() }
    }

    suspend fun queuePlay(position: Int) { post("playback/queue/play/$position") }
    suspend fun queueRemove(position: Int) { delete("playback/queue/$position") }
    suspend fun queueMove(from: Int, to: Int) { post("playback/queue/move", """{"from":$from,"to":$to}""") }
    suspend fun queueClear() { delete("playback/queue") }

    // --- Playlist add ---

    suspend fun addAlbum(albumId: String, mode: String) {
        post("playlist/add/album/$albumId", """{"mode":"$mode"}""")
    }
    suspend fun addTrack(trackId: String, mode: String) { post("playlist/add/track/$trackId", """{"mode":"$mode"}""") }
    suspend fun addAlbums(albumIds: List<String>, mode: String) {
        val ids = albumIds.joinToString(",") { "\"$it\"" }
        post("playlist/add/albums", """{"album_ids":[$ids],"mode":"$mode"}""")
    }

    // --- Devices ---

    suspend fun getDevices(): List<DeviceInfo> {
        val data = get("devices") ?: return emptyList()
        return try {
            val arr = JSONArray(data)
            (0 until arr.length()).map { i ->
                val o = arr.getJSONObject(i)
                DeviceInfo(
                    id = o.optString("id"),
                    name = o.optString("name"),
                    isLocal = o.optBoolean("is_local"),
                    type = o.optString("type"),
                    online = o.optBoolean("online"),
                    format = o.optString("format"),
                    maxBitrate = o.optInt("max_bitrate", 0),
                    active = o.optBoolean("active")
                )
            }
        } catch (e: Exception) { emptyList() }
    }

    suspend fun setActiveDevice(deviceId: String) { post("devices/active", """{"device_id":"$deviceId"}""") }

    suspend fun registerDevice(name: String, format: String, maxBitrate: Int, navidromeUrl: String = "", replaygain: String = "off"): String? {
        val obj = JSONObject().apply {
            put("name", name)
            put("type", "browser")
            put("format", format)
            put("max_bitrate", maxBitrate)
            if (navidromeUrl.isNotBlank()) put("navidrome_url", navidromeUrl)
            if (replaygain.isNotBlank() && replaygain != "off") put("replaygain", replaygain)
        }
        val data = post("devices/register", obj.toString())
        return try { JSONObject(data ?: "{}").optString("id") } catch (e: Exception) { null }
    }

    suspend fun heartbeat(id: String): Boolean = withContext(Dispatchers.IO) {
        if (!isConfigured) return@withContext false
        try {
            val reqBody = """{"id":"$id"}""".toRequestBody(json)
            val req = Request.Builder().url("$baseUrl/devices/heartbeat").withAuth().post(reqBody).build()
            client.newCall(req).execute().use { it.isSuccessful }
        } catch (e: Exception) {
            false
        }
    }

    suspend fun reportStatus(id: String, pause: Boolean, timePos: Double, duration: Double, playlistPos: Int) {
        post("devices/status", """{"id":"$id","pause":$pause,"time_pos":$timePos,"duration":$duration,"playlist_pos":$playlistPos}""")
    }

    fun sseUrl(id: String): String {
        val base = "$baseUrl/devices/stream?id=${java.net.URLEncoder.encode(id, "UTF-8")}"
        return if (deviceSecret.isNotBlank()) "$base&secret=${java.net.URLEncoder.encode(deviceSecret, "UTF-8")}" else base
    }

    // --- Rating ---

    suspend fun getTrackRating(): JSONObject? {
        val data = get("current_track/rating") ?: return null
        return try { JSONObject(data) } catch (e: Exception) { null }
    }

    suspend fun getAlbumRating(): JSONObject? {
        val data = get("current_album/rating") ?: return null
        return try { JSONObject(data) } catch (e: Exception) { null }
    }

    suspend fun rateTrack(value: String) { post("current_track/rating", """{"rating":"$value"}""") }
    suspend fun rateAlbum(value: String) { post("current_album/rating", """{"rating":"$value"}""") }

    // --- Stream URL ---

    suspend fun getStreamUrl(songId: String, deviceId: String = ""): String? {
        val params = "song_id=${java.net.URLEncoder.encode(songId, "UTF-8")}" +
            if (deviceId.isNotBlank()) "&device_id=${java.net.URLEncoder.encode(deviceId, "UTF-8")}" else ""
        val data = get("stream/url?$params") ?: return null
        return try { JSONObject(data).optString("url") } catch (e: Exception) { null }
    }

    // --- Playlists ---

    suspend fun getPlaylists(): List<PlaylistInfo> {
        val data = get("playlists") ?: return emptyList()
        return try {
            val arr = JSONArray(data)
            (0 until arr.length()).map { i ->
                val o = arr.getJSONObject(i)
                PlaylistInfo(
                    id = o.optString("id"),
                    name = o.optString("name"),
                    songCount = o.optInt("song_count"),
                    duration = o.optInt("duration"),
                    coverArt = o.optString("cover_art")
                )
            }
        } catch (e: Exception) { emptyList() }
    }

    suspend fun getPlaylistTracks(id: String): List<Track> {
        val data = get("playlists/tracks?id=${java.net.URLEncoder.encode(id, "UTF-8")}") ?: return emptyList()
        return try {
            val arr = JSONArray(data)
            (0 until arr.length()).map { i ->
                val o = arr.getJSONObject(i)
                Track(o.optString("id"), o.optString("song_id", o.optString("id")), o.optString("title"), o.optString("artist"), o.optString("album"), o.optInt("tracknumber", 0))
            }
        } catch (e: Exception) { emptyList() }
    }

    suspend fun addPlaylist(id: String, mode: String) { post("playlists/add/$id", """{"mode":"$mode"}""") }
    suspend fun addTrackToPlaylist(playlistId: String, songId: String) { post("playlists/add-track/$playlistId", """{"song_id":"$songId"}""") }

    // --- Cover Art ---

    fun coverUrl(albumId: String, size: Int = 300): String? {
        if (!isConfigured || albumId.isBlank()) return null
        return "$baseUrl/cover/${java.net.URLEncoder.encode(albumId, "UTF-8")}?size=$size"
    }

    // --- Helpers ---

    private fun parseAlbums(data: String): List<Album> {
        return try {
            val arr = JSONArray(data)
            (0 until arr.length()).map { i ->
                val o = arr.getJSONObject(i)
                Album(
                    id = o.optString("id"),
                    albumArtist = o.optString("albumartist"),
                    album = o.optString("album"),
                    date = o.optString("date")
                )
            }
        } catch (e: Exception) { emptyList() }
    }
}

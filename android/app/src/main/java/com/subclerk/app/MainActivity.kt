package com.subclerk.app

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInHorizontally
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.slideOutHorizontally
import androidx.compose.animation.slideOutVertically
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.PlaylistAdd
import androidx.compose.material.icons.automirrored.filled.PlaylistPlay
import androidx.compose.material.icons.automirrored.filled.QueueMusic
import androidx.compose.material.icons.automirrored.filled.VolumeUp
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Clear
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.DeleteSweep
import androidx.compose.material.icons.filled.Devices
import androidx.compose.material.icons.filled.FolderOpen
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.LibraryMusic
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.MusicNote
import androidx.compose.material.icons.filled.Pause
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Search
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Shuffle
import androidx.compose.material.icons.filled.SkipNext
import androidx.compose.material.icons.filled.SkipPrevious
import androidx.compose.material3.Card
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledIconButton
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.ListItem
import androidx.compose.material3.ListItemDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Slider
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.viewmodel.compose.viewModel

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        // Request notification permission on Android 13+
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            if (checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
                requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
            }
        }
        val serviceIntent = Intent(this, PlaybackService::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(serviceIntent)
        } else {
            startService(serviceIntent)
        }
        setContent {
            SubclerkTheme {
                var configured by remember { mutableStateOf(SubclerkApp.instance.api.isConfigured) }
                if (!configured) {
                    SetupScreen(onConnected = { configured = true })
                } else {
                    val vm: MainViewModel = viewModel()
                    MainScreen(vm)
                }
            }
        }
    }
}

// ==================== Theme ====================

@Composable
fun SubclerkTheme(content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = darkColorScheme(
            primary = Color(0xFF60A5FA),
            onPrimary = Color(0xFF0F172A),
            primaryContainer = Color(0xFF1E40AF),
            onPrimaryContainer = Color(0xFFDBEAFE),
            secondary = Color(0xFF94A3B8),
            onSecondary = Color(0xFF0F172A),
            surface = Color(0xFF1E293B),
            onSurface = Color(0xFFF1F5F9),
            surfaceVariant = Color(0xFF334155),
            onSurfaceVariant = Color(0xFF94A3B8),
            surfaceContainerLow = Color(0xFF1E293B),
            surfaceContainer = Color(0xFF1E293B),
            surfaceContainerHigh = Color(0xFF334155),
            background = Color(0xFF0F172A),
            onBackground = Color(0xFFF1F5F9),
            outline = Color(0xFF475569),
        ),
        content = content
    )
}

// ==================== Setup ====================

@Composable
fun SetupScreen(onConnected: () -> Unit) {
    var server by remember { mutableStateOf("") }
    Surface(
        modifier = Modifier.fillMaxSize(),
        color = MaterialTheme.colorScheme.background
    ) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .statusBarsPadding()
                .navigationBarsPadding()
                .padding(32.dp),
            verticalArrangement = Arrangement.Center,
            horizontalAlignment = Alignment.CenterHorizontally
        ) {
            Icon(
                Icons.Default.MusicNote,
                contentDescription = null,
                modifier = Modifier.size(64.dp),
                tint = MaterialTheme.colorScheme.primary
            )
            Spacer(Modifier.height(16.dp))
            Text(
                "Subclerk",
                style = MaterialTheme.typography.headlineLarge,
                fontWeight = FontWeight.Bold
            )
            Spacer(Modifier.height(8.dp))
            Text(
                "Connect to your server",
                style = MaterialTheme.typography.bodyLarge,
                color = MaterialTheme.colorScheme.onSurfaceVariant
            )
            Spacer(Modifier.height(32.dp))
            OutlinedTextField(
                value = server,
                onValueChange = { server = it },
                label = { Text("Server address") },
                placeholder = { Text("192.168.1.10:6701") },
                singleLine = true,
                modifier = Modifier.fillMaxWidth(),
                colors = OutlinedTextFieldDefaults.colors()
            )
            Spacer(Modifier.height(20.dp))
            FilledIconButton(
                onClick = {
                    if (server.isNotBlank()) {
                        SubclerkApp.instance.updateServer(server.trim())
                        onConnected()
                    }
                },
                modifier = Modifier
                    .fillMaxWidth()
                    .height(48.dp),
                shape = RoundedCornerShape(12.dp)
            ) {
                Text("Connect", style = MaterialTheme.typography.labelLarge)
            }
        }
    }
}

// ==================== Main ====================

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun MainScreen(vm: MainViewModel) {
    var selectedTab by remember { mutableIntStateOf(0) }
    var showNowPlaying by remember { mutableStateOf(false) }
    var showSettings by remember { mutableStateOf(false) }

    Box(Modifier.fillMaxSize()) {
        Scaffold(
            bottomBar = {
                Column {
                    MiniPlayerBar(vm) { showNowPlaying = true }
                    NavigationBar(
                        containerColor = MaterialTheme.colorScheme.surface,
                        tonalElevation = 0.dp
                    ) {
                        NavigationBarItem(
                            selected = selectedTab == 0,
                            onClick = { selectedTab = 0 },
                            icon = { Icon(Icons.Default.LibraryMusic, contentDescription = null) },
                            label = { Text("Library") }
                        )
                        NavigationBarItem(
                            selected = selectedTab == 1,
                            onClick = { selectedTab = 1 },
                            icon = { Icon(Icons.Default.Search, contentDescription = null) },
                            label = { Text("Search") }
                        )
                        NavigationBarItem(
                            selected = selectedTab == 2,
                            onClick = { selectedTab = 2 },
                            icon = { Icon(Icons.AutoMirrored.Filled.QueueMusic, contentDescription = null) },
                            label = { Text("Queue") }
                        )
                        NavigationBarItem(
                            selected = selectedTab == 3,
                            onClick = { selectedTab = 3; vm.loadDevices() },
                            icon = { Icon(Icons.Default.Devices, contentDescription = null) },
                            label = { Text("Devices") }
                        )
                    }
                }
            }
        ) { padding ->
            Box(Modifier.padding(padding)) {
                when (selectedTab) {
                    0 -> LibraryScreen(vm, onSettingsClick = { showSettings = true })
                    1 -> SearchScreen(vm)
                    2 -> QueueScreen(vm)
                    3 -> DevicesScreen(vm)
                }
            }
        }

        // Action menu
        if (vm.showActionMenu) {
            ActionSheet(vm)
        }

        // Now Playing overlay
        AnimatedVisibility(
            visible = showNowPlaying,
            enter = slideInVertically { it },
            exit = slideOutVertically { it }
        ) {
            NowPlayingScreen(vm) { showNowPlaying = false }
        }

        // Settings overlay
        AnimatedVisibility(
            visible = showSettings,
            enter = slideInVertically { it },
            exit = slideOutVertically { it }
        ) {
            SettingsScreen(onDismiss = { showSettings = false })
        }
    }
}

// ==================== Mini Player ====================

@Composable
fun MiniPlayerBar(vm: MainViewModel, onClick: () -> Unit) {
    val st = vm.status ?: return
    if (st.title.isBlank() && st.artist.isBlank()) return

    val dur = st.duration
    val pos = st.timePos
    val progress = if (dur > 0) (pos / dur).toFloat().coerceIn(0f, 1f) else 0f

    Surface(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onClick),
        color = MaterialTheme.colorScheme.surfaceContainerHigh,
        tonalElevation = 0.dp
    ) {
        Column {
            LinearProgressIndicator(
                progress = { progress },
                modifier = Modifier
                    .fillMaxWidth()
                    .height(2.dp),
                color = MaterialTheme.colorScheme.primary,
                trackColor = Color.Transparent
            )
            Row(
                modifier = Modifier.padding(start = 12.dp, end = 4.dp, top = 8.dp, bottom = 8.dp),
                verticalAlignment = Alignment.CenterVertically
            ) {
                // Small art placeholder
                Box(
                    modifier = Modifier
                        .size(40.dp)
                        .clip(RoundedCornerShape(8.dp))
                        .background(
                            Brush.linearGradient(
                                listOf(
                                    MaterialTheme.colorScheme.primary.copy(alpha = 0.3f),
                                    MaterialTheme.colorScheme.surfaceVariant
                                )
                            )
                        ),
                    contentAlignment = Alignment.Center
                ) {
                    Icon(
                        Icons.Default.MusicNote,
                        contentDescription = null,
                        modifier = Modifier.size(20.dp),
                        tint = MaterialTheme.colorScheme.primary.copy(alpha = 0.7f)
                    )
                }
                Spacer(Modifier.width(12.dp))
                Column(Modifier.weight(1f)) {
                    Text(
                        st.title.ifBlank { "\u2014" },
                        style = MaterialTheme.typography.bodyMedium,
                        fontWeight = FontWeight.Medium,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis
                    )
                    if (st.artist.isNotBlank()) {
                        Text(
                            st.artist,
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis
                        )
                    }
                }
                IconButton(onClick = { vm.playPrev() }) {
                    Icon(Icons.Default.SkipPrevious, "Previous", modifier = Modifier.size(22.dp))
                }
                IconButton(onClick = { vm.togglePlay() }) {
                    Icon(
                        if (st.state == "playing") Icons.Default.Pause else Icons.Default.PlayArrow,
                        "Play/Pause",
                        modifier = Modifier.size(28.dp)
                    )
                }
                IconButton(onClick = { vm.playNext() }) {
                    Icon(Icons.Default.SkipNext, "Next", modifier = Modifier.size(22.dp))
                }
            }
        }
    }
}

// ==================== Now Playing ====================

@Composable
fun NowPlayingScreen(vm: MainViewModel, onDismiss: () -> Unit) {
    val st = vm.status

    BackHandler { onDismiss() }

    Surface(
        modifier = Modifier.fillMaxSize(),
        color = MaterialTheme.colorScheme.background
    ) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .statusBarsPadding()
                .navigationBarsPadding()
                .padding(horizontal = 28.dp),
            horizontalAlignment = Alignment.CenterHorizontally
        ) {
            // Top bar
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(top = 8.dp, bottom = 4.dp),
                verticalAlignment = Alignment.CenterVertically
            ) {
                IconButton(onClick = onDismiss) {
                    Icon(
                        Icons.Default.KeyboardArrowDown,
                        "Close",
                        modifier = Modifier.size(32.dp)
                    )
                }
                Spacer(Modifier.weight(1f))
                Text(
                    "Now Playing",
                    style = MaterialTheme.typography.titleSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
                Spacer(Modifier.weight(1f))
                Spacer(Modifier.size(48.dp))
            }

            Spacer(Modifier.weight(0.5f))

            // Album art placeholder
            Box(
                modifier = Modifier
                    .fillMaxWidth(0.8f)
                    .aspectRatio(1f)
                    .clip(RoundedCornerShape(20.dp))
                    .background(
                        Brush.linearGradient(
                            colors = listOf(
                                MaterialTheme.colorScheme.primaryContainer,
                                MaterialTheme.colorScheme.surfaceVariant,
                            )
                        )
                    ),
                contentAlignment = Alignment.Center
            ) {
                Icon(
                    Icons.Default.MusicNote,
                    contentDescription = null,
                    modifier = Modifier.size(72.dp),
                    tint = MaterialTheme.colorScheme.primary.copy(alpha = 0.4f)
                )
            }

            Spacer(Modifier.height(36.dp))

            // Track info
            Text(
                st?.title?.ifBlank { "\u2014" } ?: "Not Playing",
                style = MaterialTheme.typography.headlineSmall,
                fontWeight = FontWeight.Bold,
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
                textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth()
            )
            Spacer(Modifier.height(6.dp))
            val sub = listOfNotNull(
                st?.artist?.ifBlank { null },
                st?.album?.ifBlank { null }
            ).joinToString(" \u2014 ")
            Text(
                sub.ifBlank { " " },
                style = MaterialTheme.typography.bodyLarge,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth()
            )

            Spacer(Modifier.height(32.dp))

            // Seek bar
            val dur = st?.duration ?: 0.0
            val pos = st?.timePos ?: 0.0
            var dragging by remember { mutableStateOf(false) }
            var dragValue by remember { mutableFloatStateOf(0f) }
            val displayFraction = if (dragging) dragValue else if (dur > 0) (pos / dur).toFloat().coerceIn(0f, 1f) else 0f

            Slider(
                value = displayFraction,
                onValueChange = { dragging = true; dragValue = it },
                onValueChangeFinished = {
                    vm.seek(dragValue.toDouble() * dur)
                    dragging = false
                },
                modifier = Modifier.fillMaxWidth(),
                enabled = dur > 0
            )
            Row(
                Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 4.dp)
            ) {
                Text(
                    fmtTime(if (dragging) dragValue.toDouble() * dur else pos),
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
                Spacer(Modifier.weight(1f))
                Text(
                    fmtTime(dur),
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
            }

            Spacer(Modifier.height(20.dp))

            // Transport controls
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.Center,
                verticalAlignment = Alignment.CenterVertically
            ) {
                IconButton(
                    onClick = { vm.playPrev() },
                    modifier = Modifier.size(64.dp)
                ) {
                    Icon(
                        Icons.Default.SkipPrevious,
                        "Previous",
                        modifier = Modifier.size(36.dp)
                    )
                }
                Spacer(Modifier.width(20.dp))
                FilledIconButton(
                    onClick = { vm.togglePlay() },
                    modifier = Modifier.size(72.dp),
                    shape = CircleShape
                ) {
                    Icon(
                        if (st?.state == "playing") Icons.Default.Pause else Icons.Default.PlayArrow,
                        "Play/Pause",
                        modifier = Modifier.size(40.dp)
                    )
                }
                Spacer(Modifier.width(20.dp))
                IconButton(
                    onClick = { vm.playNext() },
                    modifier = Modifier.size(64.dp)
                ) {
                    Icon(
                        Icons.Default.SkipNext,
                        "Next",
                        modifier = Modifier.size(36.dp)
                    )
                }
            }

            Spacer(Modifier.weight(1f))
        }
    }
}

// ==================== Library ====================

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun LibraryScreen(vm: MainViewModel, onSettingsClick: () -> Unit) {
    BackHandler(enabled = vm.libView != LibView.Artists) {
        vm.libBack()
    }

    Column(Modifier.fillMaxSize()) {
        TopAppBar(
            title = {
                Text(
                    when (vm.libView) {
                        LibView.Artists -> "Library"
                        LibView.Albums -> vm.curArtist
                        LibView.Tracks -> vm.curAlbum?.album ?: "Tracks"
                    }
                )
            },
            navigationIcon = {
                if (vm.libView != LibView.Artists) {
                    IconButton(onClick = { vm.libBack() }) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, "Back")
                    }
                }
            },
            actions = {
                if (vm.libView == LibView.Artists) {
                    IconButton(onClick = { vm.randomAlbum() }) {
                        Icon(Icons.Default.Shuffle, "Random album")
                    }
                    IconButton(onClick = onSettingsClick) {
                        Icon(Icons.Default.Settings, "Settings")
                    }
                }
            }
        )

        AnimatedContent(
            targetState = vm.libView,
            transitionSpec = {
                val forward = targetState.ordinal > initialState.ordinal
                (slideInHorizontally { if (forward) it else -it } + fadeIn()) togetherWith
                        (slideOutHorizontally { if (forward) -it else it } + fadeOut())
            },
            label = "library"
        ) { view ->
            when (view) {
                LibView.Artists -> ArtistList(vm)
                LibView.Albums -> AlbumList(vm)
                LibView.Tracks -> TrackList(vm)
            }
        }
    }
}

@Composable
fun ArtistList(vm: MainViewModel) {
    if (vm.artists.isEmpty()) {
        Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
            Text("Loading...", color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        return
    }
    LazyColumn(Modifier.fillMaxSize()) {
        item {
            Text(
                "${vm.artists.size} artists",
                style = MaterialTheme.typography.labelMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp)
            )
        }
        itemsIndexed(vm.artists) { _, artist ->
            ListItem(
                headlineContent = {
                    Text(artist, maxLines = 1, overflow = TextOverflow.Ellipsis)
                },
                modifier = Modifier.clickable { vm.loadAlbums(artist) },
                trailingContent = {
                    IconButton(onClick = { vm.showAction(MainViewModel.ActionTarget.ArtistTarget(artist)) }) {
                        Icon(Icons.Default.MoreVert, "Actions")
                    }
                }
            )
        }
    }
}

@Composable
fun AlbumList(vm: MainViewModel) {
    LazyColumn(Modifier.fillMaxSize()) {
        item {
            Text(
                "${vm.albums.size} albums",
                style = MaterialTheme.typography.labelMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp)
            )
        }
        itemsIndexed(vm.albums) { _, album ->
            ListItem(
                headlineContent = {
                    Text(album.album, maxLines = 1, overflow = TextOverflow.Ellipsis)
                },
                supportingContent = {
                    if (album.date.isNotBlank() && album.date != "0000") {
                        Text(album.date)
                    }
                },
                modifier = Modifier.clickable { vm.loadTracks(album) },
                trailingContent = {
                    IconButton(onClick = { vm.showAction(MainViewModel.ActionTarget.AlbumTarget(album)) }) {
                        Icon(Icons.Default.MoreVert, "Actions")
                    }
                }
            )
        }
    }
}

@Composable
fun TrackList(vm: MainViewModel) {
    LazyColumn(Modifier.fillMaxSize()) {
        // Album header
        if (vm.curAlbum != null) {
            item {
                Column(
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 12.dp)
                ) {
                    Text(
                        vm.curArtist,
                        style = MaterialTheme.typography.labelLarge,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                    if (vm.curAlbum?.date?.isNotBlank() == true && vm.curAlbum?.date != "0000") {
                        Text(
                            vm.curAlbum!!.date,
                            style = MaterialTheme.typography.labelMedium,
                            color = MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.7f)
                        )
                    }
                    Spacer(Modifier.height(4.dp))
                    Text(
                        "${vm.tracks.size} tracks",
                        style = MaterialTheme.typography.labelSmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.5f)
                    )
                }
                HorizontalDivider(color = MaterialTheme.colorScheme.outline.copy(alpha = 0.3f))
            }
        }
        itemsIndexed(vm.tracks) { _, track ->
            ListItem(
                leadingContent = {
                    Text(
                        "${track.trackNumber}",
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.width(28.dp),
                        textAlign = TextAlign.End
                    )
                },
                headlineContent = {
                    Text(track.title, maxLines = 1, overflow = TextOverflow.Ellipsis)
                },
                modifier = Modifier.clickable { vm.showAction(MainViewModel.ActionTarget.TrackTarget(track)) }
            )
        }
    }
}

// ==================== Search ====================

@Composable
fun SearchScreen(vm: MainViewModel) {
    Column(Modifier.fillMaxSize()) {
        OutlinedTextField(
            value = vm.searchQuery,
            onValueChange = { vm.updateSearch(it) },
            placeholder = { Text("Search albums and tracks\u2026") },
            singleLine = true,
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 16.dp, vertical = 12.dp),
            shape = RoundedCornerShape(16.dp),
            keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search),
            keyboardActions = KeyboardActions(onSearch = {}),
            leadingIcon = { Icon(Icons.Default.Search, "Search") },
            trailingIcon = {
                if (vm.searchQuery.isNotBlank()) {
                    IconButton(onClick = { vm.updateSearch("") }) {
                        Icon(Icons.Default.Clear, "Clear")
                    }
                }
            }
        )

        val res = vm.searchResult
        LazyColumn(Modifier.fillMaxSize()) {
            if (res.albums.isNotEmpty()) {
                item {
                    Text(
                        "Albums",
                        style = MaterialTheme.typography.titleSmall,
                        color = MaterialTheme.colorScheme.primary,
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 10.dp)
                    )
                }
                itemsIndexed(res.albums) { _, album ->
                    ListItem(
                        headlineContent = {
                            Text(album.album, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        },
                        supportingContent = {
                            val parts = mutableListOf(album.albumArtist)
                            if (album.date.isNotBlank()) parts.add(album.date)
                            Text(parts.joinToString(" \u2022 "), maxLines = 1, overflow = TextOverflow.Ellipsis)
                        },
                        modifier = Modifier.clickable { vm.showAction(MainViewModel.ActionTarget.SearchAlbumTarget(album)) }
                    )
                }
            }
            if (res.tracks.isNotEmpty()) {
                item {
                    Text(
                        "Tracks",
                        style = MaterialTheme.typography.titleSmall,
                        color = MaterialTheme.colorScheme.primary,
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 10.dp)
                    )
                }
                itemsIndexed(res.tracks) { _, track ->
                    ListItem(
                        headlineContent = {
                            Text(track.title, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        },
                        supportingContent = {
                            Text(
                                "${track.artist} \u2014 ${track.album}",
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis
                            )
                        },
                        modifier = Modifier.clickable { vm.showAction(MainViewModel.ActionTarget.SearchTrackTarget(track)) }
                    )
                }
            }
            if (vm.searchQuery.isNotBlank() && res.albums.isEmpty() && res.tracks.isEmpty()) {
                item {
                    Box(
                        Modifier
                            .fillMaxWidth()
                            .padding(48.dp),
                        contentAlignment = Alignment.Center
                    ) {
                        Text(
                            "No results found",
                            style = MaterialTheme.typography.bodyLarge,
                            color = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                    }
                }
            }
        }
    }
}

// ==================== Queue ====================

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun QueueScreen(vm: MainViewModel) {
    Column(Modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text("Queue") },
            actions = {
                if (vm.queue.isNotEmpty()) {
                    IconButton(onClick = { vm.queueClear() }) {
                        Icon(Icons.Default.DeleteSweep, "Clear queue")
                    }
                }
            }
        )

        if (vm.queue.isEmpty()) {
            Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                Column(horizontalAlignment = Alignment.CenterHorizontally) {
                    Icon(
                        Icons.AutoMirrored.Filled.QueueMusic,
                        contentDescription = null,
                        modifier = Modifier.size(48.dp),
                        tint = MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.5f)
                    )
                    Spacer(Modifier.height(12.dp))
                    Text(
                        "Queue is empty",
                        style = MaterialTheme.typography.bodyLarge,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }
            }
        } else {
            LazyColumn(Modifier.fillMaxSize()) {
                item {
                    Text(
                        "${vm.queue.size} tracks",
                        style = MaterialTheme.typography.labelMedium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp)
                    )
                }
                itemsIndexed(vm.queue) { _, item ->
                    val isCurrent = item.current
                    ListItem(
                        headlineContent = {
                            Text(
                                item.title.ifBlank { "Unknown" },
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                                fontWeight = if (isCurrent) FontWeight.SemiBold else FontWeight.Normal,
                                color = if (isCurrent) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurface
                            )
                        },
                        supportingContent = {
                            Text(
                                "${item.artist} \u2014 ${item.album}",
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis
                            )
                        },
                        leadingContent = {
                            if (isCurrent) {
                                Icon(
                                    Icons.Default.PlayArrow,
                                    "Playing",
                                    tint = MaterialTheme.colorScheme.primary,
                                    modifier = Modifier.size(20.dp)
                                )
                            } else {
                                Text(
                                    "${item.position + 1}",
                                    style = MaterialTheme.typography.bodySmall,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                                    modifier = Modifier.width(24.dp),
                                    textAlign = TextAlign.End
                                )
                            }
                        },
                        trailingContent = {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Text(
                                    fmtTime(item.duration),
                                    style = MaterialTheme.typography.labelSmall,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant
                                )
                                IconButton(
                                    onClick = { vm.queueRemove(item.position) },
                                    modifier = Modifier.size(32.dp)
                                ) {
                                    Icon(
                                        Icons.Default.Close,
                                        "Remove",
                                        modifier = Modifier.size(16.dp)
                                    )
                                }
                            }
                        },
                        colors = if (isCurrent) ListItemDefaults.colors(
                            containerColor = MaterialTheme.colorScheme.primary.copy(alpha = 0.08f)
                        ) else ListItemDefaults.colors(),
                        modifier = Modifier.clickable { vm.queuePlay(item.position) }
                    )
                }
            }
        }
    }
}

// ==================== Devices ====================

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun DevicesScreen(vm: MainViewModel) {
    Column(Modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text("Devices") },
            actions = {
                IconButton(onClick = { vm.loadDevices() }) {
                    Icon(Icons.Default.Refresh, "Refresh")
                }
            }
        )

        if (vm.devices.isEmpty()) {
            Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                Column(horizontalAlignment = Alignment.CenterHorizontally) {
                    Icon(
                        Icons.Default.Devices,
                        contentDescription = null,
                        modifier = Modifier.size(48.dp),
                        tint = MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.5f)
                    )
                    Spacer(Modifier.height(12.dp))
                    Text(
                        "No devices found",
                        style = MaterialTheme.typography.bodyLarge,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }
            }
        } else {
            LazyColumn(Modifier.fillMaxSize()) {
                itemsIndexed(vm.devices) { _, dev ->
                    ListItem(
                        headlineContent = {
                            Text(
                                dev.name,
                                fontWeight = if (dev.active) FontWeight.SemiBold else FontWeight.Normal,
                                color = if (dev.active) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurface
                            )
                        },
                        supportingContent = {
                            val parts = mutableListOf<String>()
                            parts.add(when (dev.type) {
                                "local" -> "Server"
                                "browser" -> "Stream"
                                "agent" -> "Agent"
                                else -> dev.type
                            })
                            if (dev.format.isNotBlank()) {
                                var q = dev.format
                                if (dev.maxBitrate > 0) q += " ${dev.maxBitrate}k"
                                parts.add(q)
                            }
                            Text(parts.joinToString(" \u2022 "))
                        },
                        leadingContent = {
                            Box(
                                Modifier
                                    .size(10.dp)
                                    .clip(CircleShape)
                                    .background(
                                        if (dev.online) Color(0xFF22C55E) else MaterialTheme.colorScheme.outline
                                    )
                            )
                        },
                        trailingContent = {
                            if (dev.active) {
                                Icon(
                                    Icons.AutoMirrored.Filled.VolumeUp,
                                    "Active",
                                    tint = MaterialTheme.colorScheme.primary
                                )
                            }
                        },
                        modifier = Modifier.clickable { vm.setActiveDevice(dev.id) }
                    )
                }
            }
        }
    }
}

// ==================== Settings ====================

@OptIn(ExperimentalMaterial3Api::class, ExperimentalLayoutApi::class)
@Composable
fun SettingsScreen(onDismiss: () -> Unit) {
    BackHandler { onDismiss() }

    val prefs = SubclerkApp.instance.getSharedPreferences(
        "subclerk", android.content.Context.MODE_PRIVATE
    )
    var server by remember { mutableStateOf(prefs.getString("server", "") ?: "") }
    var navidromeUrl by remember { mutableStateOf(prefs.getString("navidrome_url", "") ?: "") }
    var deviceName by remember {
        mutableStateOf(
            prefs.getString("device_name", null)
                ?: "android-${android.os.Build.MODEL}".replace(" ", "-").lowercase()
        )
    }
    var format by remember { mutableStateOf(prefs.getString("audio_format", "") ?: "") }
    var bitrate by remember { mutableIntStateOf(prefs.getInt("audio_bitrate", 0)) }
    var replaygain by remember { mutableStateOf(prefs.getString("replaygain", "off") ?: "off") }

    fun saveAndReRegister() {
        prefs.edit()
            .putString("server", server)
            .putString("navidrome_url", navidromeUrl)
            .putString("device_name", deviceName)
            .putString("audio_format", format)
            .putInt("audio_bitrate", bitrate)
            .putString("replaygain", replaygain)
            .apply()
        SubclerkApp.instance.updateServer(server)
    }

    Surface(
        modifier = Modifier.fillMaxSize(),
        color = MaterialTheme.colorScheme.background
    ) {
        Column(Modifier.fillMaxSize()) {
            TopAppBar(
                title = { Text("Settings") },
                navigationIcon = {
                    IconButton(onClick = {
                        saveAndReRegister()
                        onDismiss()
                    }) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, "Back")
                    }
                }
            )

            LazyColumn(
                modifier = Modifier.fillMaxSize(),
                verticalArrangement = Arrangement.spacedBy(8.dp)
            ) {
                // Connection
                item {
                    Card(
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(horizontal = 16.dp, vertical = 4.dp),
                        shape = RoundedCornerShape(16.dp)
                    ) {
                        Column(Modifier.padding(16.dp)) {
                            Text(
                                "Connection",
                                style = MaterialTheme.typography.titleSmall,
                                fontWeight = FontWeight.SemiBold
                            )
                            Spacer(Modifier.height(12.dp))
                            OutlinedTextField(
                                value = server,
                                onValueChange = { server = it },
                                label = { Text("Server address") },
                                placeholder = { Text("192.168.1.10:6701") },
                                singleLine = true,
                                modifier = Modifier.fillMaxWidth(),
                                colors = OutlinedTextFieldDefaults.colors()
                            )
                            Spacer(Modifier.height(12.dp))
                            OutlinedTextField(
                                value = navidromeUrl,
                                onValueChange = { navidromeUrl = it },
                                label = { Text("Navidrome URL (optional)") },
                                placeholder = { Text("https://music.example.com") },
                                singleLine = true,
                                modifier = Modifier.fillMaxWidth(),
                                supportingText = {
                                    Text("External URL for mobile network access")
                                },
                                colors = OutlinedTextFieldDefaults.colors()
                            )
                        }
                    }
                }

                // Device
                item {
                    Card(
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(horizontal = 16.dp, vertical = 4.dp),
                        shape = RoundedCornerShape(16.dp)
                    ) {
                        Column(Modifier.padding(16.dp)) {
                            Text(
                                "Device",
                                style = MaterialTheme.typography.titleSmall,
                                fontWeight = FontWeight.SemiBold
                            )
                            Spacer(Modifier.height(12.dp))
                            OutlinedTextField(
                                value = deviceName,
                                onValueChange = { deviceName = it },
                                label = { Text("Device name") },
                                singleLine = true,
                                modifier = Modifier.fillMaxWidth(),
                                colors = OutlinedTextFieldDefaults.colors()
                            )
                        }
                    }
                }

                // Audio quality
                item {
                    Card(
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(horizontal = 16.dp, vertical = 4.dp),
                        shape = RoundedCornerShape(16.dp)
                    ) {
                        Column(Modifier.padding(16.dp)) {
                            Text(
                                "Audio Quality",
                                style = MaterialTheme.typography.titleSmall,
                                fontWeight = FontWeight.SemiBold
                            )
                            Spacer(Modifier.height(12.dp))
                            Text(
                                "Format",
                                style = MaterialTheme.typography.labelMedium,
                                color = MaterialTheme.colorScheme.onSurfaceVariant
                            )
                            Spacer(Modifier.height(6.dp))
                            FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                listOf(
                                    "" to "Original",
                                    "opus" to "Opus",
                                    "mp3" to "MP3",
                                    "aac" to "AAC",
                                    "flac" to "FLAC"
                                ).forEach { (value, label) ->
                                    FilterChip(
                                        selected = format == value,
                                        onClick = { format = value },
                                        label = { Text(label) }
                                    )
                                }
                            }
                            Spacer(Modifier.height(12.dp))
                            Text(
                                "Bitrate",
                                style = MaterialTheme.typography.labelMedium,
                                color = MaterialTheme.colorScheme.onSurfaceVariant
                            )
                            Spacer(Modifier.height(6.dp))
                            FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                listOf(
                                    0 to "Max",
                                    64 to "64k",
                                    128 to "128k",
                                    192 to "192k",
                                    256 to "256k",
                                    320 to "320k"
                                ).forEach { (value, label) ->
                                    FilterChip(
                                        selected = bitrate == value,
                                        onClick = { bitrate = value },
                                        label = { Text(label) }
                                    )
                                }
                            }
                            Spacer(Modifier.height(12.dp))
                            Text(
                                "ReplayGain",
                                style = MaterialTheme.typography.labelMedium,
                                color = MaterialTheme.colorScheme.onSurfaceVariant
                            )
                            Spacer(Modifier.height(6.dp))
                            FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                listOf(
                                    "off" to "Off",
                                    "track" to "Track",
                                    "album" to "Album"
                                ).forEach { (value, label) ->
                                    FilterChip(
                                        selected = replaygain == value,
                                        onClick = { replaygain = value },
                                        label = { Text(label) }
                                    )
                                }
                            }
                        }
                    }
                }

                // Bottom spacing
                item { Spacer(Modifier.height(32.dp)) }
            }
        }
    }
}

// ==================== Action Sheet ====================

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ActionSheet(vm: MainViewModel) {
    val target = vm.actionTarget ?: return
    val sheetState = rememberModalBottomSheetState()
    val label = when (target) {
        is MainViewModel.ActionTarget.ArtistTarget -> target.name
        is MainViewModel.ActionTarget.AlbumTarget -> target.album.album
        is MainViewModel.ActionTarget.TrackTarget -> target.track.title
        is MainViewModel.ActionTarget.SearchAlbumTarget -> target.album.album
        is MainViewModel.ActionTarget.SearchTrackTarget -> target.track.title
    }
    val canBrowse = target !is MainViewModel.ActionTarget.TrackTarget &&
            target !is MainViewModel.ActionTarget.SearchTrackTarget

    ModalBottomSheet(
        onDismissRequest = { vm.dismissAction() },
        sheetState = sheetState,
        containerColor = MaterialTheme.colorScheme.surface
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(bottom = 32.dp)
        ) {
            // Title
            Text(
                label,
                style = MaterialTheme.typography.titleMedium,
                fontWeight = FontWeight.SemiBold,
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.padding(horizontal = 24.dp, vertical = 12.dp)
            )
            HorizontalDivider(color = MaterialTheme.colorScheme.outline.copy(alpha = 0.3f))

            // Actions
            ListItem(
                headlineContent = { Text("Add to queue") },
                leadingContent = { Icon(Icons.AutoMirrored.Filled.PlaylistAdd, null) },
                modifier = Modifier.clickable { vm.executeAction("add") }
            )
            ListItem(
                headlineContent = { Text("Insert after current") },
                leadingContent = { Icon(Icons.Default.Add, null) },
                modifier = Modifier.clickable { vm.executeAction("insert") }
            )
            ListItem(
                headlineContent = { Text("Replace queue") },
                leadingContent = { Icon(Icons.AutoMirrored.Filled.PlaylistPlay, null) },
                modifier = Modifier.clickable { vm.executeAction("replace") }
            )
            if (canBrowse) {
                ListItem(
                    headlineContent = { Text("Browse into") },
                    leadingContent = { Icon(Icons.Default.FolderOpen, null) },
                    modifier = Modifier.clickable { vm.browseIntoAction() }
                )
            }
        }
    }
}

// ==================== Helpers ====================

fun fmtTime(seconds: Double): String {
    if (seconds < 0 || seconds.isNaN()) return "0:00"
    val m = (seconds / 60).toInt()
    val s = (seconds % 60).toInt()
    return "$m:${s.toString().padStart(2, '0')}"
}

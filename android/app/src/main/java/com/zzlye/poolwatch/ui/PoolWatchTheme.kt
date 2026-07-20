package com.zzlye.poolwatch.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color

private val LightColors = lightColorScheme(
    primary = Color(0xFF177A4A),
    onPrimary = Color.White,
    primaryContainer = Color(0xFFD6F5E3),
    onPrimaryContainer = Color(0xFF073D24),
    secondary = Color(0xFF59645E),
    background = Color(0xFFF4F6F5),
    surface = Color(0xFFFFFFFF),
    surfaceVariant = Color(0xFFE7EBE8),
    error = Color(0xFFBA2D2D),
)

private val DarkColors = darkColorScheme(
    primary = Color(0xFF7BD9A5),
    onPrimary = Color(0xFF063B22),
    primaryContainer = Color(0xFF125D3A),
    onPrimaryContainer = Color(0xFFD6F5E3),
    secondary = Color(0xFFBCC8C0),
    background = Color(0xFF111513),
    surface = Color(0xFF181D1A),
    surfaceVariant = Color(0xFF27302B),
    error = Color(0xFFFFB4AB),
)

@Composable
fun PoolWatchTheme(content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = if (isSystemInDarkTheme()) DarkColors else LightColors,
        content = content,
    )
}

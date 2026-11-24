// Copyright 2026 肖其顿 (XIAO QI DUN)
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !android && (((amd64 || arm64) && (!guigo_gtk3 || cgo)) || (loong64 && cgo))

package guigo

import (
	"os"
	"path/filepath"
	"strings"
)

// systemDark 检测Linux桌面环境是否偏好深色外观
// 返回: bool 是否偏好深色外观
func systemDark() bool {
	if theme := strings.ToLower(strings.TrimSpace(os.Getenv("GTK_THEME"))); theme != "" {
		if strings.Contains(theme, ":light") {
			return false
		}
		if strings.Contains(theme, "dark") {
			return true
		}
	}
	if dark, ok := linuxPortalDark(); ok {
		return dark
	}
	return linuxGTKSettingsDark()
}

// linuxGTKSettingsDark 读取用户GTK配置文件回退判断深色偏好
// 返回: bool 是否偏好深色外观
func linuxGTKSettingsDark() bool {
	configDir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if configDir == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return false
		}
		configDir = dir
	}
	for _, sub := range []string{"gtk-4.0", "gtk-3.0"} {
		data, err := os.ReadFile(filepath.Join(configDir, sub, "settings.ini"))
		if err != nil {
			continue
		}
		inSettings := false
		hasPreferDark := false
		preferDark := false
		hasTheme := false
		themeDark := false
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				inSettings = strings.EqualFold(strings.TrimSpace(line[1:len(line)-1]), "settings")
				continue
			}
			if !inSettings {
				continue
			}
			key, val, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key = strings.ToLower(strings.TrimSpace(key))
			val = strings.ToLower(strings.TrimSpace(val))
			if key == "gtk-application-prefer-dark-theme" {
				hasPreferDark = true
				preferDark = val == "1" || val == "true"
			} else if key == "gtk-theme-name" && val != "" {
				hasTheme = true
				if strings.Contains(val, ":light") {
					themeDark = false
				} else {
					themeDark = strings.Contains(val, "dark")
				}
			}
		}
		if hasPreferDark {
			return preferDark
		}
		if hasTheme {
			return themeDark
		}
	}
	return false
}

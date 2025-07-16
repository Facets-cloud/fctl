package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// cleanupOldReleases keeps only the last 10 deployment directories and zip files for the given envDir and baseDir.
// It silently deletes older ones (both directories and zips) if more than 10 exist.
func cleanupOldReleases(envDir, baseDir, envID string) {
	// --- Cleanup Directories ---
	entries, err := os.ReadDir(envDir)
	if err == nil {
		var dirs []string
		for _, entry := range entries {
			if entry.IsDir() {
				dirs = append(dirs, entry.Name())
			}
		}
		// Sort by name (assuming name encodes time, as in deploymentID)
		sort.Strings(dirs)
		if len(dirs) > 10 {
			for _, dir := range dirs[:len(dirs)-10] {
				os.RemoveAll(filepath.Join(envDir, dir))
			}
		}
	}

	// --- Cleanup Zip Files ---
	// Zips are stored in the current working directory, matching pattern: <deploymentID>.zip (UUID format)
	zipPattern := regexp.MustCompile(`[a-fA-F0-9\-]{36}\.zip$`)
	files, err := os.ReadDir(baseDir)
	if err == nil {
		var zips []string
		for _, entry := range files {
			if !entry.IsDir() && zipPattern.MatchString(entry.Name()) {
				zips = append(zips, entry.Name())
			}
		}
		sort.Strings(zips)
		if len(zips) > 10 {
			for _, zip := range zips[:len(zips)-10] {
				os.Remove(filepath.Join(baseDir, zip))
			}
		}
	}
}

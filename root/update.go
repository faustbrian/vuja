package root

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/faustbrian/vuja/internal/config"
	"github.com/spf13/cobra"
)

// updateResult is passed from the async checker to the main loop
type updateResult struct {
	latestVersion string
	hasUpdate     bool
}

// pendingUpdate is set by the background goroutine and consumed once after the first VUJA_CMD_STOP
var pendingUpdate chan updateResult

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type releaseInfo struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

// FetchLatestVersion hits the GitHub Releases API and returns the latest tag name
func FetchLatestVersion() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	release, err := fetchLatestRelease(ctx, http.DefaultClient)
	if err != nil {
		return "", err
	}
	return release.TagName, nil
}

func fetchLatestRelease(ctx context.Context, client *http.Client) (releaseInfo, error) {
	endpoint := os.Getenv("VUJA_UPDATE_URL")
	if endpoint == "" {
		if config.Get().Updater.Channel == "nightly" {
			endpoint = "https://api.github.com/repos/faustbrian/vuja/releases"
		} else {
			endpoint = "https://api.github.com/repos/faustbrian/vuja/releases/latest"
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return releaseInfo{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return releaseInfo{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return releaseInfo{}, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return releaseInfo{}, err
	}

	if config.Get().Updater.Channel == "nightly" && os.Getenv("VUJA_UPDATE_URL") == "" {
		var releases []releaseInfo
		if err := json.Unmarshal(body, &releases); err != nil {
			return releaseInfo{}, err
		}
		if len(releases) == 0 {
			return releaseInfo{}, fmt.Errorf("no releases found")
		}
		return releases[0], nil
	}

	var result releaseInfo
	if err := json.Unmarshal(body, &result); err != nil {
		return releaseInfo{}, err
	}
	if result.TagName == "" {
		return releaseInfo{}, fmt.Errorf("no tag_name in response")
	}
	return result, nil
}

// IsNewer returns true if latest is a newer semantic version than current.
// it supports basic vX.Y.Z formats.
func IsNewer(current, latest string) bool {
	c := strings.TrimPrefix(current, "v")
	l := strings.TrimPrefix(latest, "v")

	// dev builds or empty versions never trigger an update
	if c == "" || c == "dev" || l == "" || l == "dev" {
		return false
	}

	// nightly builds are never shown as stable update targets
	if config.Get().Updater.Channel != "nightly" && strings.Contains(l, "-nightly.") {
		return false
	}

	if c == l {
		return false
	}

	cParts := strings.Split(c, ".")
	lParts := strings.Split(l, ".")

	// compare major.minor.patch
	for i := 0; i < len(cParts) && i < len(lParts); i++ {
		// strip pre-release tags like -beta or -rc for numeric comparison
		cClean := strings.Split(cParts[i], "-")[0]
		lClean := strings.Split(lParts[i], "-")[0]

		cv, _ := strconv.Atoi(cClean)
		lv, _ := strconv.Atoi(lClean)
		if lv > cv {
			return true
		}
		if lv < cv {
			return false
		}
	}

	// if all parts are equal, the one with more parts is newer (e.g. 1.0.1 > 1.0)
	return len(lParts) > len(cParts)
}

// startBackgroundUpdateCheck runs a non-blocking goroutine to check for updates.
// it sends a result on the returned channel exactly once, then closes it
//
// for testing without a real release, set VUJA_MOCK_LATEST_VERSION=v1.99.0
func startBackgroundUpdateCheck() chan updateResult {
	ch := make(chan updateResult, 1)

	if !config.Get().Updater.CheckOnStartup {
		close(ch)
		return ch
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				WriteCrashLog(r)
				restoreTerminal()
				printCrashNotice()
				startRescueShell()
				os.Exit(2)
			}
		}()
		defer close(ch)

		// debug override: skip network entirely, resolve immediately
		if mock := os.Getenv("VUJA_MOCK_LATEST_VERSION"); mock != "" {
			if IsNewer(Version, mock) {
				ch <- updateResult{latestVersion: mock, hasUpdate: true}
			}
			return
		}

		state := config.LoadState()

		// only check once every configured check-interval to avoid hammering the API
		if time.Since(state.Updater.LastCheckTime) < time.Duration(config.Get().Updater.CheckInterval) {
			// already checked recently; still notify if we have a cached pending update
			if state.Updater.SeenVersion != "" && IsNewer(Version, state.Updater.SeenVersion) {
				ch <- updateResult{latestVersion: state.Updater.SeenVersion, hasUpdate: true}
			}
			return
		}

		latest, err := FetchLatestVersion()
		if err != nil {
			// no network or API error: silently do nothing
			return
		}

		// update the last check time regardless of result
		state.Updater.LastCheckTime = time.Now()

		if IsNewer(Version, latest) {
			// only notify if user hasn't already seen this specific version notification
			if state.Updater.SeenVersion != latest {
				ch <- updateResult{latestVersion: latest, hasUpdate: true}
			}
			// save the latest as seen_version so future sessions don't re-notify
			// unless a NEWER version comes out (different tag)
			state.Updater.SeenVersion = latest
		} else {
			// up to date: clear the seen_version flag so the next update triggers a fresh notification
			state.Updater.SeenVersion = ""
		}

		_ = config.SaveState(state)
	}()

	return ch
}

// printUpdateNotice writes the one-time update message to stdout
func printUpdateNotice(latest string) {
	fmt.Printf(
		"\r\033[K\033[33m[VUJA] new version %s → %s available, run \033[1mvuja update\033[0m\033[33m to upgrade\033[0m\n",
		Version, latest,
	)
}

func installRelease(ctx context.Context, client *http.Client, release releaseInfo, target string) error {
	archiveName := fmt.Sprintf("vuja_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archiveAsset, ok := findReleaseAsset(release.Assets, archiveName)
	if !ok {
		return fmt.Errorf("release %s does not contain %s", release.TagName, archiveName)
	}
	checksumAsset, ok := findReleaseAsset(release.Assets, "SHA256SUMS")
	if !ok {
		return fmt.Errorf("release %s does not contain SHA256SUMS", release.TagName)
	}

	archive, err := downloadReleaseAsset(ctx, client, archiveAsset.URL, 256<<20)
	if err != nil {
		return fmt.Errorf("download %s: %w", archiveName, err)
	}
	checksums, err := downloadReleaseAsset(ctx, client, checksumAsset.URL, 1<<20)
	if err != nil {
		return fmt.Errorf("download SHA256SUMS: %w", err)
	}
	expected, err := checksumForAsset(checksums, archiveName)
	if err != nil {
		return err
	}
	actual := fmt.Sprintf("%x", sha256.Sum256(archive))
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("checksum mismatch for %s", archiveName)
	}

	binary, err := extractVujaBinary(archive)
	if err != nil {
		return err
	}
	return replaceExecutable(target, binary)
}

func findReleaseAsset(assets []releaseAsset, name string) (releaseAsset, bool) {
	for _, asset := range assets {
		if asset.Name == name && asset.URL != "" {
			return asset, true
		}
	}
	return releaseAsset{}, false
}

func downloadReleaseAsset(ctx context.Context, client *http.Client, url string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("asset exceeds %d bytes", maxBytes)
	}
	return data, nil
}

func checksumForAsset(checksums []byte, assetName string) (string, error) {
	for line := range strings.SplitSeq(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.TrimPrefix(fields[1], "*") != assetName {
			continue
		}
		if len(fields[0]) != sha256.Size*2 {
			return "", fmt.Errorf("invalid checksum for %s", assetName)
		}
		return fields[0], nil
	}
	return "", fmt.Errorf("SHA256SUMS does not contain %s", assetName)
}

func extractVujaBinary(archive []byte) ([]byte, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	defer func() { _ = gzipReader.Close() }()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, nextErr := tarReader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nil, fmt.Errorf("read release archive: %w", nextErr)
		}
		if filepath.Clean(header.Name) != "vuja" || !header.FileInfo().Mode().IsRegular() {
			continue
		}
		if header.Size > 256<<20 {
			return nil, fmt.Errorf("vuja binary exceeds 256 MiB")
		}
		binary, readErr := io.ReadAll(io.LimitReader(tarReader, header.Size))
		if readErr != nil {
			return nil, fmt.Errorf("extract vuja binary: %w", readErr)
		}
		return binary, nil
	}
	return nil, fmt.Errorf("release archive does not contain vuja")
}

func replaceExecutable(target string, binary []byte) error {
	dir := filepath.Dir(target)
	temp, err := os.CreateTemp(dir, ".vuja-update-*")
	if err != nil {
		return fmt.Errorf("create replacement: %w", err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()

	if _, err = temp.Write(binary); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write replacement: %w", err)
	}
	if err = temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync replacement: %w", err)
	}
	if err = temp.Chmod(0755); err != nil {
		_ = temp.Close()
		return fmt.Errorf("make replacement executable: %w", err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("close replacement: %w", err)
	}
	if err = os.Rename(tempName, target); err != nil {
		return fmt.Errorf("replace executable: %w", err)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the current Vuja version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("vuja %s\n", Version)
	},
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update Vuja to the latest release",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("checking for updates (current: %s)...\n", Version)

		ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
		defer cancel()
		release, err := fetchLatestRelease(ctx, http.DefaultClient)
		if err != nil {
			fmt.Printf("\033[31m[VUJA] could not reach update server: %v\033[0m\n", err)
			return
		}
		latest := release.TagName

		if Version != "dev" && Version != "" && !IsNewer(Version, latest) {
			fmt.Printf("\033[32m[VUJA] already up to date (%s)\033[0m\n", Version)
			// clear seen_version so the notification doesn't show again
			state := config.LoadState()
			state.Updater.SeenVersion = ""
			_ = config.SaveState(state)
			return
		}

		fmt.Printf("\033[36m[VUJA] updating %s → %s\033[0m\n", Version, latest)

		target, err := os.Executable()
		if err != nil {
			fmt.Printf("\n\033[31m[VUJA] update failed: %v\033[0m\n", err)
			return
		}
		if err := installRelease(ctx, http.DefaultClient, release, target); err != nil {
			fmt.Printf("\n\033[31m[VUJA] update failed: %v\033[0m\n", err)
			return
		}

		// after a successful update, mark as seen so no more notifications
		state := config.LoadState()
		state.Updater.SeenVersion = ""
		_ = config.SaveState(state)

		fmt.Printf("\n\033[32m[VUJA] restart your terminal to use the new version\033[0m\n")
	},
}

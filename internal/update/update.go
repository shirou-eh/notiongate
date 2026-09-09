// Package update — самообновление notiongate.
// Дергает GitHub Releases, скачивает бинарник под OS/ARCH, заменяет себя.
package update

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const Repo = "shirou-eh/notiongate"
const CurrentVersion = "1.2.3"

type Release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Check возвращает последнюю версию с GitHub.
func Check() (string, string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", Repo)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "notiongate-updater")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		// репо ещё без релизов — считаем текущую актуальной
		return CurrentVersion, "", nil
	}
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("github api: %d", resp.StatusCode)
	}
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", err
	}
	tag := strings.TrimPrefix(rel.TagName, "v")
	asset := fmt.Sprintf("notiongate-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		asset += ".exe"
	}
	downloadURL := ""
	for _, a := range rel.Assets {
		if strings.Contains(a.Name, runtime.GOOS) && strings.Contains(a.Name, runtime.GOARCH) {
			downloadURL = a.URL
			break
		}
	}
	if downloadURL == "" {
		// fallback: пробуем сконструировать
		downloadURL = fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s", Repo, tag, asset)
	}
	return tag, downloadURL, nil
}

// DownloadAndReplace скачивает новый бинарь и заменяет текущий.
// Работает и с curl, и с irm — один и тот же путь.
func DownloadAndReplace(downloadURL string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// если запущен через go run — пропускаем
	if strings.Contains(exe, "go-build") {
		return fmt.Errorf("запущен через go run — обнови через go install")
	}
	tmp := exe + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("GET", downloadURL, nil)
	req.Header.Set("User-Agent", "notiongate-updater")
	resp, err := client.Do(req)
	if err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("download: %d", resp.StatusCode)
	}
	// Limit to 100MB
	limited := io.LimitReader(resp.Body, 100*1024*1024+1)
	n, err := io.Copy(out, limited)
	if err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if n > 100*1024*1024 {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("download too large")
	}
	out.Close()
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	// атомарная замена
	if err := os.Rename(tmp, exe); err != nil {
		// windows: нужно удалить старый
		os.Remove(exe)
		if err2 := os.Rename(tmp, exe); err2 != nil {
			return err2
		}
	}
	return nil
}

// BundlePath возвращает путь куда сохранять бандл для переноса.
func BundlePath() string {
	// рядом с бинарем или в домашней директории
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return filepath.Join(dir, "notiongate-bundle.tgz")
		}
	}
	return "notiongate-bundle.tgz"
}

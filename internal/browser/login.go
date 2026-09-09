// Package browser automates first-time account capture: it opens a real
// Chrome window with a temporary profile, lets the user log in to Notion
// once (email code / OAuth cannot be automated), and polls the browser via
// CDP until the `token_v2` session cookie appears.
package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// FindChrome locates a Chromium-based browser executable.
// NOTIONGATE_CHROME overrides the search.
func FindChrome() (string, error) {
	if p := strings.TrimSpace(os.Getenv("NOTIONGATE_CHROME")); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("NOTIONGATE_CHROME=%q does not exist", p)
	}
	switch runtime.GOOS {
	case "darwin":
		for _, p := range []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		} {
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	case "windows":
		for _, env := range []string{"PROGRAMFILES", "PROGRAMFILES(X86)", "LOCALAPPDATA"} {
			base := os.Getenv(env)
			if base == "" {
				continue
			}
			for _, rel := range []string{
				`Google\Chrome\Application\chrome.exe`,
				`Microsoft\Edge\Application\msedge.exe`,
			} {
				p := filepath.Join(base, rel)
				if _, err := os.Stat(p); err == nil {
					return p, nil
				}
			}
		}
	default:
		for _, name := range []string{
			"google-chrome-stable", "google-chrome", "chromium-browser",
			"chromium", "brave-browser", "microsoft-edge",
		} {
			if p, err := exec.LookPath(name); err == nil {
				return p, nil
			}
		}
	}
	return "", errors.New("no Chromium-based browser found (set NOTIONGATE_CHROME=/path/to/chrome)")
}

// WaitForTokenV2 opens Notion in a browser window and waits until the user
// completes login, then returns the token_v2 cookie value.
//
// profileDir == "" uses a throw-away profile that is deleted afterwards;
// an explicit dir is reused (handy on headless-ish setups where you keep a
// dedicated profile).
func WaitForTokenV2(parent context.Context, exe, profileDir string, timeout time.Duration) (string, error) {
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return "", errors.New(
			"no graphical display detected — the login window cannot open here.\n" +
				"Options:\n" +
				"  1) add the account manually: notiongate accounts add --cookie \"token_v2=...\"\n" +
				"  2) run `notiongate login` on a machine with a GUI (cookie is stored in the DB)\n" +
				"  3) copy a dedicated Chrome profile to the server and pass --profile-dir")
	}

	if profileDir == "" {
		d, err := os.MkdirTemp("", "notiongate-chrome-")
		if err != nil {
			return "", fmt.Errorf("create temp profile: %w", err)
		}
		profileDir = d
		defer os.RemoveAll(d)
	}

	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.ExecPath(exe),
		chromedp.UserDataDir(profileDir),
		chromedp.WindowSize(1150, 900),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-session-crashed-bubble", true),
		chromedp.Flag("hide-crash-restore-bubble", true),
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(parent, opts...)
	defer cancelAlloc()
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()
	ctx, cancelTimeout := context.WithTimeout(browserCtx, timeout)
	defer cancelTimeout()

	if err := chromedp.Run(ctx, chromedp.Navigate("https://www.notion.so/")); err != nil {
		return "", fmt.Errorf("start browser: %w", err)
	}

	fmt.Println("  → в открывшемся окне Chrome войдите в аккаунт Notion (код из почты / Google)")
	fmt.Println("  → как только вход завершится, notiongate заберёт сессию автоматически и закроет окно")

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	consecutiveErrors := 0
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", fmt.Errorf("login timeout (%s): cookie token_v2 так и не появился", timeout)
			}
			return "", ctx.Err()
		case <-ticker.C:
			tok, err := readTokenV2(ctx)
			if err == nil && tok != "" {
				fmt.Println("\n  ✓ сессия получена")
				return tok, nil
			}
			if err != nil {
				// Browser window closed before login → give up quickly.
				consecutiveErrors++
				if consecutiveErrors > 10 {
					return "", errors.New("browser closed before login completed")
				}
			} else {
				consecutiveErrors = 0
				fmt.Print(".")
			}
		}
	}
}

// readTokenV2 reads the token_v2 cookie from the live browser via CDP.
// Notion migrates between domains (notion.so → notion.com, app.notion.com),
// so every plausible cookie domain is queried.
func readTokenV2(ctx context.Context) (string, error) {
	var val string
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		cookies, err := network.GetCookies().
			WithURLs([]string{
				"https://www.notion.so",
				"https://notion.so",
				"https://app.notion.so",
				"https://www.notion.com",
				"https://notion.com",
				"https://app.notion.com",
			}).
			Do(c)
		if err != nil {
			return err
		}
		for _, ck := range cookies {
			if ck.Name == "token_v2" && ck.Value != "" {
				val = ck.Value
				return nil
			}
		}
		return nil
	}))
	return val, err
}

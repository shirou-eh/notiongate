// Package tui — интерактивное меню notiongate с Гейтиком.
// Просто существует, ничего не болтает.
package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/shirou-eh/notiongate/internal/autostart"
	"github.com/shirou-eh/notiongate/internal/config"
	"github.com/shirou-eh/notiongate/internal/mascot"
	"github.com/shirou-eh/notiongate/internal/plugin"
	"github.com/shirou-eh/notiongate/internal/pool"
	"github.com/shirou-eh/notiongate/internal/store"
)

func RunMenu(cfg *config.Config, st *store.Store, p *pool.Pool) {
	br := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("\033[H\033[2J")
		fmt.Println(mascot.Banner())
		fmt.Println()
		views := p.Snapshot()
		active := 0
		for _, v := range views {
			if v.EffectiveStatus == "active" {
				active++
			}
		}
		fmt.Println(mascot.StatusArt(len(views), active))
		fmt.Printf("  notiongate — %d аккаунтов (%d active)\n\n", len(views), active)
		fmt.Println("  1) Статус пула")
		fmt.Println("  2) Добавить аккаунт (token_v2)")
		fmt.Println("  3) Список аккаунтов")
		fmt.Println("  4) Плагины")
		fmt.Println("  5) Автозапуск")
		fmt.Println("  6) Настройки (лимиты, ротация)")
		fmt.Println("  0) Выход")
		fmt.Print("\n  > ")

		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		choice := strings.TrimSpace(line)

		switch choice {
		case "1":
			showStatus(cfg, st, p, br)
		case "2":
			addAccountInteractive(p, br)
		case "3":
			listAccounts(p, br)
		case "4":
			pluginsMenu(br)
		case "5":
			autostartMenu(br)
		case "6":
			settingsMenu(cfg, br)
		case "0", "q", "exit":
			fmt.Println(mascot.ArtSmall)
			return
		default:
			fmt.Println("  Неизвестный пункт")
			pause(br)
		}
	}
}

func showStatus(cfg *config.Config, st *store.Store, p *pool.Pool, br *bufio.Reader) {
	fmt.Print("\033[H\033[2J")
	fmt.Println(mascot.ArtActive)
	views := p.Snapshot()
	fmt.Printf("  Пул: %d аккаунтов, rotate_at=%.0f%% window=%s\n", len(views), cfg.RotateAt*100, cfg.DefaultWindow)
	for _, v := range views {
		fmt.Printf("  %-24s %-10s %d/%d\n", v.Label, v.EffectiveStatus, v.ReqCount, v.Limit)
	}
	pause(br)
}

func listAccounts(p *pool.Pool, br *bufio.Reader) {
	fmt.Print("\033[H\033[2J")
	views := p.Snapshot()
	if len(views) == 0 {
		fmt.Println("  пул пуст")
	} else {
		for _, v := range views {
			fmt.Printf("  %s  %-20s  %s\n", v.ID[:8], v.Label, v.EffectiveStatus)
		}
	}
	pause(br)
}

func addAccountInteractive(p *pool.Pool, br *bufio.Reader) {
	fmt.Print("\033[H\033[2J")
	fmt.Println("  Вставь token_v2 (или полный Cookie):")
	fmt.Print("  > ")
	// Лимит 64KB — защита от OOM при вводе без \n
	limited := io.LimitReader(br, 64*1024+1)
	tmp, err := bufio.NewReader(limited).ReadString('\n')
	if err != nil && len(tmp) == 0 {
		return
	}
	raw := strings.TrimSpace(tmp)
	if raw == "" {
		return
	}
	if len(raw) > 64*1024 {
		fmt.Println("  ошибка: слишком длинный ввод")
		pause(br)
		return
	}
	// извлечь token_v2 как в accountsAdd
	tok := raw
	if idx := strings.Index(strings.ToLower(raw), "token_v2="); idx >= 0 {
		part := strings.Split(raw[idx:], ";")[0]
		tok = strings.TrimSpace(strings.SplitN(part, "=", 2)[1])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	acc, err := p.AddWithBootstrap(ctx, pool.AddInput{TokenV2: tok})
	if err != nil {
		fmt.Printf("  ошибка: %v\n", err)
	} else {
		fmt.Printf("  добавлен %s (%s)\n", acc.ID, acc.Email)
	}
	pause(br)
}

func pluginsMenu(br *bufio.Reader) {
	fmt.Print("\033[H\033[2J")
	list := plugin.List()
	if len(list) == 0 {
		fmt.Println("  нет плагинов")
	} else {
		for _, pl := range list {
			fmt.Printf("  %-18s %s\n", pl.Name(), pl.Description())
		}
	}
	fmt.Println("\n  Плагины живут в plugins/<name>/ со своим config.json")
	pause(br)
}

func autostartMenu(br *bufio.Reader) {
	fmt.Print("\033[H\033[2J")
	fmt.Printf("  Платформа: %s\n", autostart.Detect())
	fmt.Printf("  Статус: %s\n\n", autostart.Status())
	fmt.Println("  1) Установить автозапуск")
	fmt.Println("  2) Удалить автозапуск")
	fmt.Println("  0) Назад")
	fmt.Print("  > ")
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	switch strings.TrimSpace(line) {
	case "1":
		path, err := autostart.Install(autostart.InstallOpts{Port: 8787, Host: "127.0.0.1"})
		if err != nil {
			fmt.Printf("  ошибка: %v\n", err)
		} else {
			fmt.Printf("  установлено: %s\n", path)
		}
		pause(br)
	case "2":
		path, err := autostart.Uninstall()
		if err != nil {
			fmt.Printf("  ошибка: %v\n", err)
		} else {
			fmt.Printf("  удалено: %s\n", path)
		}
		pause(br)
	}
}

func settingsMenu(cfg *config.Config, br *bufio.Reader) {
	fmt.Print("\033[H\033[2J")
	fmt.Printf("  ROTATE_AT=%.2f  DEFAULT_WINDOW=%s  DEFAULT_LIMIT=%d\n", cfg.RotateAt, cfg.DefaultWindow, cfg.DefaultLimit)
	fmt.Printf("  PLUGINS_DIR=%s  MIN_INTERVAL=%s\n", cfg.PluginsDir, cfg.MinInterval)
	fmt.Println("\n  Настройки берутся из ENV / .env — отредактируй файл и перезапусти.")
	fmt.Println("  См. README.md раздел Конфигурация.")
	pause(br)
}

func pause(br *bufio.Reader) {
	fmt.Print("\n  Enter чтобы продолжить...")
	_, _ = br.ReadString('\n')
}

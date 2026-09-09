// Package bundle — перенос notiongate с одного компа на сервер одним движением.
// Собирает всё нужное (БД, .env, конфиги плагинов) в один .tgz, без токенов в логах.
// Гейтик просто стоит рядом.
package bundle

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Options struct {
	Output  string // путь к архиву
	Include []string
}

func DefaultInclude() []string {
	inc := []string{
		".env",
		"docker-compose.yml",
		"plugins",
		"extensions",
		"data",
	}
	// Глобальная БД — всегда включаем, где бы ни был запуск
	if home, err := os.UserHomeDir(); err == nil {
		gdb := filepath.Join(home, ".local", "share", "notiongate", "notiongate.db")
		if _, err := os.Stat(gdb); err == nil {
			inc = append(inc, gdb)
			// WAL тоже
			for _, suf := range []string{"-wal", "-shm"} {
				if _, err := os.Stat(gdb + suf); err == nil {
					inc = append(inc, gdb+suf)
				}
			}
		}
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			gdb2 := filepath.Join(xdg, "notiongate", "notiongate.db")
			if _, err := os.Stat(gdb2); err == nil {
				inc = append(inc, gdb2)
			}
		}
	}
	return inc
}

// Create собирает бандл.
func Create(opts Options) (string, error) {
	if opts.Output == "" {
		opts.Output = fmt.Sprintf("notiongate-bundle-%s.tgz", time.Now().Format("20060102-150405"))
	}
	if len(opts.Include) == 0 {
		opts.Include = DefaultInclude()
	}
	f, err := os.Create(opts.Output)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	for _, pattern := range opts.Include {
		matches, _ := filepath.Glob(pattern)
		if len(matches) == 0 {
			// точный путь без glob
			if _, err := os.Stat(pattern); err == nil {
				matches = []string{pattern}
			} else {
				continue
			}
		}
		for _, p := range matches {
			if err := addPath(tw, p); err != nil {
				return "", fmt.Errorf("bundle %s: %w", p, err)
			}
		}
	}
	// мета
	meta := fmt.Sprintf("created=%s\nversion=1.0.0\n", time.Now().Format(time.RFC3339))
	hdr := &tar.Header{Name: "BUNDLE.txt", Mode: 0o644, Size: int64(len(meta))}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write([]byte(meta))

	return opts.Output, nil
}

func addPath(tw *tar.Writer, p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if fi.IsDir() {
		return filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			// Lstat — не следуем по симлинкам
			if lfi, err := os.Lstat(path); err == nil {
				if lfi.Mode()&os.ModeSymlink != 0 {
					return nil
				}
				info = lfi
			}
			// игнор секретов-примеров уже в .gitignore, но в бандле — берём всё что есть
			// пропускаем логи и временные
			if strings.HasSuffix(path, ".log") || strings.Contains(path, ".git") {
				return nil
			}
			hdr, _ := tar.FileInfoHeader(info, "")
			hdr.Name = path
			// Маскируем режим — не сохраняем suid/sgid/world-writable
			if hdr.Typeflag == tar.TypeDir {
				hdr.Mode = 0o755
			} else {
				m := os.FileMode(hdr.Mode & 0o777)
				m &= ^os.FileMode(0o111)
				if info.Mode()&0o111 != 0 {
					m |= 0o755
				} else {
					m = 0o644
				}
				m &= ^(os.ModeSetuid | os.ModeSetgid)
				hdr.Mode = int64(m)
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			_, _ = io.Copy(tw, f)
			f.Close()
			return nil
		})
	}
	hdr, _ := tar.FileInfoHeader(fi, "")
	hdr.Name = p
	m := os.FileMode(hdr.Mode) &^ (os.ModeSetuid | os.ModeSetgid)
	m = (m & 0o777) | 0o644
	if m&0o111 != 0 {
		m = 0o755
	} else {
		m = 0o644
	}
	hdr.Mode = int64(m &^ (os.ModeSetuid | os.ModeSetgid))
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	_, _ = io.Copy(tw, f)
	f.Close()
	return nil
}

// Extract распаковывает бандл в текущую директорию (с проверкой traversal).
func Extract(archive string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// защита от traversal и sibling write
		clean := filepath.Clean(hdr.Name)
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || clean == ".." {
			continue
		}
		// Абсолютная проверка: распаковка только внутрь cwd
		cwd, _ := os.Getwd()
		absTarget, _ := filepath.Abs(clean)
		absCwd, _ := filepath.Abs(cwd)
		if !strings.HasPrefix(absTarget, absCwd+string(os.PathSeparator)) && absTarget != absCwd && !strings.HasPrefix(clean, "data/") && !strings.HasPrefix(clean, "plugins/") && !strings.HasPrefix(clean, "extensions/") && clean != "BUNDLE.txt" && clean != ".env" && clean != "docker-compose.yml" {
			// Разрешаем только ожидаемые префиксы
			if !strings.HasPrefix(clean, "data") && !strings.HasPrefix(clean, "plugins") && !strings.HasPrefix(clean, "extensions") && clean != "BUNDLE.txt" && clean != ".env" && clean != "docker-compose.yml" {
				continue
			}
		}
		if hdr.FileInfo().IsDir() {
			_ = os.MkdirAll(clean, 0o755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(clean), 0o755)
		// Маска режима
		mode := os.FileMode(0o644)
		if hdr.FileInfo().Mode()&0o111 != 0 {
			mode = 0o755
		}
		mode &= ^(os.ModeSetuid | os.ModeSetgid)
		out, err := os.OpenFile(clean, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			continue
		}
		_, _ = io.CopyN(out, tr, 50*1024*1024) // cap 50MB per file
		out.Close()
	}
	return nil
}

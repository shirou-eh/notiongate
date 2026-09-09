// Package mascot — фирменный персонаж notiongate.
// Гейтик — немой пиксельный привратник. Просто существует.
package mascot

const Name = "Гейтик"
const FullName = "Гейтик — привратник Notion Gate"

// Лор (коротко, для docs)
const Lore = `Гейтик — немой пиксельный привратник между Notion и твоим ADE.
Синий, зубчатый как стена замка, с двумя чёрными глазами.
Не говорит. Просто стоит и держит гейт открытым.`

// Asset — путь к SVG. Используется в README, доках, лендинге.
const AssetSVG = "assets/mascot.svg"
const AssetPNG = "assets/mascot.png" // рендерится из SVG при сборке

// Пиксельный арт для терминала — ровный, симметричный, немой.
// Без ведущих пробелов — чтобы нигде не съезжала голова.

const Art = "" +
	"┌────────────┐\n" +
	"│  ████████  │\n" +
	"│ ███    ███ │\n" +
	"│ █  ██  ██ █│\n" +
	"│ █  ██  ██ █│\n" +
	"│ ██████████ │\n" +
	"│ ██████████ │\n" +
	"└────────────┘"

const ArtSmall = "" +
	"╭─█████─╮\n" +
	"│██ █ ██│\n" +
	"│██ █ ██│\n" +
	"╰───────╯"

const ArtBanner = "" +
	"╭──────────────╮   notiongate\n" +
	"│   ████████   │   Гейтик просто есть.\n" +
	"│  ██████████  │\n" +
	"│  ███ ██ ███  │\n" +
	"│  ██████████  │\n" +
	"╰──────────────╯"

// Состояния — тоже молча, только форма глаз.

const ArtIdle = "" +
	"╭─█████─╮\n" +
	"│ █   █ │\n" +
	"│ █   █ │\n" +
	"╰───────╯"

const ArtActive = "" +
	"╭─█████─╮\n" +
	"│ ███ █ │\n" +
	"│ ███ █ │\n" +
	"╰───────╯"

const ArtSleep = ArtIdle

// Публичные хелперы — только визуал, без болтовни.

func Banner() string { return ArtBanner }

func StatusArt(accounts, active int) string {
	if accounts == 0 {
		return ArtSleep
	}
	if active == 0 {
		return ArtIdle
	}
	return ArtActive
}

package portal

import (
	"math/rand"
	"strings"
	"time"
)

type PortalLogo struct {
	ID       string
	Name     string
	Lines    []string
	MinWidth int
	Weight   int
}

func PortalLogos() []PortalLogo {
	return []PortalLogo{
		{
			ID:       "compact",
			Name:     "Compact",
			Lines:    []string{"LOOM"},
			MinWidth: 4,
			Weight:   1,
		},
		{
			ID:   "arcade",
			Name: "Arcade",
			Lines: []string{
				".-.    .---.  .---.  .-..-.",
				"| |   / / \\ \\/ / \\ \\ | || |",
				"| `--.\\ \\_/ /\\ \\_/ / | `' |",
				"`----' `---'  `---'  `----'",
			},
			MinWidth: 30,
			Weight:   4,
		},
		{
			ID:   "outline",
			Name: "Outline",
			Lines: []string{
				" _       ___    ___   __  __ ",
				"| |     / _ \\  / _ \\ |  \\/  |",
				"| |__  | (_) || (_) || |\\/| |",
				"|____|  \\___/  \\___/ |_|  |_|",
			},
			MinWidth: 31,
			Weight:   4,
		},
		{
			ID:   "slant",
			Name: "Slant",
			Lines: []string{
				" __                                    ",
				"|  \\                                  ",
				"| $$  ______    ______   ______ ____  ",
				"| $$ /      \\  /      \\ |      \\    \\ ",
				"| $$|  $$$$$$\\|  $$$$$$\\| $$$$$$\\$$$$\\",
				"| $$| $$  | $$| $$  | $$| $$ | $$ | $$",
				"| $$| $$__/ $$| $$__/ $$| $$ | $$ | $$",
				"| $$ \\$$    $$ \\$$    $$| $$ | $$ | $$",
				" \\$$  \\$$$$$$   \\$$$$$$  \\$$  \\$$  \\$$",
			},
			MinWidth: 43,
			Weight:   3,
		},
		{
			ID:   "block",
			Name: "Block",
			Lines: []string{
				" ___       ________  ________  _____ ______      ",
				"|\\  \\     |\\   __  \\|\\   __  \\|\\   _ \\  _   \\    ",
				"\\ \\  \\    \\ \\  \\|\\  \\ \\  \\|\\  \\ \\  \\\\\\__\\ \\  \\   ",
				" \\ \\  \\    \\ \\  \\\\\\  \\ \\  \\\\\\  \\ \\  \\\\|__| \\  \\  ",
				"  \\ \\  \\____\\ \\  \\\\\\  \\ \\  \\\\\\  \\ \\  \\    \\ \\  \\ ",
				"   \\ \\_______\\ \\_______\\ \\_______\\ \\__\\    \\ \\__\\",
				"    \\|_______|\\|_______|\\|_______|\\|__|     \\|__|",
			},
			MinWidth: 53,
			Weight:   4,
		},
		{
			ID:   "banner",
			Name: "Banner",
			Lines: []string{
				" :::         ...         ...     .        :   ",
				" ;;;      .;;;;;;;.   .;;;;;;;.  ;;,.    ;;; ",
				" [[[     ,[[     \\[[,,[[     \\[[,[[[[, ,[[[[,",
				" $$'     $$$,     $$$$$$,     $$$$$$$$$$$\"$$$",
				"o88oo,.__\"888,_ _,88P\"888,_ _,88P888 Y88\" 888o",
				"\"\"\"\"YUMMM  \"YMMMMMP\"   \"YMMMMMP\" MMM  M'  \"MMM",
			},
			MinWidth: 54,
			Weight:   5,
		},
		{
			ID:   "wide",
			Name: "Wide",
			Lines: []string{
				"  ,--,                                ____   ",
				",--.'|                              ,'  , `. ",
				"|  | :     ,---.     ,---.       ,-+-,.' _ |",
				":  : '    '   ,'\\   '   ,'\\   ,-+-. ;   , ||",
				"|  ' |   /   /   | /   /   | ,--.'|'   |  ||",
				"'  | |  .   ; ,. :.   ; ,. :|   |  ,', |  |,",
				"|  | :  '   | |: :'   | |: :|   | /  | |--' ",
				"'  : |__'   | .; :'   | .; :|   : |  | ,    ",
				"|  | '.'|   :    ||   :    ||   : |  |/     ",
				";  :    ;\\   \\  /  \\   \\  / |   | |`-'      ",
				"|  ,   /  `----'    `----'  |   ;/          ",
				" ---`-'                     '---'           ",
			},
			MinWidth: 51,
			Weight:   4,
		},
	}
}

func CompactPortalLogo() PortalLogo {
	return PortalLogos()[0]
}

func SelectPortalLogo(width int, rng *rand.Rand) PortalLogo {
	return SelectPortalLogoForViewport(width, 0, rng)
}

func SelectPortalLogoForViewport(width int, height int, rng *rand.Rand) PortalLogo {
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	candidates := []PortalLogo{}
	for _, logo := range PortalLogos() {
		if logo.Fits(width) && logo.FitsHeight(height) {
			candidates = append(candidates, logo)
		}
	}
	if len(candidates) == 0 {
		return CompactPortalLogo()
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	total := 0
	for _, logo := range candidates {
		if logo.Weight <= 0 {
			total++
			continue
		}
		total += logo.Weight
	}
	pick := rng.Intn(total)
	for _, logo := range candidates {
		weight := logo.Weight
		if weight <= 0 {
			weight = 1
		}
		if pick < weight {
			return logo
		}
		pick -= weight
	}
	return candidates[len(candidates)-1]
}

func (l PortalLogo) MaxWidth() int {
	maxWidth := 0
	for _, line := range l.Lines {
		if width := len(line); width > maxWidth {
			maxWidth = width
		}
	}
	return maxWidth
}

func (l PortalLogo) Fits(width int) bool {
	if len(l.Lines) == 0 {
		return false
	}
	if width <= 0 {
		return true
	}
	minWidth := l.MinWidth
	if minWidth <= 0 {
		minWidth = l.MaxWidth()
	}
	return minWidth <= width && l.MaxWidth() <= width
}

func (l PortalLogo) FitsHeight(height int) bool {
	if height <= 0 {
		return true
	}
	const reservedHomeRows = 12
	maxLogoRows := height - reservedHomeRows
	if maxLogoRows < 1 {
		maxLogoRows = 1
	}
	return len(l.Lines) <= maxLogoRows
}

func (l PortalLogo) RenderLines(visibleLines int) []string {
	if visibleLines < 0 || visibleLines > len(l.Lines) {
		visibleLines = len(l.Lines)
	}
	lines := make([]string, 0, visibleLines)
	for _, line := range l.Lines[:visibleLines] {
		lines = append(lines, strings.TrimRight(line, " "))
	}
	return lines
}

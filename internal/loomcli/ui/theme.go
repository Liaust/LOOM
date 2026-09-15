package ui

import (
	"fmt"
	"strings"

	loomerrors "loom.local/loom/internal/errors"
)

const (
	ThemeCoffee  = "coffee"
	ThemeNoColor = "no-color"
)

type Role string

const (
	RoleBackground   Role = "background"
	RoleSurface      Role = "surface"
	RoleSurfaceMuted Role = "surface_muted"
	RoleBorder       Role = "border"
	RoleBorderFocus  Role = "border_focus"
	RoleTextPrimary  Role = "text_primary"
	RoleTextMuted    Role = "text_muted"
	RoleTextDisabled Role = "text_disabled"
	RoleFooter       Role = "footer"
	RoleBrand        Role = "brand"
	RoleAccent       Role = "accent"
	RoleAccentDim    Role = "accent_dim"
	RoleSelection    Role = "selection"
	RoleDanger       Role = "danger"
	RoleWarning      Role = "warning"
	RoleSuccess      Role = "success"
	RoleInfo         Role = "info"
	RoleID           Role = "id"
	RoleTimestamp    Role = "timestamp"
	RoleCode         Role = "code"
	RoleLink         Role = "link"
)

type Theme struct {
	Name  string
	Roles map[Role]string
}

func ResolveTheme(name string) (Theme, error) {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "", ThemeCoffee:
		return CoffeeTheme(), nil
	case ThemeNoColor:
		return NoColorTheme(), nil
	default:
		return Theme{}, loomerrors.New("theme.unknown", "cli", name, fmt.Sprintf("Unknown CLI theme %q.", name))
	}
}

func CoffeeTheme() Theme {
	return Theme{
		Name: ThemeCoffee,
		Roles: map[Role]string{
			RoleBackground:   "#150101",
			RoleSurface:      "#150101",
			RoleSurfaceMuted: "#2A1613",
			RoleBorder:       "#5A3C34",
			RoleBorderFocus:  "#FF9500",
			RoleTextPrimary:  "#E0C5A9",
			RoleTextMuted:    "#A88D76",
			RoleTextDisabled: "#6D5648",
			RoleFooter:       "#5F5F5F",
			RoleBrand:        "#8FBFDA",
			RoleAccent:       "#FF9500",
			RoleAccentDim:    "#A86100",
			RoleSelection:    "#750F0F",
			RoleDanger:       "#750F0F",
			RoleWarning:      "#FF9500",
			RoleSuccess:      "#75B48A",
			RoleInfo:         "#7FA7C7",
			RoleID:           "#B89A80",
			RoleTimestamp:    "#9E816A",
			RoleCode:         "#E0C5A9",
			RoleLink:         "#FFB34D",
		},
	}
}

func NoColorTheme() Theme {
	theme := CoffeeTheme()
	theme.Name = ThemeNoColor
	return theme
}

func (t Theme) Color(role Role) string {
	if t.Roles == nil {
		return ""
	}
	if color := t.Roles[role]; color != "" {
		return color
	}
	return t.Roles[RoleTextPrimary]
}

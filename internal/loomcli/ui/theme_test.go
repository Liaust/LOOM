package ui

import "testing"

func TestCoffeeThemeDefinesBaselinePalette(t *testing.T) {
	theme, err := ResolveTheme(ThemeCoffee)
	if err != nil {
		t.Fatalf("ResolveTheme returned error: %v", err)
	}
	for role, want := range map[Role]string{
		RoleBackground:  "#150101",
		RoleTextPrimary: "#E0C5A9",
		RoleFooter:      "#5F5F5F",
		RoleBrand:       "#8FBFDA",
		RoleAccent:      "#FF9500",
		RoleDanger:      "#750F0F",
	} {
		if got := theme.Color(role); got != want {
			t.Fatalf("role %s = %s, want %s", role, got, want)
		}
	}
}

func TestNoColorStylesRenderIdentity(t *testing.T) {
	mode := Mode{
		Output:    OutputTable,
		Color:     false,
		ThemeName: ThemeCoffee,
		TTY:       TerminalInfo{ColorProfile: ColorTrueColor},
	}
	styles := NewStyles(mode)
	if got := styles.Accent.Render("loom"); got != "loom" {
		t.Fatalf("no-color accent rendered %q", got)
	}
}

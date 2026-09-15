package portal

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"loom.local/loom/internal/search"
)

type databaseSearchLoadedMsg struct {
	Query  string
	Result search.SearchResultSet
	Err    error
}

func databaseSearchCmd(client Client, correlationID, query string) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return databaseSearchLoadedMsg{Query: query, Err: ErrMissingClient}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		envelope, err := client.Search(ctx, correlationID, search.SearchInput{
			Query: strings.TrimSpace(query),
			Limit: 10,
		})
		if err != nil {
			return databaseSearchLoadedMsg{Query: query, Err: err}
		}
		return databaseSearchLoadedMsg{Query: query, Result: envelope.Data}
	}
}

func (m Model) handleDatabaseSearchLoaded(msg databaseSearchLoadedMsg) (tea.Model, tea.Cmd) {
	state := m.currentScreenState()
	data := DatabaseSearchData{
		Query:     msg.Query,
		ResultSet: msg.Result,
		Status:    ScreenLoadLoaded,
		LoadedAt:  time.Now().UTC(),
	}
	if msg.Err != nil {
		data.Status = ScreenLoadFailed
		data.Error = msg.Err.Error()
	}
	state.Data.Database.Search = data
	m.setScreenState(ScreenDatabase, state)
	return m, nil
}

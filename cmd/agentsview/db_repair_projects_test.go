package main

import (
	"bytes"
	"encoding/json/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/export"
)

func TestDBRepairProjectsPreviewAndApply(t *testing.T) {
	testDataDir(t)
	cfg, err := config.LoadReadOnly()
	require.NoError(t, err)
	database, err := db.Open(t.Context(), cfg.DBPath)
	require.NoError(t, err)
	s := db.Session{ID: "old", Agent: "codex", Machine: "local", Project: "pr123", Cwd: "/workspace/pr123"}
	obs := export.WithRecordedGitRemote(export.ProjectIdentityObservation{
		SessionID: s.ID, Project: s.Project, Machine: s.Machine, RootPath: s.Cwd, ObservedAt: time.Now().UTC(),
	}, "https://example.com/team/sample.git")
	require.NoError(t, database.UpsertSessionWithProjectIdentity(t.Context(), s, obs, s.Project))
	require.NoError(t, database.UpsertSession(t.Context(), s))
	require.NoError(t, database.Close())

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--format", "json"}, "pr123"},
		{[]string{"--format", "json", "--apply", "--project", "pr123"}, "sample"},
	} {
		cmd := newDBRepairProjectsCommand()
		cmd.SetArgs(tc.args)
		var output bytes.Buffer
		cmd.SetOut(&output)
		require.NoError(t, cmd.Execute())
		var report db.RepositoryProjectRepair
		require.NoError(t, json.Unmarshal(output.Bytes(), &report))
		assert.Equal(t, 1, report.Sessions)
		database, err := db.OpenReadOnly(t.Context(), cfg.DBPath)
		require.NoError(t, err)
		got, err := database.GetSessionFull(t.Context(), s.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, tc.want, got.Project)
		require.NoError(t, database.Close())
	}
}

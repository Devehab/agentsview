package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/export"
)

func repositorySession(id, project, remote string) (Session, export.ProjectIdentityObservation) {
	s := Session{ID: id, Project: project, Machine: "remote", Agent: "codex", Cwd: "/workspace/" + project}
	obs := export.WithRecordedGitRemote(export.ProjectIdentityObservation{
		SessionID: id, Project: project, Machine: s.Machine, RootPath: s.Cwd,
		ObservedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, remote)
	return s, obs
}

func TestRepositoryProjectAttribution(t *testing.T) {
	d := testDB(t)
	ctx := t.Context()
	// The checkout name is an established alias for this repository.
	main, mainObs := repositorySession("main", "sample", "https://example.com/team/sample-dev.git")
	mainObs.RepositoryPath = "/workspace/sample"
	mainObs.WorktreeRelationship = export.WorktreeMain
	require.NoError(t, d.UpsertSessionWithProjectIdentity(ctx, main, mainObs, "sample"))
	_, err := d.AssignSessionProject(ctx, main.ID, "curated")
	require.NoError(t, err)

	s, obs := repositorySession("worktree", "sample_root_package", "git@example.com:team/sample-dev.git")
	require.NoError(t, d.UpsertSessionWithProjectIdentity(ctx, s, obs, s.Project))
	got, err := d.GetSessionFull(ctx, s.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "sample", got.Project)
	snapshots, err := d.listSessionProjectIdentitySnapshots(ctx, []string{s.ID})
	require.NoError(t, err)
	assert.Equal(t, "sample_root_package", snapshots[s.ID].Project, "keep the producer's project evidence")

	// Reimports must not restore the worktree label.
	result, err := d.WriteSessionBatchContext(ctx, []SessionBatchWrite{{
		Session: s, IdentityObservation: obs, IdentitySnapshotProject: &s.Project,
	}})
	require.NoError(t, err)
	require.Equal(t, 1, result.WrittenSessions)
	got, err = d.GetSessionFull(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, "sample", got.Project)

	_, err = d.AssignSessionProject(ctx, s.ID, "chosen")
	require.NoError(t, err)
	require.NoError(t, d.UpsertSessionWithProjectIdentity(ctx, s, obs, s.Project))
	got, err = d.GetSessionFull(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, "chosen", got.Project)
	_, err = d.RestoreSessionProjectsFromIdentitySnapshots(ctx)
	require.NoError(t, err)
	got, err = d.GetSessionFull(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, "chosen", got.Project)
	cleared, err := d.ClearSessionProjectAssignment(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, "sample", cleared.Project)
}

func TestRepositoryProjectAttributionPreservesFolderRules(t *testing.T) {
	d := testDB(t)
	s, obs := repositorySession("mapped", "pr123", "https://example.com/team/sample.git")
	_, err := d.CreateWorktreeProjectMapping(t.Context(), WorktreeProjectMapping{
		Machine: "remote", PathPrefix: "/workspace", Layout: WorktreeMappingLayoutExplicit,
		Project: "pr123", Enabled: true,
	})
	require.NoError(t, err)
	require.NoError(t, d.UpsertSessionWithProjectIdentity(t.Context(), s, obs, s.Project))
	got, err := d.GetSessionFull(t.Context(), s.ID)
	require.NoError(t, err)
	assert.Equal(t, "pr123", got.Project, "a rule equal to the source label is still explicit")
}

func TestRepositoryProjectAttributionRequiresProducerRemote(t *testing.T) {
	d := testDB(t)
	s, obs := repositorySession("missing", "pr123", "")
	obs.GitRemote = "https://example.com/team/sample.git"
	obs.RemoteResolution = "resolved"
	require.NoError(t, d.UpsertSessionWithProjectIdentity(t.Context(), s, obs, s.Project))
	got, err := d.GetSessionFull(t.Context(), s.ID)
	require.NoError(t, err)
	assert.Equal(t, "pr123", got.Project, "host discovery alone must not relabel historical sessions")
}

func TestRepositoryProjectRepairPreservesContentAndRebuild(t *testing.T) {
	ctx := t.Context()
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	d := testDBAtPath(t, sourcePath, "source")
	t.Cleanup(func() { _ = d.Close() })
	s, obs := repositorySession("old", "pr123", "https://example.com/team/sample.git")
	require.NoError(t, d.UpsertSessionWithProjectIdentity(ctx, s, obs, s.Project))
	// An archive written by the old importer retained the parser label.
	require.NoError(t, d.UpsertSession(ctx, s))
	require.NoError(t, d.InsertMessages(ctx, []Message{{SessionID: s.ID, Ordinal: 0, Role: "user", Content: "Keep this transcript."}}))
	before, err := d.GetAllMessages(ctx, s.ID)
	require.NoError(t, err)
	require.Len(t, before, 1)
	preview, err := d.PreviewRepositoryProjects(ctx, "pr123")
	require.NoError(t, err)
	require.Equal(t, 1, preview.Sessions)
	require.Len(t, preview.Changes, 1)
	assert.Equal(t, "sample", preview.Changes[0].To)
	got, err := d.GetSessionFull(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, "pr123", got.Project, "preview is read-only")
	applied, err := d.RepairRepositoryProjects(ctx, "pr123")
	require.NoError(t, err)
	assert.Equal(t, preview, applied)
	after, err := d.GetAllMessages(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after, "metadata repair preserves content and message IDs")
	preview, err = d.PreviewRepositoryProjects(ctx, "")
	require.NoError(t, err)
	assert.Zero(t, preview.Sessions)
	observations, err := d.ListProjectIdentityObservations(ctx, []string{"pr123"})
	require.NoError(t, err)
	assert.Empty(t, observations, "remove stale effective-project observations")
	require.NoError(t, d.Close())

	rebuilt := testDB(t)
	require.NoError(t, rebuilt.UpsertSessionWithProjectIdentity(ctx, s, obs, s.Project))
	require.NoError(t, rebuilt.CopySessionMetadataFrom(sourcePath))
	_, err = rebuilt.RestoreSessionProjectsFromIdentitySnapshots(ctx)
	require.NoError(t, err)
	got, err = rebuilt.GetSessionFull(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, "sample", got.Project)
	snapshots, err := rebuilt.listSessionProjectIdentitySnapshots(ctx, []string{s.ID})
	require.NoError(t, err)
	assert.Equal(t, "pr123", snapshots[s.ID].Project)
}

func TestRepositoryProjectAmbiguousCheckoutNames(t *testing.T) {
	d := testDB(t)
	ctx := t.Context()
	for _, project := range []string{"alias_one", "alias_two"} {
		s, obs := repositorySession(project, project, "https://example.com/team/sample.git")
		obs.RepositoryPath = s.Cwd
		obs.WorktreeRelationship = export.WorktreeMain
		require.NoError(t, d.UpsertSessionWithProjectIdentity(ctx, s, obs, s.Project))
	}
	s, obs := repositorySession("ambiguous", "pr123", "https://example.com/team/sample.git")
	require.NoError(t, d.UpsertSessionWithProjectIdentity(ctx, s, obs, s.Project))
	preview, err := d.PreviewRepositoryProjects(ctx, "")
	require.NoError(t, err)
	assert.Zero(t, preview.Sessions)
	assert.Equal(t, 1, preview.AmbiguousSessions)
	got, err := d.GetSessionFull(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, "pr123", got.Project)
}

func TestRepositoryProjectPreviewAndRebuildOfOlderArchive(t *testing.T) {
	ctx := t.Context()
	sourcePath := filepath.Join(t.TempDir(), "old.db")
	source := testDBAtPath(t, sourcePath, "old")
	t.Cleanup(func() { _ = source.Close() })
	main, mainObs := repositorySession("main", "sample", "https://example.com/team/sample-dev.git")
	mainObs.RepositoryPath = main.Cwd
	mainObs.WorktreeRelationship = export.WorktreeMain
	require.NoError(t, source.UpsertSessionWithProjectIdentity(ctx, main, mainObs, main.Project))
	s, obs := repositorySession("old", "pr123", "https://example.com/team/sample-dev.git")
	require.NoError(t, source.UpsertSessionWithProjectIdentity(ctx, s, obs, s.Project))
	require.NoError(t, source.UpsertSession(ctx, s))
	// Released archives have source snapshots but no automatic naming table.
	require.NoError(t, source.Update(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DROP TABLE repository_project_names")
		return err
	}))
	require.NoError(t, source.Close())
	reader, err := OpenReadOnly(ctx, sourcePath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })
	preview, err := reader.PreviewRepositoryProjects(ctx, "")
	require.NoError(t, err)
	require.Len(t, preview.Changes, 1)
	assert.Equal(t, "sample", preview.Changes[0].To)
	require.NoError(t, reader.Close())

	rebuilt := testDB(t)
	// The first reparsed file cannot see the original checkout alias yet.
	require.NoError(t, rebuilt.UpsertSessionWithProjectIdentity(ctx, s, obs, s.Project))
	provisional, err := rebuilt.GetSessionFull(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, "sample_dev", provisional.Project)
	require.NoError(t, rebuilt.UpsertSessionWithProjectIdentity(ctx, main, mainObs, main.Project))
	require.NoError(t, rebuilt.CopySessionMetadataFrom(sourcePath))
	_, err = rebuilt.RestoreSessionProjectsFromIdentitySnapshots(ctx)
	require.NoError(t, err)
	got, err := rebuilt.GetSessionFull(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, "sample", got.Project, "complete historical evidence must replace provisional names")
}

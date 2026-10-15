package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The outbox: every memory is kept on the hub first and forwarded to TaskAI here, with retries, so a write
// never fails because TaskAI is slow or down.

// Run forwards pending memories every interval until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	if s.TaskAI == nil {
		return
	}
	for ctx.Err() == nil {
		if n, err := s.SyncOnce(ctx); err != nil && ctx.Err() == nil {
			s.logf("memory: sync: %v", err)
		} else if n > 0 {
			s.logf("memory: %d sent to TaskAI", n)
		}
		select {
		case <-ctx.Done():
		case <-time.After(every):
		}
	}
}

// backoff after the nth failure: 30 s, 1 m, 2 m … up to an hour. It never gives up; the page shows failing ones.
func backoff(n int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < n && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// SyncOnce sends what is due and returns how many went through.
func (s *Service) SyncOnce(ctx context.Context) (int, error) {
	if s.TaskAI == nil {
		return 0, nil
	}
	ms, err := due(ctx, s.db(), s.clock(), 25)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, m := range ms {
		if err := s.send(ctx, m); err != nil {
			if ctx.Err() != nil {
				return sent, ctx.Err()
			}
			n := m.Attempts + 1
			msg := err.Error()
			if len(msg) > 500 {
				msg = msg[:500]
			}
			if err := setRetry(ctx, s.db(), m.ID, n, s.clock().Add(backoff(n)), msg); err != nil {
				return sent, err
			}
			var ae *APIError
			if errors.As(err, &ae) && (ae.Status == 401 || ae.Status == 403) {
				return sent, err // the key is wrong: the rest would fail the same way
			}
			continue
		}
		sent++
	}
	return sent, nil
}

// send writes m's page: made on first send, rewritten when m is superseded.
func (s *Service) send(ctx context.Context, m Memory) error {
	if m.PageID == 0 && m.Status != "active" {
		return setSynced(ctx, s.db(), m.ID, 0) // superseded before it was ever sent: nothing to show there
	}
	pid, err := s.TaskAI.EnsureProject(ctx)
	if err != nil {
		return err
	}
	page := m.PageID
	if page == 0 {
		if page, err = s.TaskAI.CreatePage(ctx, pid, PageTitle(m)); err != nil {
			return err
		}
		if err := setPage(ctx, s.db(), m.ID, page); err != nil {
			return err
		}
	} else if err := s.TaskAI.SetTitle(ctx, page, PageTitle(m)); err != nil {
		return err
	}
	if err := s.TaskAI.SetContent(ctx, page, PageMarkdown(m)); err != nil {
		return err
	}
	if err := setSynced(ctx, s.db(), m.ID, page); err != nil {
		return err
	}
	s.mu.Lock()
	for i := range s.active {
		if s.active[i].ID == m.ID {
			s.active[i].PageID, s.active[i].SyncState = page, "synced"
		}
	}
	s.mu.Unlock()
	return nil
}

// PageTitle: "[gotcha] owner/repo#12: title". Superseded pages say so, so search shows it at a glance.
func PageTitle(m Memory) string {
	var b strings.Builder
	if m.Status != "active" {
		b.WriteString("[superseded] ")
	}
	fmt.Fprintf(&b, "[%s] %s: %s", m.Kind, ScopeLabel(m.Scope), m.Title)
	t := b.String()
	if len(t) > 250 {
		t = t[:250]
	}
	return t
}

// ScopeLabel is a short form of a scope: owner/repo#12, owner/repo PR 7, or the folder.
func ScopeLabel(sc Scope) string {
	switch {
	case sc.Repo != "" && sc.Issue > 0:
		return fmt.Sprintf("%s#%d", sc.Repo, sc.Issue)
	case sc.Repo != "" && sc.PR > 0:
		return fmt.Sprintf("%s PR %d", sc.Repo, sc.PR)
	case sc.Repo != "":
		return sc.Repo
	}
	return sc.Folder
}

// PageMarkdown is the page body. The scope line repeats the repo, issue, files and kind as words, so TaskAI's
// search finds the memory by them; the footer names the hub's id so a page can be traced back.
func PageMarkdown(m Memory) string {
	var b strings.Builder
	if m.Status != "active" {
		fmt.Fprintf(&b, "> Superseded by memory %d. Kept for history; agents no longer see it.\n\n", m.Superseded)
	}
	fmt.Fprintf(&b, "**%s** · %s · confidence %s", m.Kind, scopeWords(m.Scope), m.Confidence)
	if !m.ExpiresAt.IsZero() {
		fmt.Fprintf(&b, " · expires %s", m.ExpiresAt.Format("2006-01-02"))
	}
	b.WriteString("\n\n")
	b.WriteString(m.Body)
	b.WriteString("\n\n---\n\n")
	var by []string
	for _, x := range []string{m.Author.Agent, m.Author.Provider} {
		if x != "" {
			by = append(by, x)
		}
	}
	fmt.Fprintf(&b, "_%s", m.CreatedAt.UTC().Format("2006-01-02"))
	if len(by) > 0 {
		fmt.Fprintf(&b, " · by %s", strings.Join(by, " / "))
	}
	if m.Author.Host != "" {
		fmt.Fprintf(&b, " on %s", m.Author.Host)
	}
	if m.Author.Session != "" {
		fmt.Fprintf(&b, " · session `%s`", m.Author.Session)
	}
	if m.Supersedes > 0 {
		fmt.Fprintf(&b, " · replaces memory %d", m.Supersedes)
	}
	fmt.Fprintf(&b, " · llm-brain memory %d_\n", m.ID)
	return b.String()
}

func scopeWords(sc Scope) string {
	var p []string
	if sc.Repo != "" {
		p = append(p, "repo `"+sc.Repo+"`")
	}
	if sc.Issue > 0 {
		p = append(p, fmt.Sprintf("issue #%d", sc.Issue))
	}
	if sc.PR > 0 {
		p = append(p, fmt.Sprintf("PR #%d", sc.PR))
	}
	if sc.Folder != "" {
		p = append(p, "folder `"+sc.Folder+"`")
	}
	for _, f := range sc.Files {
		p = append(p, "`"+f+"`")
	}
	return strings.Join(p, " · ")
}

// Label is ScopeLabel, for templates.
func (sc Scope) Label() string { return ScopeLabel(sc) }

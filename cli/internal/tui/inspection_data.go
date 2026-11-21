package tui

import (
	"fmt"
	"strings"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/charmbracelet/x/ansi"
)

func (m *Model) archiveInspection() (content, detail, identity string) {
	b := m.browser
	m.title = "Repositories"
	m.subtitle = "Choose a repository · listing archives may contact it and open credentials"
	switch m.page {
	case archivesPage:
		names := m.repositoryNames()
		var items []inspectionItem
		for _, name := range names {
			items = append(items, inspectionItem{config.DisplayText(name), mutedStyle.Render("Configured repository")})
		}
		content = m.itemList(items, b.repository, "No repositories configured.")
		if b.repository < len(names) {
			identity = names[b.repository]
			detail = m.repositoryDetail(identity)
		}
	case archiveListPage, archiveDetailPage, fileListPage:
		m.title = "Archives / " + config.DisplayText(b.name)
		if m.page == fileListPage {
			m.title = "Files / " + displayMetadata(string(b.archive.Name), 512)
		}
		var banner, guidance string
		status := "Complete."
		switch {
		case b.loading:
			banner = heading("Scanning…")
			guidance = "Waiting for results."
			status = "Scanning… results are partial. Esc cancels."
		case b.outcome == borg.Warning:
			banner = state("Borg warnings")
			guidance = "Use the archive CLI for diagnostics."
			status = "Borg warnings; results may be incomplete."
		case b.outcome == borg.Failure:
			banner = state("Scan failed")
			guidance = "Check repository access with the archive CLI."
			status = "Failed; results are partial."
		case b.outcome == borg.Interrupted:
			banner = state("Scan cancelled")
			guidance = "No results were retained."
			status = "Cancelled; results are partial."
		}
		m.notice = status
		stats := fmt.Sprintf("%d scanned · %d matched · %d retained (limit 1,000 / 8 MiB)", b.scanned, b.matched, len(b.rows))
		m.subtitle = stats
		if b.truncated {
			m.subtitle = state("TRUNCATED") + " · " + stats
		}
		if m.page == fileListPage && b.query != "" {
			m.title += " / Search: " + displayMetadata(b.query, maxQueryBytes)
		}
		if b.editing {
			input := config.DisplayText(b.input)
			const prompt = "Search> "
			// Reserve the cursor and one cell for a wide-character boundary.
			available := max(1, m.width-2-ansi.StringWidth(prompt)-2)
			if width := ansi.StringWidth(input); width > available {
				input = "…" + ansi.Cut(input, width-available+1, width)
			}
			m.notice = prompt + input + "_"
		}
		var items []inspectionItem
		for _, row := range b.rows {
			if m.page == fileListPage {
				items = append(items, inspectionItem{displayMetadata(string(row.file.Path), 512),
					mutedStyle.Render(fmt.Sprintf("%s · %d B", displayMetadata(row.file.Type, 16), row.file.Size))})
			} else {
				items = append(items, inspectionItem{displayMetadata(string(row.archive.Name), 512), mutedStyle.Render(displayMetadata(row.archive.Start, 40))})
			}
		}
		content = m.itemList(items, b.cursor, "No retained results.")
		if b.cursor < len(b.rows) {
			identity = fmt.Sprint(b.cursor)
			if m.page == fileListPage {
				detail = m.fileDetail()
			} else {
				a := b.rows[b.cursor].archive
				detail = section("Archive", displayMetadata(string(a.Name), 16384)) + "\n\n" +
					section("Stored metadata", field("Started", displayMetadata(a.Start, 128))+"\n\n"+field("ID", displayMetadata(a.ID, 512))) +
					"\n\n" + mutedStyle.Render("Names/paths are lossy display text,\nNOT exact restore selectors.\n\nEnter browses files; no file contents are read.")
			}
		}
		if m.page == fileListPage && !b.editing {
			m.notice += " Paths are lossy; NOT exact restore selectors."
		}
		if m.page == archiveDetailPage {
			m.title, m.subtitle = "Archive metadata", "Repository: "+config.DisplayText(b.name)
			content, detail = detail, ""
		} else if banner != "" {
			switch {
			case len(items) == 0:
				content = banner
				if m.split() {
					detail = guidance
				} else {
					content += "\n" + guidance
				}
			case m.body.Height() > 1:
				if b.outcome != borg.Warning {
					banner += mutedStyle.Render(" · partial")
				}
				content = clipped(banner, m.body.Width()) + "\n" + content
			default:
				// Keep the selected row visible in a one-line list.
				m.subtitle = banner + " · " + m.subtitle
			}
		}
	case filePage:
		m.title, m.subtitle = "File metadata", "Repository: "+config.DisplayText(b.name)+" / Archive: "+displayMetadata(string(b.archive.Name), 512)
		content = m.fileDetail()
	}
	return
}

func (m Model) fileDetail() string {
	b := m.browser
	if b.cursor >= len(b.rows) {
		return "No file selected."
	}
	f := b.rows[b.cursor].file
	return section("Path (lossy display)", displayMetadata(string(f.Path), 16384)) + "\n\n" +
		section("Stored metadata", field("Type", displayMetadata(f.Type, 64))+"\n\n"+
			field("Mode", displayMetadata(f.Mode, 128))+"\n\n"+
			field("Size", fmt.Sprintf("%d bytes", f.Size))+"\n\n"+
			field("Modified", displayMetadata(f.Mtime, 128))) + "\n\n" +
		section("Ownership", field("UID", fmt.Sprint(f.UID))+"\n\n"+field("GID", fmt.Sprint(f.GID))) + "\n\n" +
		section("Link / chunk status", field("Link target (lossy display)", displayMetadata(string(f.LinkTarget), 16384))+"\n\n"+
			field("Healthy marker", fmt.Sprint(f.Healthy))) + "\n\n" +
		mutedStyle.Render("NOT an exact restore selector.\nStored metadata only; no file contents or integrity verification.\nThe healthy marker is Borg's stored replacement-chunk status,\nnot a fresh check.")
}

func (m *Model) historyInspection() (content, detail, identity string) {
	b := m.history
	m.title, m.subtitle = "History", fmt.Sprintf("%d retained · newest last event first · no automatic refresh", len(b.runs))
	m.notice = "Incomplete ≠ active · recorded outcomes are separate from momentary execution evidence"
	if !b.at.IsZero() {
		m.subtitle = "Snapshot inspected: " + recordedTime(&b.at) + " · g refresh"
	}
	if b.loading {
		m.notice = "Reading history…"
	} else if b.status != "" {
		m.notice = b.status
	}
	var items []inspectionItem
	for _, run := range b.runs {
		s := run.Summary
		name := s.Workflow
		if name == "" {
			name = s.Operation
		}
		if name == "" {
			name = s.RunID
		}
		outcome := recordedOutcome(s)
		flags := state(outcome)
		if s.Incomplete {
			flags += " · " + state("incomplete")
		}
		if s.Partial {
			flags += " · " + state("partial")
		}
		if s.Preview {
			flags += " · preview"
		}
		items = append(items, inspectionItem{config.DisplayText(name), flags + mutedStyle.Render(" · "+recordedTime(&s.LastObserved))})
	}
	empty := "No recorded runs."
	if b.loading || b.status != "" {
		empty = "No history snapshot available."
	}
	content = m.itemList(items, b.cursor, empty)
	if b.cursor < len(b.runs) {
		identity = b.runs[b.cursor].Summary.RunID
		detail = m.runDetail()
	}
	if m.page == runPage {
		m.title = "Run summary"
		content, detail = detail, ""
	}
	return
}

func (m Model) runDetail() string {
	run := m.history.runs[m.history.cursor]
	s := run.Summary
	unknown := func(value string) string {
		if value == "" {
			return "unknown"
		}
		return config.DisplayText(value)
	}
	groups := []string{
		section("Run summary", field("Recorded outcome", state(recordedOutcome(s)))+"\n"+
			field("Workflow", config.DisplayText(s.Workflow))+"\n"+
			field("Operation", config.DisplayText(s.Operation))+"\n"+
			field("Source", config.DisplayText(string(s.Source)))+"\n"+
			field("Preview", fmt.Sprint(s.Preview))+"\n"+
			field("Reason", config.DisplayText(string(s.Reason)))+"\n"+
			field("Incomplete", fmt.Sprint(s.Incomplete))+"\n"+field("Partial", fmt.Sprint(s.Partial))),
		section("Timing", field("Started", recordedTime(s.StartedAt))+"\n"+field("Finished", recordedTime(s.FinishedAt))+"\n"+
			field("Due", recordedTime(&s.ScheduledAt))+"\n"+field("First event", recordedTime(&s.FirstObserved))+"\n"+
			field("Last event", recordedTime(&s.LastObserved))),
		section("Recorded terminal task counts", fmt.Sprintf("Success: %d · warning: %d · failure: %d\nInterrupted: %d · skipped: %d",
			s.Tasks.Success, s.Tasks.Warning, s.Tasks.Failure, s.Tasks.Interrupted, s.Tasks.Skipped)+
			"\n\n"+mutedStyle.Render("Per-stage/task details are not retained;\npartial counts may be incomplete.")),
		section("Momentary evidence", field("Owner", unknown(string(run.Owner)))+"\n"+field("Lock", unknown(string(run.Lock)))+
			"\n\n"+mutedStyle.Render("Owner present is not Borg liveness;\ngone does not mean Borg exited.\nLock held may belong to another run;\nunknown does not mean free.\nIncomplete means no terminal event, not active.")),
		section("Identity", field("Run", config.DisplayText(s.RunID))+"\n"+field("Last sequence", fmt.Sprint(s.Sequence))+"\n"+
			field("Configuration identity", unknown(s.ConfigID))+"\n\n"+
			mutedStyle.Render("Path identity only, not a content hash.\nConfiguration contents are not retained;\ndifferences from TUI/agent snapshots are unknown.")),
	}
	return strings.Join(groups, "\n\n")
}

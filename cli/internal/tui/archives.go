package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/Besi97/cairn-backup/cli/internal/archive"
	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/config"
)

const (
	maxBrowseRows  = 1000
	maxBrowseBytes = 8 << 20
	maxQueryBytes  = 1024
)

type metadata struct {
	archive archive.Archive
	file    archive.File
}

func (r metadata) bytes() int {
	a, f := r.archive, r.file
	// Reserve space for row storage and slice growth as well as string payloads.
	return 512 + len(a.Name) + len(a.ID) + len(a.Start) + len(f.Path) +
		len(f.Type) + len(f.Mode) + len(f.Mtime) + len(f.LinkTarget)
}

type scanUpdate struct {
	job              *scanJob
	rows             []metadata
	scanned, matched uint64
	truncated, done  bool
	result           borg.Result
	err              error
}

type scanJob struct {
	cancel  context.CancelFunc
	done    chan struct{}
	updates chan scanUpdate
}

type metadataSource func(context.Context, *config.Config, string, string, func(metadata) error) (borg.Result, error)

type archiveBrowser struct {
	ctx        context.Context
	job        *scanJob
	source     metadataSource
	repository int
	from       page
	name       string
	archive    archive.Archive
	rows       []metadata
	cursor     int
	scanned    uint64
	matched    uint64
	truncated  bool
	loading    bool
	outcome    string
	query      string
	input      string
	editing    bool
}

func newArchiveBrowser() *archiveBrowser {
	return &archiveBrowser{ctx: context.Background(), source: readMetadata}
}

func readMetadata(ctx context.Context, selected *config.Config, name, archiveName string, emit func(metadata) error) (borg.Result, error) {
	repository := selected.Repositories[name]
	options := archive.Options{
		Repository: repository.Location, PassphraseFile: repository.PassphraseFile,
		IdentityFile: repository.IdentityFile,
	}
	var request borg.Request
	var err error
	if archiveName == "" {
		request, err = archive.PrepareList(options, selected.Directory)
	} else {
		request, err = archive.PrepareInspect(options, selected.Directory, archiveName)
	}
	if err != nil {
		return borg.Result{Outcome: borg.Failure}, err
	}
	request.Access.Passphrase = repository.Passphrase
	// Diagnostics can contain credentials, including secrets printed by passcommands.
	// Keep them off the terminal rather than attempting to redact arbitrary output.
	if archiveName == "" {
		return archive.List(ctx, borg.Runner{}, request, func(a archive.Archive) error {
			return emit(metadata{archive: a})
		})
	}
	return archive.Inspect(ctx, borg.Runner{}, request, func(f archive.File) error {
		return emit(metadata{file: f})
	})
}

func (b *archiveBrowser) stop() {
	if b.job != nil {
		b.job.cancel()
	}
	b.loading = false
	b.editing = false
}

func (b *archiveBrowser) close() {
	b.stop()
	if b.job != nil {
		<-b.job.done
	}
}

func (b *archiveBrowser) start(selected *config.Config, files bool) tea.Cmd {
	previous := b.job
	b.stop()
	ctx, cancel := context.WithCancel(b.ctx)
	job := &scanJob{cancel: cancel, done: make(chan struct{}), updates: make(chan scanUpdate, 1)}
	b.job = job
	b.rows, b.cursor, b.scanned, b.matched = nil, 0, 0, 0
	b.loading, b.truncated, b.outcome = true, false, ""
	name, query, source := b.name, b.query, b.source
	archiveName := ""
	if files {
		// This lossy name is used only for metadata browsing, never restoration.
		archiveName = string(b.archive.Name)
	}
	go func() {
		defer close(job.done)
		defer cancel()
		if previous != nil {
			<-previous.done
			previous = nil
		}
		if ctx.Err() != nil {
			return
		}
		update := scanUpdate{job: job}
		retained, bytes := 0, 0
		var last time.Time
		publish := func(final bool) error {
			update.done = final
			if final {
				select {
				case job.updates <- update:
				case <-ctx.Done():
					return ctx.Err()
				}
			} else {
				select {
				case job.updates <- update:
				default:
					return nil
				}
			}
			update.rows = nil
			last = time.Now()
			return nil
		}
		result, err := source(ctx, selected, name, archiveName, func(row metadata) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			update.scanned++
			if !files || strings.Contains(string(row.file.Path), query) {
				update.matched++
				size := row.bytes()
				if retained < maxBrowseRows && size <= maxBrowseBytes-bytes {
					update.rows = append(update.rows, row)
					retained++
					bytes += size
				} else {
					update.truncated = true
				}
			}
			if time.Since(last) >= 100*time.Millisecond {
				return publish(false)
			}
			return nil
		})
		update.result, update.err = result, err
		_ = publish(true)
	}()
	return awaitScan(job)
}

func awaitScan(job *scanJob) tea.Cmd {
	return func() tea.Msg {
		select {
		case update := <-job.updates:
			return update
		case <-job.done:
			select {
			case update := <-job.updates:
				return update
			default:
			}
			return nil
		}
	}
}

func (m Model) repositoryNames() []string {
	names := make([]string, 0, len(m.selected.Repositories))
	for name := range m.selected.Repositories {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (m Model) browserPage() bool {
	return m.page == archivesPage || m.page == archiveListPage || m.page == fileListPage || m.page == filePage
}

func (m Model) startBrowse(files bool) (tea.Model, tea.Cmd) {
	m.body.GotoTop()
	cmd := m.browser.start(m.selected, files)
	m.refresh()
	return m, cmd
}

func (m Model) editSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	b := m.browser
	switch msg.Code {
	case tea.KeyEscape:
		b.editing, b.input = false, ""
	case tea.KeyEnter:
		b.query, b.input = b.input, ""
		return m.startBrowse(true)
	case tea.KeyBackspace:
		if b.input != "" {
			_, size := utf8.DecodeLastRuneInString(b.input)
			b.input = b.input[:len(b.input)-size]
		}
	default:
		if msg.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper) == 0 {
			value := msg.Text
			if value == "" && unicode.IsPrint(msg.Code) {
				value = string(msg.Code)
			}
			for _, r := range value {
				if unicode.IsPrint(r) && len(b.input)+utf8.RuneLen(r) <= maxQueryBytes {
					b.input += string(r)
				}
			}
		}
	}
	m.refresh()
	return m, nil
}

func (m Model) updateScan(update scanUpdate) (tea.Model, tea.Cmd) {
	b := m.browser
	if update.job != b.job || !b.loading {
		return m, nil
	}
	b.rows = append(b.rows, update.rows...)
	b.scanned, b.matched, b.truncated = update.scanned, update.matched, update.truncated
	if update.done {
		b.loading = false
		switch {
		case update.result.Outcome == borg.Interrupted:
			b.outcome = "Cancelled; results are partial."
		case update.err != nil || update.result.Outcome == borg.Failure:
			b.outcome = "Failed; results are partial. Check repository access with the archive CLI."
		case update.result.Outcome == borg.Warning:
			b.outcome = "Completed with Borg warnings; results may be incomplete. Use the archive CLI for diagnostics."
		default:
			b.outcome = "Complete."
		}
	}
	m.refresh()
	if m.page == archiveListPage || m.page == fileListPage {
		m.revealSelection()
	}
	if update.done {
		return m, nil
	}
	return m, awaitScan(b.job)
}

func displayMetadata(value string, limit int) string {
	if len(value) > limit {
		end := limit
		for end > 0 && !utf8.RuneStart(value[end]) {
			end--
		}
		return config.DisplayText(value[:end]) + "… [display shortened]"
	}
	return config.DisplayText(value)
}

func (m Model) browserText() string {
	b := m.browser
	var text strings.Builder
	switch m.page {
	case archivesPage:
		text.WriteString("Repositories\n\n↑/↓ select · enter list archives · esc home\n\n")
		names := m.repositoryNames()
		if len(names) == 0 {
			text.WriteString("No repositories configured.")
		}
		for i, name := range names {
			marker := "  "
			if i == b.repository {
				marker = "> "
			}
			fmt.Fprintf(&text, "%s%s\n", marker, config.DisplayText(name))
		}
	case archiveListPage, fileListPage:
		fmt.Fprintf(&text, "Repository: %s\n", config.DisplayText(b.name))
		if m.page == fileListPage {
			fmt.Fprintf(&text, "Archive: %s\nSearch (literal, case-sensitive path): %s\n",
				displayMetadata(string(b.archive.Name), 512), displayMetadata(b.query, maxQueryBytes))
		}
		if b.loading {
			text.WriteString("Scanning… results are partial. Esc cancels and returns.\n")
		} else {
			text.WriteString(b.outcome + "\n")
		}
		fmt.Fprintf(&text, "%d scanned · %d matched · %d retained (limit 1,000 / 8 MiB)\n",
			b.scanned, b.matched, len(b.rows))
		if b.truncated {
			text.WriteString("TRUNCATED: some matching metadata is not retained; scan still covers the whole listing.\n")
		}
		if m.page == fileListPage {
			text.WriteString("↑/↓ select · enter details · / search whole archive · esc archives\n")
			if b.editing {
				fmt.Fprintf(&text, "Search> %s_\nEnter scans from start; Esc discards edit.\n", config.DisplayText(b.input))
			}
		} else {
			text.WriteString("↑/↓ select · enter browse files · esc back\n")
		}
		text.WriteString("Names/paths are lossy display text, NOT exact restore selectors.\n\n")
		if len(b.rows) == 0 {
			text.WriteString("No retained results.")
		}
		start := max(0, b.cursor-max(1, m.height-14)/2)
		end := min(len(b.rows), start+max(1, m.height-14))
		if start > 0 {
			fmt.Fprintf(&text, "… %d earlier retained rows\n", start)
		}
		for i := start; i < end; i++ {
			row, marker := b.rows[i], "  "
			if i == b.cursor {
				marker = "> "
			}
			if m.page == fileListPage {
				fmt.Fprintf(&text, "%s%s  %d B  %s\n", marker,
					displayMetadata(row.file.Type, 16), row.file.Size,
					displayMetadata(string(row.file.Path), max(24, m.width-30)))
			} else {
				fmt.Fprintf(&text, "%s%s  %s\n", marker, displayMetadata(row.archive.Start, 40),
					displayMetadata(string(row.archive.Name), max(24, m.width-45)))
			}
		}
		if end < len(b.rows) {
			fmt.Fprintf(&text, "… %d later retained rows", len(b.rows)-end)
		}
	case filePage:
		if b.cursor >= len(b.rows) {
			break
		}
		f := b.rows[b.cursor].file
		fmt.Fprintf(&text, "File metadata · esc returns to results\n\n"+
			"Path (lossy display, NOT an exact restore selector): %s\n"+
			"Type: %s\nMode: %s\nSize: %d bytes\nModified: %s\nUID: %d\nGID: %d\n"+
			"Link target (lossy display): %s\nHealthy marker: %t\n\n"+
			"Stored metadata only; no file contents or integrity verification.\n"+
			"The healthy marker is Borg's stored replacement-chunk status, not a fresh check.",
			displayMetadata(string(f.Path), 16384), displayMetadata(f.Type, 64),
			displayMetadata(f.Mode, 128), f.Size, displayMetadata(f.Mtime, 128),
			f.UID, f.GID, displayMetadata(string(f.LinkTarget), 16384), f.Healthy)
	}
	return text.String()
}

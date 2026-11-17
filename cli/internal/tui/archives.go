package tui

import (
	"context"
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

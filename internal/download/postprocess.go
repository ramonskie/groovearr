package download

import (
	"context"
	"log"
	"sync"

	"github.com/ramonskie/groovearr/internal/domain"
)

// PostDownloadHook processes a completed download and returns the new file path
// (or the original path if no changes were made).
type PostDownloadHook func(ctx context.Context, record domain.DownloadRecord) (string, error)

// PostProcessor runs post-download hooks on completed downloads.
// It is source-agnostic — any download plugin benefits from the same pipeline.
type PostProcessor struct {
	hooks     []PostDownloadHook
	processed map[string]bool // download IDs already processed
	mu        sync.Mutex
}

// NewPostProcessor creates a post-download processor with the given hooks.
func NewPostProcessor(hooks ...PostDownloadHook) *PostProcessor {
	return &PostProcessor{
		hooks:     hooks,
		processed: make(map[string]bool),
	}
}

// ProcessDownloads runs all registered hooks on newly completed downloads.
// It mutates the records slice in-place with updated file paths.
func (p *PostProcessor) ProcessDownloads(ctx context.Context, records []domain.DownloadRecord) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := range records {
		r := &records[i]

		// Only process terminal successful downloads with a file path set.
		if r.State != domain.DownloadSucceeded || r.FilePath == "" {
			continue
		}
		// Skip already processed.
		if p.processed[r.ID] {
			continue
		}

		failed := false
		for _, hook := range p.hooks {
			newPath, err := hook(ctx, *r)
			if err != nil {
				log.Printf("postprocess: hook failed for %s (%s): %v", r.ID, r.Filename, err)
				failed = true
				break
			}
			if newPath != "" && newPath != r.FilePath {
				r.FilePath = newPath
			}
		}
		if failed {
			continue // don't mark as processed — retry on next poll
		}

		p.processed[r.ID] = true
	}
}

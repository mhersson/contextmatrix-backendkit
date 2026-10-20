package webhookcore

import (
	"context"
	"strings"
)

// ImageRevisionLabel is the OCI label worker image builds stamp with their commit.
const ImageRevisionLabel = "org.opencontainers.image.revision"

// ImageLabeler reads one image's labels from the node's local image store. The
// serve layer supplies a Docker-backed implementation; tests supply a fake.
type ImageLabeler interface {
	ImageLabels(ctx context.Context, ref string) (map[string]string, error)
}

// ImageRevisionHook returns a CoreConfig.WorkerImageRevision hook that reads
// ImageRevisionLabel from ref, or nil when labeler is nil or ref is empty.
func ImageRevisionHook(labeler ImageLabeler, ref string) func(ctx context.Context) string {
	if labeler == nil || ref == "" {
		return nil
	}

	// No logging: CM probes /health every few seconds, so a missing image would
	// log on every probe.
	return func(ctx context.Context) string {
		labels, err := labeler.ImageLabels(ctx, ref)
		if err != nil {
			return ""
		}

		return strings.TrimSpace(labels[ImageRevisionLabel])
	}
}

package webhookcore

import (
	"net/http"

	protocol "github.com/mhersson/contextmatrix-protocol"
)

func (c *Core) HandleImages(w http.ResponseWriter, r *http.Request) {
	if c.images == nil {
		WriteError(w, http.StatusInternalServerError, protocol.CodeInternal, "image lister not wired")

		return
	}

	summaries, err := c.images.ListImages(r.Context())
	if err != nil {
		c.logger.Error("image list failed", "error", err)
		WriteError(w, http.StatusBadGateway, protocol.CodeUpstreamFailure, "image list failed")

		return
	}

	items := make([]protocol.ImageListItem, 0, len(summaries))

	for _, sum := range summaries {
		tags := matchingTags(sum.Tags, c.imageListFilters)
		if len(tags) == 0 {
			continue
		}

		items = append(items, protocol.ImageListItem{
			Tags:    tags,
			Digests: sum.Digests,
			Created: sum.CreatedAt,
			Size:    sum.SizeBytes,
		})
	}

	WriteJSON(w, http.StatusOK, protocol.ListImagesResponse{OK: true, Images: items})
}

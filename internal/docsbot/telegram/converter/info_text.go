package converter

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/convert"
)

// plainInfo is the whole list of fields as plain text, for the file that goes
// with a cut message.
func plainInfo(title string, info convert.FileInfo) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n%s · %s · %s\n", titleText(textInfoTitle),
		cleanText(title), typeName(info.Type), humanSize(int64(info.Size)))
	for _, g := range info.Groups {
		name, _ := groupMeta(g.Group)
		fmt.Fprintf(&sb, "\n%s\n", titleText(name))
		for _, f := range g.Fields {
			s := showField(g.Group, f)
			if s.text == "" {
				continue
			}
			label := s.label
			if label == "" {
				label = s.key
			}
			fmt.Fprintf(&sb, "%s: %s", label, s.text)
			if s.zone != "" {
				fmt.Fprintf(&sb, " (%s)", s.zone)
			}
			sb.WriteString("\n")
			if s.link != "" {
				fmt.Fprintf(&sb, textInfoMapPlain+"\n", s.link)
			}
		}
	}
	if info.Dropped > 0 {
		fmt.Fprintf(&sb, "\n"+textInfoMore+"\n", info.Dropped, plural(info.Dropped, pluralFieldOne, pluralFieldFew, pluralFieldMany))
	}
	return sb.String()
}

// metadataFileName is "metadata-<name>.txt" for a document title.
func metadataFileName(title string) string {
	base := docsbot.DerivedTitle(title, "")
	if base == "" {
		base = nameFallbackDocument
	}
	return fmt.Sprintf(nameMetadataFile, strings.NewReplacer("/", "_", "\\", "_").Replace(base))
}

// sendFileTimeout bounds the upload of the field list.
const sendFileTimeout = 30 * time.Second

// sendFieldList sends the whole list of fields as a text file. The list is a
// courtesy to the message above, so a failure is logged and nothing more.
func (c *Converter) sendFieldList(ctx context.Context, b *bot.Bot, chatID int64, title string, info convert.FileInfo) {
	ctx, cancel := context.WithTimeout(ctx, sendFileTimeout)
	defer cancel()
	_, err := b.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID: chatID,
		Document: &models.InputFileUpload{
			Filename: metadataFileName(title),
			Data:     bytes.NewReader([]byte(plainInfo(title, info))),
		},
	})
	if err != nil {
		c.log.ErrorContext(ctx, "send field list", "err", err)
	}
}

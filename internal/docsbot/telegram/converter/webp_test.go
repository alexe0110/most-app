package converter_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram/converter"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// tinyWebP is a 1x1 lossless WEBP.
func tinyWebP(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	require.NoError(t, err)
	return data
}

// padWebP grows a WebP to at least size bytes with a trailing JUNK chunk, which
// decoders skip.
func padWebP(data []byte, size int) []byte {
	pad := size - len(data) - 8
	out := append(bytes.Clone(data), []byte("JUNK")...)
	out = binary.LittleEndian.AppendUint32(out, uint32(pad))
	out = append(out, make([]byte, pad)...)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8)) // RIFF size
	return out
}

func namedDocumentUpdate(fileID, mime, name string) *models.Update {
	return &models.Update{Message: &models.Message{
		ID: 1, From: &models.User{ID: testUser}, Chat: models.Chat{ID: testChat, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: fileID, FileUniqueID: "u-" + fileID, MimeType: mime, FileName: name},
	}}
}

func TestConvertWebP_compressInCommand(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	h.serve("webp-in", padWebP(tinyWebP(t), bigPhotoSize))
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, namedDocumentUpdate("webp-in", "image/webp", "a.webp"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limit1"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:fmtjpeg"))

	require.Len(t, h.docs, 1)
	_, format, err := image.Decode(bytes.NewReader(h.docs[0].data))
	require.NoError(t, err)
	require.Equal(t, "jpeg", format)
}

func TestConvertWebP_pdfInCommand(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	h.serve("webp-in", tinyWebP(t))
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:pdf"))
	h.bot.ProcessUpdate(ctx, namedDocumentUpdate("webp-in", "image/webp", "a.webp"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

	require.Len(t, h.docs, 1)
	require.True(t, strings.HasPrefix(string(h.docs[0].data), "%PDF"))
	require.Contains(t, string(h.docs[0].data), "/Count 1")
}

func TestConvertWebP_cardToJPG(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	h.serve("webp-src", tinyWebP(t))
	src := h.repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, MIME: "image/webp",
		Category: docsbot.CatOther, Title: "Скан.webp", TgFileID: "webp-src", TgFileUniqueID: "webp-uniq",
	})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:doc:"+string(src.ID)))
	require.Contains(t, h.lastMarkup(), "conv:q:"+string(src.ID)+":jpg")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":jpg"))

	require.Len(t, h.docs, 1)
	require.Equal(t, "Скан-JPG.jpg", h.docs[0].filename)
	_, format, err := image.Decode(bytes.NewReader(h.docs[0].data))
	require.NoError(t, err)
	require.Equal(t, "jpeg", format)

	tok := resultToken.FindStringSubmatch(h.docMarkups[0])
	require.NotNil(t, tok)
	require.Contains(t, h.docMarkups[0], converter.BtnReplaceOriginal, "the source is saved")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rp:"+tok[1]))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok[1]))
	require.Len(t, h.repo.Docs, 1)
	require.Equal(t, "res-uniq-0", h.repo.Docs[src.ID].TgFileUniqueID)
}

func TestConvertWebP_pendingCardOffersToSaveJPG(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	h.serve("webp-src", tinyWebP(t))
	src := h.repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusPending, Kind: docsbot.KindDocument, MIME: "image/webp",
		Title: "Скан.webp", TgFileID: "webp-src", TgFileUniqueID: "webp-uniq",
	})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":jpg"))

	require.Len(t, h.docs, 1)
	require.Contains(t, h.docMarkups[0], converter.BtnSaveJPG)
	require.NotContains(t, h.docMarkups[0], "Заменить")
}

func TestConvertWebP_jpgButtonOnlyForWebP(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved) // a photo
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:doc:"+string(src.ID)))
	require.NotContains(t, h.lastMarkup(), ":jpg")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":jpg"))
	require.Empty(t, h.docs)
}

func TestConvertHEIC_refusedInCommand(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, mime, file string }{
		{"heic mime", "image/heic", "a.heic"},
		{"heif mime", "image/heif", ""},
		{"avif mime", "image/avif", "a.avif"},
		{"heic by extension", "application/octet-stream", "IMG_1.HEIC"},
		{"avif by extension, no mime", "", "x.avif"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

			h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
			h.bot.ProcessUpdate(ctx, namedDocumentUpdate("x", tt.mime, tt.file))

			require.Empty(t, h.docs)
			require.Equal(t, converter.TextConvertFormatNotSupported, h.lastText())
		})
	}
}

func TestConvertHEIC_callbacksAreRefused(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	heic := h.repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, MIME: "image/heic",
		Category: docsbot.CatOther, Title: "IMG.heic", TgFileID: "x", TgFileUniqueID: "heic-uniq",
	})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:doc:"+string(heic.ID)))
	require.Contains(t, h.lastText(), "не поддерживается")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(heic.ID)+":c1"))
	require.Contains(t, h.lastText(), "не поддерживается")
	require.Empty(t, h.docs)
}

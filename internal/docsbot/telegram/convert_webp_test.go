package telegram_test

import (
	"bytes"
	"encoding/base64"
	"image"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// tinyWebP is a 1x1 lossless WEBP.
func tinyWebP(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	require.NoError(t, err)
	return data
}

func namedDocumentUpdate(fileID, mime, name string) *models.Update {
	return &models.Update{Message: &models.Message{
		ID: 1, From: &models.User{ID: testUser}, Chat: models.Chat{ID: testChat, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: fileID, FileUniqueID: "u-" + fileID, MimeType: mime, FileName: name},
	}}
}

func TestConvertWebP_compressInCommand(t *testing.T) {
	h := newConvHarness(t)
	h.serve("webp-in", tinyWebP(t))
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
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
	h := newConvHarness(t)
	h.serve("webp-in", tinyWebP(t))
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:pdf"))
	h.bot.ProcessUpdate(ctx, namedDocumentUpdate("webp-in", "image/webp", "a.webp"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

	require.Len(t, h.docs, 1)
	require.True(t, strings.HasPrefix(string(h.docs[0].data), "%PDF"))
	require.Contains(t, string(h.docs[0].data), "/Count 1")
}

func TestConvertWebP_cardToJPG(t *testing.T) {
	h := newConvHarness(t)
	h.serve("webp-src", tinyWebP(t))
	src := h.repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, MIME: "image/webp",
		Category: docsbot.CatOther, Title: "Скан.webp", TgFileID: "webp-src", TgFileUniqueID: "webp-uniq",
	})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:doc:"+string(src.ID)))
	require.Contains(t, h.lastMarkup(), "conv:q:"+string(src.ID)+":jpg")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":jpg"))

	require.Len(t, h.docs, 1)
	require.Equal(t, "Скан-JPG.jpg", h.docs[0].filename)
	_, format, err := image.Decode(bytes.NewReader(h.docs[0].data))
	require.NoError(t, err)
	require.Equal(t, "jpeg", format)

	tok := saveToken.FindStringSubmatch(h.docMarkups[0])
	require.NotNil(t, tok)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok[1]))
	require.Len(t, h.repo.docs, 2)
	for id, d := range h.repo.docs {
		if id != src.ID {
			require.Equal(t, "Скан (JPG)", d.Title)
		}
	}
}

func TestConvertWebP_jpgButtonOnlyForWebP(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved) // a photo
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:doc:"+string(src.ID)))
	require.NotContains(t, h.lastMarkup(), ":jpg")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":jpg"))
	require.Empty(t, h.docs)
}

func TestConvertHEIC_refusedInCommand(t *testing.T) {
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
			ctx := tg.ContextWithUserID(t.Context(), testUserID)

			h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
			h.bot.ProcessUpdate(ctx, namedDocumentUpdate("x", tt.mime, tt.file))

			require.Empty(t, h.docs)
			require.Equal(t, "Этот формат пока не поддерживается. Пришли JPG, PNG или WEBP.", h.lastText())
		})
	}
}

func TestConvertHEIC_noConvertRowAndRefusedCallbacks(t *testing.T) {
	h := newConvHarness(t)
	heic := h.repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, MIME: "image/heic",
		Category: docsbot.CatOther, Title: "IMG.heic", TgFileID: "x", TgFileUniqueID: "heic-uniq",
	})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:doc:"+string(heic.ID)))
	require.Contains(t, h.lastText(), "не поддерживается")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(heic.ID)+":c1"))
	require.Contains(t, h.lastText(), "не поддерживается")
	require.Empty(t, h.docs)

	b, sent, repo := newBot(t)
	doc := repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, MIME: "image/heic",
		Category: docsbot.CatOther, Title: "IMG.heic", TgFileID: "x", TgFileUniqueID: "heic-uniq",
	})
	b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(doc.ID)+":s"))
	require.Len(t, sent.editMarkups, 1)
	require.NotContains(t, sent.editMarkups[0], "conv:doc:")
}

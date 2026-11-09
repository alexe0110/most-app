package telegram_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

var albumToken = regexp.MustCompile(`conv:alb:([0-9a-f]+)`)

// albumImageUpdate is album photo n (message id n), sent as part of group.
func albumImageUpdate(group string, n int, size int) *models.Update {
	return &models.Update{Message: &models.Message{
		ID:           n,
		From:         &models.User{ID: testUser},
		Chat:         models.Chat{ID: testChat, Type: models.ChatTypePrivate},
		MediaGroupID: group,
		Photo:        []models.PhotoSize{{FileID: fmt.Sprintf("img%02d", n), FileUniqueID: fmt.Sprintf("u%02d", n), FileSize: size}},
	}}
}

func albumHarness(t *testing.T) (*convHarness, *fakeTimer) {
	t.Helper()
	ft := &fakeTimer{}
	return newConvHarness(t, telegram.WithAlbumTimer(ft.after)), ft
}

func TestConvertAlbum_shuffledPhotosMakeOnePDFInOrder(t *testing.T) {
	h, ft := albumHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	for _, n := range []int{3, 1, 4, 2} {
		h.bot.ProcessUpdate(ctx, albumImageUpdate("alb", n, 1000))
	}
	ft.fire()
	require.Len(t, h.repo.docs, 4)
	require.Contains(t, h.lastText(), "4 изображения")

	tok := albumToken.FindStringSubmatch(h.albumMarkup())
	require.NotNil(t, tok)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:alb:"+tok[1]))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:alb:"+tok[1])) // double tap

	require.Equal(t, []string{"img01", "img02", "img03", "img04"}, h.downloads, "pages follow message order")
	require.Len(t, h.docs, 1, "one PDF")
	require.Contains(t, string(h.docs[0].data), "/Count 4")
	require.True(t, strings.HasPrefix(h.docs[0].filename, "scan-"))
	require.Empty(t, h.repo.docs, "the album's pending documents are dropped")
	require.NotNil(t, saveToken.FindStringSubmatch(h.docMarkups[0]), "the PDF can be saved")
}

func TestConvertAlbum_saveOneByOneShowsGroupPicker(t *testing.T) {
	h, ft := albumHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	for n := 1; n <= 2; n++ {
		h.bot.ProcessUpdate(ctx, albumImageUpdate("alb", n, 1000))
	}
	ft.fire()
	m := regexp.MustCompile(`d:gpk:([^"]+)"`).FindStringSubmatch(h.albumMarkup())
	require.NotNil(t, m)

	h.bot.ProcessUpdate(ctx, callbackUpdate("d:gpk:"+m[1]))
	h.bot.ProcessUpdate(ctx, callbackUpdate("d:ga:"+m[1]+":passport"))

	require.Empty(t, h.docs)
	for _, d := range h.repo.docs {
		require.Equal(t, docsbot.StatusSaved, d.Status)
	}
}

func TestConvertAlbum_tooBigRefused(t *testing.T) {
	h, ft := albumHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	for n := 1; n <= 2; n++ {
		h.bot.ProcessUpdate(ctx, albumImageUpdate("alb", n, 11<<20))
	}
	ft.fire()
	tok := albumToken.FindStringSubmatch(h.albumMarkup())
	require.NotNil(t, tok)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:alb:"+tok[1]))

	require.Empty(t, h.docs)
	require.Empty(t, h.downloads, "limits are checked before downloading")
	require.Contains(t, h.lastText(), "20 МБ")
	require.Len(t, h.repo.docs, 2, "photos stay pending to save one by one")
}

func TestConvertAlbum_tooManyPhotosKeepsOldPicker(t *testing.T) {
	h, ft := albumHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	for n := 1; n <= 21; n++ {
		h.bot.ProcessUpdate(ctx, albumImageUpdate("alb", n, 1000))
	}
	ft.fire()

	require.Empty(t, h.docs)
	require.NotContains(t, h.albumMarkup(), "conv:alb:")
	require.Contains(t, h.allTexts(), "Сначала выбери категорию", "the pending limit is reported")
}

func TestConvertAlbum_mixedAlbumKeepsOldPicker(t *testing.T) {
	h, ft := albumHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	h.bot.ProcessUpdate(ctx, albumImageUpdate("alb", 1, 1000))
	h.bot.ProcessUpdate(ctx, albumImageUpdate("alb", 2, 1000))
	h.bot.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
		ID: 3, From: &models.User{ID: testUser}, Chat: models.Chat{ID: testChat, Type: models.ChatTypePrivate}, MediaGroupID: "alb",
		Document: &models.Document{FileID: "pdf", FileUniqueID: "updf", MimeType: "application/pdf"},
	}})
	ft.fire()

	require.Contains(t, h.albumMarkup(), "d:ga:")
	require.NotContains(t, h.albumMarkup(), "conv:alb:")
}

func TestConvertAlbum_singlePhotoKeepsPicker(t *testing.T) {
	h, ft := albumHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	ft.fire()

	require.Contains(t, h.albumMarkup(), "d:cat:")
	require.Contains(t, h.albumMarkup(), "conv:doc:")
}
